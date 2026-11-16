package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"

	agentapplication "rag-reasoning-platform/backend/internal/application/agent"
	accessdomain "rag-reasoning-platform/backend/internal/domain/access"
	agentdomain "rag-reasoning-platform/backend/internal/domain/agent"
	conversationdomain "rag-reasoning-platform/backend/internal/domain/conversation"
	documentdomain "rag-reasoning-platform/backend/internal/domain/document"
)

type fakeAgentRunService struct {
	queueResult  agentapplication.QueueResult
	queueErr     error
	queueFunc    func(agentapplication.QueueInput) (agentapplication.QueueResult, error)
	getResult    agentapplication.RunSnapshot
	getErr       error
	activeResult agentapplication.RunSnapshot
	activeErr    error
	latestResult agentapplication.RunSnapshot
	latestErr    error
	cancelResult agentapplication.RunSnapshot
	cancelErr    error

	queueInput  agentapplication.QueueInput
	getID       int64
	activeID    int64
	latestID    int64
	cancelID    int64
	queueCalls  int
	getCalls    int
	activeCalls int
	latestCalls int
	cancelCalls int
	scopes      []int64
}

func (f *fakeAgentRunService) Queue(
	_ context.Context,
	scope accessdomain.OwnerScope,
	input agentapplication.QueueInput,
) (agentapplication.QueueResult, error) {
	f.queueCalls++
	f.scopes = append(f.scopes, scope.OwnerUserID())
	f.queueInput = input
	if f.queueFunc != nil {
		return f.queueFunc(input)
	}
	return f.queueResult, f.queueErr
}

func (f *fakeAgentRunService) GetByID(
	_ context.Context,
	scope accessdomain.OwnerScope,
	runID int64,
) (agentapplication.RunSnapshot, error) {
	f.getCalls++
	f.scopes = append(f.scopes, scope.OwnerUserID())
	f.getID = runID
	return f.getResult, f.getErr
}

func (f *fakeAgentRunService) GetActiveConversationRun(
	_ context.Context,
	scope accessdomain.OwnerScope,
	conversationID int64,
) (agentapplication.RunSnapshot, error) {
	f.activeCalls++
	f.scopes = append(f.scopes, scope.OwnerUserID())
	f.activeID = conversationID
	return f.activeResult, f.activeErr
}

func (f *fakeAgentRunService) GetLatestConversationRun(
	_ context.Context,
	scope accessdomain.OwnerScope,
	conversationID int64,
) (agentapplication.RunSnapshot, error) {
	f.latestCalls++
	f.scopes = append(f.scopes, scope.OwnerUserID())
	f.latestID = conversationID
	return f.latestResult, f.latestErr
}

func (f *fakeAgentRunService) Cancel(
	_ context.Context,
	scope accessdomain.OwnerScope,
	runID int64,
) (agentapplication.RunSnapshot, error) {
	f.cancelCalls++
	f.scopes = append(f.scopes, scope.OwnerUserID())
	f.cancelID = runID
	return f.cancelResult, f.cancelErr
}

func newAgentRunTestRouter(service agentRunService, authenticated bool) *gin.Engine {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.Use(RequestIDMiddleware())
	if authenticated {
		useTestAuthenticatedIdentity(router)
	}
	NewAgentRunHandler(service, slog.New(slog.NewTextHandler(io.Discard, nil))).RegisterRoutes(router)
	return router
}

func TestAgentRunHandlerRegistersFiveSnapshotRoutes(t *testing.T) {
	service := &fakeAgentRunService{
		getResult:    validQueuedAgentSnapshot(),
		activeResult: validProcessingAgentSnapshot(),
		latestResult: validSucceededAgentSnapshot(),
		cancelResult: validCanceledAgentSnapshot(),
	}
	service.queueFunc = func(input agentapplication.QueueInput) (agentapplication.QueueResult, error) {
		return agentQueueResultForInput(input, true), nil
	}
	router := newAgentRunTestRouter(service, true)

	queueResponse := performAgentRequest(router, http.MethodPost, "/conversations/11/agent-runs", []byte(`{"content":" exact question ","client_run_id":" run-1 ","response_language":""}`))
	if queueResponse.Code != http.StatusAccepted || service.queueCalls != 1 ||
		service.queueInput.Content != " exact question " || service.queueInput.ClientRunID != " run-1 " ||
		service.queueInput.ResponseLanguage != "" || service.queueInput.ConversationID != 11 {
		t.Fatalf("Queue status/input = %d / %+v: %s", queueResponse.Code, service.queueInput, queueResponse.Body.String())
	}
	assertAgentCreateResponse(t, queueResponse.Body.Bytes(), 11, "user", "assistant")

	service.queueFunc = func(input agentapplication.QueueInput) (agentapplication.QueueResult, error) {
		result := agentQueueResultForInput(input, false)
		return result, nil
	}
	replayResponse := performAgentRequest(router, http.MethodPost, "/conversations/11/agent-runs", []byte(`{"content":" exact question ","client_run_id":"run-1"}`))
	if replayResponse.Code != http.StatusOK {
		t.Fatalf("replay status = %d: %s", replayResponse.Code, replayResponse.Body.String())
	}

	for _, test := range []struct {
		name       string
		method     string
		path       string
		wantStatus int
	}{
		{name: "get", method: http.MethodGet, path: "/agent-runs/101", wantStatus: http.StatusOK},
		{name: "active", method: http.MethodGet, path: "/conversations/11/agent-runs/active", wantStatus: http.StatusOK},
		{name: "latest", method: http.MethodGet, path: "/conversations/11/agent-runs/latest", wantStatus: http.StatusOK},
		{name: "cancel", method: http.MethodPost, path: "/agent-runs/101/cancel", wantStatus: http.StatusOK},
	} {
		t.Run(test.name, func(t *testing.T) {
			response := performAgentRequest(router, test.method, test.path, nil)
			if response.Code != test.wantStatus || !strings.Contains(response.Body.String(), `"agent_run"`) {
				t.Fatalf("status/body = %d / %s", response.Code, response.Body.String())
			}
		})
	}
	if service.getCalls != 1 || service.activeCalls != 1 || service.latestCalls != 1 || service.cancelCalls != 1 ||
		service.getID != 101 || service.activeID != 11 || service.latestID != 11 || service.cancelID != 101 {
		t.Fatalf("service calls/IDs = get:%d/%d active:%d/%d latest:%d/%d cancel:%d/%d",
			service.getCalls, service.getID, service.activeCalls, service.activeID,
			service.latestCalls, service.latestID, service.cancelCalls, service.cancelID)
	}
	for _, ownerID := range service.scopes {
		if ownerID != testAPIOwnerUserID {
			t.Fatalf("service OwnerScope = %d", ownerID)
		}
	}
}

func TestAgentRunHandlerRequiresAuthenticationForEveryRoute(t *testing.T) {
	service := &fakeAgentRunService{}
	router := newAgentRunTestRouter(service, false)
	for _, test := range []struct {
		method string
		path   string
		body   []byte
	}{
		{method: http.MethodPost, path: "/conversations/11/agent-runs", body: []byte(`{"content":"q","client_run_id":"r"}`)},
		{method: http.MethodGet, path: "/agent-runs/101"},
		{method: http.MethodGet, path: "/conversations/11/agent-runs/active"},
		{method: http.MethodGet, path: "/conversations/11/agent-runs/latest"},
		{method: http.MethodPost, path: "/agent-runs/101/cancel"},
	} {
		response := performAgentRequest(router, test.method, test.path, test.body)
		if response.Code != http.StatusUnauthorized || !strings.Contains(response.Body.String(), `"code":"authentication_required"`) {
			t.Fatalf("%s %s status/body = %d / %s", test.method, test.path, response.Code, response.Body.String())
		}
	}
	if service.queueCalls+service.getCalls+service.activeCalls+service.latestCalls+service.cancelCalls != 0 {
		t.Fatalf("unauthenticated service calls = %+v", service)
	}
}

func TestAgentRunHandlerRejectsInvalidPathIDsBeforeService(t *testing.T) {
	for _, test := range []struct {
		name   string
		path   func(string) string
		method string
		code   string
	}{
		{name: "create conversation", method: http.MethodPost, path: func(id string) string { return "/conversations/" + id + "/agent-runs" }, code: errorCodeInvalidConversationID},
		{name: "active conversation", method: http.MethodGet, path: func(id string) string { return "/conversations/" + id + "/agent-runs/active" }, code: errorCodeInvalidConversationID},
		{name: "latest conversation", method: http.MethodGet, path: func(id string) string { return "/conversations/" + id + "/agent-runs/latest" }, code: errorCodeInvalidConversationID},
		{name: "get Run", method: http.MethodGet, path: func(id string) string { return "/agent-runs/" + id }, code: errorCodeInvalidAgentRunID},
		{name: "cancel Run", method: http.MethodPost, path: func(id string) string { return "/agent-runs/" + id + "/cancel" }, code: errorCodeInvalidAgentRunID},
	} {
		for _, id := range []string{"x", "0", "-1", "9223372036854775808"} {
			t.Run(test.name+" "+id, func(t *testing.T) {
				service := &fakeAgentRunService{}
				response := performAgentRequest(newAgentRunTestRouter(service, true), test.method, test.path(id), []byte(`{}`))
				if response.Code != http.StatusBadRequest || !strings.Contains(response.Body.String(), `"code":"`+test.code+`"`) {
					t.Fatalf("status/body = %d / %s", response.Code, response.Body.String())
				}
				if service.queueCalls+service.getCalls+service.activeCalls+service.latestCalls+service.cancelCalls != 0 {
					t.Fatalf("invalid path reached service: %+v", service)
				}
			})
		}
	}
}

func TestAgentRunHandlerStrictCreateJSON(t *testing.T) {
	invalidUTF8 := append([]byte(`{"content":"`), append([]byte{0xff}, []byte(`","client_run_id":"r"}`)...)...)
	tests := []struct {
		name string
		body []byte
	}{
		{name: "empty"},
		{name: "malformed", body: []byte(`{"content":`)},
		{name: "unknown field", body: []byte(`{"content":"q","client_run_id":"r","user_id":42}`)},
		{name: "duplicate field", body: []byte(`{"content":"q","content":"other","client_run_id":"r"}`)},
		{name: "trailing object", body: []byte(`{"content":"q","client_run_id":"r"} {}`)},
		{name: "null", body: []byte(`null`)},
		{name: "array", body: []byte(`[]`)},
		{name: "scalar", body: []byte(`true`)},
		{name: "wrong type", body: []byte(`{"content":1,"client_run_id":"r"}`)},
		{name: "invalid raw UTF-8", body: invalidUTF8},
		{name: "unpaired surrogate", body: []byte(`{"content":"\ud800","client_run_id":"r"}`)},
		{name: "oversized body", body: bytes.Repeat([]byte(" "), maxCreateAgentRunBodyBytes+1)},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			service := &fakeAgentRunService{}
			response := performAgentRequest(newAgentRunTestRouter(service, true), http.MethodPost, "/conversations/11/agent-runs", test.body)
			if response.Code != http.StatusBadRequest || !strings.Contains(response.Body.String(), `"code":"invalid_agent_run_request"`) {
				t.Fatalf("status/body = %d / %s", response.Code, response.Body.String())
			}
			if service.queueCalls != 0 {
				t.Fatalf("invalid JSON reached service %d times", service.queueCalls)
			}
		})
	}

	t.Run("accepts a paired surrogate escape", func(t *testing.T) {
		service := &fakeAgentRunService{}
		service.queueFunc = func(input agentapplication.QueueInput) (agentapplication.QueueResult, error) {
			if input.Content != "😀" {
				t.Fatalf("decoded paired surrogate = %q", input.Content)
			}
			return agentQueueResultForInput(input, true), nil
		}
		response := performAgentRequest(newAgentRunTestRouter(service, true), http.MethodPost, "/conversations/11/agent-runs", []byte(`{"content":"\ud83d\ude00","client_run_id":"r"}`))
		if response.Code != http.StatusAccepted {
			t.Fatalf("status/body = %d / %s", response.Code, response.Body.String())
		}
	})
}

func TestAgentRunHandlerPreservesCreateContentAndApplicationNormalizationBoundary(t *testing.T) {
	for _, test := range []struct {
		name         string
		content      string
		languageJSON string
		wantLanguage agentdomain.ResponseLanguage
		wantStatus   int
	}{
		{name: "exact 100000 bytes", content: strings.Repeat("a", agentapplication.MaxContentBytes), wantLanguage: agentdomain.ResponseLanguageAuto, wantStatus: http.StatusAccepted},
		{name: "100001 bytes rejected by Application", content: strings.Repeat("a", agentapplication.MaxContentBytes+1), wantLanguage: agentdomain.ResponseLanguageAuto, wantStatus: http.StatusBadRequest},
		{name: "preserves whitespace", content: "  exact content  ", languageJSON: `null`, wantLanguage: agentdomain.ResponseLanguageAuto, wantStatus: http.StatusAccepted},
		{name: "empty language", content: "q", languageJSON: `""`, wantLanguage: agentdomain.ResponseLanguageAuto, wantStatus: http.StatusAccepted},
		{name: "forced language normalization", content: "q", languageJSON: `" EN "`, wantLanguage: agentdomain.ResponseLanguageEnglish, wantStatus: http.StatusAccepted},
		{name: "valid surrogate pair", content: "😀", wantLanguage: agentdomain.ResponseLanguageAuto, wantStatus: http.StatusAccepted},
	} {
		t.Run(test.name, func(t *testing.T) {
			service := &fakeAgentRunService{}
			service.queueFunc = func(input agentapplication.QueueInput) (agentapplication.QueueResult, error) {
				if input.Content != test.content {
					t.Fatalf("content changed before Application: len=%d want=%d", len(input.Content), len(test.content))
				}
				if len(input.Content) > agentapplication.MaxContentBytes {
					return agentapplication.QueueResult{}, agentapplication.ErrInvalidContent
				}
				return agentQueueResultForInput(input, true), nil
			}
			languageField := ""
			if test.languageJSON != "" {
				languageField = `,"response_language":` + test.languageJSON
			}
			body, err := json.Marshal(test.content)
			if err != nil {
				t.Fatal(err)
			}
			requestBody := []byte(`{"content":` + string(body) + `,"client_run_id":" run-1 "` + languageField + `}`)
			response := performAgentRequest(newAgentRunTestRouter(service, true), http.MethodPost, "/conversations/11/agent-runs", requestBody)
			if response.Code != test.wantStatus {
				t.Fatalf("status/body = %d / %s", response.Code, response.Body.String())
			}
			if test.wantStatus < 300 {
				if service.queueInput.Content != test.content {
					t.Fatalf("forwarded content changed")
				}
				wantInputLanguage := agentdomain.ResponseLanguage("")
				if test.languageJSON != "" && test.languageJSON != `null` {
					var raw string
					_ = json.Unmarshal([]byte(test.languageJSON), &raw)
					wantInputLanguage = agentdomain.ResponseLanguage(raw)
				}
				if service.queueInput.ResponseLanguage != wantInputLanguage {
					t.Fatalf("forwarded language = %q, want %q", service.queueInput.ResponseLanguage, wantInputLanguage)
				}
			}
		})
	}

	t.Run("allows worst-case escaped exact content limit", func(t *testing.T) {
		service := &fakeAgentRunService{}
		service.queueFunc = func(input agentapplication.QueueInput) (agentapplication.QueueResult, error) {
			if len(input.Content) != agentapplication.MaxContentBytes || input.Content != strings.Repeat("a", agentapplication.MaxContentBytes) {
				t.Fatalf("escaped content len = %d", len(input.Content))
			}
			return agentQueueResultForInput(input, true), nil
		}
		escaped := strings.Repeat(`\u0061`, agentapplication.MaxContentBytes)
		body := []byte(`{"content":"` + escaped + `","client_run_id":"r","document_id":null}`)
		response := performAgentRequest(newAgentRunTestRouter(service, true), http.MethodPost, "/conversations/11/agent-runs", body)
		if response.Code != http.StatusAccepted || service.queueInput.RequestedDocumentID != nil {
			t.Fatalf("status/body/document = %d / %s / %v", response.Code, response.Body.String(), service.queueInput.RequestedDocumentID)
		}
	})
}

func TestAgentRunHandlerMapsQueueErrorsAndCapacityRetryAfter(t *testing.T) {
	for _, test := range []struct {
		name       string
		err        error
		wantStatus int
		wantCode   string
		wantRetry  bool
	}{
		{name: "invalid conversation", err: agentapplication.ErrInvalidConversationID, wantStatus: 400, wantCode: errorCodeInvalidConversationID},
		{name: "invalid request", err: agentapplication.ErrInvalidContent, wantStatus: 400, wantCode: errorCodeInvalidAgentRunRequest},
		{name: "conversation missing", err: conversationdomain.ErrNotFound, wantStatus: 404, wantCode: errorCodeConversationNotFound},
		{name: "document missing", err: documentdomain.ErrNotFound, wantStatus: 404, wantCode: errorCodeDocumentNotFound},
		{name: "archived", err: conversationdomain.ErrArchived, wantStatus: 409, wantCode: errorCodeConversationArchived},
		{name: "idempotency", err: agentapplication.ErrIdempotencyConflict, wantStatus: 409, wantCode: errorCodeAgentRunIdempotencyConflict},
		{name: "active execution", err: agentapplication.ErrConversationExecutionActive, wantStatus: 409, wantCode: errorCodeConversationExecutionActive},
		{name: "owner capacity", err: agentapplication.ErrOwnerQueueCapacity, wantStatus: 429, wantCode: errorCodeAgentRunOwnerQueueCapacity, wantRetry: true},
		{name: "global capacity", err: agentapplication.ErrGlobalQueueCapacity, wantStatus: 503, wantCode: errorCodeAgentRunQueueCapacity, wantRetry: true},
		{name: "internal", err: errors.New("SQL provider-secret"), wantStatus: 500, wantCode: errorCodeInternal},
	} {
		t.Run(test.name, func(t *testing.T) {
			service := &fakeAgentRunService{queueErr: test.err}
			response := performAgentRequest(newAgentRunTestRouter(service, true), http.MethodPost, "/conversations/11/agent-runs", []byte(`{"content":"q","client_run_id":"r"}`))
			if response.Code != test.wantStatus || !strings.Contains(response.Body.String(), `"code":"`+test.wantCode+`"`) ||
				strings.Contains(response.Body.String(), "provider-secret") {
				t.Fatalf("status/body = %d / %s", response.Code, response.Body.String())
			}
			if test.wantRetry {
				retry, err := strconv.Atoi(response.Header().Get("Retry-After"))
				if err != nil || retry <= 0 {
					t.Fatalf("Retry-After = %q", response.Header().Get("Retry-After"))
				}
			} else if response.Header().Get("Retry-After") != "" {
				t.Fatalf("unexpected Retry-After = %q", response.Header().Get("Retry-After"))
			}
			if test.wantStatus == 500 && response.Header().Get(RequestIDHeader) == "" {
				t.Fatal("internal error response is missing X-Request-ID")
			}
		})
	}
}

func TestAgentRunHandlerMapsLookupNoContentAndCancelErrors(t *testing.T) {
	t.Run("active and latest distinguish 404 from 204", func(t *testing.T) {
		for _, test := range []struct {
			name       string
			path       string
			err        error
			wantStatus int
			wantCode   string
		}{
			{name: "active no Run", path: "/conversations/11/agent-runs/active", err: agentapplication.ErrRunNotFound, wantStatus: 204},
			{name: "latest no Run", path: "/conversations/11/agent-runs/latest", err: agentapplication.ErrRunNotFound, wantStatus: 204},
			{name: "active no conversation", path: "/conversations/11/agent-runs/active", err: conversationdomain.ErrNotFound, wantStatus: 404, wantCode: errorCodeConversationNotFound},
			{name: "latest no conversation", path: "/conversations/11/agent-runs/latest", err: conversationdomain.ErrNotFound, wantStatus: 404, wantCode: errorCodeConversationNotFound},
		} {
			t.Run(test.name, func(t *testing.T) {
				service := &fakeAgentRunService{activeErr: test.err, latestErr: test.err}
				response := performAgentRequest(newAgentRunTestRouter(service, true), http.MethodGet, test.path, nil)
				if response.Code != test.wantStatus {
					t.Fatalf("status/body = %d / %s", response.Code, response.Body.String())
				}
				if test.wantStatus == 204 && response.Body.Len() != 0 {
					t.Fatalf("204 body = %q", response.Body.String())
				}
				if test.wantCode != "" && !strings.Contains(response.Body.String(), `"code":"`+test.wantCode+`"`) {
					t.Fatalf("body = %s", response.Body.String())
				}
			})
		}
	})

	for _, test := range []struct {
		name       string
		err        error
		wantStatus int
		wantCode   string
	}{
		{name: "invalid", err: agentapplication.ErrInvalidRunID, wantStatus: 400, wantCode: errorCodeInvalidAgentRunID},
		{name: "not found", err: agentapplication.ErrRunNotFound, wantStatus: 404, wantCode: errorCodeAgentRunNotFound},
		{name: "processing", err: agentapplication.ErrRunProcessingCannotCancel, wantStatus: 409, wantCode: errorCodeAgentRunProcessing},
		{name: "terminal", err: agentapplication.ErrRunTerminalCannotCancel, wantStatus: 409, wantCode: errorCodeAgentRunTerminal},
		{name: "internal", err: errors.New("SQL secret"), wantStatus: 500, wantCode: errorCodeInternal},
	} {
		t.Run("cancel "+test.name, func(t *testing.T) {
			service := &fakeAgentRunService{cancelErr: test.err}
			response := performAgentRequest(newAgentRunTestRouter(service, true), http.MethodPost, "/agent-runs/101/cancel", nil)
			if response.Code != test.wantStatus || !strings.Contains(response.Body.String(), `"code":"`+test.wantCode+`"`) {
				t.Fatalf("status/body = %d / %s", response.Code, response.Body.String())
			}
		})
	}

	for _, test := range []struct {
		name   string
		err    error
		status int
		code   string
	}{
		{name: "get missing", err: agentapplication.ErrRunNotFound, status: 404, code: errorCodeAgentRunNotFound},
		{name: "get internal", err: errors.New("provider secret"), status: 500, code: errorCodeInternal},
	} {
		t.Run(test.name, func(t *testing.T) {
			service := &fakeAgentRunService{getErr: test.err}
			response := performAgentRequest(newAgentRunTestRouter(service, true), http.MethodGet, "/agent-runs/101", nil)
			if response.Code != test.status || !strings.Contains(response.Body.String(), `"code":"`+test.code+`"`) {
				t.Fatalf("status/body = %d / %s", response.Code, response.Body.String())
			}
		})
	}
}

func TestAgentRunHandlerFailsClosedOnBadSnapshots(t *testing.T) {
	bad := validQueuedAgentSnapshot()
	bad.OwnerUserID++
	for _, test := range []struct {
		name    string
		method  string
		path    string
		service *fakeAgentRunService
	}{
		{name: "get", method: http.MethodGet, path: "/agent-runs/101", service: &fakeAgentRunService{getResult: bad}},
		{name: "active", method: http.MethodGet, path: "/conversations/11/agent-runs/active", service: &fakeAgentRunService{activeResult: bad}},
		{name: "latest", method: http.MethodGet, path: "/conversations/11/agent-runs/latest", service: &fakeAgentRunService{latestResult: bad}},
		{name: "cancel non-canceled", method: http.MethodPost, path: "/agent-runs/101/cancel", service: &fakeAgentRunService{cancelResult: validQueuedAgentSnapshot()}},
		{name: "get wrong Run", method: http.MethodGet, path: "/agent-runs/101", service: &fakeAgentRunService{getResult: func() agentapplication.RunSnapshot {
			snapshot := validQueuedAgentSnapshot()
			snapshot.ID = 102
			return snapshot
		}()}},
		{name: "active wrong conversation", method: http.MethodGet, path: "/conversations/11/agent-runs/active", service: &fakeAgentRunService{activeResult: func() agentapplication.RunSnapshot {
			snapshot := validProcessingAgentSnapshot()
			snapshot.ConversationID = 12
			return snapshot
		}()}},
		{name: "latest wrong conversation", method: http.MethodGet, path: "/conversations/11/agent-runs/latest", service: &fakeAgentRunService{latestResult: func() agentapplication.RunSnapshot {
			snapshot := validQueuedAgentSnapshot()
			snapshot.ConversationID = 12
			return snapshot
		}()}},
		{name: "cancel wrong Run", method: http.MethodPost, path: "/agent-runs/101/cancel", service: &fakeAgentRunService{cancelResult: func() agentapplication.RunSnapshot {
			snapshot := validQueuedAgentSnapshot()
			snapshot.ID = 102
			snapshot.Status = agentdomain.RunStatusCanceled
			completed := agentResponseTestBaseTime.Add(time.Second)
			snapshot.CompletedAt = &completed
			snapshot.UpdatedAt = completed
			return snapshot
		}()}},
	} {
		t.Run(test.name, func(t *testing.T) {
			response := performAgentRequest(newAgentRunTestRouter(test.service, true), test.method, test.path, nil)
			if response.Code != http.StatusInternalServerError || !strings.Contains(response.Body.String(), `"code":"internal_error"`) ||
				strings.Contains(response.Body.String(), `"agent_run"`) {
				t.Fatalf("status/body = %d / %s", response.Code, response.Body.String())
			}
		})
	}

	t.Run("create message mismatch", func(t *testing.T) {
		result := agentQueueResultForInput(agentapplication.QueueInput{
			ConversationID: 11, Content: "q", ClientRunID: "r",
		}, true)
		result.UserMessage.ID++
		service := &fakeAgentRunService{queueResult: result}
		response := performAgentRequest(newAgentRunTestRouter(service, true), http.MethodPost, "/conversations/11/agent-runs", []byte(`{"content":"q","client_run_id":"r"}`))
		if response.Code != 500 || strings.Contains(response.Body.String(), `"agent_run"`) {
			t.Fatalf("status/body = %d / %s", response.Code, response.Body.String())
		}
	})

	t.Run("bad final payload", func(t *testing.T) {
		snapshot := validSucceededAgentSnapshot()
		setAgentFinalPayload(&snapshot, []byte(`{"sources":[]}`))
		service := &fakeAgentRunService{getResult: snapshot}
		response := performAgentRequest(newAgentRunTestRouter(service, true), http.MethodGet, "/agent-runs/101", nil)
		if response.Code != http.StatusInternalServerError || !strings.Contains(response.Body.String(), `"code":"internal_error"`) ||
			strings.Contains(response.Body.String(), `"agent_run"`) {
			t.Fatalf("status/body = %d / %s", response.Code, response.Body.String())
		}
	})
}

func performAgentRequest(handler http.Handler, method, path string, body []byte) *httptest.ResponseRecorder {
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(method, path, bytes.NewReader(body))
	if body != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	handler.ServeHTTP(recorder, request)
	return recorder
}

func agentQueueResultForInput(input agentapplication.QueueInput, created bool) agentapplication.QueueResult {
	snapshot := validQueuedAgentSnapshot()
	snapshot.ConversationID = input.ConversationID
	snapshot.ClientRunID = strings.TrimSpace(input.ClientRunID)
	snapshot.RequestedDocumentID = input.RequestedDocumentID
	language := agentdomain.ResponseLanguage(strings.ToLower(strings.TrimSpace(string(input.ResponseLanguage))))
	if language == "" {
		language = agentdomain.ResponseLanguageAuto
	}
	snapshot.ResponseLanguage = language
	result := validAgentQueueResult(snapshot, input.Content)
	result.Created = created
	return result
}

func assertAgentCreateResponse(t *testing.T, body []byte, conversationID int64, userRole, assistantRole string) {
	t.Helper()
	var response struct {
		ConversationID   int64                       `json:"conversation_id"`
		UserMessage      conversationMessageResponse `json:"user_message"`
		AssistantMessage conversationMessageResponse `json:"assistant_message"`
		AgentRun         agentRunResponse            `json:"agent_run"`
	}
	if err := json.Unmarshal(body, &response); err != nil {
		t.Fatalf("decode create response: %v: %s", err, body)
	}
	if response.ConversationID != conversationID || response.UserMessage.Role != userRole ||
		response.AssistantMessage.Role != assistantRole || response.AgentRun.ConversationID != conversationID ||
		response.AgentRun.UserMessageID != response.UserMessage.ID ||
		response.AgentRun.AssistantMessageID != response.AssistantMessage.ID {
		t.Fatalf("create response associations = %+v", response)
	}
}
