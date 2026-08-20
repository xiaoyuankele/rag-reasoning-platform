package pythonprocessor

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"

	documentapplication "rag-reasoning-platform/backend/internal/application/document"
	documentdomain "rag-reasoning-platform/backend/internal/domain/document"
)

const (
	pythonProcessorModule     = "rag_ai.entrypoints.document_processing_cli"
	defaultMaxChunkCharacters = 1000
	defaultMaxStdoutBytes     = 32 * 1024 * 1024
	defaultMaxStderrBytes     = 1 * 1024 * 1024
)

var (
	// ErrSourcePathResolverRequired 表示没有提供可信存储路径解析器。
	ErrSourcePathResolverRequired = errors.New(
		"Python processor source path resolver is required",
	)

	// ErrPythonExecutableRequired 表示没有配置 Python 可执行程序。
	ErrPythonExecutableRequired = errors.New(
		"Python executable is required",
	)

	// ErrPythonSourceRootRequired 表示没有配置 rag_ai 包所在的源码目录。
	ErrPythonSourceRootRequired = errors.New(
		"Python source root is required",
	)

	// ErrInvalidPDFProcessingLimits 表示传给 Python 的 PDF 安全限制无效。
	ErrInvalidPDFProcessingLimits = errors.New(
		"Python PDF processing limits are invalid",
	)

	// ErrPythonProcessFailed 表示 Python 没有以退出码 0 正常完成协议。
	ErrPythonProcessFailed = errors.New(
		"Python document processor process failed",
	)

	// ErrPythonProcessOutputTooLarge 表示 stdout 或 stderr 超过安全上限。
	ErrPythonProcessOutputTooLarge = errors.New(
		"Python document processor output exceeded limit",
	)

	errLimitedBufferFull = errors.New("limited buffer is full")
	requestSequence      atomic.Uint64
)

// StoredFilePathResolver 定义 PythonProcessor 获取可信绝对路径所需的最小能力。
//
// 接口定义在使用方包中。LocalStorage 只要提供同名方法即可自动满足它，
// PythonProcessor 不需要依赖具体的本地存储结构。
type StoredFilePathResolver interface {
	ResolveAbsolutePath(storagePath string) (string, error)
}

// commandFactory 是测试接缝。生产环境创建真实 Python 命令，单元测试则可以
// 替换成当前 Go 测试进程，稳定模拟成功、崩溃、超时和超限等情况。
type commandFactory func(ctx context.Context) *exec.Cmd

// pythonRuntime 保存已经校验并解析完成的 Python 运行时位置。
// oneshot Processor 和常驻进程池共享它，避免两种模式产生不同的 PYTHONPATH。
type pythonRuntime struct {
	executable string
	sourceRoot string
}

func newPythonRuntime(
	pythonExecutable string,
	pythonSourceRoot string,
) (pythonRuntime, error) {
	pythonExecutable = strings.TrimSpace(pythonExecutable)
	if pythonExecutable == "" {
		return pythonRuntime{}, ErrPythonExecutableRequired
	}
	resolvedPythonExecutable, err := exec.LookPath(pythonExecutable)
	if err != nil {
		return pythonRuntime{}, fmt.Errorf(
			"find Python executable %q: %w",
			pythonExecutable,
			err,
		)
	}

	pythonSourceRoot = strings.TrimSpace(pythonSourceRoot)
	if pythonSourceRoot == "" {
		return pythonRuntime{}, ErrPythonSourceRootRequired
	}
	absolutePythonSourceRoot, err := filepath.Abs(pythonSourceRoot)
	if err != nil {
		return pythonRuntime{}, fmt.Errorf(
			"resolve Python source root: %w",
			err,
		)
	}

	pythonSourceInfo, err := os.Stat(absolutePythonSourceRoot)
	if err != nil {
		return pythonRuntime{}, fmt.Errorf(
			"inspect Python source root: %w",
			err,
		)
	}
	if !pythonSourceInfo.IsDir() {
		return pythonRuntime{}, fmt.Errorf(
			"%w: %q is not a directory",
			ErrPythonSourceRootRequired,
			absolutePythonSourceRoot,
		)
	}

	return pythonRuntime{
		executable: resolvedPythonExecutable,
		sourceRoot: absolutePythonSourceRoot,
	}, nil
}

func (r pythonRuntime) environment() []string {
	pythonPath := r.sourceRoot
	if existingPythonPath := os.Getenv("PYTHONPATH"); existingPythonPath != "" {
		pythonPath += string(os.PathListSeparator) + existingPythonPath
	}

	return append(os.Environ(), "PYTHONPATH="+pythonPath)
}

func (r pythonRuntime) newOneShotCommand(ctx context.Context) *exec.Cmd {
	command := exec.CommandContext(
		ctx,
		r.executable,
		"-m",
		pythonProcessorModule,
	)
	command.Env = r.environment()
	return command
}

func (r pythonRuntime) newStreamCommand() *exec.Cmd {
	command := exec.Command(
		r.executable,
		"-m",
		pythonProcessorModule,
		"--stream",
	)
	command.Env = r.environment()
	return command
}

// Processor 通过一次性 Python 子进程处理 PDF、DOCX 等复杂文档。
type Processor struct {
	paths              StoredFilePathResolver
	newCommand         commandFactory
	maxChunkCharacters int
	maxPDFFileBytes    int64
	maxPDFPages        int
	maxStdoutBytes     int64
	maxStderrBytes     int64
}

var _ documentapplication.DocumentProcessor = (*Processor)(nil)

// NewProcessor 创建生产环境使用的 Python 文档处理器。
//
// pythonSourceRoot 应指向包含 rag_ai 包的目录，例如项目中的 ../ai/src。
// 构造时转换为绝对路径，使子进程行为不受后续工作目录变化影响。
func NewProcessor(
	paths StoredFilePathResolver,
	pythonExecutable string,
	pythonSourceRoot string,
	maxPDFFileBytes int64,
	maxPDFPages int,
) (*Processor, error) {
	if paths == nil {
		return nil, ErrSourcePathResolverRequired
	}

	runtime, err := newPythonRuntime(pythonExecutable, pythonSourceRoot)
	if err != nil {
		return nil, err
	}
	if maxPDFFileBytes < 1 || maxPDFFileBytes > maxPDFFileBytesMaximum {
		return nil, fmt.Errorf(
			"%w: max file bytes must be between 1 and %d",
			ErrInvalidPDFProcessingLimits,
			maxPDFFileBytesMaximum,
		)
	}
	if maxPDFPages < 1 || maxPDFPages > maxPDFPagesMaximum {
		return nil, fmt.Errorf(
			"%w: max pages must be between 1 and %d",
			ErrInvalidPDFProcessingLimits,
			maxPDFPagesMaximum,
		)
	}

	return &Processor{
		paths:              paths,
		newCommand:         runtime.newOneShotCommand,
		maxChunkCharacters: defaultMaxChunkCharacters,
		maxPDFFileBytes:    maxPDFFileBytes,
		maxPDFPages:        maxPDFPages,
		maxStdoutBytes:     defaultMaxStdoutBytes,
		maxStderrBytes:     defaultMaxStderrBytes,
	}, nil
}

// Process 完成一次 Go -> Python -> Go 的同步协议调用。
//
// 整个 Worker 相对 HTTP 请求是异步的，但这里的一次子进程调用是同步等待的：
// 只有 Python 退出并且响应通过校验后，本方法才会返回给 Worker。
func (p *Processor) Process(
	ctx context.Context,
	document documentdomain.Document,
) (documentapplication.ProcessingResult, error) {
	if err := ctx.Err(); err != nil {
		return documentapplication.ProcessingResult{}, err
	}

	request, err := prepareProcessRequest(
		ctx,
		p.paths,
		document,
		p.maxChunkCharacters,
		p.maxPDFFileBytes,
		p.maxPDFPages,
	)
	if err != nil {
		return documentapplication.ProcessingResult{}, err
	}

	var stdin bytes.Buffer
	if err := encodeProcessRequest(ctx, &stdin, request); err != nil {
		return documentapplication.ProcessingResult{}, fmt.Errorf(
			"prepare Python processing stdin: %w",
			err,
		)
	}

	stdout := newLimitedBuffer(p.maxStdoutBytes)
	stderr := newLimitedBuffer(p.maxStderrBytes)

	command := p.newCommand(ctx)
	command.Stdin = &stdin
	command.Stdout = stdout
	command.Stderr = stderr

	runErr := command.Run()

	// CommandContext 在取消时通常返回 signal killed。优先返回 ctx.Err，
	// Worker 才能用 errors.Is 准确识别超时或停机取消。
	if err := ctx.Err(); err != nil {
		return documentapplication.ProcessingResult{}, fmt.Errorf(
			"run Python document processor: %w",
			err,
		)
	}

	if stdout.Exceeded() {
		return documentapplication.ProcessingResult{}, fmt.Errorf(
			"%w: stdout limit is %d bytes",
			ErrPythonProcessOutputTooLarge,
			p.maxStdoutBytes,
		)
	}
	if stderr.Exceeded() {
		return documentapplication.ProcessingResult{}, fmt.Errorf(
			"%w: stderr limit is %d bytes",
			ErrPythonProcessOutputTooLarge,
			p.maxStderrBytes,
		)
	}

	if runErr != nil {
		return documentapplication.ProcessingResult{}, fmt.Errorf(
			"%w: %w%s",
			ErrPythonProcessFailed,
			runErr,
			formatStderr(stderr.String()),
		)
	}

	result, err := decodeProcessResponse(
		ctx,
		stdout.Reader(),
		request.RequestID,
	)
	if err != nil {
		return documentapplication.ProcessingResult{}, fmt.Errorf(
			"decode Python document processing result%s: %w",
			formatStderr(stderr.String()),
			err,
		)
	}

	return result, nil
}

func prepareProcessRequest(
	ctx context.Context,
	paths StoredFilePathResolver,
	document documentdomain.Document,
	maxChunkCharacters int,
	maxPDFFileBytes int64,
	maxPDFPages int,
) (processRequest, error) {
	if err := ctx.Err(); err != nil {
		return processRequest{}, err
	}

	sourcePath, err := paths.ResolveAbsolutePath(document.StoragePath)
	if err != nil {
		return processRequest{}, fmt.Errorf(
			"resolve Python processing source path: %w",
			err,
		)
	}

	return newProcessRequest(
		nextRequestID(document.ID),
		document,
		sourcePath,
		maxChunkCharacters,
		maxPDFFileBytes,
		maxPDFPages,
	)
}

// nextRequestID 生成进程内唯一的关联 ID。
// atomic.Uint64 使未来出现多个 Worker 时也不会发生数据竞争。
func nextRequestID(documentID int64) string {
	sequence := requestSequence.Add(1)
	return fmt.Sprintf("document-%d-%d", documentID, sequence)
}

// formatStderr 只把受限后的诊断输出附加到后端错误链。
// Python stdout 属于协议，stderr 才允许写诊断信息。
func formatStderr(stderr string) string {
	stderr = strings.TrimSpace(stderr)
	if stderr == "" {
		return ""
	}

	return fmt.Sprintf("; stderr=%q", stderr)
}

// limitedBuffer 是带硬上限的内存缓冲区。
//
// 它实现 io.Writer，因此可以直接交给 exec.Cmd。超过上限时停止接收数据，
// 并留下 Exceeded 标记供 Process 转换成稳定错误。
type limitedBuffer struct {
	buffer   bytes.Buffer
	limit    int64
	exceeded bool
}

func newLimitedBuffer(limit int64) *limitedBuffer {
	return &limitedBuffer{limit: limit}
}

func (b *limitedBuffer) Write(content []byte) (int, error) {
	remaining := b.limit - int64(b.buffer.Len())
	if remaining <= 0 {
		b.exceeded = true
		return 0, errLimitedBufferFull
	}

	if int64(len(content)) <= remaining {
		return b.buffer.Write(content)
	}

	written, _ := b.buffer.Write(content[:int(remaining)])
	b.exceeded = true
	return written, errLimitedBufferFull
}

func (b *limitedBuffer) Exceeded() bool {
	return b.exceeded
}

func (b *limitedBuffer) String() string {
	return b.buffer.String()
}

func (b *limitedBuffer) Reader() *bytes.Reader {
	return bytes.NewReader(b.buffer.Bytes())
}
