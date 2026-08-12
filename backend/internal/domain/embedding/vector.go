package embedding

// ChunkVector 把一条已经持久化的文本块与它的向量绑定在一起。
//
// Values 中的浮点数数量必须等于创建任务时冻结的 dimensions。
// ChunkID 而不是数组下标负责建立向量与 text_chunks 表记录的稳定联系。
type ChunkVector struct {
	ChunkID int64
	Values  []float32
}

// JobCompletion 保存一次向量任务成功完成时需要原子落库的全部结果。
//
// Token 数量来自远程模型响应，后续可以用于成本统计；它们不参与向量检索。
type JobCompletion struct {
	Vectors      []ChunkVector
	PromptTokens int
	TotalTokens  int
}
