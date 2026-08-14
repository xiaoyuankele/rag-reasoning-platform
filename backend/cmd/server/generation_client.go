package main

import (
	"fmt"
	"net/http"

	"rag-reasoning-platform/backend/internal/config"
	generationdomain "rag-reasoning-platform/backend/internal/domain/generation"
	"rag-reasoning-platform/backend/internal/infrastructure/dashscopegeneration"
)

// newGenerationClient 创建本次进程使用的 Generator 具体实现。
//
// 该函数属于组合根辅助代码：它只负责把配置、HTTP Client 与 DashScope
// 适配器组装起来，不包含证据选择、Prompt 构造或 HTTP 状态映射规则。
func newGenerationClient(
	generationConfig config.GenerationConfig,
) (generationdomain.Generator, error) {
	httpClient := &http.Client{
		Timeout: generationConfig.HTTPTimeout,
	}

	client, err := dashscopegeneration.NewClient(
		generationConfig.APIKey,
		generationConfig.Endpoint,
		httpClient,
		generationConfig.ThinkingEnabled,
	)
	if err != nil {
		return nil, fmt.Errorf("create DashScope generation client: %w", err)
	}

	return client, nil
}
