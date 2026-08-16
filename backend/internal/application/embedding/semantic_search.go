package embedding

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"

	accessdomain "rag-reasoning-platform/backend/internal/domain/access"
	documentdomain "rag-reasoning-platform/backend/internal/domain/document"
	embeddingdomain "rag-reasoning-platform/backend/internal/domain/embedding"
)

const (
	// MaxSemanticSearchQueryRunes 限制一次自然语言问题最多包含的 Unicode 字符数。
	// 语义问题通常比关键词更长，因此上限高于 GET /search 的关键词上限。
	MaxSemanticSearchQueryRunes = 1000

	// MaxSemanticSearchTopK 限制一次请求最多返回的相似文本块数量。
	// 第一版控制为 20，避免响应和未来交给生成模型的上下文无限增长。
	MaxSemanticSearchTopK = 20
)

var (
	// ErrSemanticSearchDependencies 表示服务缺少 Embedder 或语义搜索仓储。
	ErrSemanticSearchDependencies = errors.New(
		"semantic search dependencies must be provided",
	)

	// ErrSemanticSearchConfiguration 表示模型名称或向量维度配置无效。
	ErrSemanticSearchConfiguration = errors.New(
		"semantic search model configuration is invalid",
	)

	// ErrSemanticSearchQueryRequired 表示自然语言问题缺失或只包含空白。
	ErrSemanticSearchQueryRequired = errors.New(
		"semantic search query is required",
	)

	// ErrSemanticSearchQueryInvalidUTF8 表示问题不是合法 UTF-8 文本。
	ErrSemanticSearchQueryInvalidUTF8 = errors.New(
		"semantic search query must be valid UTF-8",
	)

	// ErrSemanticSearchQueryTooLong 表示问题超过第一版字符上限。
	ErrSemanticSearchQueryTooLong = errors.New(
		"semantic search query must not exceed 1000 characters",
	)

	// ErrInvalidSemanticSearchTopK 表示返回数量不在 1～20 范围内。
	ErrInvalidSemanticSearchTopK = errors.New(
		"semantic search top_k must be between 1 and 20",
	)

	// ErrDocumentEmbeddingsNotReady 表示指定文档还没有当前模型的完整可用向量。
	ErrDocumentEmbeddingsNotReady = errors.New(
		"document embeddings are not ready",
	)
)

// semanticSearchRepository 组合语义检索用例需要的两项仓储能力。
//
// Application 只依赖这个小接口，不依赖 PostgreSQL 的具体实现。生产环境和测试 Fake
// 都必须同时提供“检查向量就绪状态”和“执行相似度查询”这两个插口。
type semanticSearchRepository interface {
	documentdomain.ScopedSemanticEmbeddingReadinessChecker
	documentdomain.ScopedSemanticChunkSearcher
}

// SemanticSearchInput 是 HTTP 等上层入口交给语义检索用例的数据。
type SemanticSearchInput struct {
	Query      string
	DocumentID *int64
	TopK       int
}

// SemanticSearchOutput 是 Application 返回给上层入口的统一结果。
type SemanticSearchOutput struct {
	Query string
	Hits  []documentdomain.SemanticSearchHit
}

// SemanticSearchService 编排“问题向量化 → 相似文本块查询”用例。
//
// 它只依赖 Domain 接口：不知道向量来自 DashScope 还是 OpenAI，也不知道仓储使用
// PostgreSQL pgvector 还是其他向量数据库。
type SemanticSearchService struct {
	embedder   embeddingdomain.Embedder
	repository semanticSearchRepository
	modelName  string
	dimensions int
}

// NewSemanticSearchService 创建语义检索应用服务。
func NewSemanticSearchService(
	embedder embeddingdomain.Embedder,
	repository semanticSearchRepository,
	modelName string,
	dimensions int,
) (*SemanticSearchService, error) {
	if embedder == nil || repository == nil {
		return nil, ErrSemanticSearchDependencies
	}

	modelName = strings.TrimSpace(modelName)
	if modelName == "" || dimensions <= 0 {
		return nil, ErrSemanticSearchConfiguration
	}

	return &SemanticSearchService{
		embedder:   embedder,
		repository: repository,
		modelName:  modelName,
		dimensions: dimensions,
	}, nil
}

// Search 校验自然语言问题，生成一条查询向量，再读取最相近的文本块。
func (s *SemanticSearchService) Search(
	ctx context.Context,
	scope accessdomain.OwnerScope,
	input SemanticSearchInput,
) (SemanticSearchOutput, error) {
	query, err := validateSemanticSearchInput(input)
	if err != nil {
		return SemanticSearchOutput{}, err
	}

	// 全库检索允许搜索所有已经存在的向量；指定文档检索则必须先证明该文档的
	// 当前模型向量完整。这个检查位于远程 Embedder 调用之前，可以避免为一个
	// 明知无法检索的文档产生 API 费用。
	if input.DocumentID != nil {
		ready, err := s.repository.HasCompleteSemanticEmbeddings(
			ctx,
			scope,
			documentdomain.SemanticEmbeddingReadinessOptions{
				DocumentID: *input.DocumentID,
				ModelName:  s.modelName,
				Dimensions: s.dimensions,
			},
		)
		if err != nil {
			return SemanticSearchOutput{}, fmt.Errorf(
				"check document semantic embedding readiness: %w",
				err,
			)
		}
		if !ready {
			return SemanticSearchOutput{}, ErrDocumentEmbeddingsNotReady
		}
	}

	embeddedQuery, err := s.embedder.Embed(
		ctx,
		embeddingdomain.EmbedRequest{
			Inputs:     []string{query},
			Model:      s.modelName,
			Dimensions: s.dimensions,
		},
	)
	if err != nil {
		return SemanticSearchOutput{}, fmt.Errorf(
			"embed semantic search query: %w",
			err,
		)
	}

	// 一次用户问题必须严格对应一条查询向量。即使当前 HTTP 适配器已经校验，
	// Application 仍要保护自己免受未来实现或错误 Fake 返回值的影响。
	if len(embeddedQuery.Vectors) != 1 ||
		len(embeddedQuery.Vectors[0]) != s.dimensions {
		return SemanticSearchOutput{}, fmt.Errorf(
			"%w: semantic query returned %d vectors with expected dimensions %d",
			embeddingdomain.ErrInvalidEmbeddingResponse,
			len(embeddedQuery.Vectors),
			s.dimensions,
		)
	}

	hits, err := s.repository.SearchSimilar(
		ctx,
		scope,
		documentdomain.SemanticSearchOptions{
			QueryVector: embeddedQuery.Vectors[0],
			ModelName:   s.modelName,
			Dimensions:  s.dimensions,
			DocumentID:  input.DocumentID,
			Limit:       input.TopK,
		},
	)
	if err != nil {
		return SemanticSearchOutput{}, fmt.Errorf(
			"search semantically similar document chunks: %w",
			err,
		)
	}
	if hits == nil {
		hits = make([]documentdomain.SemanticSearchHit, 0)
	}

	return SemanticSearchOutput{
		Query: query,
		Hits:  hits,
	}, nil
}

func validateSemanticSearchInput(input SemanticSearchInput) (string, error) {
	query := strings.TrimSpace(input.Query)
	if query == "" {
		return "", ErrSemanticSearchQueryRequired
	}
	if !utf8.ValidString(query) {
		return "", ErrSemanticSearchQueryInvalidUTF8
	}
	if utf8.RuneCountInString(query) > MaxSemanticSearchQueryRunes {
		return "", ErrSemanticSearchQueryTooLong
	}
	if input.DocumentID != nil && *input.DocumentID <= 0 {
		return "", ErrInvalidDocumentID
	}
	if input.TopK <= 0 || input.TopK > MaxSemanticSearchTopK {
		return "", ErrInvalidSemanticSearchTopK
	}

	return query, nil
}
