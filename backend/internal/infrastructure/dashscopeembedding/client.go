// Package dashscopeembedding 把项目内部 Embedder 契约适配到阿里云百炼 Embeddings API。
package dashscopeembedding

import "rag-reasoning-platform/backend/internal/infrastructure/openaiembedding"

const (
	// DefaultEndpoint 是阿里云百炼中国内地地域的 OpenAI 兼容 Embeddings 地址。
	DefaultEndpoint = "https://dashscope.aliyuncs.com/compatible-mode/v1/embeddings"
)

// HTTPDoer 是发送 HTTP 请求所需的最小能力。
// 类型别名表示它与 openaiembedding.HTTPDoer 是同一个接口，不产生额外转换层。
type HTTPDoer = openaiembedding.HTTPDoer

// Client 复用已经验证过的 OpenAI 兼容协议实现。
//
// 这里的“兼容”仅表示 HTTP 请求和响应格式相同；模型名称、批量上限、API Key 和
// 错误码仍由 DashScope 配置与错误适配规则负责。
type Client = openaiembedding.Client

// NewClient 创建 DashScope Embeddings 适配器。
func NewClient(
	apiKey string,
	endpoint string,
	httpClient HTTPDoer,
) (*Client, error) {
	return openaiembedding.NewCompatibleClient(
		"DashScope",
		apiKey,
		endpoint,
		httpClient,
	)
}
