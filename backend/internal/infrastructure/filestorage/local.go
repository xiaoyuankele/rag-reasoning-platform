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

	applicationdocument "rag-reasoning-platform/backend/internal/application/document"
)

const pdfHeader = "%PDF-"

var (
	// ErrRootDirectoryRequired 表示没有配置文件存储根目录。
	ErrRootDirectoryRequired = errors.New("root directory is required")

	// ErrInvalidMaxFileSize 表示最大文件大小不是正整数。
	ErrInvalidMaxFileSize = errors.New("maximum file size must be positive")

	// ErrFileTooLarge 表示文件内容超过允许的最大字节数。
	ErrFileTooLarge = errors.New("file exceeds maximum allowed size")

	// ErrInvalidStoragePath 表示存储路径不属于允许的文档目录。
	ErrInvalidStoragePath = errors.New("invalid storage path")

	// ErrInvalidPDFContent 表示文件不是有效的 PDF 文件。
	ErrInvalidPDFContent = errors.New("file content is not a PDF")
)

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
		return nil, ErrInvalidPDFContent
	}

	bufferedContent := bufio.NewReader(content)

	header, err := bufferedContent.Peek(len(pdfHeader))
	if errors.Is(err, io.EOF) {
		return nil, ErrInvalidPDFContent
	}
	if err != nil {
		return nil, fmt.Errorf(
			"read PDF header: %w",
			err,
		)
	}

	if !bytes.Equal(header, []byte(pdfHeader)) {
		return nil, ErrInvalidPDFContent
	}

	return bufferedContent, nil
}

// Save 流式保存文件，并返回实际文件元数据。
func (s *LocalStorage) Save(
	ctx context.Context,
	_ string,
	content io.Reader,
) (applicationdocument.StoredFile, error) {
	if err := ctx.Err(); err != nil {
		return applicationdocument.StoredFile{}, fmt.Errorf(
			"save document file: %w",
			err,
		)
	}

	if content == nil {
		return applicationdocument.StoredFile{}, ErrInvalidPDFContent
	}

	contextContent := &contextReader{
		ctx:    ctx,
		reader: content,
	}

	bufferedContent, err := validatePDFHeader(contextContent)
	if err != nil {
		return applicationdocument.StoredFile{}, err
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
		bufferedContent,
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
		return applicationdocument.StoredFile{}, ErrFileTooLarge
	}

	// Windows 不能可靠地重命名仍处于打开状态的文件，
	// 所以必须先关闭，再执行 os.Rename。
	if err := temporaryFile.Close(); err != nil {
		return applicationdocument.StoredFile{}, fmt.Errorf(
			"close temporary document file: %w",
			err,
		)
	}

	temporaryName := filepath.Base(temporaryPath)
	finalName := strings.TrimSuffix(
		temporaryName,
		filepath.Ext(temporaryName),
	) + ".pdf"

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
		SizeBytes:   sizeBytes,
		SHA256:      hex.EncodeToString(hasher.Sum(nil)),
	}, nil
}

// resolveStoragePath 校验相对存储路径，并转换为本机绝对路径。
//
// 当前存储布局只允许 documents 目录下的单层 PDF 文件，
// 例如 documents/document-123.pdf。
func (s *LocalStorage) resolveStoragePath(storagePath string) (string, error) {
	localPath := filepath.Clean(
		filepath.FromSlash(strings.TrimSpace(storagePath)),
	)

	documentsDirectoryName := filepath.Base(s.documentsDir)

	if !filepath.IsLocal(localPath) ||
		filepath.Dir(localPath) != documentsDirectoryName ||
		!strings.EqualFold(filepath.Ext(localPath), ".pdf") {
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
