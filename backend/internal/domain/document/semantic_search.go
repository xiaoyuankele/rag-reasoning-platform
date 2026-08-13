package document

import "context"

// SemanticSearchOptions 表示语义检索仓储执行一次向量查询所需的完整条件。
//
// QueryVector 是用户问题经过 Embedder 转换后的向量。ModelName 和 Dimensions
// 必须与生成文档向量的 embedding_job 一致；仅仅维度相同并不代表两个模型的
// 向量可以互相比较。DocumentID 为 nil 时搜索全部 ready 文档，非 nil 时只搜索
// 指定文档；Limit 表示最多返回多少个最相似文本块。
type SemanticSearchOptions struct {
	QueryVector []float32
	ModelName   string
	Dimensions  int
	DocumentID  *int64
	Limit       int
}

// SemanticSearchHit 表示一个与查询语义相近的文本块及其来源信息。
//
// Similarity 使用余弦相似度，值越大表示方向越接近。第一版不在 Domain 中强制
// 把它裁剪到 0～1，因为余弦相似度在数学上允许负值；HTTP 层只负责原样展示。
type SemanticSearchHit struct {
	ChunkID      int64
	DocumentID   int64
	ChunkIndex   int
	Title        *string
	OriginalName string
	MIMEType     string
	Content      string
	PageStart    *int
	PageEnd      *int
	Similarity   float64
}

// SemanticChunkSearcher 定义“按照查询向量找到最相近文本块”的最小仓储能力。
//
// Application 只依赖这个端口，不知道 PostgreSQL 的 vector 类型、余弦距离运算符
// 或未来可能采用的向量数据库。返回结果必须按 Similarity 从高到低稳定排序。
type SemanticChunkSearcher interface {
	SearchSimilar(
		ctx context.Context,
		options SemanticSearchOptions,
	) ([]SemanticSearchHit, error)
}
