// Package filestorage 提供文件存储接口的本地磁盘实现。
package filestorage

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	applicationdocument "rag-reasoning-platform/backend/internal/application/document"
)

var (
	// ErrRootDirectoryRequired 表示没有配置文件存储根目录。
	ErrRootDirectoryRequired = errors.New("root directory is required")

	// ErrInvalidMaxFileSize 表示最大文件大小不是正整数。
	ErrInvalidMaxFileSize = errors.New("maximum file size must be positive")

	// ErrInvalidStoragePath 表示存储路径不属于允许的文档目录。
	ErrInvalidStoragePath = errors.New("invalid storage path")
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

// contextReadCloser 把上下文取消检查和关闭能力组合进读取器。
// 它同时实现 io.Reader 与 io.Closer，因此也实现 io.ReadCloser。
type contextReadCloser struct {
	ctx    context.Context
	reader io.Reader
	closer io.Closer
}

// Read 在每次读取前检查调用方是否已经取消任务。
func (r *contextReadCloser) Read(p []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}

	return r.reader.Read(p)
}

// Close 把资源关闭操作委托给底层文件。
func (r *contextReadCloser) Close() error {
	return r.closer.Close()
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

	staged, err := stageDocumentUpload(
		ctx,
		s.documentsDir,
		s.maxSizeBytes,
		originalName,
		content,
	)
	if err != nil {
		return applicationdocument.StoredFile{}, err
	}
	defer func() {
		_ = staged.Remove()
	}()

	temporaryName := filepath.Base(staged.path)
	finalName := strings.TrimSuffix(
		temporaryName,
		filepath.Ext(temporaryName),
	) + staged.format.storageExtension

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

	if err := os.Rename(staged.path, finalPath); err != nil {
		return applicationdocument.StoredFile{}, fmt.Errorf(
			"finalize document file: %w",
			err,
		)
	}

	return applicationdocument.StoredFile{
		StoragePath: filepath.ToSlash(relativePath),
		MIMEType:    staged.format.mimeType,
		SizeBytes:   staged.sizeBytes,
		SHA256:      staged.sha256,
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

// ResolveAbsolutePath 把数据库中保存的受控相对路径转换为本机绝对路径。
//
// PythonProcessor 需要把物理文件路径交给 Python 子进程，但不能直接信任
// Document.StoragePath。本方法复用 LocalStorage 的目录和扩展名校验，确保只有
// documents 目录下由后端管理的文件能够跨越进程边界。
func (s *LocalStorage) ResolveAbsolutePath(storagePath string) (string, error) {
	absolutePath, err := s.resolveStoragePath(storagePath)
	if err != nil {
		return "", fmt.Errorf(
			"resolve stored document absolute path: %w",
			err,
		)
	}

	return absolutePath, nil
}

// Materialize 把不透明存储键准备成 Python 子进程可读取的本地绝对路径。
//
// LocalStorage 的文件原本就在本机，因此不需要复制；返回的 release 是空清理
// 函数。未来对象存储实现可以使用同一契约下载临时文件，并在 release 中删除。
func (s *LocalStorage) Materialize(
	ctx context.Context,
	storagePath string,
) (localPath string, release func() error, err error) {
	if err := ctx.Err(); err != nil {
		return "", nil, fmt.Errorf(
			"materialize stored document file: %w",
			err,
		)
	}

	absolutePath, err := s.ResolveAbsolutePath(storagePath)
	if err != nil {
		return "", nil, err
	}

	return absolutePath, func() error { return nil }, nil
}

// Open 根据相对存储路径安全地打开文档文件。
//
// 返回的 io.ReadCloser 由调用方负责关闭。
func (s *LocalStorage) Open(
	ctx context.Context,
	storagePath string,
) (io.ReadCloser, error) {
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf(
			"open stored document file: %w",
			err,
		)
	}

	absolutePath, err := s.resolveStoragePath(storagePath)
	if err != nil {
		return nil, err
	}

	file, err := os.Open(absolutePath)
	if err != nil {
		return nil, fmt.Errorf(
			"open stored document file: %w",
			err,
		)
	}

	return &contextReadCloser{
		ctx:    ctx,
		reader: file,
		closer: file,
	}, nil
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
