package document

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"

	documentdomain "rag-reasoning-platform/backend/internal/domain/document"
)

// UploadInput 表示上传文档用例接收到的数据。
//
// Content 使用 io.Reader，而不是 []byte。
// 这样应用层可以一边读取一边保存文件，避免把整个 PDF 一次性加载进内存。
type UploadInput struct {
	OriginalName string
	Content      io.Reader
}

// StoredFile 表示文件存储成功后得到的结果。
//
// 文件大小和 SHA-256 由文件存储实现根据实际读取到的内容计算，
// 不能直接相信浏览器传来的文件大小和哈希值。
type StoredFile struct {
	StoragePath string
	SizeBytes   int64
	SHA256      string
}

// FileStorage 定义应用层保存文件时需要的最小能力。
//
// 应用层只依赖这个接口，不关心文件最终保存在本地磁盘、
// 对象存储还是其他位置。
type FileStorage interface {
	// Save 流式保存文件，并返回最终存储路径、文件大小和 SHA-256。
	//
	// 文件超限时返回 ErrFileTooLarge，内容不是 PDF 时返回
	// ErrInvalidPDFContent。
	Save(ctx context.Context, originalName string, content io.Reader) (StoredFile, error)

	// Delete 删除已经保存的文件。
	//
	// 如果文件保存成功、但数据库记录创建失败，
	// UploadService 将调用 Delete，避免留下没有数据库记录的孤立文件。
	Delete(ctx context.Context, storagePath string) error
}

const pdfMIMEType = "application/pdf"

var (
	// ErrOriginalNameRequired 表示上传时没有提供有效的原始文件名。
	ErrOriginalNameRequired = errors.New("original file name is required")

	// ErrFileContentRequired 表示上传时没有提供文件内容。
	ErrFileContentRequired = errors.New("file content is required")

	// ErrFileTooLarge 表示上传文件超过应用允许的最大大小。
	ErrFileTooLarge = errors.New("file exceeds maximum allowed size")

	// ErrInvalidPDFContent 表示上传内容不具有合法的 PDF 文件头。
	ErrInvalidPDFContent = errors.New("file content is not a PDF")
)

// UploadService 编排文件保存和文档元数据入库流程。
type UploadService struct {
	repository documentdomain.Repository
	storage    FileStorage
}

// NewUploadService 创建文档上传应用服务。
//
// repository 负责数据库元数据，storage 负责文件内容，
// 两个依赖都通过构造函数传入。
func NewUploadService(repository documentdomain.Repository, storage FileStorage) *UploadService {
	return &UploadService{
		repository: repository,
		storage:    storage,
	}
}

// Upload 保存文件，并创建对应的文档数据库记录。
func (s *UploadService) Upload(ctx context.Context, input UploadInput) (documentdomain.Document, error) {
	originalName := strings.TrimSpace(input.OriginalName)
	if originalName == "" {
		return documentdomain.Document{}, ErrOriginalNameRequired
	}

	if input.Content == nil {
		return documentdomain.Document{}, ErrFileContentRequired
	}

	storedFile, err := s.storage.Save(ctx, originalName, input.Content)

	if err != nil {
		return documentdomain.Document{}, fmt.Errorf(
			"save uploaded file: %w",
			err,
		)
	}

	createdDocument, err := s.repository.Create(ctx, documentdomain.CreateInput{
		OriginalName: originalName,
		StoragePath:  storedFile.StoragePath,
		MIMEType:     pdfMIMEType,
		SizeBytes:    storedFile.SizeBytes,
		SHA256:       storedFile.SHA256,
	})

	if err != nil {
		deleteErr := s.storage.Delete(ctx, storedFile.StoragePath)
		if deleteErr != nil {
			return documentdomain.Document{}, fmt.Errorf(
				"create document record: %w; delete stored file: %w",
				err,
				deleteErr,
			)
		}

		return documentdomain.Document{}, fmt.Errorf("create document record: %w", err)
	}

	return createdDocument, nil
}
