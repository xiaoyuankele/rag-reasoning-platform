package document

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"

	accessdomain "rag-reasoning-platform/backend/internal/domain/access"
	documentdomain "rag-reasoning-platform/backend/internal/domain/document"
)

var (
	// ErrInvalidPreflightSHA256 表示客户端提供的内容指纹不是规范的小写 SHA-256。
	ErrInvalidPreflightSHA256 = errors.New(
		"SHA-256 must contain exactly 64 lowercase hexadecimal characters",
	)

	// ErrInvalidPreflightSize 表示客户端提供的文件字节数不是正整数。
	ErrInvalidPreflightSize = errors.New("file size must be positive")
)

// PreflightInput 是上传文件正文前用于查重的最小输入。
// 原始文件名不参与去重，因此不进入该用例。
type PreflightInput struct {
	SHA256    string
	SizeBytes int64
}

// PreflightResult 表示当前用户是否已经拥有二进制内容相同的文档。
// Exists 为 false 时 Document 保持零值，上层必须依据 Exists 决定是否读取它。
type PreflightResult struct {
	Exists   bool
	Document documentdomain.Document
}

// PreflightService 编排上传前查重；它不读取文件，也不创建数据库记录。
type PreflightService struct {
	repository       documentdomain.ScopedContentFinder
	maxFileSizeBytes int64
}

// NewPreflightService 创建上传前查重应用服务。
func NewPreflightService(
	repository documentdomain.ScopedContentFinder,
	maxFileSizeBytes int64,
) *PreflightService {
	return &PreflightService{
		repository:       repository,
		maxFileSizeBytes: maxFileSizeBytes,
	}
}

// Check 校验客户端指纹并在当前用户范围内查询已有文档。
//
// 客户端哈希只用于节省重复上传流量。真正上传时，UploadService 仍会根据
// 后端实际读取的字节重新计算哈希，并依靠数据库唯一约束处理并发竞争。
func (s *PreflightService) Check(
	ctx context.Context,
	scope accessdomain.OwnerScope,
	input PreflightInput,
) (PreflightResult, error) {
	if !isCanonicalSHA256(input.SHA256) {
		return PreflightResult{}, ErrInvalidPreflightSHA256
	}
	if input.SizeBytes <= 0 {
		return PreflightResult{}, ErrInvalidPreflightSize
	}
	if input.SizeBytes > s.maxFileSizeBytes {
		return PreflightResult{}, ErrFileTooLarge
	}

	foundDocument, err := s.repository.FindBySHA256AndSize(
		ctx,
		scope,
		input.SHA256,
		input.SizeBytes,
	)
	if errors.Is(err, documentdomain.ErrNotFound) {
		return PreflightResult{Exists: false}, nil
	}
	if err != nil {
		return PreflightResult{}, fmt.Errorf(
			"check document upload preflight: %w",
			err,
		)
	}

	return PreflightResult{
		Exists:   true,
		Document: foundDocument,
	}, nil
}

// isCanonicalSHA256 只接受 Web Crypto 常用的 64 位小写十六进制形式。
func isCanonicalSHA256(value string) bool {
	if len(value) != 64 || value != strings.ToLower(value) {
		return false
	}

	decoded, err := hex.DecodeString(value)
	return err == nil && len(decoded) == 32
}
