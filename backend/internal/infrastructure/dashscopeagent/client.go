// Package dashscopeagent configures the generic OpenAI-compatible AgentModel
// client for Alibaba Cloud Model Studio Qwen tool calling.
package dashscopeagent

import "rag-reasoning-platform/backend/internal/infrastructure/openaiagent"

const DefaultEndpoint = "https://dashscope.aliyuncs.com/compatible-mode/v1/chat/completions"

type HTTPDoer = openaiagent.HTTPDoer
type Client = openaiagent.Client

func NewClient(
	apiKey string,
	endpoint string,
	httpClient HTTPDoer,
	model string,
	maxCompletionTokens int,
	temperature float64,
) (*Client, error) {
	disabled := false
	return openaiagent.NewCompatibleClientWithOptions(
		"DashScope",
		apiKey,
		endpoint,
		httpClient,
		model,
		maxCompletionTokens,
		temperature,
		openaiagent.CompatibleOptions{EnableThinking: &disabled},
	)
}
