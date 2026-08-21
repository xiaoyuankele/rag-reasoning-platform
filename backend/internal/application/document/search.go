package document

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"

	accessdomain "rag-reasoning-platform/backend/internal/domain/access"
	documentdomain "rag-reasoning-platform/backend/internal/domain/document"
)

const (
	// MaxSearchQueryRunes 限制一次关键词查询最多包含的 Unicode 字符数。
	// 使用字符数而不是字节数，避免中文等多字节字符受到不公平限制。
	MaxSearchQueryRunes = 200

	// MinSearchTermCount 和 MaxSearchTermCount 限制一次多关键词检索的规模。
	MinSearchTermCount = 2
	MaxSearchTermCount = 8

	// MaxSearchTermRunes 限制单个关键词长度；全部关键词合计仍受
	// MaxSearchQueryRunes 保护，避免构造过大的数据库查询条件。
	MaxSearchTermRunes = 100
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

	// ErrSearchInputConflict 表示完整短语 q 和多关键词 term 同时出现。
	ErrSearchInputConflict = errors.New("q and term cannot be used together")

	// ErrInvalidSearchTermCount 表示规范化后的关键词数量不在 2～8 个范围内。
	ErrInvalidSearchTermCount = errors.New("term must contain between 2 and 8 unique keywords")

	// ErrSearchTermInvalidUTF8 表示至少一个关键词不是合法 UTF-8 文本。
	ErrSearchTermInvalidUTF8 = errors.New("every term must be valid UTF-8")

	// ErrSearchTermTooLong 表示单个关键词超过 100 个 Unicode 字符。
	ErrSearchTermTooLong = errors.New("each term must not exceed 100 characters")

	// ErrSearchTermsTooLong 表示全部关键词合计超过 200 个 Unicode 字符。
	ErrSearchTermsTooLong = errors.New("all terms together must not exceed 200 characters")

	// ErrInvalidSearchOperator 表示 operator 不是 all 或 any。
	ErrInvalidSearchOperator = errors.New("operator must be all or any")

	// ErrInvalidSearchWithin 表示请求了当前版本尚不支持的位置范围。
	ErrInvalidSearchWithin = errors.New("within must be chunk")
)

// SearchInput 是上层调用关键词检索用例时提供的数据。
type SearchInput struct {
	Query      string
	Terms      []string
	Operator   documentdomain.SearchOperator
	Within     documentdomain.SearchWithin
	DocumentID *int64
	Page       int64
	PageSize   int64
}

// SearchOutput 是关键词检索用例返回给 HTTP 层的结果。
type SearchOutput struct {
	Query      string
	Terms      []string
	Operator   documentdomain.SearchOperator
	Within     documentdomain.SearchWithin
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
	searcher documentdomain.ScopedChunkSearcher
}

// NewSearchService 创建关键词检索应用服务。
func NewSearchService(
	searcher documentdomain.ScopedChunkSearcher,
) *SearchService {
	return &SearchService{
		searcher: searcher,
	}
}

// Search 校验并规范化查询词，将页码换算为 limit/offset，再调用仓储。
func (s *SearchService) Search(
	ctx context.Context,
	scope accessdomain.OwnerScope,
	input SearchInput,
) (SearchOutput, error) {
	query := strings.TrimSpace(input.Query)
	if query != "" && len(input.Terms) > 0 {
		return SearchOutput{}, ErrSearchInputConflict
	}

	terms := make([]string, 0, len(input.Terms))
	operator := input.Operator
	within := input.Within

	if len(input.Terms) > 0 {
		seen := make(map[string]struct{}, len(input.Terms))
		totalRunes := 0
		for _, rawTerm := range input.Terms {
			term := strings.TrimSpace(rawTerm)
			if !utf8.ValidString(term) {
				return SearchOutput{}, ErrSearchTermInvalidUTF8
			}
			if term == "" {
				continue
			}
			termRunes := utf8.RuneCountInString(term)
			if termRunes > MaxSearchTermRunes {
				return SearchOutput{}, ErrSearchTermTooLong
			}
			// PostgreSQL 使用 ILIKE 执行大小写不敏感匹配，因此去重也采用
			// 小写键；否则 Maglev 与 maglev 会被错误计为两个不同条件。
			deduplicationKey := strings.ToLower(term)
			if _, duplicated := seen[deduplicationKey]; duplicated {
				continue
			}
			seen[deduplicationKey] = struct{}{}
			terms = append(terms, term)
			totalRunes += termRunes
		}
		if len(terms) < MinSearchTermCount || len(terms) > MaxSearchTermCount {
			return SearchOutput{}, ErrInvalidSearchTermCount
		}
		if totalRunes > MaxSearchQueryRunes {
			return SearchOutput{}, ErrSearchTermsTooLong
		}
		if operator == "" {
			operator = documentdomain.SearchOperatorAll
		}
		if !operator.IsValid() {
			return SearchOutput{}, ErrInvalidSearchOperator
		}
		if within == "" {
			within = documentdomain.SearchWithinChunk
		}
		if !within.IsValid() {
			return SearchOutput{}, ErrInvalidSearchWithin
		}
	} else if query == "" {
		return SearchOutput{}, ErrSearchQueryRequired
	} else {
		if !utf8.ValidString(query) {
			return SearchOutput{}, ErrSearchQueryInvalidUTF8
		}
		if utf8.RuneCountInString(query) > MaxSearchQueryRunes {
			return SearchOutput{}, ErrSearchQueryTooLong
		}
		terms = []string{query}
		operator = documentdomain.SearchOperatorAll
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
	repositoryTerms := terms
	repositoryOperator := operator
	responseTerms := terms
	responseOperator := operator
	if query != "" {
		repositoryTerms = nil
		repositoryOperator = ""
		responseTerms = nil
		responseOperator = ""
	}
	result, err := s.searcher.Search(
		ctx,
		scope,
		documentdomain.SearchOptions{
			Query:      query,
			Terms:      repositoryTerms,
			Operator:   repositoryOperator,
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
		Terms:      responseTerms,
		Operator:   responseOperator,
		Within:     within,
		Hits:       hits,
		Page:       input.Page,
		PageSize:   input.PageSize,
		Total:      result.Total,
		TotalPages: totalPages,
	}, nil
}
