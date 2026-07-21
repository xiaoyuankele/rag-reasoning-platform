package document

import (
	"context"
	"errors"
	"fmt"

	documentdomain "rag-reasoning-platform/backend/internal/domain/document"
)

const (
	// DefaultPage 是 HTTP 请求没有提供 page 时使用的默认页码。
	DefaultPage int64 = 1

	// DefaultPageSize 是 HTTP 请求没有提供 page_size 时使用的默认每页数量。
	DefaultPageSize int64 = 20

	// MaxPageSize 防止客户端一次查询过多数据。
	MaxPageSize int64 = 100
)

var (
	// ErrInvalidPage 表示页码不是正整数。
	ErrInvalidPage = errors.New("page must be positive")

	// ErrInvalidPageSize 表示每页数量不在允许范围内。
	ErrInvalidPageSize = errors.New("page size must be between 1 and 100")
)

// ListInput 是调用列表应用服务时提供的分页参数。
type ListInput struct {
	Page     int64
	PageSize int64
}

// ListOutput 是列表应用服务返回给上层的结果。
type ListOutput struct {
	Documents  []documentdomain.Document
	Page       int64
	PageSize   int64
	Total      int64
	TotalPages int64
}

// ListService 编排查询文档列表的应用用例。
//
// 它只依赖 Lister，不需要创建文档或按 ID 查询的能力。
type ListService struct {
	repository documentdomain.Lister
}

// NewListService 创建文档列表应用服务。
func NewListService(repository documentdomain.Lister) *ListService {
	return &ListService{
		repository: repository,
	}
}

// List 校验分页参数，将 page/page_size 转换成 limit/offset，
// 调用仓储并计算总页数。
func (s *ListService) List(
	ctx context.Context,
	input ListInput,
) (ListOutput, error) {
	if input.Page <= 0 {
		return ListOutput{}, ErrInvalidPage
	}

	if input.PageSize <= 0 || input.PageSize > MaxPageSize {
		return ListOutput{}, ErrInvalidPageSize
	}

	// 第 1 页跳过 0 条，第 2 页跳过 PageSize 条，以此类推。
	offset := (input.Page - 1) * input.PageSize

	result, err := s.repository.List(
		ctx,
		documentdomain.ListOptions{
			Limit:  input.PageSize,
			Offset: offset,
		},
	)
	if err != nil {
		// %w 保留原始仓储错误，使 HTTP 层或日志层仍可用 errors.Is 识别。
		return ListOutput{}, fmt.Errorf(
			"list documents: %w",
			err,
		)
	}

	// 使用整数运算完成“向上取整”，避免引入浮点数误差。
	totalPages := result.Total / input.PageSize
	if result.Total%input.PageSize != 0 {
		totalPages++
	}

	return ListOutput{
		Documents:  result.Documents,
		Page:       input.Page,
		PageSize:   input.PageSize,
		Total:      result.Total,
		TotalPages: totalPages,
	}, nil
}
