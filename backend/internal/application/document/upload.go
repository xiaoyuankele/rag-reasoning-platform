package document

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	accessdomain "rag-reasoning-platform/backend/internal/domain/access"
	documentdomain "rag-reasoning-platform/backend/internal/domain/document"
)

// UploadInput 表示上传文档用例接收到的数据。
//
// Content 使用 io.Reader，而不是 []byte。
// 这样应用层可以一边读取一边保存文件，避免把整个文件一次性加载进内存。
type UploadInput struct {
	OriginalName string
	Content      io.Reader
}

// UploadResult 表示上传用例的最终结果。
//
// Duplicate 为 true 时，Document 是该用户之前上传的相同内容；本次临时保存的
// 物理文件已经被补偿删除。这个结果不是错误，上层可以向用户提示“已存在”。
type UploadResult struct {
	Document  documentdomain.Document
	Duplicate bool
}

const uploadCleanupTimeout = 5 * time.Second

var (
	// ErrOriginalNameRequired 表示上传时没有提供有效的原始文件名。
	ErrOriginalNameRequired = errors.New("original file name is required")

	// ErrFileContentRequired 表示上传时没有提供文件内容。
	ErrFileContentRequired = errors.New("file content is required")

	// ErrFileTooLarge 表示上传文件超过应用允许的最大大小。
	ErrFileTooLarge = errors.New("file exceeds maximum allowed size")

	// ErrInvalidPDFContent 表示上传内容不具有合法的 PDF 文件头。
	ErrInvalidPDFContent = errors.New("file content is not a PDF")

	// ErrUnsupportedFileType 表示文件扩展名不在上传白名单中。
	ErrUnsupportedFileType = errors.New(
		"file type must be PDF, Markdown, or plain text",
	)

	// ErrInvalidTextContent 表示文本文件不是合法的 UTF-8 文本。
	ErrInvalidTextContent = errors.New(
		"text file content must be valid UTF-8",
	)
)

// UploadService 编排文件保存和文档元数据入库流程。
type UploadService struct {
	repository documentdomain.ScopedCreateOrGetter
	storage    UploadFileStorage
}

// NewUploadService 创建文档上传应用服务。
//
// repository 负责数据库元数据，storage 负责文件内容，
// 两个依赖都通过构造函数传入。
func NewUploadService(
	repository documentdomain.ScopedCreateOrGetter,
	storage UploadFileStorage,
) *UploadService {
	return &UploadService{
		repository: repository,
		storage:    storage,
	}
}

// Upload 保存文件，并创建对应的文档数据库记录。
func (s *UploadService) Upload(
	ctx context.Context,
	scope accessdomain.OwnerScope,
	input UploadInput,
) (UploadResult, error) {
	originalName := strings.TrimSpace(input.OriginalName)
	if originalName == "" {
		return UploadResult{}, ErrOriginalNameRequired
	}

	if input.Content == nil {
		return UploadResult{}, ErrFileContentRequired
	}

	storedFile, err := s.storage.Save(ctx, originalName, input.Content)

	if err != nil {
		return UploadResult{}, fmt.Errorf(
			"save uploaded file: %w",
			err,
		)
	}

	createResult, err := s.repository.CreateOrGetBySHA256(
		ctx,
		scope,
		documentdomain.CreateInput{
			OriginalName: originalName,
			StoragePath:  storedFile.StoragePath,
			MIMEType:     storedFile.MIMEType,
			SizeBytes:    storedFile.SizeBytes,
			SHA256:       storedFile.SHA256,
		},
	)

	if err != nil {
		deleteErr := s.deleteStoredFileForCleanup(ctx, storedFile.StoragePath)
		if deleteErr != nil {
			return UploadResult{}, fmt.Errorf(
				"create document record: %w; delete stored file: %w",
				err,
				deleteErr,
			)
		}

		return UploadResult{}, fmt.Errorf("create document record: %w", err)
	}

	if !createResult.Created {
		// Save 必须先完整读取文件才能得到可信 SHA-256，所以查重发生在保存后。
		// 命中已有记录时删除本次新文件，确保“一个用户 + 一份内容”只留下
		// 一条数据库记录和一个物理文件。
		if err := s.deleteStoredFileForCleanup(ctx, storedFile.StoragePath); err != nil {
			return UploadResult{}, fmt.Errorf(
				"delete duplicate stored file: %w",
				err,
			)
		}

		return UploadResult{
			Document:  createResult.Document,
			Duplicate: true,
		}, nil
	}

	return UploadResult{Document: createResult.Document}, nil
}

// deleteStoredFileForCleanup 使用独立的短生命周期执行补偿删除。
// HTTP 请求可能在数据库返回前被取消；清理不能因此直接放弃，否则会遗留孤立文件。
func (s *UploadService) deleteStoredFileForCleanup(
	parent context.Context,
	storagePath string,
) error {
	cleanupContext, cancel := context.WithTimeout(
		context.WithoutCancel(parent),
		uploadCleanupTimeout,
	)
	defer cancel()

	return s.storage.Delete(cleanupContext, storagePath)
}
