package openaiagent

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	agentapplication "rag-reasoning-platform/backend/internal/application/agent"
)

func validModelRequest() agentapplication.ModelRequest {
	return agentapplication.ModelRequest{
		RunID: 7,
		Context: agentapplication.AgentContext{
			SystemInstruction: "Use trusted evidence only.",
			History: []agentapplication.ModelMessage{
				{Role: "user", Content: "earlier question"},
				{Role: "assistant", Content: "earlier answer"},
			},
			CurrentQuestion: "current question",
		},
		Tools: []agentapplication.ToolDefinition{
			{
				Name:        agentapplication.ToolSearchDocuments,
				Description: "search",
				Parameters:  json.RawMessage(`{"type":"object","properties":{"query":{"type":"string"}}}`),
			},
			{
				Name:        agentapplication.ToolReadDocumentEvidence,
				Description: "read",
				Parameters:  json.RawMessage(`{"type":"object","properties":{"evidence_refs":{"type":"array"}}}`),
			},
		},
		Observations: []agentapplication.ModelObservation{
			{
				SequenceNo: 1,
				ToolName:   agentapplication.ToolSearchDocuments,
				Arguments:  json.RawMessage(`{"query":"bridge","strategy":"semantic","top_k":1}`),
				Result:     json.RawMessage(`{"items":[{"evidence_ref":"step-1:item-1"}]}`),
			},
			{
				SequenceNo: 2,
				ToolName:   agentapplication.ToolReadDocumentEvidence,
				Arguments:  json.RawMessage(`{"evidence_refs":["step-1:item-1"]}`),
				Result:     json.RawMessage(`{"items":[{"evidence_ref":"step-1:item-1","excerpt":"evidence"}]}`),
			},
		},
		Remaining: agentapplication.RemainingBudget{
			Steps: 3, Tokens: 100, DeadlineAt: time.Now().Add(time.Minute),
		},
	}
}

func newTestClient(t *testing.T, endpoint string, httpClient HTTPDoer) *Client {
	t.Helper()
	client, err := NewClient(" test-api-key ", endpoint, httpClient, "test-model", 64, 0.1)
	if err != nil {
		t.Fatalf("NewClient() error = %v", err)
	}
	return client
}

func TestClientDecideSendsToolsAndObservationRoundTrip(t *testing.T) {
	modelRequest := validModelRequest()
	modelRequest.Remaining.Tokens = 50
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodPost || request.Header.Get("Authorization") != "Bearer test-api-key" ||
			request.Header.Get("Content-Type") != "application/json" {
			t.Fatalf("request method/headers = %s / %#v", request.Method, request.Header)
		}
		body, err := io.ReadAll(request.Body)
		if err != nil {
			t.Fatalf("read request: %v", err)
		}
		if strings.Contains(string(body), "reasoning_content") || strings.Contains(string(body), "provider-secret-id") {
			t.Fatalf("request contains forbidden provider fields: %s", body)
		}
		var payload chatCompletionRequest
		if err := json.Unmarshal(body, &payload); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		if payload.Model != "test-model" || payload.Stream || payload.N != 1 ||
			payload.ToolChoice != "auto" || payload.ParallelToolCalls ||
			payload.MaxCompletionTokens != 50 || payload.EnableThinking != nil || len(payload.Tools) != 2 {
			t.Fatalf("request payload = %+v", payload)
		}
		if payload.Tools[0].Type != "function" || payload.Tools[0].Function.Name != agentapplication.ToolSearchDocuments ||
			payload.Tools[1].Type != "function" || payload.Tools[1].Function.Name != agentapplication.ToolReadDocumentEvidence ||
			!isJSONObject(payload.Tools[0].Function.Parameters) || !isJSONObject(payload.Tools[1].Function.Parameters) {
			t.Fatalf("tools = %+v", payload.Tools)
		}
		for index := range payload.Tools {
			var gotSchema any
			var wantSchema any
			if err := json.Unmarshal(payload.Tools[index].Function.Parameters, &gotSchema); err != nil {
				t.Fatalf("decode outgoing schema %d: %v", index, err)
			}
			if err := json.Unmarshal(modelRequest.Tools[index].Parameters, &wantSchema); err != nil {
				t.Fatalf("decode expected schema %d: %v", index, err)
			}
			if payload.Tools[index].Function.Description != modelRequest.Tools[index].Description ||
				!reflect.DeepEqual(gotSchema, wantSchema) {
				t.Fatalf("tool %d changed: got=%+v want=%+v", index, payload.Tools[index], modelRequest.Tools[index])
			}
		}
		if len(payload.Messages) != 8 || payload.Messages[0].Role != "system" ||
			!strings.Contains(payload.Messages[0].Content, finalProtocolInstruction) ||
			payload.Messages[1].Content != "earlier question" ||
			payload.Messages[3].Content != "current question" {
			t.Fatalf("messages = %+v", payload.Messages)
		}
		firstID := deterministicToolCallID(7, 1)
		secondID := deterministicToolCallID(7, 2)
		if payload.Messages[4].Role != "assistant" || payload.Messages[4].Content != "" ||
			len(payload.Messages[4].ToolCalls) != 1 || payload.Messages[4].ToolCalls[0].ID != firstID ||
			payload.Messages[4].ToolCalls[0].Type != "function" ||
			payload.Messages[4].ToolCalls[0].Function.Name != agentapplication.ToolSearchDocuments ||
			payload.Messages[4].ToolCalls[0].Function.Arguments != `{"query":"bridge","strategy":"semantic","top_k":1}` ||
			payload.Messages[5].Role != "tool" || payload.Messages[5].ToolCallID != firstID ||
			payload.Messages[5].Content != `{"items":[{"evidence_ref":"step-1:item-1"}]}` ||
			payload.Messages[6].Role != "assistant" || payload.Messages[6].Content != "" ||
			len(payload.Messages[6].ToolCalls) != 1 || payload.Messages[6].ToolCalls[0].ID != secondID ||
			payload.Messages[6].ToolCalls[0].Function.Name != agentapplication.ToolReadDocumentEvidence ||
			payload.Messages[6].ToolCalls[0].Function.Arguments != `{"evidence_refs":["step-1:item-1"]}` ||
			payload.Messages[7].Role != "tool" || payload.Messages[7].ToolCallID != secondID ||
			payload.Messages[7].Content != `{"items":[{"evidence_ref":"step-1:item-1","excerpt":"evidence"}]}` {
			t.Fatalf("observation messages = %+v", payload.Messages[4:])
		}
		response.Header().Set("Content-Type", "application/json")
		_, _ = response.Write([]byte(`{
			"choices":[{
				"index":0,
				"message":{"role":"assistant","content":"  ","reasoning_content":"private","tool_calls":[{
					"id":"provider-secret-id","type":"function",
					"function":{"name":"search_documents","arguments":"{\"query\":\"new\",\"strategy\":\"keyword\",\"top_k\":1}"}
				}]},
				"finish_reason":"tool_calls"
			}],
			"usage":{"prompt_tokens":10,"completion_tokens":3,"total_tokens":13}
		}`))
	}))
	defer server.Close()

	decision, err := newTestClient(t, server.URL, server.Client()).Decide(t.Context(), modelRequest)
	if err != nil {
		t.Fatalf("Decide() error = %v", err)
	}
	if decision.ToolCall == nil || decision.Final != nil || decision.ToolCall.Name != agentapplication.ToolSearchDocuments ||
		string(decision.ToolCall.Arguments) != `{"query":"new","strategy":"keyword","top_k":1}` ||
		decision.Usage.TotalTokens != 13 {
		t.Fatalf("decision = %+v", decision)
	}
}

func TestClientDecideParsesStrictFinalJSON(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
		_, _ = response.Write([]byte(`{
			"choices":[{
				"message":{"role":"assistant","reasoning_content":"must be ignored","content":"{\"text\":\" final answer \",\"evidence_refs\":[\"step-1:item-1\"]}"},
				"finish_reason":"stop"
			}],
			"usage":{"prompt_tokens":20,"completion_tokens":5,"total_tokens":25}
		}`))
	}))
	defer server.Close()
	decision, err := newTestClient(t, server.URL, server.Client()).Decide(t.Context(), validModelRequest())
	if err != nil {
		t.Fatalf("Decide() error = %v", err)
	}
	if decision.Final == nil || decision.ToolCall != nil || decision.Final.Text != "final answer" ||
		len(decision.Final.EvidenceRefs) != 1 || decision.Final.EvidenceRefs[0] != "step-1:item-1" ||
		decision.Usage.TotalTokens != 25 {
		t.Fatalf("decision = %+v", decision)
	}
}

func TestClientDecideAcceptsCompleteZeroUsage(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
		_, _ = response.Write([]byte(`{
			"choices":[{"message":{"role":"assistant","content":"{\"text\":\"cached\",\"evidence_refs\":[\"step-1:item-1\"]}"},"finish_reason":"stop"}],
			"usage":{"prompt_tokens":0,"completion_tokens":0,"total_tokens":0}
		}`))
	}))
	defer server.Close()
	decision, err := newTestClient(t, server.URL, server.Client()).Decide(t.Context(), validModelRequest())
	if err != nil || decision.Final == nil || decision.Usage.TotalTokens != 0 {
		t.Fatalf("zero usage decision = %+v, %v", decision, err)
	}
}

func TestClientDecideRejectsMalformedSuccessResponses(t *testing.T) {
	validUsage := `"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}`
	for _, test := range []struct {
		name string
		body string
	}{
		{name: "invalid JSON", body: `{`},
		{name: "empty choices", body: `{` + validUsage + `,"choices":[]}`},
		{name: "multiple choices", body: `{` + validUsage + `,"choices":[{"message":{"role":"assistant","content":"{\"text\":\"x\",\"evidence_refs\":[\"step-1:item-1\"]}"},"finish_reason":"stop"},{"message":{"role":"assistant","content":"{\"text\":\"y\",\"evidence_refs\":[\"step-1:item-1\"]}"},"finish_reason":"stop"}]}`},
		{name: "missing assistant role", body: `{` + validUsage + `,"choices":[{"message":{"content":"{\"text\":\"x\",\"evidence_refs\":[\"step-1:item-1\"]}"},"finish_reason":"stop"}]}`},
		{name: "wrong assistant role", body: `{` + validUsage + `,"choices":[{"message":{"role":"user","content":"{\"text\":\"x\",\"evidence_refs\":[\"step-1:item-1\"]}"},"finish_reason":"stop"}]}`},
		{name: "assistant refusal", body: `{` + validUsage + `,"choices":[{"message":{"role":"assistant","refusal":"blocked","content":"{\"text\":\"x\",\"evidence_refs\":[\"step-1:item-1\"]}"},"finish_reason":"stop"}]}`},
		{name: "unknown finish", body: `{` + validUsage + `,"choices":[{"message":{"role":"assistant","content":"{}"},"finish_reason":"length"}]}`},
		{name: "tool no calls", body: `{` + validUsage + `,"choices":[{"message":{"role":"assistant","content":""},"finish_reason":"tool_calls"}]}`},
		{name: "multiple tools", body: `{` + validUsage + `,"choices":[{"message":{"role":"assistant","content":"","tool_calls":[{"type":"function","function":{"name":"search_documents","arguments":"{}"}},{"type":"function","function":{"name":"search_documents","arguments":"{}"}}]},"finish_reason":"tool_calls"}]}`},
		{name: "tool content mixed", body: `{` + validUsage + `,"choices":[{"message":{"role":"assistant","content":"answer","tool_calls":[{"type":"function","function":{"name":"search_documents","arguments":"{}"}}]},"finish_reason":"tool_calls"}]}`},
		{name: "tool wrong type", body: `{` + validUsage + `,"choices":[{"message":{"role":"assistant","content":"","tool_calls":[{"type":"other","function":{"name":"search_documents","arguments":"{}"}}]},"finish_reason":"tool_calls"}]}`},
		{name: "tool unknown name", body: `{` + validUsage + `,"choices":[{"message":{"role":"assistant","content":"","tool_calls":[{"type":"function","function":{"name":"write_document","arguments":"{}"}}]},"finish_reason":"tool_calls"}]}`},
		{name: "tool invalid arguments", body: `{` + validUsage + `,"choices":[{"message":{"role":"assistant","content":"","tool_calls":[{"type":"function","function":{"name":"search_documents","arguments":"[]"}}]},"finish_reason":"tool_calls"}]}`},
		{name: "tool duplicate argument", body: `{` + validUsage + `,"choices":[{"message":{"role":"assistant","content":"","tool_calls":[{"type":"function","function":{"name":"search_documents","arguments":"{\"query\":\"x\",\"query\":\"y\"}"}}]},"finish_reason":"tool_calls"}]}`},
		{name: "stop with tool", body: `{` + validUsage + `,"choices":[{"message":{"role":"assistant","content":"{\"text\":\"x\",\"evidence_refs\":[\"step-1:item-1\"]}","tool_calls":[{"type":"function","function":{"name":"search_documents","arguments":"{}"}}]},"finish_reason":"stop"}]}`},
		{name: "free text final", body: `{` + validUsage + `,"choices":[{"message":{"role":"assistant","content":"plain answer"},"finish_reason":"stop"}]}`},
		{name: "final unknown field", body: `{` + validUsage + `,"choices":[{"message":{"role":"assistant","content":"{\"text\":\"x\",\"evidence_refs\":[\"step-1:item-1\"],\"extra\":1}"},"finish_reason":"stop"}]}`},
		{name: "final duplicate text", body: `{` + validUsage + `,"choices":[{"message":{"role":"assistant","content":"{\"text\":\"x\",\"text\":\"y\",\"evidence_refs\":[\"step-1:item-1\"]}"},"finish_reason":"stop"}]}`},
		{name: "final duplicate refs field", body: `{` + validUsage + `,"choices":[{"message":{"role":"assistant","content":"{\"text\":\"x\",\"evidence_refs\":[\"step-1:item-1\"],\"evidence_refs\":[\"step-2:item-1\"]}"},"finish_reason":"stop"}]}`},
		{name: "final trailing JSON", body: `{` + validUsage + `,"choices":[{"message":{"role":"assistant","content":"{\"text\":\"x\",\"evidence_refs\":[\"step-1:item-1\"]} {}"},"finish_reason":"stop"}]}`},
		{name: "final empty text", body: `{` + validUsage + `,"choices":[{"message":{"role":"assistant","content":"{\"text\":\" \",\"evidence_refs\":[\"step-1:item-1\"]}"},"finish_reason":"stop"}]}`},
		{name: "final empty refs", body: `{` + validUsage + `,"choices":[{"message":{"role":"assistant","content":"{\"text\":\"x\",\"evidence_refs\":[]}"},"finish_reason":"stop"}]}`},
		{name: "final duplicate ref", body: `{` + validUsage + `,"choices":[{"message":{"role":"assistant","content":"{\"text\":\"x\",\"evidence_refs\":[\"step-1:item-1\",\"step-1:item-1\"]}"},"finish_reason":"stop"}]}`},
		{name: "final invalid ref", body: `{` + validUsage + `,"choices":[{"message":{"role":"assistant","content":"{\"text\":\"x\",\"evidence_refs\":[\"other\"]}"},"finish_reason":"stop"}]}`},
		{name: "missing usage", body: `{"choices":[{"message":{"role":"assistant","content":"{\"text\":\"x\",\"evidence_refs\":[\"step-1:item-1\"]}"},"finish_reason":"stop"}]}`},
		{name: "partial usage", body: `{"choices":[{"message":{"role":"assistant","content":"{\"text\":\"x\",\"evidence_refs\":[\"step-1:item-1\"]}"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"total_tokens":1}}`},
		{name: "negative usage", body: `{"choices":[{"message":{"role":"assistant","content":"{\"text\":\"x\",\"evidence_refs\":[\"step-1:item-1\"]}"},"finish_reason":"stop"}],"usage":{"prompt_tokens":-1,"completion_tokens":1,"total_tokens":0}}`},
		{name: "inconsistent usage", body: `{"choices":[{"message":{"role":"assistant","content":"{\"text\":\"x\",\"evidence_refs\":[\"step-1:item-1\"]}"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":3}}`},
	} {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
				_, _ = response.Write([]byte(test.body))
			}))
			defer server.Close()
			if _, err := newTestClient(t, server.URL, server.Client()).Decide(t.Context(), validModelRequest()); !errors.Is(err, agentapplication.ErrInvalidModelDecision) {
				t.Fatalf("Decide() error = %v", err)
			}
		})
	}
}

func TestClientDecideMapsHTTPAndNetworkErrorsSafely(t *testing.T) {
	for _, test := range []struct {
		status int
		want   error
	}{
		{status: http.StatusBadRequest, want: agentapplication.ErrModelRequestRejected},
		{status: http.StatusUnauthorized, want: agentapplication.ErrModelRequestRejected},
		{status: http.StatusForbidden, want: agentapplication.ErrModelRequestRejected},
		{status: http.StatusUnprocessableEntity, want: agentapplication.ErrModelRequestRejected},
		{status: http.StatusRequestTimeout, want: agentapplication.ErrModelTemporarilyUnavailable},
		{status: http.StatusTooManyRequests, want: agentapplication.ErrModelTemporarilyUnavailable},
		{status: http.StatusInternalServerError, want: agentapplication.ErrModelTemporarilyUnavailable},
		{status: http.StatusServiceUnavailable, want: agentapplication.ErrModelTemporarilyUnavailable},
	} {
		t.Run(http.StatusText(test.status), func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
				response.WriteHeader(test.status)
				_, _ = response.Write([]byte(`{"error":{"message":"provider-secret-marker"}}`))
			}))
			defer server.Close()
			_, err := newTestClient(t, server.URL, server.Client()).Decide(t.Context(), validModelRequest())
			if !errors.Is(err, test.want) || strings.Contains(err.Error(), "provider-secret-marker") {
				t.Fatalf("Decide() error = %v, want %v without body", err, test.want)
			}
		})
	}

	client := newTestClient(t, "https://example.invalid", HTTPDoerFunc(func(*http.Request) (*http.Response, error) {
		return nil, errors.New("network-secret-marker")
	}))
	if _, err := client.Decide(t.Context(), validModelRequest()); !errors.Is(err, agentapplication.ErrModelTemporarilyUnavailable) || strings.Contains(err.Error(), "network-secret-marker") {
		t.Fatalf("network error = %v", err)
	}
	client = newTestClient(t, "https://example.invalid", HTTPDoerFunc(func(*http.Request) (*http.Response, error) {
		return nil, nil
	}))
	if _, err := client.Decide(t.Context(), validModelRequest()); !errors.Is(err, agentapplication.ErrModelTemporarilyUnavailable) {
		t.Fatalf("nil transport response error = %v", err)
	}
	tracked := &trackingReadCloser{Reader: strings.NewReader("unused")}
	client = newTestClient(t, "https://example.invalid", HTTPDoerFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusOK, Body: tracked}, errors.New("redirect failure")
	}))
	if _, err := client.Decide(t.Context(), validModelRequest()); !errors.Is(err, agentapplication.ErrModelTemporarilyUnavailable) || !tracked.closed {
		t.Fatalf("response+error = %v, body closed=%t", err, tracked.closed)
	}
	client = newTestClient(t, "https://example.invalid", HTTPDoerFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusOK}, nil
	}))
	if _, err := client.Decide(t.Context(), validModelRequest()); !errors.Is(err, agentapplication.ErrInvalidModelDecision) {
		t.Fatalf("nil success body error = %v", err)
	}
}

func TestClientDecideMapsContextTimeoutAndResponseLimit(t *testing.T) {
	t.Run("context timeout", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
		defer cancel()
		client := newTestClient(t, "https://example.com", HTTPDoerFunc(func(request *http.Request) (*http.Response, error) {
			<-request.Context().Done()
			return nil, request.Context().Err()
		}))
		_, err := client.Decide(ctx, validModelRequest())
		if !errors.Is(err, agentapplication.ErrModelTemporarilyUnavailable) || !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("timeout error = %v", err)
		}
	})

	t.Run("oversized body", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
			_, _ = response.Write([]byte(strings.Repeat("x", int(maxResponseBodyBytes)+1)))
		}))
		defer server.Close()
		if _, err := newTestClient(t, server.URL, server.Client()).Decide(t.Context(), validModelRequest()); !errors.Is(err, agentapplication.ErrInvalidModelDecision) {
			t.Fatalf("oversized body error = %v", err)
		}
	})

	t.Run("exact body limit", func(t *testing.T) {
		base := `{"choices":[{"message":{"role":"assistant","content":"{\"text\":\"x\",\"evidence_refs\":[\"step-1:item-1\"]}"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}`
		body := base + strings.Repeat(" ", int(maxResponseBodyBytes)-len(base))
		server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
			_, _ = response.Write([]byte(body))
		}))
		defer server.Close()
		decision, err := newTestClient(t, server.URL, server.Client()).Decide(t.Context(), validModelRequest())
		if err != nil || decision.Final == nil {
			t.Fatalf("exact-limit response = %+v, %v", decision, err)
		}
	})
}

func TestClientRejectsInvalidConfigurationAndRequestWithoutHTTP(t *testing.T) {
	for _, test := range []struct {
		name        string
		provider    string
		apiKey      string
		endpoint    string
		httpClient  HTTPDoer
		model       string
		maxTokens   int
		temperature float64
	}{
		{name: "provider", apiKey: "key", endpoint: DefaultEndpoint, httpClient: http.DefaultClient, model: "model", maxTokens: 10},
		{name: "api key", provider: "Provider", endpoint: DefaultEndpoint, httpClient: http.DefaultClient, model: "model", maxTokens: 10},
		{name: "endpoint", provider: "Provider", apiKey: "key", endpoint: "bad", httpClient: http.DefaultClient, model: "model", maxTokens: 10},
		{name: "http client", provider: "Provider", apiKey: "key", endpoint: DefaultEndpoint, model: "model", maxTokens: 10},
		{name: "model", provider: "Provider", apiKey: "key", endpoint: DefaultEndpoint, httpClient: http.DefaultClient, maxTokens: 10},
		{name: "max tokens", provider: "Provider", apiKey: "key", endpoint: DefaultEndpoint, httpClient: http.DefaultClient, model: "model"},
		{name: "temperature", provider: "Provider", apiKey: "key", endpoint: DefaultEndpoint, httpClient: http.DefaultClient, model: "model", maxTokens: 10, temperature: -1},
	} {
		t.Run(test.name, func(t *testing.T) {
			client, err := NewCompatibleClient(test.provider, test.apiKey, test.endpoint, test.httpClient, test.model, test.maxTokens, test.temperature)
			if err == nil || client != nil {
				t.Fatalf("NewCompatibleClient(invalid) = %v, %v", client, err)
			}
		})
	}
	calls := 0
	client := newTestClient(t, "https://example.com", HTTPDoerFunc(func(*http.Request) (*http.Response, error) {
		calls++
		return nil, errors.New("unexpected")
	}))
	request := validModelRequest()
	request.Observations[0].Arguments = json.RawMessage(`[]`)
	if _, err := client.Decide(t.Context(), request); !errors.Is(err, agentapplication.ErrModelRequestRejected) {
		t.Fatalf("invalid request error = %v", err)
	}
	if calls != 0 {
		t.Fatalf("HTTP calls = %d, want 0", calls)
	}
}

func TestDeterministicToolCallIDUsesOnlyRunAndSequence(t *testing.T) {
	first := deterministicToolCallID(7, 1)
	if first != deterministicToolCallID(7, 1) || first == deterministicToolCallID(7, 2) ||
		first == deterministicToolCallID(8, 1) || !strings.HasPrefix(first, "call_") || len(first) > 64 {
		t.Fatalf("deterministic IDs = %q / %q / %q", first, deterministicToolCallID(7, 2), deterministicToolCallID(8, 1))
	}
}

type HTTPDoerFunc func(*http.Request) (*http.Response, error)

func (f HTTPDoerFunc) Do(request *http.Request) (*http.Response, error) {
	return f(request)
}

type trackingReadCloser struct {
	io.Reader
	closed bool
}

func (r *trackingReadCloser) Close() error {
	r.closed = true
	return nil
}
