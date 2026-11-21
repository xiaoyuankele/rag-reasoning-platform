package api

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
	"time"

	agentapplication "rag-reasoning-platform/backend/internal/application/agent"
	agentdomain "rag-reasoning-platform/backend/internal/domain/agent"
	conversationdomain "rag-reasoning-platform/backend/internal/domain/conversation"
)

var agentResponseTestBaseTime = time.Date(2026, 11, 16, 10, 0, 0, 0, time.UTC)

const agentResponseTestHash = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

func TestAgentRunResponseMapsEveryPublicStateWithoutInternalFields(t *testing.T) {
	for _, test := range []struct {
		name       string
		snapshot   agentapplication.RunSnapshot
		cancelable bool
		wantResult bool
		wantError  bool
	}{
		{name: "queued", snapshot: validQueuedAgentSnapshot(), cancelable: true},
		{name: "queued recovered pending", snapshot: validRecoveredAgentSnapshot(), cancelable: true},
		{name: "processing running", snapshot: validProcessingAgentSnapshot()},
		{name: "succeeded final", snapshot: validSucceededAgentSnapshot(), wantResult: true},
		{name: "failed", snapshot: validFailedAgentSnapshot(), wantError: true},
		{name: "canceled", snapshot: validCanceledAgentSnapshot()},
	} {
		t.Run(test.name, func(t *testing.T) {
			response, err := newAgentRunResponse(testAPIOwnerUserID, test.snapshot)
			if err != nil {
				t.Fatalf("newAgentRunResponse() error = %v", err)
			}
			if response.Cancelable != test.cancelable || (response.Result != nil) != test.wantResult ||
				(response.Error != nil) != test.wantError || response.Steps == nil ||
				len(response.Steps) != test.snapshot.StepCount {
				t.Fatalf("response = %+v", response)
			}
			encoded, err := json.Marshal(response)
			if err != nil {
				t.Fatalf("json.Marshal() error = %v", err)
			}
			body := string(encoded)
			for _, forbidden := range []string{
				"owner_user_id", "attempt_count", "next_attempt_at", "deadline_at",
				"worker_id", "lease_token", "lease_expires_at", "heartbeat_at",
				"arguments", "result_json", "excerpt", "score", "private-snippet", "private-excerpt", "SQL secret marker",
			} {
				if strings.Contains(body, forbidden) {
					t.Fatalf("response leaked %q: %s", forbidden, body)
				}
			}
			if test.wantError && (response.Error.Code != string(agentdomain.ErrorCodeToolFailed) ||
				response.Error.Message != "Agent tool execution failed") {
				t.Fatalf("safe failed response error = %+v", response.Error)
			}
			if test.wantResult {
				if len(response.Result.Sources) != 1 || response.Result.Sources[0].Citation != 1 ||
					response.Result.Sources[0].ContentHash != agentResponseTestHash {
					t.Fatalf("result sources = %+v", response.Result.Sources)
				}
			}
			if test.wantResult {
				if strings.Contains(body, `"result":null`) || !strings.Contains(body, `"error":null`) {
					t.Fatalf("succeeded result/error nullability = %s", body)
				}
			} else if !strings.Contains(body, `"result":null`) {
				t.Fatalf("non-succeeded result must be null: %s", body)
			}
			if !test.wantError && !strings.Contains(body, `"error":null`) {
				t.Fatalf("non-failed error must be null: %s", body)
			}
			if test.snapshot.Status == agentdomain.RunStatusQueued && test.snapshot.StartedAt == nil &&
				(!strings.Contains(body, `"started_at":null`) || !strings.Contains(body, `"completed_at":null`)) {
				t.Fatalf("fresh queued timestamps must be null: %s", body)
			}
			if test.wantResult && !strings.Contains(body, `"kind":"final","status":"succeeded","tool_name":null`) {
				t.Fatalf("final Step null tool_name is missing: %s", body)
			}
		})
	}
}

func TestAgentRunResponseRejectsBrokenSnapshotInvariants(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*agentapplication.RunSnapshot)
	}{
		{name: "wrong owner", mutate: func(s *agentapplication.RunSnapshot) { s.OwnerUserID++ }},
		{name: "invalid Run ID", mutate: func(s *agentapplication.RunSnapshot) { s.ID = 0 }},
		{name: "same message IDs", mutate: func(s *agentapplication.RunSnapshot) { s.AssistantMessageID = s.UserMessageID }},
		{name: "invalid requested language", mutate: func(s *agentapplication.RunSnapshot) { s.ResponseLanguage = "fr" }},
		{name: "invalid requested document", mutate: func(s *agentapplication.RunSnapshot) {
			invalid := int64(0)
			s.RequestedDocumentID = &invalid
		}},
		{name: "unnormalized client Run ID", mutate: func(s *agentapplication.RunSnapshot) { s.ClientRunID = " run-1 " }},
		{name: "nil Steps", mutate: func(s *agentapplication.RunSnapshot) { s.Steps = nil }},
		{name: "step count mismatch", mutate: func(s *agentapplication.RunSnapshot) { s.StepCount-- }},
		{name: "step sequence gap", mutate: func(s *agentapplication.RunSnapshot) { s.Steps[1].SequenceNo = 4 }},
		{name: "duplicate Step IDs", mutate: func(s *agentapplication.RunSnapshot) { s.Steps[1].ID = s.Steps[0].ID }},
		{name: "step Run mismatch", mutate: func(s *agentapplication.RunSnapshot) { s.Steps[0].RunID++ }},
		{name: "Run Token mismatch", mutate: func(s *agentapplication.RunSnapshot) { s.TotalTokens++ }},
		{name: "Step Token mismatch", mutate: func(s *agentapplication.RunSnapshot) { s.Steps[0].TotalTokens++ }},
		{name: "invalid budget", mutate: func(s *agentapplication.RunSnapshot) { s.Budget.MaxSteps = 0 }},
		{name: "worker internals", mutate: func(s *agentapplication.RunSnapshot) { s.WorkerID = "secret-worker" }},
		{name: "terminal active Step", mutate: func(s *agentapplication.RunSnapshot) {
			s.Steps[0].Status = agentdomain.StepStatusRunning
			s.Steps[0].CompletedAt = nil
		}},
		{name: "unknown tool", mutate: func(s *agentapplication.RunSnapshot) { *s.Steps[0].ToolName = "unknown" }},
		{name: "final has tool", mutate: func(s *agentapplication.RunSnapshot) {
			name := agentapplication.ToolSearchDocuments
			s.Steps[len(s.Steps)-1].ToolName = &name
		}},
		{name: "succeeded without result", mutate: func(s *agentapplication.RunSnapshot) { s.Result = nil }},
		{name: "final and Run answer mismatch", mutate: func(s *agentapplication.RunSnapshot) { s.Result.Answer = "different" }},
		{name: "processing without started time", mutate: func(s *agentapplication.RunSnapshot) {
			*s = validProcessingAgentSnapshot()
			s.StartedAt = nil
		}},
		{name: "fresh Run with started time", mutate: func(s *agentapplication.RunSnapshot) {
			*s = validQueuedAgentSnapshot()
			started := agentResponseTestBaseTime
			s.StartedAt = &started
		}},
		{name: "succeeded without an attempt", mutate: func(s *agentapplication.RunSnapshot) { s.AttemptCount = 0 }},
		{name: "pending with started time", mutate: func(s *agentapplication.RunSnapshot) {
			*s = validRecoveredAgentSnapshot()
			started := agentResponseTestBaseTime.Add(time.Second)
			s.Steps[0].StartedAt = &started
		}},
		{name: "terminal without completion", mutate: func(s *agentapplication.RunSnapshot) { s.CompletedAt = nil }},
		{name: "updated before creation", mutate: func(s *agentapplication.RunSnapshot) { s.UpdatedAt = s.CreatedAt.Add(-time.Second) }},
		{name: "succeeded Step contains an error", mutate: func(s *agentapplication.RunSnapshot) {
			message := "internal error"
			s.Steps[0].ErrorCode = agentdomain.ErrorCodeExecutionFailed
			s.Steps[0].ErrorMessage = &message
		}},
		{name: "failed without error", mutate: func(s *agentapplication.RunSnapshot) {
			*s = validFailedAgentSnapshot()
			s.ErrorCode = agentdomain.ErrorCodeNone
			s.ErrorMessage = nil
		}},
		{name: "succeeded with stored error", mutate: func(s *agentapplication.RunSnapshot) {
			message := "internal retry error"
			s.ErrorCode = agentdomain.ErrorCodeTemporarilyUnavailable
			s.ErrorMessage = &message
		}},
		{name: "read arguments and result mismatch", mutate: func(s *agentapplication.RunSnapshot) {
			s.Steps[1].Arguments = []byte(`{"evidence_refs":["step-1:item-2"]}`)
		}},
		{name: "read result count mismatch", mutate: func(s *agentapplication.RunSnapshot) {
			s.Steps[1].Result = []byte(`{"items":[]}`)
		}},
		{name: "read evidence metadata differs from search", mutate: func(s *agentapplication.RunSnapshot) {
			s.Steps[1].Result = bytes.ReplaceAll(s.Steps[1].Result, []byte(`"document_id":7`), []byte(`"document_id":70`))
			payload := bytes.ReplaceAll(s.Result.Payload, []byte(`"document_id":7`), []byte(`"document_id":70`))
			setAgentFinalPayload(s, payload)
		}},
		{name: "read occurs before matching search", mutate: func(s *agentapplication.RunSnapshot) {
			s.Steps[0], s.Steps[1] = s.Steps[1], s.Steps[0]
			s.Steps[0].SequenceNo = 1
			s.Steps[1].SequenceNo = 2
			for index := 0; index < 2; index++ {
				s.Steps[index].Arguments = bytes.ReplaceAll(s.Steps[index].Arguments, []byte("step-1:item-1"), []byte("step-2:item-1"))
				s.Steps[index].Result = bytes.ReplaceAll(s.Steps[index].Result, []byte("step-1:item-1"), []byte("step-2:item-1"))
			}
			payload := bytes.ReplaceAll(s.Result.Payload, []byte("step-1:item-1"), []byte("step-2:item-1"))
			setAgentFinalPayload(s, payload)
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			snapshot := validSucceededAgentSnapshot()
			test.mutate(&snapshot)
			if response, err := newAgentRunResponse(testAPIOwnerUserID, snapshot); err == nil {
				t.Fatalf("accepted broken snapshot: %+v", response)
			}
		})
	}
}

func TestAgentRunResponseRejectsBadFinalPayloads(t *testing.T) {
	validSource := `{"evidence_ref":"step-1:item-1","document_id":7,"chunk_id":8,"title":"Trusted Doc","page_start":2,"page_end":3,"content_hash":"` + agentResponseTestHash + `"}`
	badPayloads := []struct {
		name    string
		payload []byte
	}{
		{name: "null root", payload: []byte(`null`)},
		{name: "array root", payload: []byte(`[]`)},
		{name: "unknown root field", payload: []byte(`{"sources":[` + validSource + `],"secret":true}`)},
		{name: "duplicate root field", payload: []byte(`{"sources":[` + validSource + `],"sources":[` + validSource + `]}`)},
		{name: "trailing JSON", payload: []byte(`{"sources":[` + validSource + `]} {}`)},
		{name: "empty sources", payload: []byte(`{"sources":[]}`)},
		{name: "unknown source field", payload: []byte(`{"sources":[` + strings.TrimSuffix(validSource, `}`) + `,"excerpt":"secret"}]}`)},
		{name: "duplicate source field", payload: []byte(`{"sources":[` + strings.TrimSuffix(validSource, `}`) + `,"document_id":7}]}`)},
		{name: "bad hash", payload: []byte(`{"sources":[{"evidence_ref":"step-1:item-1","document_id":7,"chunk_id":8,"title":"Trusted Doc","page_start":2,"page_end":3,"content_hash":"ABC"}]}`)},
		{name: "half page range", payload: []byte(`{"sources":[{"evidence_ref":"step-1:item-1","document_id":7,"chunk_id":8,"title":"Trusted Doc","page_start":2,"page_end":null,"content_hash":"` + agentResponseTestHash + `"}]}`)},
		{name: "missing item", payload: []byte(`{"sources":[{"evidence_ref":"step-1:item-2","document_id":7,"chunk_id":8,"title":"Trusted Doc","page_start":2,"page_end":3,"content_hash":"` + agentResponseTestHash + `"}]}`)},
		{name: "wrong document", payload: []byte(`{"sources":[{"evidence_ref":"step-1:item-1","document_id":70,"chunk_id":8,"title":"Trusted Doc","page_start":2,"page_end":3,"content_hash":"` + agentResponseTestHash + `"}]}`)},
		{name: "null title", payload: []byte(`{"sources":[{"evidence_ref":"step-1:item-1","document_id":7,"chunk_id":8,"title":null,"page_start":2,"page_end":3,"content_hash":"` + agentResponseTestHash + `"}]}`)},
		{name: "invalid Unicode bytes", payload: append([]byte(`{"sources":[{"evidence_ref":"step-1:item-1","document_id":7,"chunk_id":8,"title":"`), append([]byte{0xff}, []byte(`","page_start":2,"page_end":3,"content_hash":"`+agentResponseTestHash+`"}]}`)...)...)},
	}
	for _, test := range badPayloads {
		t.Run(test.name, func(t *testing.T) {
			snapshot := validSucceededAgentSnapshot()
			setAgentFinalPayload(&snapshot, test.payload)
			if response, err := newAgentRunResponse(testAPIOwnerUserID, snapshot); err == nil {
				t.Fatalf("accepted bad final payload: %+v", response)
			}
		})
	}

	t.Run("duplicate evidence refs", func(t *testing.T) {
		snapshot := validSucceededAgentSnapshot()
		payload := []byte(`{"sources":[` + validSource + `,` + validSource + `]}`)
		setAgentFinalPayload(&snapshot, payload)
		if _, err := newAgentRunResponse(testAPIOwnerUserID, snapshot); err == nil {
			t.Fatal("accepted duplicate final evidence refs")
		}
	})

	t.Run("too many sources", func(t *testing.T) {
		snapshot := validSucceededAgentSnapshot()
		sources := make([]json.RawMessage, agentapplication.MaxReadEvidenceRefs+1)
		for index := range sources {
			sources[index] = json.RawMessage(validSource)
		}
		payload := mustAgentResponseJSON(map[string]any{"sources": sources})
		setAgentFinalPayload(&snapshot, payload)
		if _, err := newAgentRunResponse(testAPIOwnerUserID, snapshot); err == nil {
			t.Fatal("accepted too many final sources")
		}
	})
}

func TestAgentRunResponseAssignsCitationsInTrustedSourceOrder(t *testing.T) {
	snapshot := validSucceededAgentSnapshot()
	secondHash := strings.Repeat("b", 64)
	snapshot.Steps[0].Result = []byte(`{"items":[` +
		`{"evidence_ref":"step-1:item-1","document_id":7,"chunk_id":8,"title":"Trusted Doc","page_start":2,"page_end":3,"snippet":"one","score":0.9},` +
		`{"evidence_ref":"step-1:item-2","document_id":9,"chunk_id":10,"title":"Second Doc","snippet":"two","score":0.8}]}`)
	snapshot.Steps[1].Arguments = []byte(`{"evidence_refs":["step-1:item-1","step-1:item-2"]}`)
	snapshot.Steps[1].Result = []byte(`{"items":[` +
		`{"evidence_ref":"step-1:item-1","document_id":7,"chunk_id":8,"title":"Trusted Doc","page_start":2,"page_end":3,"excerpt":"one","content_hash":"` + agentResponseTestHash + `"},` +
		`{"evidence_ref":"step-1:item-2","document_id":9,"chunk_id":10,"title":"Second Doc","excerpt":"two","content_hash":"` + secondHash + `"}]}`)
	payload := []byte(`{"sources":[` +
		`{"evidence_ref":"step-1:item-2","document_id":9,"chunk_id":10,"title":"Second Doc","content_hash":"` + secondHash + `"},` +
		`{"evidence_ref":"step-1:item-1","document_id":7,"chunk_id":8,"title":"Trusted Doc","page_start":2,"page_end":3,"content_hash":"` + agentResponseTestHash + `"}]}`)
	setAgentFinalPayload(&snapshot, payload)

	response, err := newAgentRunResponse(testAPIOwnerUserID, snapshot)
	if err != nil {
		t.Fatalf("newAgentRunResponse() error = %v", err)
	}
	if len(response.Result.Sources) != 2 ||
		response.Result.Sources[0].Citation != 1 || response.Result.Sources[0].EvidenceRef != "step-1:item-2" ||
		response.Result.Sources[0].PageStart != nil || response.Result.Sources[0].PageEnd != nil ||
		response.Result.Sources[1].Citation != 2 || response.Result.Sources[1].EvidenceRef != "step-1:item-1" {
		t.Fatalf("ordered citations = %+v", response.Result.Sources)
	}
}

func TestAgentExecutionErrorsUseFrozenMessages(t *testing.T) {
	for code, wantMessage := range map[agentdomain.ErrorCode]string{
		agentdomain.ErrorCodeExecutionFailed:        "Agent execution failed",
		agentdomain.ErrorCodeTemporarilyUnavailable: "Agent is temporarily unavailable",
		agentdomain.ErrorCodeStepLimitExceeded:      "Agent step limit exceeded",
		agentdomain.ErrorCodeTokenBudgetExceeded:    "Agent token budget exceeded",
		agentdomain.ErrorCodeDeadlineExceeded:       "Agent execution deadline exceeded",
		agentdomain.ErrorCodeInvalidToolCall:        "Agent produced an invalid tool call",
		agentdomain.ErrorCodeToolFailed:             "Agent tool execution failed",
		agentdomain.ErrorCodeWorkerInterrupted:      "Agent worker was interrupted",
	} {
		got, ok := newAgentExecutionErrorResponse(code)
		if !ok || got.Code != string(code) || got.Message != wantMessage {
			t.Fatalf("safe error %q = %+v, %t", code, got, ok)
		}
	}
	if _, ok := newAgentExecutionErrorResponse(agentdomain.ErrorCode("provider-secret")); ok {
		t.Fatal("unknown error code was exposed")
	}
}

func TestCreateAgentRunResponseValidatesMessagesAndRequestAssociation(t *testing.T) {
	request := createAgentRunRequest{Content: " exact question ", ClientRunID: " run-1 "}
	result := validAgentQueueResult(validQueuedAgentSnapshot(), request.Content)
	response, err := newCreateAgentRunResponse(11, testAPIOwnerUserID, request, result)
	if err != nil || response.AgentRun.ID != result.Run.ID || response.UserMessage.Role != "user" ||
		response.AssistantMessage.Role != "assistant" {
		t.Fatalf("newCreateAgentRunResponse() = %+v, %v", response, err)
	}

	t.Run("terminal idempotent replay", func(t *testing.T) {
		language := "en"
		terminalRequest := request
		terminalRequest.ResponseLanguage = &language
		terminal := validAgentQueueResult(validSucceededAgentSnapshot(), request.Content)
		terminal.Created = false
		terminal.AssistantMessage.Status = conversationdomain.MessageStatusCompleted
		terminal.AssistantMessage.Content = terminal.Run.Result.Answer
		mapped, err := newCreateAgentRunResponse(11, testAPIOwnerUserID, terminalRequest, terminal)
		if err != nil || mapped.AgentRun.Result == nil || mapped.AssistantMessage.Status != "completed" {
			t.Fatalf("terminal replay response = %+v, %v", mapped, err)
		}
	})

	for _, test := range []struct {
		name   string
		mutate func(*agentapplication.QueueResult)
	}{
		{name: "user association", mutate: func(r *agentapplication.QueueResult) { r.UserMessage.ID++ }},
		{name: "assistant association", mutate: func(r *agentapplication.QueueResult) { r.AssistantMessage.ConversationID++ }},
		{name: "wrong user role", mutate: func(r *agentapplication.QueueResult) { r.UserMessage.Role = conversationdomain.RoleAssistant }},
		{name: "wrong assistant role", mutate: func(r *agentapplication.QueueResult) { r.AssistantMessage.Role = conversationdomain.RoleUser }},
		{name: "non-adjacent sequence", mutate: func(r *agentapplication.QueueResult) { r.AssistantMessage.SequenceNo++ }},
		{name: "wrong assistant status", mutate: func(r *agentapplication.QueueResult) {
			r.AssistantMessage.Status = conversationdomain.MessageStatusCompleted
		}},
		{name: "wrong content", mutate: func(r *agentapplication.QueueResult) { r.UserMessage.Content = "different" }},
		{name: "bad message time", mutate: func(r *agentapplication.QueueResult) { r.UserMessage.CreatedAt = time.Time{} }},
		{name: "invalid assistant client ID Unicode", mutate: func(r *agentapplication.QueueResult) {
			invalid := string([]byte{0xff})
			r.AssistantMessage.ClientMessageID = &invalid
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			broken := validAgentQueueResult(validQueuedAgentSnapshot(), request.Content)
			test.mutate(&broken)
			if _, err := newCreateAgentRunResponse(11, testAPIOwnerUserID, request, broken); err == nil {
				t.Fatalf("accepted broken QueueResult: %+v", broken)
			}
		})
	}
}

func validQueuedAgentSnapshot() agentapplication.RunSnapshot {
	return agentapplication.RunSnapshot{
		Run: agentdomain.Run{
			ID: 101, OwnerUserID: testAPIOwnerUserID, ConversationID: 11,
			UserMessageID: 21, AssistantMessageID: 22, ClientRunID: "run-1",
			ResponseLanguage: agentdomain.ResponseLanguageAuto, Status: agentdomain.RunStatusQueued,
			Budget: agentdomain.Budget{
				MaxSteps: agentdomain.MaxPersistedSteps, MaxTotalTokens: agentdomain.MaxRunTotalTokens,
				DeadlineAt: agentResponseTestBaseTime.Add(2 * time.Minute),
			},
			NextAttemptAt: agentResponseTestBaseTime,
			CreatedAt:     agentResponseTestBaseTime, UpdatedAt: agentResponseTestBaseTime,
		},
		Steps: []agentdomain.Step{},
	}
}

func validRecoveredAgentSnapshot() agentapplication.RunSnapshot {
	snapshot := validQueuedAgentSnapshot()
	started := agentResponseTestBaseTime.Add(time.Second)
	created := started
	snapshot.AttemptCount = 1
	snapshot.StartedAt = &started
	snapshot.UpdatedAt = agentResponseTestBaseTime.Add(3 * time.Second)
	snapshot.ErrorCode = agentdomain.ErrorCodeTemporarilyUnavailable
	message := "SQL secret marker"
	snapshot.ErrorMessage = &message
	toolName := agentapplication.ToolSearchDocuments
	snapshot.Steps = []agentdomain.Step{{
		ID: 201, RunID: snapshot.ID, SequenceNo: 1, Kind: agentdomain.StepKindToolCall,
		Status: agentdomain.StepStatusPending, ToolName: &toolName,
		Arguments:    []byte(`{"query":"internal-arguments"}`),
		PromptTokens: 2, CompletionTokens: 1, TotalTokens: 3, CreatedAt: created,
	}}
	snapshot.StepCount = 1
	snapshot.PromptTokens, snapshot.CompletionTokens, snapshot.TotalTokens = 2, 1, 3
	return snapshot
}

func validProcessingAgentSnapshot() agentapplication.RunSnapshot {
	snapshot := validRecoveredAgentSnapshot()
	snapshot.Status = agentdomain.RunStatusProcessing
	snapshot.ErrorCode = agentdomain.ErrorCodeNone
	snapshot.ErrorMessage = nil
	started := agentResponseTestBaseTime.Add(time.Second)
	snapshot.Steps[0].Status = agentdomain.StepStatusRunning
	snapshot.Steps[0].StartedAt = &started
	return snapshot
}

func validSucceededAgentSnapshot() agentapplication.RunSnapshot {
	snapshot := validQueuedAgentSnapshot()
	snapshot.ResponseLanguage = agentdomain.ResponseLanguageEnglish
	snapshot.Status = agentdomain.RunStatusSucceeded
	snapshot.AttemptCount = 1
	started := agentResponseTestBaseTime.Add(time.Second)
	completed := agentResponseTestBaseTime.Add(5 * time.Second)
	snapshot.StartedAt = &started
	snapshot.CompletedAt = &completed
	snapshot.UpdatedAt = completed
	searchName := agentapplication.ToolSearchDocuments
	readName := agentapplication.ToolReadDocumentEvidence
	searchResult := []byte(`{"items":[{"evidence_ref":"step-1:item-1","document_id":7,"chunk_id":8,"title":"Trusted Doc","page_start":2,"page_end":3,"snippet":"private-snippet","score":0.9}]}`)
	readResult := []byte(`{"items":[{"evidence_ref":"step-1:item-1","document_id":7,"chunk_id":8,"title":"Trusted Doc","page_start":2,"page_end":3,"excerpt":"private-excerpt","content_hash":"` + agentResponseTestHash + `"}]}`)
	payload := []byte(`{"sources":[{"evidence_ref":"step-1:item-1","document_id":7,"chunk_id":8,"title":"Trusted Doc","page_start":2,"page_end":3,"content_hash":"` + agentResponseTestHash + `"}]}`)
	step1Start := agentResponseTestBaseTime.Add(time.Second)
	step1Complete := agentResponseTestBaseTime.Add(2 * time.Second)
	step2Start := agentResponseTestBaseTime.Add(2 * time.Second)
	step2Complete := agentResponseTestBaseTime.Add(3 * time.Second)
	step3Start := agentResponseTestBaseTime.Add(4 * time.Second)
	step3Complete := completed
	snapshot.Steps = []agentdomain.Step{
		{
			ID: 201, RunID: snapshot.ID, SequenceNo: 1, Kind: agentdomain.StepKindToolCall,
			Status: agentdomain.StepStatusSucceeded, ToolName: &searchName,
			Arguments: []byte(`{"query":"internal-arguments","strategy":"keyword","top_k":1}`), Result: searchResult,
			PromptTokens: 2, CompletionTokens: 1, TotalTokens: 3,
			CreatedAt: step1Start, StartedAt: &step1Start, CompletedAt: &step1Complete,
		},
		{
			ID: 202, RunID: snapshot.ID, SequenceNo: 2, Kind: agentdomain.StepKindToolCall,
			Status: agentdomain.StepStatusSucceeded, ToolName: &readName,
			Arguments: []byte(`{"evidence_refs":["step-1:item-1"]}`), Result: readResult,
			PromptTokens: 2, CompletionTokens: 1, TotalTokens: 3,
			CreatedAt: step2Start, StartedAt: &step2Start, CompletedAt: &step2Complete,
		},
		{
			ID: 203, RunID: snapshot.ID, SequenceNo: 3, Kind: agentdomain.StepKindFinal,
			Status:       agentdomain.StepStatusSucceeded,
			PromptTokens: 1, CompletionTokens: 1, TotalTokens: 2,
			CreatedAt: step3Start, StartedAt: &step3Start, CompletedAt: &step3Complete,
		},
	}
	snapshot.StepCount = len(snapshot.Steps)
	snapshot.PromptTokens, snapshot.CompletionTokens, snapshot.TotalTokens = 5, 3, 8
	snapshot.Result = &agentdomain.Result{
		Answer: "trusted answer", ResponseLanguage: agentdomain.ResponseLanguageEnglish, Payload: payload,
		PromptTokens: 5, CompletionTokens: 3, TotalTokens: 8,
	}
	snapshot.Steps[2].Result = mustAgentResponseJSON(agentapplication.FinalStepResult{
		Answer: snapshot.Result.Answer, ResponseLanguage: snapshot.Result.ResponseLanguage, Payload: payload,
	})
	return snapshot
}

func validFailedAgentSnapshot() agentapplication.RunSnapshot {
	snapshot := validProcessingAgentSnapshot()
	snapshot.Status = agentdomain.RunStatusFailed
	completed := agentResponseTestBaseTime.Add(4 * time.Second)
	snapshot.CompletedAt = &completed
	snapshot.UpdatedAt = completed
	snapshot.ErrorCode = agentdomain.ErrorCodeToolFailed
	message := "SQL secret marker"
	snapshot.ErrorMessage = &message
	snapshot.Steps[0].Status = agentdomain.StepStatusFailed
	snapshot.Steps[0].ErrorCode = agentdomain.ErrorCodeToolFailed
	snapshot.Steps[0].ErrorMessage = &message
	snapshot.Steps[0].CompletedAt = &completed
	return snapshot
}

func validCanceledAgentSnapshot() agentapplication.RunSnapshot {
	snapshot := validRecoveredAgentSnapshot()
	snapshot.Status = agentdomain.RunStatusCanceled
	completed := agentResponseTestBaseTime.Add(4 * time.Second)
	snapshot.CompletedAt = &completed
	snapshot.UpdatedAt = completed
	snapshot.ErrorCode = agentdomain.ErrorCodeNone
	snapshot.ErrorMessage = nil
	message := "SQL secret marker"
	snapshot.Steps[0].Status = agentdomain.StepStatusFailed
	snapshot.Steps[0].ErrorCode = agentdomain.ErrorCodeWorkerInterrupted
	snapshot.Steps[0].ErrorMessage = &message
	stepStarted := agentResponseTestBaseTime.Add(3 * time.Second)
	snapshot.Steps[0].StartedAt = &stepStarted
	snapshot.Steps[0].CompletedAt = &completed
	return snapshot
}

func validAgentQueueResult(snapshot agentapplication.RunSnapshot, content string) agentapplication.QueueResult {
	return agentapplication.QueueResult{
		RunSnapshot: snapshot,
		UserMessage: conversationdomain.Message{
			ID: snapshot.UserMessageID, ConversationID: snapshot.ConversationID, SequenceNo: 1,
			Role: conversationdomain.RoleUser, Content: content, Status: conversationdomain.MessageStatusCompleted,
			CreatedAt: agentResponseTestBaseTime, UpdatedAt: agentResponseTestBaseTime,
		},
		AssistantMessage: conversationdomain.Message{
			ID: snapshot.AssistantMessageID, ConversationID: snapshot.ConversationID, SequenceNo: 2,
			Role: conversationdomain.RoleAssistant, Content: conversationdomain.AssistantGeneratingPlaceholder,
			Status:    conversationdomain.MessageStatusGenerating,
			CreatedAt: agentResponseTestBaseTime, UpdatedAt: agentResponseTestBaseTime,
		},
		Created: true,
	}
}

func setAgentFinalPayload(snapshot *agentapplication.RunSnapshot, payload json.RawMessage) {
	snapshot.Result.Payload = append(json.RawMessage(nil), payload...)
	final := agentapplication.FinalStepResult{
		Answer: snapshot.Result.Answer, ResponseLanguage: snapshot.Result.ResponseLanguage,
		Payload: payload,
	}
	encoded, err := json.Marshal(final)
	if err != nil {
		encoded = append([]byte(`{"answer":"trusted answer","response_language":"en","payload":`), payload...)
		encoded = append(encoded, '}')
	}
	snapshot.Steps[len(snapshot.Steps)-1].Result = encoded
}

func mustAgentResponseJSON(value any) json.RawMessage {
	encoded, err := json.Marshal(value)
	if err != nil {
		panic(err)
	}
	return encoded
}
