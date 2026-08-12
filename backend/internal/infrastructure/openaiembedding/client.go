// Package openaiembedding 把项目内部的 Embedder 契约适配到 OpenAI Embeddings API。
package openaiembedding

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

	embeddingdomain "rag-reasoning-platform/backend/internal/domain/embedding"
)

const (
	defaultProviderName = "OpenAI"

	// DefaultEndpoint 是 OpenAI 官方 Embeddings API 地址。
	DefaultEndpoint = "https://api.openai.com/v1/embeddings"

	// maxResponseBodyBytes 防止异常远程响应无限占用本机内存。
	maxResponseBodyBytes int64 = 64 * 1024 * 1024
)

// HTTPDoer 是 *http.Client 已经实现的最小 HTTP 能力。
// 抽成接口后，测试和未来的监控封装不必依赖具体客户端类型。
type HTTPDoer interface {
	Do(request *http.Request) (*http.Response, error)
}

// Client 使用 OpenAI HTTP API 生成文本向量。
type Client struct {
	providerName string
	apiKey       string
	endpoint     string
	httpClient   HTTPDoer
}

var _ embeddingdomain.Embedder = (*Client)(nil)

// NewClient 创建 OpenAI Embeddings 适配器。
//
// apiKey 只保存在内存中并写入 Authorization 请求头，绝不能写入日志。
// endpoint 在生产环境应使用 DefaultEndpoint；允许传入测试服务器地址，方便在
// 不消耗真实额度的情况下验证完整 HTTP 契约。
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

// NewCompatibleClient 创建一个遵循 OpenAI Embeddings HTTP 契约的提供方适配器。
//
// DashScope 等服务虽然供应商不同，但兼容相同的请求和成功响应结构。这里复用协议
// 编解码、安全响应体限制和结果顺序校验；providerName 只用于错误说明与差异化错误解析。
func NewCompatibleClient(
	providerName string,
	apiKey string,
	endpoint string,
	httpClient HTTPDoer,
) (*Client, error) {
	providerName = strings.TrimSpace(providerName)
	if providerName == "" {
		return nil, errors.New("embedding provider name must be provided")
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
			"%s embeddings endpoint must be a valid HTTP URL",
			providerName,
		)
	}
	if httpClient == nil {
		return nil, errors.New("HTTP client must be provided")
	}

	return &Client{
		providerName: providerName,
		apiKey:       apiKey,
		endpoint:     endpoint,
		httpClient:   httpClient,
	}, nil
}

type embeddingRequest struct {
	Input          []string `json:"input"`
	Model          string   `json:"model"`
	Dimensions     int      `json:"dimensions"`
	EncodingFormat string   `json:"encoding_format"`
}

type embeddingResponse struct {
	Data []struct {
		Index     int       `json:"index"`
		Embedding []float32 `json:"embedding"`
	} `json:"data"`
	Usage struct {
		PromptTokens int `json:"prompt_tokens"`
		TotalTokens  int `json:"total_tokens"`
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

// Embed 把一批文本发送到 OpenAI，并按输入顺序返回向量。
func (c *Client) Embed(
	ctx context.Context,
	request embeddingdomain.EmbedRequest,
) (embeddingdomain.EmbedResult, error) {
	if err := validateRequest(request); err != nil {
		return embeddingdomain.EmbedResult{}, err
	}

	requestBody, err := json.Marshal(embeddingRequest{
		Input:          request.Inputs,
		Model:          request.Model,
		Dimensions:     request.Dimensions,
		EncodingFormat: "float",
	})
	if err != nil {
		return embeddingdomain.EmbedResult{}, fmt.Errorf(
			"encode %s embedding request: %w",
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
		return embeddingdomain.EmbedResult{}, fmt.Errorf(
			"create %s embedding request: %w",
			c.providerName,
			err,
		)
	}
	httpRequest.Header.Set("Authorization", "Bearer "+c.apiKey)
	httpRequest.Header.Set("Content-Type", "application/json")

	httpResponse, err := c.httpClient.Do(httpRequest)
	if err != nil {
		return embeddingdomain.EmbedResult{}, fmt.Errorf(
			"send %s embedding request: %w",
			c.providerName,
			err,
		)
	}
	defer httpResponse.Body.Close()

	limitedBody := io.LimitReader(httpResponse.Body, maxResponseBodyBytes)
	if httpResponse.StatusCode < http.StatusOK ||
		httpResponse.StatusCode >= http.StatusMultipleChoices {
		return embeddingdomain.EmbedResult{}, decodeAPIError(
			c.providerName,
			httpResponse.StatusCode,
			limitedBody,
		)
	}

	var response embeddingResponse
	decoder := json.NewDecoder(limitedBody)
	if err := decoder.Decode(&response); err != nil {
		return embeddingdomain.EmbedResult{}, fmt.Errorf(
			"%w: decode response: %v",
			embeddingdomain.ErrInvalidEmbeddingResponse,
			err,
		)
	}

	return normalizeResponse(request, response)
}

func validateRequest(request embeddingdomain.EmbedRequest) error {
	if len(request.Inputs) == 0 {
		return fmt.Errorf(
			"%w: at least one input is required",
			embeddingdomain.ErrEmbeddingRequestRejected,
		)
	}
	for index, input := range request.Inputs {
		if strings.TrimSpace(input) == "" {
			return fmt.Errorf(
				"%w: input %d must not be blank",
				embeddingdomain.ErrEmbeddingRequestRejected,
				index,
			)
		}
	}
	if strings.TrimSpace(request.Model) == "" {
		return fmt.Errorf(
			"%w: model must be provided",
			embeddingdomain.ErrEmbeddingRequestRejected,
		)
	}
	if request.Dimensions <= 0 {
		return fmt.Errorf(
			"%w: dimensions must be positive",
			embeddingdomain.ErrEmbeddingRequestRejected,
		)
	}

	return nil
}

func decodeAPIError(providerName string, statusCode int, reader io.Reader) error {
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
		category = embeddingdomain.ErrEmbeddingQuotaExceeded
	} else {
		switch statusCode {
		case http.StatusUnauthorized, http.StatusForbidden:
			category = embeddingdomain.ErrEmbeddingAuthentication
		case http.StatusTooManyRequests:
			category = embeddingdomain.ErrEmbeddingRateLimited
		case http.StatusBadRequest, http.StatusNotFound, http.StatusUnprocessableEntity:
			category = embeddingdomain.ErrEmbeddingRequestRejected
		default:
			if statusCode >= http.StatusInternalServerError {
				category = embeddingdomain.ErrEmbeddingUnavailable
			} else {
				category = embeddingdomain.ErrEmbeddingRequestRejected
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

// isPermanentQuotaError 只匹配“补充额度或购买服务前不会恢复”的明确错误码。
//
// 注意：DashScope 的普通 Throttling.AllocationQuota 可能只是临时 TPS/TPM 限流，
// 不能在这里归为永久失败，否则一次流量尖峰就会错误终止整个任务。
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

func normalizeResponse(
	request embeddingdomain.EmbedRequest,
	response embeddingResponse,
) (embeddingdomain.EmbedResult, error) {
	if len(response.Data) != len(request.Inputs) {
		return embeddingdomain.EmbedResult{}, fmt.Errorf(
			"%w: received %d vectors for %d inputs",
			embeddingdomain.ErrInvalidEmbeddingResponse,
			len(response.Data),
			len(request.Inputs),
		)
	}

	vectors := make([][]float32, len(request.Inputs))
	seenIndexes := make([]bool, len(request.Inputs))
	for _, item := range response.Data {
		if item.Index < 0 || item.Index >= len(vectors) || seenIndexes[item.Index] {
			return embeddingdomain.EmbedResult{}, fmt.Errorf(
				"%w: invalid or duplicate vector index %d",
				embeddingdomain.ErrInvalidEmbeddingResponse,
				item.Index,
			)
		}
		if len(item.Embedding) != request.Dimensions {
			return embeddingdomain.EmbedResult{}, fmt.Errorf(
				"%w: vector %d has %d dimensions, want %d",
				embeddingdomain.ErrInvalidEmbeddingResponse,
				item.Index,
				len(item.Embedding),
				request.Dimensions,
			)
		}

		seenIndexes[item.Index] = true
		vectors[item.Index] = item.Embedding
	}

	return embeddingdomain.EmbedResult{
		Vectors:      vectors,
		PromptTokens: response.Usage.PromptTokens,
		TotalTokens:  response.Usage.TotalTokens,
	}, nil
}
