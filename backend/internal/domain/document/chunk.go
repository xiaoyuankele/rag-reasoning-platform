package document

import (
	"context"
	"time"
)

// ChunkInput 表示处理器生成、准备写入数据库的一条文本块。
//
// ID、DocumentID 和 CreatedAt 由仓储或数据库补充，因此不由处理器提供。
type ChunkInput struct {
	Index   int
	Content string
}

// TextChunk 表示一份文档中已经持久化的文本块。
type TextChunk struct {
	ID         int64
	DocumentID int64
	Index      int
	Content    string
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

// ChunkRepository 组合 Worker 入库和后续查询所需的文本块能力。
type ChunkRepository interface {
	ChunkReplacer
	ChunkLister
}
