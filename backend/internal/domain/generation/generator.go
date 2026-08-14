// Package generation 定义与具体大模型供应商无关的文本生成契约。
package generation

import (
	"context"
	"errors"
)

var (
	// ErrGenerationRequestRejected 表示远程提供方拒绝了生成请求。
	// 典型原因包括模型名称错误、参数不支持或输入触发提供方策略。
	ErrGenerationRequestRejected = errors.New("generation request rejected")

	// ErrGenerationAuthentication 表示 API Key 无效、缺失或没有模型权限。
	ErrGenerationAuthentication = errors.New("generation authentication failed")

	// ErrGenerationRateLimited 表示远程服务要求降低调用频率。
	ErrGenerationRateLimited = errors.New("generation request rate limited")

	// ErrGenerationQuotaExceeded 表示账户余额或可用额度不足。
	ErrGenerationQuotaExceeded = errors.New("generation quota exceeded")

	// ErrGenerationUnavailable 表示远程生成服务暂时无法使用。
	ErrGenerationUnavailable = errors.New("generation service unavailable")

	// ErrInvalidGenerationResponse 表示远程响应缺少答案或不符合预期结构。
	ErrInvalidGenerationResponse = errors.New("invalid generation response")
)

// GenerateRequest 是 Application 交给文本生成器的稳定输入。
//
// SystemInstruction 规定模型必须遵守的系统规则；UserPrompt 包含用户问题和
// 已编号的检索证据。Model、MaxOutputTokens 和 Temperature 来自受控配置，
// 不能直接由前端任意指定。
type GenerateRequest struct {
	SystemInstruction string
	UserPrompt        string
	Model             string
	MaxOutputTokens   int
	Temperature       float64
}

// GenerateResult 是不同模型供应商返回结果的统一表示。
//
// Text 是最终可展示文字；token 数量只用于成本与性能观测，不参与答案判断。
type GenerateResult struct {
	Text             string
	PromptTokens     int
	CompletionTokens int
	TotalTokens      int
}

// Generator 定义“根据系统规则和用户 Prompt 生成一段文本”的最小能力。
//
// Application 只依赖这个接口，不知道底层使用 DashScope、OpenAI、Kimi，
// 还是未来的 Python/LangChain 适配器。
type Generator interface {
	Generate(
		ctx context.Context,
		request GenerateRequest,
	) (GenerateResult, error)
}
