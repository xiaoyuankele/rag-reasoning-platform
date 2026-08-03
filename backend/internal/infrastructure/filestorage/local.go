// Package filestorage 提供文件存储接口的本地磁盘实现。
package filestorage

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"unicode/utf8"

	applicationdocument "rag-reasoning-platform/backend/internal/application/document"
)

const pdfHeader = "%PDF-"

var (
	// ErrRootDirectoryRequired 表示没有配置文件存储根目录。
	ErrRootDirectoryRequired = errors.New("root directory is required")

	// ErrInvalidMaxFileSize 表示最大文件大小不是正整数。
	ErrInvalidMaxFileSize = errors.New("maximum file size must be positive")

	// ErrInvalidStoragePath 表示存储路径不属于允许的文档目录。
	ErrInvalidStoragePath = errors.New("invalid storage path")
)

// fileFormat 描述一种允许上传的文件格式。
// storageExtension 是后端生成物理文件名时使用的规范化扩展名，
// mimeType 是 LocalStorage 完成格式判断后交给应用层的可信 MIME 类型。
type fileFormat struct {
	storageExtension string
	mimeType         string
}

// resolveFileFormat 根据原始文件名确定允许使用的存储扩展名和 MIME 类型。
// 文件名只用于选择预期格式；Save 后续还必须校验真实文件内容，
// 不能仅凭客户端提供的扩展名信任文件。
func resolveFileFormat(originalName string) (fileFormat, error) {
	extension := strings.ToLower(
		filepath.Ext(strings.TrimSpace(originalName)),
	)

	switch extension {
	case ".pdf":
		return fileFormat{
			storageExtension: ".pdf",
			mimeType:         "application/pdf",
		}, nil

	case ".md", ".markdown":
		return fileFormat{
			storageExtension: ".md",
			mimeType:         "text/markdown",
		}, nil

	case ".txt":
		return fileFormat{
			storageExtension: ".txt",
			mimeType:         "text/plain",
		}, nil

	default:
		return fileFormat{}, applicationdocument.ErrUnsupportedFileType
	}
}

// LocalStorage 将上传文件保存在本地磁盘。
type LocalStorage struct {
	// rootDir 是整个运行数据目录的绝对路径。
	rootDir string

	// documentsDir 是实际保存文档文件的目录。
	documentsDir string

	// maxSizeBytes 是单个文件允许写入的最大字节数。
	maxSizeBytes int64
}

var _ applicationdocument.FileStorage = (*LocalStorage)(nil)

// NewLocalStorage 创建本地文件存储，并准备文档目录。
func NewLocalStorage(rootDir string, maxSizeBytes int64) (*LocalStorage, error) {
	rootDir = strings.TrimSpace(rootDir)

	if rootDir == "" {
		return nil, ErrRootDirectoryRequired
	}

	if maxSizeBytes <= 0 {
		return nil, ErrInvalidMaxFileSize
	}

	absoluteRootDir, err := filepath.Abs(rootDir)
	if err != nil {
		return nil, fmt.Errorf("resolve storage root directory: %w", err)
	}

	documentsDir := filepath.Join(absoluteRootDir, "documents")

	if err := os.MkdirAll(documentsDir, 0o755); err != nil {
		return nil, fmt.Errorf("create documents directory: %w", err)
	}

	return &LocalStorage{
		rootDir:      absoluteRootDir,
		documentsDir: documentsDir,
		maxSizeBytes: maxSizeBytes,
	}, nil
}

// contextReader 在每次读取前检查请求是否已取消。
//
// 它把 context.Context 的生命周期控制加入普通 io.Reader，
// 同时仍然满足 io.Reader 接口。
type contextReader struct {
	ctx    context.Context
	reader io.Reader
}

// Read 实现 io.Reader 接口。
func (r *contextReader) Read(p []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}

	return r.reader.Read(p)
}

// validatePDFHeader 检查文件开头是否包含 PDF 标识。
//
// 返回 bufio.Reader 是为了让 Peek 检查过的字节仍然能被后续保存。
func validatePDFHeader(content io.Reader) (*bufio.Reader, error) {
	if content == nil {
		return nil, applicationdocument.ErrInvalidPDFContent
	}

	bufferedContent := bufio.NewReader(content)

	header, err := bufferedContent.Peek(len(pdfHeader))
	if errors.Is(err, io.EOF) {
		return nil, applicationdocument.ErrInvalidPDFContent
	}
	if err != nil {
		return nil, fmt.Errorf(
			"read PDF header: %w",
			err,
		)
	}

	if !bytes.Equal(header, []byte(pdfHeader)) {
		return nil, applicationdocument.ErrInvalidPDFContent
	}

	return bufferedContent, nil
}

// validateUTF8File 以固定大小缓冲区流式检查临时文本文件是否为合法 UTF-8。
//
// 一个 UTF-8 字符最多占 4 个字节，并且可能恰好跨越两次 Read。
// pendingBytes 会把上一轮末尾尚不完整的字符字节移动到缓冲区开头，
// 再与下一轮读到的数据一起判断，避免误把合法的跨块字符识别为错误。
func validateUTF8File(ctx context.Context, path string) error {
	file, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("open temporary text file for validation: %w", err)
	}
	defer func() {
		_ = file.Close()
	}()

	const readBufferSize = 32 * 1024
	buffer := make([]byte, readBufferSize+utf8.UTFMax)
	pendingBytes := 0

	for {
		if err := ctx.Err(); err != nil {
			return fmt.Errorf("validate text file: %w", err)
		}

		bytesRead, readErr := file.Read(buffer[pendingBytes:])
		data := buffer[:pendingBytes+bytesRead]
		offset := 0

		for offset < len(data) {
			remaining := data[offset:]
			if !utf8.FullRune(remaining) {
				break
			}

			runeValue, runeSize := utf8.DecodeRune(remaining)
			if runeValue == utf8.RuneError && runeSize == 1 {
				return applicationdocument.ErrInvalidTextContent
			}

			offset += runeSize
		}

		pendingBytes = copy(buffer, data[offset:])

		if errors.Is(readErr, io.EOF) {
			if pendingBytes != 0 {
				return applicationdocument.ErrInvalidTextContent
			}

			return nil
		}
		if readErr != nil {
			return fmt.Errorf("read temporary text file for validation: %w", readErr)
		}
	}
}

// Save 流式保存文件，并返回实际文件元数据。
func (s *LocalStorage) Save(
	ctx context.Context,
	originalName string,
	content io.Reader,
) (applicationdocument.StoredFile, error) {
	if err := ctx.Err(); err != nil {
		return applicationdocument.StoredFile{}, fmt.Errorf(
			"save document file: %w",
			err,
		)
	}

	format, err := resolveFileFormat(originalName)
	if err != nil {
		return applicationdocument.StoredFile{}, err
	}

	if content == nil {
		if format.storageExtension == ".pdf" {
			return applicationdocument.StoredFile{}, applicationdocument.ErrInvalidPDFContent
		}

		return applicationdocument.StoredFile{}, applicationdocument.ErrInvalidTextContent
	}

	contextContent := &contextReader{
		ctx:    ctx,
		reader: content,
	}

	var contentToStore io.Reader = contextContent
	if format.storageExtension == ".pdf" {
		bufferedContent, err := validatePDFHeader(contextContent)
		if err != nil {
			return applicationdocument.StoredFile{}, err
		}

		contentToStore = bufferedContent
	}

	temporaryFile, err := os.CreateTemp(
		s.documentsDir,
		"document-*.tmp",
	)
	if err != nil {
		return applicationdocument.StoredFile{}, fmt.Errorf(
			"create temporary file: %w",
			err,
		)
	}

	temporaryPath := temporaryFile.Name()
	cleanupTemporaryFile := true

	// 无论后续在哪一步失败，都关闭并删除未完成的临时文件。
	defer func() {
		_ = temporaryFile.Close()

		if cleanupTemporaryFile {
			_ = os.Remove(temporaryPath)
		}
	}()

	hasher := sha256.New()

	// 最多读取限制值加一个字节。
	// 多出来的一个字节用于判断文件是否真正超过上限。
	limitedContent := io.LimitReader(
		contentToStore,
		s.maxSizeBytes+1,
	)

	// 每次读取到的数据会同时写入文件和 SHA-256 计算器。
	writer := io.MultiWriter(
		temporaryFile,
		hasher,
	)

	sizeBytes, err := io.Copy(writer, limitedContent)
	if err != nil {
		return applicationdocument.StoredFile{}, fmt.Errorf(
			"write temporary document file: %w",
			err,
		)
	}

	if sizeBytes > s.maxSizeBytes {
		return applicationdocument.StoredFile{}, applicationdocument.ErrFileTooLarge
	}

	// Windows 不能可靠地重命名仍处于打开状态的文件，
	// 所以必须先关闭，再执行 os.Rename。
	if err := temporaryFile.Close(); err != nil {
		return applicationdocument.StoredFile{}, fmt.Errorf(
			"close temporary document file: %w",
			err,
		)
	}

	if format.storageExtension != ".pdf" {
		if err := validateUTF8File(ctx, temporaryPath); err != nil {
			return applicationdocument.StoredFile{}, err
		}
	}

	temporaryName := filepath.Base(temporaryPath)
	finalName := strings.TrimSuffix(
		temporaryName,
		filepath.Ext(temporaryName),
	) + format.storageExtension

	finalPath := filepath.Join(
		s.documentsDir,
		finalName,
	)

	relativePath, err := filepath.Rel(
		s.rootDir,
		finalPath,
	)
	if err != nil {
		return applicationdocument.StoredFile{}, fmt.Errorf(
			"build relative document path: %w",
			err,
		)
	}

	if err := os.Rename(temporaryPath, finalPath); err != nil {
		return applicationdocument.StoredFile{}, fmt.Errorf(
			"finalize document file: %w",
			err,
		)
	}

	// 临时文件已经成功改名，延迟清理不能再删除它。
	cleanupTemporaryFile = false
	return applicationdocument.StoredFile{
		StoragePath: filepath.ToSlash(relativePath),
		MIMEType:    format.mimeType,
		SizeBytes:   sizeBytes,
		SHA256:      hex.EncodeToString(hasher.Sum(nil)),
	}, nil
}

// resolveStoragePath 校验相对存储路径，并转换为本机绝对路径。
//
// 当前存储布局只允许 documents 目录下的单层受支持文件，
// 例如 documents/document-123.pdf 或 documents/document-456.md。
func (s *LocalStorage) resolveStoragePath(storagePath string) (string, error) {
	localPath := filepath.Clean(
		filepath.FromSlash(strings.TrimSpace(storagePath)),
	)

	documentsDirectoryName := filepath.Base(s.documentsDir)
	storageExtension := strings.ToLower(filepath.Ext(localPath))
	allowedExtension := storageExtension == ".pdf" ||
		storageExtension == ".md" ||
		storageExtension == ".txt"

	if !filepath.IsLocal(localPath) ||
		filepath.Dir(localPath) != documentsDirectoryName ||
		!allowedExtension {
		return "", fmt.Errorf(
			"%w: %q",
			ErrInvalidStoragePath,
			storagePath,
		)
	}

	return filepath.Join(s.rootDir, localPath), nil
}

// Delete 根据相对存储路径删除本地文件。
//
// 文件已经不存在时也视为删除成功，
// 这样重复执行清理操作不会产生不必要的错误。
func (s *LocalStorage) Delete(
	ctx context.Context,
	storagePath string,
) error {
	if err := ctx.Err(); err != nil {
		return fmt.Errorf(
			"delete stored document file: %w",
			err,
		)
	}

	absolutePath, err := s.resolveStoragePath(storagePath)
	if err != nil {
		return err
	}

	err = os.Remove(absolutePath)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf(
			"delete stored document file: %w",
			err,
		)
	}

	return nil
}
