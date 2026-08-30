package filestorage

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	pathpkg "path"
	"path/filepath"
	"strings"
	"sync"
	"time"

	applicationdocument "rag-reasoning-platform/backend/internal/application/document"
)

const objectCleanupTimeout = 5 * time.Second

var (
	// ErrObjectClientRequired 表示没有提供具体云厂商或测试用对象客户端。
	ErrObjectClientRequired = errors.New("object storage client is required")

	// ErrObjectStagingDirectoryRequired 表示没有配置本地受控暂存目录。
	ErrObjectStagingDirectoryRequired = errors.New(
		"object storage staging directory is required",
	)

	// ErrObjectNotFound 是对象客户端必须返回的稳定“对象不存在”错误。
	ErrObjectNotFound = errors.New("stored object was not found")

	// ErrObjectReaderRequired 表示客户端成功返回时没有提供对象内容读取器。
	ErrObjectReaderRequired = errors.New("object storage reader is required")
)

// ObjectMetadata 是上传给对象存储的可信元数据。
type ObjectMetadata struct {
	ContentType   string
	ContentLength int64
	SHA256        string
}

// ObjectClient 隔离 COS、S3 或测试 Fake 的具体 SDK。
//
// 实现必须让 DeleteObject 保持幂等，或者在对象不存在时返回
// ErrObjectNotFound，让上层统一转换成幂等成功。
type ObjectClient interface {
	PutObject(
		ctx context.Context,
		key string,
		content io.Reader,
		metadata ObjectMetadata,
	) error
	GetObject(ctx context.Context, key string) (io.ReadCloser, error)
	DeleteObject(ctx context.Context, key string) error
}

// ObjectStorage 使用对象客户端保存正式文件，并使用本机暂存目录完成上传校验
// 和 Python 子进程所需的临时下载。
type ObjectStorage struct {
	client       ObjectClient
	stagingDir   string
	maxSizeBytes int64
}

var _ applicationdocument.FileStorage = (*ObjectStorage)(nil)

// NewObjectStorage 创建对象存储适配器。
func NewObjectStorage(
	client ObjectClient,
	stagingDirectory string,
	maxSizeBytes int64,
) (*ObjectStorage, error) {
	if client == nil {
		return nil, ErrObjectClientRequired
	}
	stagingDirectory = strings.TrimSpace(stagingDirectory)
	if stagingDirectory == "" {
		return nil, ErrObjectStagingDirectoryRequired
	}
	if maxSizeBytes <= 0 {
		return nil, ErrInvalidMaxFileSize
	}

	absoluteStagingDirectory, err := filepath.Abs(stagingDirectory)
	if err != nil {
		return nil, fmt.Errorf("resolve object staging directory: %w", err)
	}
	if err := os.MkdirAll(absoluteStagingDirectory, 0o700); err != nil {
		return nil, fmt.Errorf("create object staging directory: %w", err)
	}

	return &ObjectStorage{
		client:       client,
		stagingDir:   absoluteStagingDirectory,
		maxSizeBytes: maxSizeBytes,
	}, nil
}

// Save 在本地受控目录完成统一校验，再把文件流式写入对象客户端。
func (s *ObjectStorage) Save(
	ctx context.Context,
	originalName string,
	content io.Reader,
) (storedFile applicationdocument.StoredFile, saveErr error) {
	staged, err := stageDocumentUpload(
		ctx,
		s.stagingDir,
		s.maxSizeBytes,
		originalName,
		content,
	)
	if err != nil {
		return applicationdocument.StoredFile{}, err
	}

	objectKey := ""
	objectPersisted := false
	defer func() {
		cleanupErr := staged.Remove()
		if cleanupErr == nil {
			return
		}

		// 正式对象已经写入但本地暂存清理失败时，不返回看似成功的结果。
		// 删除正式对象可以让 UploadService 安全重试，避免数据库没有记录却
		// 留下无法定位的孤立对象。
		var objectCleanupErr error
		if objectPersisted {
			objectCleanupErr = s.compensateObject(objectKey)
			storedFile = applicationdocument.StoredFile{}
		}
		saveErr = errors.Join(saveErr, cleanupErr, objectCleanupErr)
	}()

	objectKey, err = newDocumentObjectKey(staged.format.storageExtension)
	if err != nil {
		return applicationdocument.StoredFile{}, err
	}

	stagedFile, err := os.Open(staged.path)
	if err != nil {
		return applicationdocument.StoredFile{}, fmt.Errorf(
			"open staged document for object upload: %w",
			err,
		)
	}
	putErr := s.client.PutObject(
		ctx,
		objectKey,
		stagedFile,
		ObjectMetadata{
			ContentType:   staged.format.mimeType,
			ContentLength: staged.sizeBytes,
			SHA256:        staged.sha256,
		},
	)
	closeErr := stagedFile.Close()
	if putErr != nil || closeErr != nil {
		cleanupErr := s.compensateObject(objectKey)
		return applicationdocument.StoredFile{}, errors.Join(
			wrapOptionalError("put document object", putErr),
			wrapOptionalError("close staged document after object upload", closeErr),
			cleanupErr,
		)
	}

	objectPersisted = true
	return applicationdocument.StoredFile{
		StoragePath: objectKey,
		MIMEType:    staged.format.mimeType,
		SizeBytes:   staged.sizeBytes,
		SHA256:      staged.sha256,
	}, nil
}

// Open 流式打开一个对象；调用方负责关闭返回值。
func (s *ObjectStorage) Open(
	ctx context.Context,
	storagePath string,
) (io.ReadCloser, error) {
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("open stored document object: %w", err)
	}

	objectKey, err := normalizeDocumentObjectKey(storagePath)
	if err != nil {
		return nil, err
	}
	reader, err := s.client.GetObject(ctx, objectKey)
	if err != nil {
		if errors.Is(err, ErrObjectNotFound) {
			return nil, fmt.Errorf(
				"open stored document object: %w",
				errors.Join(os.ErrNotExist, err),
			)
		}
		return nil, fmt.Errorf("open stored document object: %w", err)
	}
	if reader == nil {
		return nil, ErrObjectReaderRequired
	}

	return &contextReadCloser{
		ctx:    ctx,
		reader: reader,
		closer: reader,
	}, nil
}

// Delete 幂等删除正式对象。
func (s *ObjectStorage) Delete(ctx context.Context, storagePath string) error {
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("delete stored document object: %w", err)
	}

	objectKey, err := normalizeDocumentObjectKey(storagePath)
	if err != nil {
		return err
	}
	err = s.client.DeleteObject(ctx, objectKey)
	if errors.Is(err, ErrObjectNotFound) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("delete stored document object: %w", err)
	}

	return nil
}

// Materialize 把远端对象下载为本次 Python 调用使用的本地临时文件。
func (s *ObjectStorage) Materialize(
	ctx context.Context,
	storagePath string,
) (localPath string, release func() error, err error) {
	objectKey, err := normalizeDocumentObjectKey(storagePath)
	if err != nil {
		return "", nil, err
	}
	if err := ctx.Err(); err != nil {
		return "", nil, fmt.Errorf("materialize document object: %w", err)
	}

	source, err := s.Open(ctx, objectKey)
	if err != nil {
		return "", nil, err
	}
	sourceOpen := true

	extension := pathpkg.Ext(objectKey)
	temporaryFile, err := os.CreateTemp(
		s.stagingDir,
		"materialized-*"+extension,
	)
	if err != nil {
		_ = source.Close()
		return "", nil, fmt.Errorf("create materialized document file: %w", err)
	}
	temporaryPath := temporaryFile.Name()
	temporaryFileOpen := true
	keepTemporaryFile := false
	defer func() {
		if temporaryFileOpen {
			_ = temporaryFile.Close()
		}
		if sourceOpen {
			_ = source.Close()
		}
		if !keepTemporaryFile {
			_ = os.Remove(temporaryPath)
		}
	}()

	sizeBytes, copyErr := io.Copy(
		temporaryFile,
		io.LimitReader(source, s.maxSizeBytes+1),
	)
	temporaryCloseErr := temporaryFile.Close()
	temporaryFileOpen = temporaryCloseErr != nil
	sourceCloseErr := source.Close()
	sourceOpen = sourceCloseErr != nil
	if copyErr != nil || temporaryCloseErr != nil || sourceCloseErr != nil {
		return "", nil, errors.Join(
			wrapOptionalError("download document object", copyErr),
			wrapOptionalError("close materialized document file", temporaryCloseErr),
			wrapOptionalError("close downloaded document object", sourceCloseErr),
		)
	}
	if sizeBytes > s.maxSizeBytes {
		return "", nil, applicationdocument.ErrFileTooLarge
	}

	keepTemporaryFile = true
	var once sync.Once
	var releaseErr error
	release = func() error {
		once.Do(func() {
			releaseErr = os.Remove(temporaryPath)
			if errors.Is(releaseErr, os.ErrNotExist) {
				releaseErr = nil
			}
			if releaseErr != nil {
				releaseErr = fmt.Errorf(
					"remove materialized document object: %w",
					releaseErr,
				)
			}
		})
		return releaseErr
	}

	return temporaryPath, release, nil
}

func newDocumentObjectKey(extension string) (string, error) {
	randomBytes := make([]byte, 16)
	if _, err := rand.Read(randomBytes); err != nil {
		return "", fmt.Errorf("generate document object key: %w", err)
	}

	return "documents/document-" + hex.EncodeToString(randomBytes) + extension, nil
}

func normalizeDocumentObjectKey(storagePath string) (string, error) {
	objectKey := strings.TrimSpace(storagePath)
	cleanedKey := pathpkg.Clean(objectKey)
	extension := strings.ToLower(pathpkg.Ext(cleanedKey))
	allowedExtension := extension == ".pdf" || extension == ".md" || extension == ".txt"

	if objectKey == "" ||
		objectKey != storagePath ||
		strings.Contains(objectKey, "\\") ||
		pathpkg.IsAbs(cleanedKey) ||
		cleanedKey != objectKey ||
		pathpkg.Dir(cleanedKey) != "documents" ||
		!allowedExtension {
		return "", fmt.Errorf("%w: %q", ErrInvalidStoragePath, storagePath)
	}

	return cleanedKey, nil
}

func (s *ObjectStorage) compensateObject(objectKey string) error {
	if strings.TrimSpace(objectKey) == "" {
		return nil
	}

	cleanupContext, cancel := context.WithTimeout(
		context.Background(),
		objectCleanupTimeout,
	)
	defer cancel()

	err := s.client.DeleteObject(cleanupContext, objectKey)
	if errors.Is(err, ErrObjectNotFound) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("clean up document object after failed save: %w", err)
	}

	return nil
}

func wrapOptionalError(operation string, err error) error {
	if err == nil {
		return nil
	}
	return fmt.Errorf("%s: %w", operation, err)
}
