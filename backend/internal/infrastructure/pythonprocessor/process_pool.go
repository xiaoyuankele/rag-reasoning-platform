package pythonprocessor

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"sync"

	documentapplication "rag-reasoning-platform/backend/internal/application/document"
	documentdomain "rag-reasoning-platform/backend/internal/domain/document"
)

const (
	minimumPythonProcessPoolSize     = 1
	maximumPythonProcessPoolSize     = 4
	minimumPythonProcessMaxDocuments = 1
	maximumPythonProcessMaxDocuments = 10000
)

var (
	// ErrPythonProcessPoolClosed 表示服务已经开始关闭，不能再接收文档。
	ErrPythonProcessPoolClosed = errors.New(
		"Python document process pool is closed",
	)

	// ErrInvalidPythonProcessPoolConfiguration 表示进程数量或回收上限无效。
	ErrInvalidPythonProcessPoolConfiguration = errors.New(
		"invalid Python document process pool configuration",
	)

	errStreamResponseTooLarge = errors.New(
		"Python stream response line exceeded limit",
	)
)

type streamCommandFactory func() *exec.Cmd

// ProcessPool 维护固定数量的可复用 Python 文档处理进程。
//
// available 是一个有界租借队列：每个 streamProcess 同一时刻只会交给一个
// Go Worker，因此单个 Python 进程仍然串行处理文档；多个槽位之间可以并发。
type ProcessPool struct {
	files              StoredFileMaterializer
	maxChunkCharacters int
	maxPDFFileBytes    int64
	maxPDFPages        int

	workers   []*streamProcess
	available chan *streamProcess
	closedCh  chan struct{}
	closeDone chan struct{}

	stateMu  sync.Mutex
	active   sync.WaitGroup
	closed   bool
	closeErr error
}

var _ documentapplication.DocumentProcessor = (*ProcessPool)(nil)
var _ io.Closer = (*ProcessPool)(nil)

// NewProcessPool 创建生产环境使用的固定大小 Python 进程池。
//
// 进程采用惰性启动：构造池时只创建槽位，第一份文档借用某个槽位时才真正
// 启动 Python。这样关闭但没有处理任务的服务不会产生无用子进程。
func NewProcessPool(
	files StoredFileMaterializer,
	pythonExecutable string,
	pythonSourceRoot string,
	maxPDFFileBytes int64,
	maxPDFPages int,
	poolSize int,
	maxDocumentsPerProcess int,
) (*ProcessPool, error) {
	if files == nil {
		return nil, ErrSourceMaterializerRequired
	}
	if poolSize < minimumPythonProcessPoolSize ||
		poolSize > maximumPythonProcessPoolSize {
		return nil, fmt.Errorf(
			"%w: pool size must be between %d and %d",
			ErrInvalidPythonProcessPoolConfiguration,
			minimumPythonProcessPoolSize,
			maximumPythonProcessPoolSize,
		)
	}
	if maxDocumentsPerProcess < minimumPythonProcessMaxDocuments ||
		maxDocumentsPerProcess > maximumPythonProcessMaxDocuments {
		return nil, fmt.Errorf(
			"%w: max documents must be between %d and %d",
			ErrInvalidPythonProcessPoolConfiguration,
			minimumPythonProcessMaxDocuments,
			maximumPythonProcessMaxDocuments,
		)
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

	runtime, err := newPythonRuntime(pythonExecutable, pythonSourceRoot)
	if err != nil {
		return nil, err
	}

	pool := &ProcessPool{
		files:              files,
		maxChunkCharacters: defaultMaxChunkCharacters,
		maxPDFFileBytes:    maxPDFFileBytes,
		maxPDFPages:        maxPDFPages,
		workers:            make([]*streamProcess, 0, poolSize),
		available:          make(chan *streamProcess, poolSize),
		closedCh:           make(chan struct{}),
		closeDone:          make(chan struct{}),
	}

	for range poolSize {
		worker := &streamProcess{
			newCommand:   runtime.newStreamCommand,
			maxDocuments: maxDocumentsPerProcess,
			maxStdout:    defaultMaxStdoutBytes,
			maxStderr:    defaultMaxStderrBytes,
		}
		pool.workers = append(pool.workers, worker)
		pool.available <- worker
	}

	return pool, nil
}

// Process 借用一个空闲 Python 进程并执行一次同步协议调用。
//
// 如果全部进程都忙，本调用会等待槽位；ctx 超时或服务关闭会立即停止等待。
// Application 只看到 DocumentProcessor 契约，不需要知道进程是否被复用。
func (p *ProcessPool) Process(
	ctx context.Context,
	document documentdomain.Document,
) (result documentapplication.ProcessingResult, processingErr error) {
	worker, err := p.acquire(ctx)
	if err != nil {
		return documentapplication.ProcessingResult{}, err
	}
	defer p.release(worker)

	sourcePath, release, err := materializeSourceFile(
		ctx,
		p.files,
		document.StoragePath,
	)
	if err != nil {
		return documentapplication.ProcessingResult{}, err
	}
	defer releaseMaterializedSource(&result, &processingErr, release)

	request, err := prepareProcessRequest(
		ctx,
		document,
		sourcePath,
		p.maxChunkCharacters,
		p.maxPDFFileBytes,
		p.maxPDFPages,
	)
	if err != nil {
		return documentapplication.ProcessingResult{}, err
	}

	result, processingErr = worker.process(ctx, request)
	return result, processingErr
}

func (p *ProcessPool) acquire(ctx context.Context) (*streamProcess, error) {
	var worker *streamProcess
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-p.closedCh:
		return nil, ErrPythonProcessPoolClosed
	case worker = <-p.available:
	}

	// stateMu 使 Add 必定发生在 Close 的 Wait 之前，避免 WaitGroup 的
	// Add/Wait 并发误用。若关闭已经开始，槽位由 Close 统一回收。
	p.stateMu.Lock()
	if p.closed {
		p.stateMu.Unlock()
		return nil, ErrPythonProcessPoolClosed
	}
	p.active.Add(1)
	p.stateMu.Unlock()

	return worker, nil
}

func (p *ProcessPool) release(worker *streamProcess) {
	defer p.active.Done()

	p.stateMu.Lock()
	closed := p.closed
	p.stateMu.Unlock()
	if closed {
		_ = worker.close()
		return
	}

	p.available <- worker
}

// Close 停止接收新任务，等待正在使用的槽位归还，再关闭所有 Python 进程。
// 方法可以安全重复调用；后续调用会等待第一次关闭完成并返回同一个结果。
func (p *ProcessPool) Close() error {
	p.stateMu.Lock()
	if p.closed {
		closeDone := p.closeDone
		p.stateMu.Unlock()
		<-closeDone

		p.stateMu.Lock()
		defer p.stateMu.Unlock()
		return p.closeErr
	}
	p.closed = true
	close(p.closedCh)
	p.stateMu.Unlock()

	p.active.Wait()

	var closeErrors []error
	for _, worker := range p.workers {
		if err := worker.close(); err != nil {
			closeErrors = append(closeErrors, err)
		}
	}

	p.stateMu.Lock()
	p.closeErr = errors.Join(closeErrors...)
	close(p.closeDone)
	closeErr := p.closeErr
	p.stateMu.Unlock()
	return closeErr
}

// streamProcess 表示一个可复用但一次只处理一个请求的 Python 进程槽位。
// 它只会在 ProcessPool 租借期间被一个 goroutine 访问，不需要额外互斥锁。
type streamProcess struct {
	newCommand   streamCommandFactory
	maxDocuments int
	maxStdout    int64
	maxStderr    int64

	command   *exec.Cmd
	stdin     io.WriteCloser
	stdout    *bufio.Reader
	stderr    *diagnosticBuffer
	processed int
	starts    int
}

func (p *streamProcess) process(
	ctx context.Context,
	request processRequest,
) (documentapplication.ProcessingResult, error) {
	if err := ctx.Err(); err != nil {
		return documentapplication.ProcessingResult{}, err
	}
	if err := p.startIfRequired(); err != nil {
		return documentapplication.ProcessingResult{}, err
	}

	p.stderr.Reset()
	responseLine, err := p.exchange(ctx, request)
	if err != nil {
		diagnostics, _ := p.retire(true)
		if contextError := ctx.Err(); contextError != nil {
			return documentapplication.ProcessingResult{}, fmt.Errorf(
				"run pooled Python document processor: %w",
				contextError,
			)
		}
		if errors.Is(err, errStreamResponseTooLarge) {
			return documentapplication.ProcessingResult{}, fmt.Errorf(
				"%w: stdout limit is %d bytes",
				ErrPythonProcessOutputTooLarge,
				p.maxStdout,
			)
		}
		return documentapplication.ProcessingResult{}, fmt.Errorf(
			"%w: %w%s",
			ErrPythonProcessFailed,
			err,
			formatStderr(diagnostics),
		)
	}

	if p.stderr.Exceeded() {
		diagnostics, _ := p.retire(true)
		return documentapplication.ProcessingResult{}, fmt.Errorf(
			"%w: stderr limit is %d bytes%s",
			ErrPythonProcessOutputTooLarge,
			p.maxStderr,
			formatStderr(diagnostics),
		)
	}

	result, decodeErr := decodeProcessResponse(
		ctx,
		bytes.NewReader(responseLine),
		request.RequestID,
	)
	var processingFailure *ProcessingFailureError
	validResponse := decodeErr == nil || errors.As(decodeErr, &processingFailure)
	if !validResponse {
		diagnostics, _ := p.retire(true)
		return documentapplication.ProcessingResult{}, fmt.Errorf(
			"decode pooled Python document processing result%s: %w",
			formatStderr(diagnostics),
			decodeErr,
		)
	}

	p.processed++
	if p.processed >= p.maxDocuments {
		// 已经收到完整响应，回收失败不能把本次有效业务结果改写为失败。
		_, _ = p.retire(false)
	}

	return result, decodeErr
}

func (p *streamProcess) startIfRequired() error {
	if p.command != nil {
		return nil
	}

	command := p.newCommand()
	stdin, err := command.StdinPipe()
	if err != nil {
		return fmt.Errorf("create Python stream stdin: %w", err)
	}
	stdoutPipe, err := command.StdoutPipe()
	if err != nil {
		_ = stdin.Close()
		return fmt.Errorf("create Python stream stdout: %w", err)
	}
	stderr := newDiagnosticBuffer(p.maxStderr)
	command.Stderr = stderr

	if err := command.Start(); err != nil {
		_ = stdin.Close()
		return fmt.Errorf(
			"%w: start stream process: %w",
			ErrPythonProcessFailed,
			err,
		)
	}

	p.command = command
	p.stdin = stdin
	p.stdout = bufio.NewReader(stdoutPipe)
	p.stderr = stderr
	p.processed = 0
	p.starts++
	return nil
}

type streamExchangeResult struct {
	response []byte
	err      error
}

func (p *streamProcess) exchange(
	ctx context.Context,
	request processRequest,
) ([]byte, error) {
	var encodedRequest bytes.Buffer
	if err := encodeProcessRequest(ctx, &encodedRequest, request); err != nil {
		return nil, err
	}

	completed := make(chan streamExchangeResult, 1)
	go func() {
		if _, err := p.stdin.Write(encodedRequest.Bytes()); err != nil {
			completed <- streamExchangeResult{
				err: fmt.Errorf("write Python stream request: %w", err),
			}
			return
		}

		response, err := readJSONLine(p.stdout, p.maxStdout)
		completed <- streamExchangeResult{response: response, err: err}
	}()

	select {
	case result := <-completed:
		return result.response, result.err
	case <-ctx.Done():
		p.interrupt()
		<-completed
		return nil, ctx.Err()
	}
}

func readJSONLine(reader *bufio.Reader, limit int64) ([]byte, error) {
	var response bytes.Buffer
	for {
		fragment, err := reader.ReadSlice('\n')
		if int64(response.Len()+len(fragment)) > limit {
			return nil, errStreamResponseTooLarge
		}
		_, _ = response.Write(fragment)

		switch {
		case err == nil:
			return bytes.TrimSuffix(response.Bytes(), []byte{'\n'}), nil
		case errors.Is(err, bufio.ErrBufferFull):
			continue
		default:
			return nil, fmt.Errorf("read Python stream response: %w", err)
		}
	}
}

func (p *streamProcess) interrupt() {
	if p.stdin != nil {
		_ = p.stdin.Close()
	}
	if p.command != nil && p.command.Process != nil {
		_ = p.command.Process.Kill()
	}
}

func (p *streamProcess) retire(force bool) (string, error) {
	if p.command == nil {
		return "", nil
	}
	if force {
		p.interrupt()
	} else if p.stdin != nil {
		_ = p.stdin.Close()
	}

	waitErr := p.command.Wait()
	diagnostics := ""
	if p.stderr != nil {
		diagnostics = p.stderr.String()
	}

	p.command = nil
	p.stdin = nil
	p.stdout = nil
	p.stderr = nil
	p.processed = 0
	return diagnostics, waitErr
}

func (p *streamProcess) close() error {
	_, err := p.retire(false)
	return err
}

// diagnosticBuffer 保存单次请求有限长度的 stderr，同时始终向 exec.Cmd
// 报告完整写入，避免 Python 因诊断输出超过上限而收到额外的管道错误。
type diagnosticBuffer struct {
	mu       sync.Mutex
	buffer   bytes.Buffer
	limit    int64
	exceeded bool
}

func newDiagnosticBuffer(limit int64) *diagnosticBuffer {
	return &diagnosticBuffer{limit: limit}
}

func (b *diagnosticBuffer) Write(content []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()

	remaining := b.limit - int64(b.buffer.Len())
	if remaining > 0 {
		writeLength := int64(len(content))
		if writeLength > remaining {
			writeLength = remaining
		}
		_, _ = b.buffer.Write(content[:int(writeLength)])
	}
	if int64(len(content)) > remaining {
		b.exceeded = true
	}
	return len(content), nil
}

func (b *diagnosticBuffer) Reset() {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.buffer.Reset()
	b.exceeded = false
}

func (b *diagnosticBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buffer.String()
}

func (b *diagnosticBuffer) Exceeded() bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.exceeded
}
