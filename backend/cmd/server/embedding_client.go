package main

import (
	"fmt"
	"net/http"

	"rag-reasoning-platform/backend/internal/config"
	embeddingdomain "rag-reasoning-platform/backend/internal/domain/embedding"
	"rag-reasoning-platform/backend/internal/infrastructure/dashscopeembedding"
	"rag-reasoning-platform/backend/internal/infrastructure/openaiembedding"
)

// newEmbeddingClient 根据启动配置创建本次进程共用的 Embedder 实现。
//
// 这个函数属于组合根辅助代码：它知道可选的具体提供方，但不包含向量任务或
// 语义检索业务规则。Worker 与在线语义检索共用返回的无状态 HTTP 客户端。
func newEmbeddingClient(
	embeddingConfig config.EmbeddingConfig,
) (embeddingdomain.Embedder, error) {
	httpClient := &http.Client{
		Timeout: embeddingConfig.HTTPTimeout,
	}

	switch embeddingConfig.Provider {
	case config.EmbeddingProviderDashScope:
		client, err := dashscopeembedding.NewClient(
			embeddingConfig.APIKey,
			embeddingConfig.Endpoint,
			httpClient,
		)
		if err != nil {
			return nil, fmt.Errorf("create DashScope embedding client: %w", err)
		}
		return client, nil

	case config.EmbeddingProviderOpenAI:
		client, err := openaiembedding.NewClient(
			embeddingConfig.APIKey,
			embeddingConfig.Endpoint,
			httpClient,
		)
		if err != nil {
			return nil, fmt.Errorf("create OpenAI embedding client: %w", err)
		}
		return client, nil

	default:
		return nil, fmt.Errorf(
			"create embedding client: unsupported provider %q",
			embeddingConfig.Provider,
		)
	}
}
