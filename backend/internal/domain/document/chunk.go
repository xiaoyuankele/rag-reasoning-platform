package document

import (
	"context"
	"errors"
	"time"
)

var ErrInvalidChunkPageRange = errors.New(
	"chunk page range must be absent or contain positive ordered pages",
)

// ChunkInput 表示处理器生成、准备写入数据库的一条文本块。
//
// ID、DocumentID 和 CreatedAt 由仓储或数据库补充，因此不由处理器提供。
type ChunkInput struct {
	Index     int
	Content   string
	PageStart *int
	PageEnd   *int
}

// HasValidPageRange 判断文本块的来源页码是否满足领域约束。
//
// Markdown/TXT 没有固定页码，因此起止页都为 nil 是合法的。PDF 必须同时提供
// 两个正数页码，并且结束页不能早于开始页。
func (c ChunkInput) HasValidPageRange() bool {
	if c.PageStart == nil && c.PageEnd == nil {
		return true
	}
	if c.PageStart == nil || c.PageEnd == nil {
		return false
	}

	return *c.PageStart > 0 && *c.PageEnd >= *c.PageStart
}

// TextChunk 表示一份文档中已经持久化的文本块。
type TextChunk struct {
	ID         int64
	DocumentID int64
	Index      int
	Content    string
	PageStart  *int
	PageEnd    *int
	CreatedAt  time.Time
}

// ChunkReplacer 定义用一组新文本块原子替换文档旧文本块的能力。
//
// 重新解析同一文档时使用替换而不是追加，避免重试产生重复文本块。
type ChunkReplacer interface {
	ReplaceForDocument(
		ctx context.Context,
		documentID int64,
		chunks []ChunkInput,
	) error
}

// ChunkLister 定义按文档和块序号查询全部文本块的能力。
type ChunkLister interface {
	ListByDocumentID(
		ctx context.Context,
		documentID int64,
	) ([]TextChunk, error)
}

// ChunkPageOptions 表示仓储分页读取文本块时使用的参数。
// Limit 是最多返回多少条，Offset 是从原文顺序中跳过多少条。
type ChunkPageOptions struct {
	Limit  int64
	Offset int64
}

// ChunkPageResult 表示一页文本块和该文档的文本块总数。
type ChunkPageResult struct {
	Chunks []TextChunk
	Total  int64
}

// ChunkPageLister 定义按文档分页读取文本块的能力。
// 该端口服务于 HTTP 浏览，不改变 Worker 使用的全量 ChunkLister。
type ChunkPageLister interface {
	ListPageByDocumentID(
		ctx context.Context,
		documentID int64,
		options ChunkPageOptions,
	) (ChunkPageResult, error)
}

// ChunkRepository 组合 Worker 入库和后续查询所需的文本块能力。
type ChunkRepository interface {
	ChunkReplacer
	ChunkLister
}
