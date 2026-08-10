package document

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"

	documentdomain "rag-reasoning-platform/backend/internal/domain/document"
)

const (
	// MaxSearchQueryRunes 限制一次关键词查询最多包含的 Unicode 字符数。
	// 使用字符数而不是字节数，避免中文等多字节字符受到不公平限制。
	MaxSearchQueryRunes = 200
)

var (
	// ErrSearchQueryRequired 表示查询词缺失或只包含空白。
	ErrSearchQueryRequired = errors.New("search query is required")

	// ErrSearchQueryInvalidUTF8 表示查询词不是合法 UTF-8 文本。
	ErrSearchQueryInvalidUTF8 = errors.New(
		"search query must be valid UTF-8",
	)

	// ErrSearchQueryTooLong 表示查询词超过当前接口允许的字符数。
	ErrSearchQueryTooLong = errors.New(
		"search query must not exceed 200 characters",
	)
)

// SearchInput 是上层调用关键词检索用例时提供的数据。
type SearchInput struct {
	Query      string
	DocumentID *int64
	Page       int64
	PageSize   int64
}

// SearchOutput 是关键词检索用例返回给 HTTP 层的结果。
type SearchOutput struct {
	Query      string
	Hits       []documentdomain.SearchHit
	Page       int64
	PageSize   int64
	Total      int64
	TotalPages int64
}

// SearchService 编排跨文档关键词检索用例。
//
// 它负责业务输入校验和分页换算，但不处理 HTTP，也不知道仓储最终使用
// PostgreSQL 字面匹配、全文索引还是其他搜索引擎。
type SearchService struct {
	searcher documentdomain.ChunkSearcher
}

// NewSearchService 创建关键词检索应用服务。
func NewSearchService(
	searcher documentdomain.ChunkSearcher,
) *SearchService {
	return &SearchService{
		searcher: searcher,
	}
}

// Search 校验并规范化查询词，将页码换算为 limit/offset，再调用仓储。
func (s *SearchService) Search(
	ctx context.Context,
	input SearchInput,
) (SearchOutput, error) {
	query := strings.TrimSpace(input.Query)
	if query == "" {
		return SearchOutput{}, ErrSearchQueryRequired
	}
	if !utf8.ValidString(query) {
		return SearchOutput{}, ErrSearchQueryInvalidUTF8
	}
	if utf8.RuneCountInString(query) > MaxSearchQueryRunes {
		return SearchOutput{}, ErrSearchQueryTooLong
	}
	if input.DocumentID != nil && *input.DocumentID <= 0 {
		return SearchOutput{}, ErrInvalidID
	}

	// 复用文档列表已经建立的分页边界，使同一个 API 中的分页规则一致。
	if input.Page <= 0 {
		return SearchOutput{}, ErrInvalidPage
	}
	if input.PageSize <= 0 || input.PageSize > MaxPageSize {
		return SearchOutput{}, ErrInvalidPageSize
	}

	offset := (input.Page - 1) * input.PageSize
	result, err := s.searcher.Search(
		ctx,
		documentdomain.SearchOptions{
			Query:      query,
			DocumentID: input.DocumentID,
			Limit:      input.PageSize,
			Offset:     offset,
		},
	)
	if err != nil {
		// %w 保留底层错误链，HTTP 层只返回安全的通用错误，日志层仍可诊断根因。
		return SearchOutput{}, fmt.Errorf(
			"search document chunks: %w",
			err,
		)
	}

	// 空结果必须编码为 JSON []，因此 Application 先把 nil 规范化为空切片。
	hits := result.Hits
	if hits == nil {
		hits = make([]documentdomain.SearchHit, 0)
	}

	totalPages := result.Total / input.PageSize
	if result.Total%input.PageSize != 0 {
		totalPages++
	}

	return SearchOutput{
		Query:      query,
		Hits:       hits,
		Page:       input.Page,
		PageSize:   input.PageSize,
		Total:      result.Total,
		TotalPages: totalPages,
	}, nil
}
