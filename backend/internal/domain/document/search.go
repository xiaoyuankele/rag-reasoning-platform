package document

import "context"

// SearchOptions 表示文本块仓储执行关键词检索时需要的参数。
//
// Query 已由 Application 层完成去除首尾空白和合法性校验；DocumentID
// 为 nil 时跨全部 ready 文档搜索，非 nil 时只搜索指定文档；Limit 与
// Offset 是仓储可以直接转换为 SQL LIMIT/OFFSET 的分页参数。
type SearchOptions struct {
	Query      string
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
