// Package dashscopegeneration 将 Generator 契约适配到阿里云百炼文本生成接口。
package dashscopegeneration

import "rag-reasoning-platform/backend/internal/infrastructure/openaigeneration"

const (
	// DefaultEndpoint 是百炼中国内地地域的 OpenAI 兼容 Chat Completions 地址。
	DefaultEndpoint = "https://dashscope.aliyuncs.com/compatible-mode/v1/chat/completions"
)

// HTTPDoer 是 openaigeneration.HTTPDoer 的类型别名。
type HTTPDoer = openaigeneration.HTTPDoer

// Client 复用已经测试的 OpenAI 兼容请求、响应和错误处理。
type Client = openaigeneration.Client

// NewClient 创建 DashScope 文本生成适配器。
func NewClient(
	apiKey string,
	endpoint string,
	httpClient HTTPDoer,
	enableThinking bool,
) (*Client, error) {
	return openaigeneration.NewCompatibleClientWithOptions(
		"DashScope",
		apiKey,
		endpoint,
		httpClient,
		openaigeneration.CompatibleOptions{
			EnableThinking: &enableThinking,
		},
	)
}
