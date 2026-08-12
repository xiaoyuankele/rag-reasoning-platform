package embedding

import (
	"context"
	"errors"
)

var (
	// ErrEmbeddingRequestRejected 表示提供方拒绝了请求，例如文本过长或参数不受支持。
	// 这类错误通常不能通过原样重试解决。
	ErrEmbeddingRequestRejected = errors.New("embedding request rejected")

	// ErrEmbeddingAuthentication 表示 API Key 无效、缺失或没有访问权限。
	ErrEmbeddingAuthentication = errors.New("embedding authentication failed")

	// ErrEmbeddingRateLimited 表示远程服务要求调用方降低请求频率。
	ErrEmbeddingRateLimited = errors.New("embedding request rate limited")

	// ErrEmbeddingQuotaExceeded 表示账户余额、订阅或可用额度不足。
	// 继续快速重试不会自行恢复，应在补充额度或更换提供方后再创建或恢复任务。
	ErrEmbeddingQuotaExceeded = errors.New("embedding quota exceeded")

	// ErrEmbeddingUnavailable 表示远程服务暂时不可用，后续 Worker 可以延迟重试。
	ErrEmbeddingUnavailable = errors.New("embedding service unavailable")

	// ErrInvalidEmbeddingResponse 表示远程服务返回的数据不满足本项目约定。
	ErrInvalidEmbeddingResponse = errors.New("invalid embedding response")
)

// EmbedRequest 是 Application 交给向量提供方的稳定请求。
//
// Inputs 中每个字符串对应一个文本块；返回向量必须保持相同顺序。
// Model 和 Dimensions 来自 embedding_jobs 中已经冻结的任务配置。
type EmbedRequest struct {
	Inputs     []string
	Model      string
	Dimensions int
}

// EmbedResult 是远程向量结果在 Go 应用内部的统一表示。
//
// Vectors[i] 必须对应 EmbedRequest.Inputs[i]。Token 数量用于后续记录调用成本，
// 不参与业务判断。
type EmbedResult struct {
	Vectors      [][]float32
	PromptTokens int
	TotalTokens  int
}

// Embedder 定义“把一批文本转换成向量”的最小能力。
//
// Application 只依赖这个接口，不依赖 OpenAI、DashScope 等提供方的 HTTP 请求结构。
// 因此切换远程服务或本地模型时，应用编排不需要跟着修改。
type Embedder interface {
	Embed(ctx context.Context, request EmbedRequest) (EmbedResult, error)
}
