// Package openaigeneration 把项目 Generator 契约适配到 OpenAI 兼容的
// Chat Completions HTTP API。
package openaigeneration

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"

	generationdomain "rag-reasoning-platform/backend/internal/domain/generation"
)

const (
	defaultProviderName = "OpenAI"

	// DefaultEndpoint 是 OpenAI 官方 Chat Completions 地址。
	DefaultEndpoint = "https://api.openai.com/v1/chat/completions"

	// maxResponseBodyBytes 防止错误或恶意远程响应无限占用内存。
	maxResponseBodyBytes int64 = 4 * 1024 * 1024
)

// HTTPDoer 是 *http.Client 已经实现的最小请求能力。
// 测试可以注入本地服务器或 Fake，而不需要访问真实远程模型。
type HTTPDoer interface {
	Do(request *http.Request) (*http.Response, error)
}

// Client 调用 OpenAI 兼容的非流式 Chat Completions API。
type Client struct {
	providerName   string
	apiKey         string
	endpoint       string
	httpClient     HTTPDoer
	enableThinking *bool
}

var _ generationdomain.Generator = (*Client)(nil)

// NewClient 创建 OpenAI 官方文本生成适配器。
func NewClient(
	apiKey string,
	endpoint string,
	httpClient HTTPDoer,
) (*Client, error) {
	return NewCompatibleClient(
		defaultProviderName,
		apiKey,
		endpoint,
		httpClient,
	)
}

// NewCompatibleClient 创建遵循 OpenAI Chat Completions 契约的供应商适配器。
//
// API Key 只保存在内存中并写入 Authorization 请求头，不能写入日志。
// endpoint 可以在测试中指向 httptest.Server，避免产生真实费用。
func NewCompatibleClient(
	providerName string,
	apiKey string,
	endpoint string,
	httpClient HTTPDoer,
) (*Client, error) {
	return NewCompatibleClientWithOptions(
		providerName,
		apiKey,
		endpoint,
		httpClient,
		CompatibleOptions{},
	)
}

// CompatibleOptions 保存 OpenAI 标准之外、由兼容提供方支持的可选参数。
// nil 表示完全不发送该字段，避免 OpenAI 官方端点拒绝未知参数。
type CompatibleOptions struct {
	EnableThinking *bool
}

// NewCompatibleClientWithOptions 创建带提供方扩展参数的兼容客户端。
func NewCompatibleClientWithOptions(
	providerName string,
	apiKey string,
	endpoint string,
	httpClient HTTPDoer,
	options CompatibleOptions,
) (*Client, error) {
	providerName = strings.TrimSpace(providerName)
	if providerName == "" {
		return nil, errors.New("generation provider name must be provided")
	}

	apiKey = strings.TrimSpace(apiKey)
	if apiKey == "" {
		return nil, fmt.Errorf("%s API key must be provided", providerName)
	}

	endpoint = strings.TrimSpace(endpoint)
	parsedEndpoint, err := url.Parse(endpoint)
	if err != nil || parsedEndpoint.Host == "" ||
		(parsedEndpoint.Scheme != "http" && parsedEndpoint.Scheme != "https") {
		return nil, fmt.Errorf(
			"%s generation endpoint must be a valid HTTP URL",
			providerName,
		)
	}
	if httpClient == nil {
		return nil, errors.New("HTTP client must be provided")
	}

	var enableThinking *bool
	if options.EnableThinking != nil {
		value := *options.EnableThinking
		enableThinking = &value
	}

	return &Client{
		providerName:   providerName,
		apiKey:         apiKey,
		endpoint:       endpoint,
		httpClient:     httpClient,
		enableThinking: enableThinking,
	}, nil
}

type chatMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type chatCompletionRequest struct {
	Model               string        `json:"model"`
	Messages            []chatMessage `json:"messages"`
	Stream              bool          `json:"stream"`
	MaxCompletionTokens int           `json:"max_completion_tokens"`
	Temperature         float64       `json:"temperature"`
	EnableThinking      *bool         `json:"enable_thinking,omitempty"`
}

type chatCompletionResponse struct {
	Choices []struct {
		Message struct {
			Content string `json:"content"`
		} `json:"message"`
		FinishReason string `json:"finish_reason"`
	} `json:"choices"`
	Usage struct {
		PromptTokens     int `json:"prompt_tokens"`
		CompletionTokens int `json:"completion_tokens"`
		TotalTokens      int `json:"total_tokens"`
	} `json:"usage"`
}

type errorResponse struct {
	Error struct {
		Message string `json:"message"`
		Type    string `json:"type"`
		Code    string `json:"code"`
	} `json:"error"`
	Code    string `json:"code"`
	Message string `json:"message"`
}

// Generate 发送系统规则与用户 Prompt，并返回第一条非流式答案。
func (c *Client) Generate(
	ctx context.Context,
	request generationdomain.GenerateRequest,
) (generationdomain.GenerateResult, error) {
	if err := validateRequest(request); err != nil {
		return generationdomain.GenerateResult{}, err
	}

	requestBody, err := json.Marshal(chatCompletionRequest{
		Model: request.Model,
		Messages: []chatMessage{
			{Role: "system", Content: request.SystemInstruction},
			{Role: "user", Content: request.UserPrompt},
		},
		Stream:              false,
		MaxCompletionTokens: request.MaxOutputTokens,
		Temperature:         request.Temperature,
		EnableThinking:      c.enableThinking,
	})
	if err != nil {
		return generationdomain.GenerateResult{}, fmt.Errorf(
			"encode %s generation request: %w",
			c.providerName,
			err,
		)
	}

	httpRequest, err := http.NewRequestWithContext(
		ctx,
		http.MethodPost,
		c.endpoint,
		bytes.NewReader(requestBody),
	)
	if err != nil {
		return generationdomain.GenerateResult{}, fmt.Errorf(
			"create %s generation request: %w",
			c.providerName,
			err,
		)
	}
	httpRequest.Header.Set("Authorization", "Bearer "+c.apiKey)
	httpRequest.Header.Set("Content-Type", "application/json")

	httpResponse, err := c.httpClient.Do(httpRequest)
	if err != nil {
		return generationdomain.GenerateResult{}, fmt.Errorf(
			"send %s generation request: %w",
			c.providerName,
			err,
		)
	}
	defer httpResponse.Body.Close()

	limitedBody := io.LimitReader(httpResponse.Body, maxResponseBodyBytes)
	if httpResponse.StatusCode < http.StatusOK ||
		httpResponse.StatusCode >= http.StatusMultipleChoices {
		return generationdomain.GenerateResult{}, decodeAPIError(
			c.providerName,
			httpResponse.StatusCode,
			limitedBody,
		)
	}

	var response chatCompletionResponse
	decoder := json.NewDecoder(limitedBody)
	if err := decoder.Decode(&response); err != nil {
		return generationdomain.GenerateResult{}, fmt.Errorf(
			"%w: decode response: %v",
			generationdomain.ErrInvalidGenerationResponse,
			err,
		)
	}

	return normalizeResponse(response)
}

func validateRequest(request generationdomain.GenerateRequest) error {
	if strings.TrimSpace(request.SystemInstruction) == "" {
		return fmt.Errorf(
			"%w: system instruction must be provided",
			generationdomain.ErrGenerationRequestRejected,
		)
	}
	if strings.TrimSpace(request.UserPrompt) == "" {
		return fmt.Errorf(
			"%w: user prompt must be provided",
			generationdomain.ErrGenerationRequestRejected,
		)
	}
	if strings.TrimSpace(request.Model) == "" {
		return fmt.Errorf(
			"%w: model must be provided",
			generationdomain.ErrGenerationRequestRejected,
		)
	}
	if request.MaxOutputTokens <= 0 {
		return fmt.Errorf(
			"%w: max output tokens must be positive",
			generationdomain.ErrGenerationRequestRejected,
		)
	}
	if request.Temperature < 0 || request.Temperature > 2 {
		return fmt.Errorf(
			"%w: temperature must be between 0 and 2",
			generationdomain.ErrGenerationRequestRejected,
		)
	}

	return nil
}

func normalizeResponse(
	response chatCompletionResponse,
) (generationdomain.GenerateResult, error) {
	if len(response.Choices) == 0 {
		return generationdomain.GenerateResult{}, fmt.Errorf(
			"%w: response contains no choices",
			generationdomain.ErrInvalidGenerationResponse,
		)
	}

	text := strings.TrimSpace(response.Choices[0].Message.Content)
	if text == "" {
		return generationdomain.GenerateResult{}, fmt.Errorf(
			"%w: first choice contains no answer text",
			generationdomain.ErrInvalidGenerationResponse,
		)
	}

	return generationdomain.GenerateResult{
		Text:             text,
		PromptTokens:     response.Usage.PromptTokens,
		CompletionTokens: response.Usage.CompletionTokens,
		TotalTokens:      response.Usage.TotalTokens,
	}, nil
}

func decodeAPIError(
	providerName string,
	statusCode int,
	reader io.Reader,
) error {
	var response errorResponse
	_ = json.NewDecoder(reader).Decode(&response)

	message := strings.TrimSpace(response.Error.Message)
	if message == "" {
		message = strings.TrimSpace(response.Message)
	}
	if message == "" {
		message = http.StatusText(statusCode)
	}

	var category error
	if isPermanentQuotaError(response) {
		category = generationdomain.ErrGenerationQuotaExceeded
	} else {
		switch statusCode {
		case http.StatusUnauthorized, http.StatusForbidden:
			category = generationdomain.ErrGenerationAuthentication
		case http.StatusTooManyRequests:
			category = generationdomain.ErrGenerationRateLimited
		case http.StatusBadRequest, http.StatusNotFound,
			http.StatusUnprocessableEntity:
			category = generationdomain.ErrGenerationRequestRejected
		default:
			if statusCode >= http.StatusInternalServerError {
				category = generationdomain.ErrGenerationUnavailable
			} else {
				category = generationdomain.ErrGenerationRequestRejected
			}
		}
	}

	return fmt.Errorf(
		"%w: %s returned HTTP %d: %s",
		category,
		providerName,
		statusCode,
		message,
	)
}

func isPermanentQuotaError(response errorResponse) bool {
	codes := []string{
		response.Code,
		response.Error.Code,
		response.Error.Type,
	}
	permanentCodes := []string{
		"insufficient_quota",
		"AllocationQuota.FreeTierOnly",
		"Arrearage",
		"CommodityNotPurchased",
		"PrepaidBillOverdue",
		"PostpaidBillOverdue",
	}

	for _, code := range codes {
		for _, permanentCode := range permanentCodes {
			if strings.EqualFold(strings.TrimSpace(code), permanentCode) {
				return true
			}
		}
	}

	return false
}
