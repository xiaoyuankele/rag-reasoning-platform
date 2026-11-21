// Package openaiagent adapts the provider-neutral AgentModel port to the
// OpenAI-compatible non-streaming Chat Completions tool-call protocol.
package openaiagent

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"

	agentapplication "rag-reasoning-platform/backend/internal/application/agent"
)

const (
	defaultProviderName                = "OpenAI"
	DefaultEndpoint                    = "https://api.openai.com/v1/chat/completions"
	maxResponseBodyBytes         int64 = 1024 * 1024
	maxObservationArgumentsBytes       = 8 * 1024
	maxObservationResultBytes          = 32 * 1024
	maxFinalEvidenceRefs               = 10
)

const finalProtocolInstruction = `When you have enough evidence, return only one JSON object with exactly this shape: {"text":"final answer","evidence_refs":["step-N:item-M"]}. Do not include markdown fences, extra fields, hidden reasoning, tool metadata, or prose outside the JSON object.`

var evidenceRefPattern = regexp.MustCompile(`^step-[1-9][0-9]*:item-[1-9][0-9]*$`)

type HTTPDoer interface {
	Do(request *http.Request) (*http.Response, error)
}

type CompatibleOptions struct {
	EnableThinking *bool
}

type Client struct {
	providerName        string
	apiKey              string
	endpoint            string
	httpClient          HTTPDoer
	model               string
	maxCompletionTokens int
	temperature         float64
	enableThinking      *bool
}

var _ agentapplication.AgentModel = (*Client)(nil)

func NewClient(
	apiKey string,
	endpoint string,
	httpClient HTTPDoer,
	model string,
	maxCompletionTokens int,
	temperature float64,
) (*Client, error) {
	return NewCompatibleClient(
		defaultProviderName,
		apiKey,
		endpoint,
		httpClient,
		model,
		maxCompletionTokens,
		temperature,
	)
}

func NewCompatibleClient(
	providerName string,
	apiKey string,
	endpoint string,
	httpClient HTTPDoer,
	model string,
	maxCompletionTokens int,
	temperature float64,
) (*Client, error) {
	return NewCompatibleClientWithOptions(
		providerName,
		apiKey,
		endpoint,
		httpClient,
		model,
		maxCompletionTokens,
		temperature,
		CompatibleOptions{},
	)
}

func NewCompatibleClientWithOptions(
	providerName string,
	apiKey string,
	endpoint string,
	httpClient HTTPDoer,
	model string,
	maxCompletionTokens int,
	temperature float64,
	options CompatibleOptions,
) (*Client, error) {
	providerName = strings.TrimSpace(providerName)
	apiKey = strings.TrimSpace(apiKey)
	endpoint = strings.TrimSpace(endpoint)
	model = strings.TrimSpace(model)
	parsedEndpoint, endpointErr := url.Parse(endpoint)
	if providerName == "" || apiKey == "" || model == "" || httpClient == nil ||
		maxCompletionTokens <= 0 || math.IsNaN(temperature) || math.IsInf(temperature, 0) ||
		temperature < 0 || temperature > 2 || endpointErr != nil || parsedEndpoint.Host == "" ||
		(parsedEndpoint.Scheme != "http" && parsedEndpoint.Scheme != "https") {
		return nil, errors.New("agent model client configuration is invalid")
	}
	var enableThinking *bool
	if options.EnableThinking != nil {
		value := *options.EnableThinking
		enableThinking = &value
	}
	return &Client{
		providerName:        providerName,
		apiKey:              apiKey,
		endpoint:            endpoint,
		httpClient:          httpClient,
		model:               model,
		maxCompletionTokens: maxCompletionTokens,
		temperature:         temperature,
		enableThinking:      enableThinking,
	}, nil
}

type chatFunction struct {
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	Parameters  json.RawMessage `json:"parameters,omitempty"`
	Arguments   string          `json:"arguments,omitempty"`
}

type chatToolCall struct {
	ID       string       `json:"id"`
	Type     string       `json:"type"`
	Function chatFunction `json:"function"`
}

type chatMessage struct {
	Role       string         `json:"role"`
	Content    string         `json:"content"`
	ToolCalls  []chatToolCall `json:"tool_calls,omitempty"`
	ToolCallID string         `json:"tool_call_id,omitempty"`
}

type chatTool struct {
	Type     string       `json:"type"`
	Function chatFunction `json:"function"`
}

type chatCompletionRequest struct {
	Model               string        `json:"model"`
	Messages            []chatMessage `json:"messages"`
	Tools               []chatTool    `json:"tools"`
	ToolChoice          string        `json:"tool_choice"`
	ParallelToolCalls   bool          `json:"parallel_tool_calls"`
	Stream              bool          `json:"stream"`
	N                   int           `json:"n"`
	MaxCompletionTokens int           `json:"max_completion_tokens"`
	Temperature         float64       `json:"temperature"`
	EnableThinking      *bool         `json:"enable_thinking,omitempty"`
}

type chatCompletionResponse struct {
	Choices []struct {
		Index   int `json:"index"`
		Message struct {
			Role      string  `json:"role"`
			Content   *string `json:"content"`
			Refusal   *string `json:"refusal"`
			ToolCalls []struct {
				ID       string `json:"id"`
				Type     string `json:"type"`
				Function struct {
					Name      string `json:"name"`
					Arguments string `json:"arguments"`
				} `json:"function"`
			} `json:"tool_calls"`
		} `json:"message"`
		FinishReason string `json:"finish_reason"`
	} `json:"choices"`
	Usage *struct {
		PromptTokens     *int `json:"prompt_tokens"`
		CompletionTokens *int `json:"completion_tokens"`
		TotalTokens      *int `json:"total_tokens"`
	} `json:"usage"`
}

type finalContent struct {
	Text         string   `json:"text"`
	EvidenceRefs []string `json:"evidence_refs"`
}

func (c *Client) Decide(
	ctx context.Context,
	request agentapplication.ModelRequest,
) (agentapplication.ModelDecision, error) {
	messages, tools, maxCompletionTokens, err := c.buildRequest(request)
	if err != nil {
		return agentapplication.ModelDecision{}, err
	}
	payload, err := json.Marshal(chatCompletionRequest{
		Model:               c.model,
		Messages:            messages,
		Tools:               tools,
		ToolChoice:          "auto",
		ParallelToolCalls:   false,
		Stream:              false,
		N:                   1,
		MaxCompletionTokens: maxCompletionTokens,
		Temperature:         c.temperature,
		EnableThinking:      c.enableThinking,
	})
	if err != nil {
		return agentapplication.ModelDecision{}, fmt.Errorf("%w: encode request", agentapplication.ErrModelRequestRejected)
	}
	httpRequest, err := http.NewRequestWithContext(ctx, http.MethodPost, c.endpoint, bytes.NewReader(payload))
	if err != nil {
		return agentapplication.ModelDecision{}, fmt.Errorf("%w: create request", agentapplication.ErrModelRequestRejected)
	}
	httpRequest.Header.Set("Authorization", "Bearer "+c.apiKey)
	httpRequest.Header.Set("Content-Type", "application/json")
	httpResponse, err := c.httpClient.Do(httpRequest)
	if err != nil {
		if httpResponse != nil && httpResponse.Body != nil {
			_ = httpResponse.Body.Close()
		}
		if ctx.Err() != nil {
			return agentapplication.ModelDecision{}, errors.Join(agentapplication.ErrModelTemporarilyUnavailable, ctx.Err())
		}
		return agentapplication.ModelDecision{}, fmt.Errorf("%w: %s request failed", agentapplication.ErrModelTemporarilyUnavailable, c.providerName)
	}
	if httpResponse == nil {
		return agentapplication.ModelDecision{}, fmt.Errorf("%w: %s returned no response", agentapplication.ErrModelTemporarilyUnavailable, c.providerName)
	}
	if httpResponse.Body == nil {
		if httpResponse.StatusCode >= http.StatusOK && httpResponse.StatusCode < http.StatusMultipleChoices {
			return agentapplication.ModelDecision{}, fmt.Errorf("%w: response body is missing", agentapplication.ErrInvalidModelDecision)
		}
		return agentapplication.ModelDecision{}, modelHTTPStatusError(c.providerName, httpResponse.StatusCode)
	}
	defer httpResponse.Body.Close()
	if httpResponse.StatusCode < http.StatusOK || httpResponse.StatusCode >= http.StatusMultipleChoices {
		_, _ = io.Copy(io.Discard, io.LimitReader(httpResponse.Body, maxResponseBodyBytes))
		return agentapplication.ModelDecision{}, modelHTTPStatusError(c.providerName, httpResponse.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(httpResponse.Body, maxResponseBodyBytes+1))
	if err != nil {
		return agentapplication.ModelDecision{}, fmt.Errorf("%w: %s response read failed", agentapplication.ErrModelTemporarilyUnavailable, c.providerName)
	}
	if int64(len(body)) > maxResponseBodyBytes {
		return agentapplication.ModelDecision{}, fmt.Errorf("%w: response exceeds size limit", agentapplication.ErrInvalidModelDecision)
	}
	var response chatCompletionResponse
	if err := json.Unmarshal(body, &response); err != nil {
		return agentapplication.ModelDecision{}, fmt.Errorf("%w: malformed response", agentapplication.ErrInvalidModelDecision)
	}
	return normalizeResponse(response, tools)
}

func modelHTTPStatusError(providerName string, statusCode int) error {
	if statusCode == http.StatusRequestTimeout || statusCode == http.StatusTooManyRequests ||
		statusCode >= http.StatusInternalServerError {
		return fmt.Errorf(
			"%w: %s returned status %d",
			agentapplication.ErrModelTemporarilyUnavailable,
			providerName,
			statusCode,
		)
	}
	return fmt.Errorf(
		"%w: %s returned status %d",
		agentapplication.ErrModelRequestRejected,
		providerName,
		statusCode,
	)
}

func (c *Client) buildRequest(
	request agentapplication.ModelRequest,
) ([]chatMessage, []chatTool, int, error) {
	if request.RunID <= 0 || !request.Context.IsValid() || request.Remaining.Steps <= 0 ||
		request.Remaining.Tokens <= 0 || request.Remaining.DeadlineAt.IsZero() ||
		!time.Now().Before(request.Remaining.DeadlineAt) || len(request.Tools) != 2 {
		return nil, nil, 0, agentapplication.ErrModelRequestRejected
	}
	tools := make([]chatTool, 0, len(request.Tools))
	toolNames := make(map[string]struct{}, len(request.Tools))
	for _, definition := range request.Tools {
		if (definition.Name != agentapplication.ToolSearchDocuments && definition.Name != agentapplication.ToolReadDocumentEvidence) ||
			strings.TrimSpace(definition.Description) == "" || !isJSONObject(definition.Parameters) {
			return nil, nil, 0, agentapplication.ErrModelRequestRejected
		}
		if _, duplicate := toolNames[definition.Name]; duplicate {
			return nil, nil, 0, agentapplication.ErrModelRequestRejected
		}
		toolNames[definition.Name] = struct{}{}
		tools = append(tools, chatTool{
			Type: "function",
			Function: chatFunction{
				Name:        definition.Name,
				Description: definition.Description,
				Parameters:  append(json.RawMessage(nil), definition.Parameters...),
			},
		})
	}
	messages := []chatMessage{{
		Role:    "system",
		Content: strings.TrimSpace(request.Context.SystemInstruction) + "\n" + finalProtocolInstruction,
	}}
	for _, message := range request.Context.History {
		messages = append(messages, chatMessage{Role: message.Role, Content: message.Content})
	}
	messages = append(messages, chatMessage{Role: "user", Content: request.Context.CurrentQuestion})
	lastSequence := 0
	for _, observation := range request.Observations {
		if observation.SequenceNo != lastSequence+1 ||
			len(observation.Arguments) == 0 || len(observation.Arguments) > maxObservationArgumentsBytes ||
			len(observation.Result) == 0 || len(observation.Result) > maxObservationResultBytes ||
			!isJSONObject(observation.Arguments) || !isJSONObject(observation.Result) {
			return nil, nil, 0, agentapplication.ErrModelRequestRejected
		}
		if _, exists := toolNames[observation.ToolName]; !exists {
			return nil, nil, 0, agentapplication.ErrModelRequestRejected
		}
		callID := deterministicToolCallID(request.RunID, observation.SequenceNo)
		messages = append(messages,
			chatMessage{
				Role:    "assistant",
				Content: "",
				ToolCalls: []chatToolCall{{
					ID:   callID,
					Type: "function",
					Function: chatFunction{
						Name:      observation.ToolName,
						Arguments: string(observation.Arguments),
					},
				}},
			},
			chatMessage{Role: "tool", Content: string(observation.Result), ToolCallID: callID},
		)
		lastSequence = observation.SequenceNo
	}
	maxTokens := min(c.maxCompletionTokens, request.Remaining.Tokens)
	if maxTokens <= 0 {
		return nil, nil, 0, agentapplication.ErrModelRequestRejected
	}
	return messages, tools, maxTokens, nil
}

func normalizeResponse(
	response chatCompletionResponse,
	requestTools []chatTool,
) (agentapplication.ModelDecision, error) {
	usage, err := normalizeUsage(response.Usage)
	if err != nil || len(response.Choices) != 1 {
		return agentapplication.ModelDecision{}, agentapplication.ErrInvalidModelDecision
	}
	choice := response.Choices[0]
	if choice.Message.Role != "assistant" ||
		(choice.Message.Refusal != nil && strings.TrimSpace(*choice.Message.Refusal) != "") {
		return agentapplication.ModelDecision{}, agentapplication.ErrInvalidModelDecision
	}
	toolNames := make(map[string]struct{}, len(requestTools))
	for _, tool := range requestTools {
		toolNames[tool.Function.Name] = struct{}{}
	}
	switch choice.FinishReason {
	case "tool_calls":
		if choice.Message.Content != nil && strings.TrimSpace(*choice.Message.Content) != "" || len(choice.Message.ToolCalls) != 1 {
			return agentapplication.ModelDecision{}, agentapplication.ErrInvalidModelDecision
		}
		call := choice.Message.ToolCalls[0]
		arguments := json.RawMessage(call.Function.Arguments)
		if call.Type != "function" || strings.TrimSpace(call.Function.Name) != call.Function.Name ||
			call.Function.Name == "" || !isJSONObject(arguments) {
			return agentapplication.ModelDecision{}, agentapplication.ErrInvalidModelDecision
		}
		if _, exists := toolNames[call.Function.Name]; !exists {
			return agentapplication.ModelDecision{}, agentapplication.ErrInvalidModelDecision
		}
		return agentapplication.ModelDecision{
			ToolCall: &agentapplication.ModelToolCall{
				Name:      call.Function.Name,
				Arguments: append(json.RawMessage(nil), arguments...),
			},
			Usage: usage,
		}, nil
	case "stop":
		if len(choice.Message.ToolCalls) != 0 || choice.Message.Content == nil {
			return agentapplication.ModelDecision{}, agentapplication.ErrInvalidModelDecision
		}
		final, err := decodeFinalContent(*choice.Message.Content)
		if err != nil {
			return agentapplication.ModelDecision{}, agentapplication.ErrInvalidModelDecision
		}
		return agentapplication.ModelDecision{
			Final: &agentapplication.ModelFinalAnswer{
				Text:         final.Text,
				EvidenceRefs: append([]string(nil), final.EvidenceRefs...),
			},
			Usage: usage,
		}, nil
	default:
		return agentapplication.ModelDecision{}, agentapplication.ErrInvalidModelDecision
	}
}

func normalizeUsage(usage *struct {
	PromptTokens     *int `json:"prompt_tokens"`
	CompletionTokens *int `json:"completion_tokens"`
	TotalTokens      *int `json:"total_tokens"`
}) (agentapplication.TokenUsage, error) {
	if usage == nil || usage.PromptTokens == nil || usage.CompletionTokens == nil || usage.TotalTokens == nil {
		return agentapplication.TokenUsage{}, agentapplication.ErrInvalidModelDecision
	}
	result := agentapplication.TokenUsage{
		PromptTokens:     *usage.PromptTokens,
		CompletionTokens: *usage.CompletionTokens,
		TotalTokens:      *usage.TotalTokens,
	}
	if !result.IsValid() {
		return agentapplication.TokenUsage{}, agentapplication.ErrInvalidModelDecision
	}
	return result, nil
}

func decodeFinalContent(content string) (finalContent, error) {
	if !validateRootObjectKeys([]byte(content), "text", "evidence_refs") {
		return finalContent{}, agentapplication.ErrInvalidModelDecision
	}
	decoder := json.NewDecoder(strings.NewReader(content))
	decoder.DisallowUnknownFields()
	var final finalContent
	if err := decoder.Decode(&final); err != nil {
		return finalContent{}, err
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return finalContent{}, agentapplication.ErrInvalidModelDecision
	}
	final.Text = strings.TrimSpace(final.Text)
	if final.Text == "" || len(final.EvidenceRefs) == 0 || len(final.EvidenceRefs) > maxFinalEvidenceRefs {
		return finalContent{}, agentapplication.ErrInvalidModelDecision
	}
	seen := make(map[string]struct{}, len(final.EvidenceRefs))
	for _, ref := range final.EvidenceRefs {
		if len(ref) > 64 || !evidenceRefPattern.MatchString(ref) {
			return finalContent{}, agentapplication.ErrInvalidModelDecision
		}
		if _, duplicate := seen[ref]; duplicate {
			return finalContent{}, agentapplication.ErrInvalidModelDecision
		}
		seen[ref] = struct{}{}
	}
	return final, nil
}

func isJSONObject(raw json.RawMessage) bool {
	return validateRootObjectKeys(raw)
}

func validateRootObjectKeys(raw []byte, allowedKeys ...string) bool {
	if !json.Valid(raw) {
		return false
	}
	allowed := make(map[string]struct{}, len(allowedKeys))
	for _, key := range allowedKeys {
		allowed[key] = struct{}{}
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	opening, err := decoder.Token()
	if err != nil || opening != json.Delim('{') {
		return false
	}
	seen := make(map[string]struct{})
	for decoder.More() {
		token, err := decoder.Token()
		if err != nil {
			return false
		}
		key, ok := token.(string)
		if !ok {
			return false
		}
		if len(allowed) > 0 {
			if _, exists := allowed[key]; !exists {
				return false
			}
		}
		if _, duplicate := seen[key]; duplicate {
			return false
		}
		seen[key] = struct{}{}
		var value json.RawMessage
		if err := decoder.Decode(&value); err != nil {
			return false
		}
	}
	closing, err := decoder.Token()
	if err != nil || closing != json.Delim('}') {
		return false
	}
	if _, err := decoder.Token(); !errors.Is(err, io.EOF) {
		return false
	}
	if len(allowed) > 0 && len(seen) != len(allowed) {
		return false
	}
	return true
}

func deterministicToolCallID(runID int64, sequenceNo int) string {
	digest := sha256.Sum256([]byte(fmt.Sprintf("agent-run:%d:step:%d", runID, sequenceNo)))
	return "call_" + hex.EncodeToString(digest[:12])
}
