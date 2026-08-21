package document

import (
	"context"

	accessdomain "rag-reasoning-platform/backend/internal/domain/access"
)

// SearchOperator 定义多个关键词在同一个文本块中的组合方式。
type SearchOperator string

const (
	// SearchOperatorAll 要求同一文本块包含全部关键词。
	SearchOperatorAll SearchOperator = "all"
	// SearchOperatorAny 要求同一文本块至少包含一个关键词。
	SearchOperatorAny SearchOperator = "any"
)

// IsValid 判断组合方式是否由当前关键词检索支持。
func (o SearchOperator) IsValid() bool {
	return o == SearchOperatorAll || o == SearchOperatorAny
}

// SearchWithin 定义多个关键词必须共同出现的数据范围。
type SearchWithin string

const (
	// SearchWithinChunk 表示全部匹配判断都在一条 text_chunks 记录内完成。
	SearchWithinChunk SearchWithin = "chunk"
)

// IsValid 判断位置范围是否由当前关键词检索支持。
func (w SearchWithin) IsValid() bool {
	return w == SearchWithinChunk
}

// SearchOptions 表示文本块仓储执行关键词检索时需要的参数。
//
// Query 保存向后兼容的单个完整短语；Terms 保存多关键词模式的规范化词项。
// 二者由 Application 保证不会同时提供。Terms 已完成去除首尾空白、去重和合法性校验；
// Operator 决定同一 chunk 需要命中全部词还是任意词；DocumentID 为 nil
// 时跨全部 ready 文档搜索，非 nil 时只搜索指定文档；Limit 与 Offset
// 是仓储可以直接转换为 SQL LIMIT/OFFSET 的分页参数。
type SearchOptions struct {
	Query      string
	Terms      []string
	Operator   SearchOperator
	DocumentID *int64
	Limit      int64
	Offset     int64
}

// SearchHit 表示一个命中关键词的统一文本块及其来源文档信息。
//
// 搜索结果以 chunk 为单位，而不是只返回文档。这样调用方既能展示命中正文，
// 又能通过 DocumentID 和页码定位原始文档。
type SearchHit struct {
	ChunkID      int64
	DocumentID   int64
	ChunkIndex   int
	Title        *string
	OriginalName string
	MIMEType     string
	Content      string
	PageStart    *int
	PageEnd      *int
}

// SearchResult 表示仓储返回的一页搜索命中和全部命中数量。
type SearchResult struct {
	Hits  []SearchHit
	Total int64
}

// ChunkSearcher 定义跨文档检索统一文本块的最小仓储能力。
//
// Application 只依赖这个窄接口，不需要知道 PostgreSQL、ILIKE、全文索引
// 或其他具体检索技术。未来替换检索实现时不需要修改应用服务。
type ChunkSearcher interface {
	Search(
		ctx context.Context,
		options SearchOptions,
	) (SearchResult, error)
}

// ScopedChunkSearcher 定义只能在当前所有者文档范围内执行关键词检索的能力。
//
// “跨全部文档”在这个接口中只表示跨当前 OwnerScope 的全部 ready 文档；
// Repository 必须在 SQL 候选集合中限制 owner，不能先全局查询再在内存中过滤。
type ScopedChunkSearcher interface {
	Search(
		ctx context.Context,
		scope accessdomain.OwnerScope,
		options SearchOptions,
	) (SearchResult, error)
}
