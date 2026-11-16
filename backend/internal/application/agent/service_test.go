package agent

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"testing"
	"time"

	accessdomain "rag-reasoning-platform/backend/internal/domain/access"
	agentdomain "rag-reasoning-platform/backend/internal/domain/agent"
	conversationdomain "rag-reasoning-platform/backend/internal/domain/conversation"
)

type fakeScopedRepository struct {
	createInput  CreateRunInput
	createResult QueueResult
	createErr    error
	getResult    RunSnapshot
	getErr       error
	activeResult RunSnapshot
	activeErr    error
	latestResult RunSnapshot
	latestErr    error
	cancelResult RunSnapshot
	cancelErr    error
	calls        int
}

func (f *fakeScopedRepository) CreateRun(_ context.Context, _ accessdomain.OwnerScope, input CreateRunInput) (QueueResult, error) {
	f.calls++
	f.createInput = input
	return f.createResult, f.createErr
}

func (f *fakeScopedRepository) GetRunByID(context.Context, accessdomain.OwnerScope, int64) (RunSnapshot, error) {
	return f.getResult, f.getErr
}

func (f *fakeScopedRepository) GetActiveRunByConversationID(context.Context, accessdomain.OwnerScope, int64) (RunSnapshot, error) {
	return f.activeResult, f.activeErr
}

func (f *fakeScopedRepository) GetLatestRunByConversationID(context.Context, accessdomain.OwnerScope, int64) (RunSnapshot, error) {
	return f.latestResult, f.latestErr
}

func (f *fakeScopedRepository) CancelRun(context.Context, accessdomain.OwnerScope, int64) (RunSnapshot, error) {
	return f.cancelResult, f.cancelErr
}

type fakeStepRepository struct {
	steps []agentdomain.Step
}

func (f *fakeStepRepository) ListSteps(context.Context, accessdomain.OwnerScope, int64) ([]agentdomain.Step, error) {
	return f.steps, nil
}

func (f *fakeStepRepository) ListStepsForWorker(context.Context, int64, string) ([]agentdomain.Step, error) {
	return f.steps, nil
}

func (f *fakeStepRepository) ClaimPendingStep(context.Context, int64, string) (agentdomain.Step, error) {
	return agentdomain.Step{}, ErrNoPendingStep
}

func (f *fakeStepRepository) PersistDecision(context.Context, int64, string, PersistDecisionInput) (agentdomain.Step, error) {
	return agentdomain.Step{}, nil
}

func (f *fakeStepRepository) MarkStepSucceeded(context.Context, int64, int64, string, CompleteStepInput) (agentdomain.Step, error) {
	return agentdomain.Step{}, nil
}

func (f *fakeStepRepository) MarkStepFailed(context.Context, int64, int64, string, FailStepInput) (agentdomain.Step, error) {
	return agentdomain.Step{}, nil
}

func testScope(t *testing.T) accessdomain.OwnerScope {
	t.Helper()
	scope, err := accessdomain.NewOwnerScope(42)
	if err != nil {
		t.Fatalf("NewOwnerScope() error = %v", err)
	}
	return scope
}

func testService(t *testing.T, runs *fakeScopedRepository, steps *fakeStepRepository) *Service {
	t.Helper()
	service, err := NewService(runs, steps, RunPolicy{
		MaxSteps: 5, MaxTotalTokens: 48_000, RunTimeout: 2 * time.Minute,
	})
	if err != nil {
		t.Fatalf("NewService() error = %v", err)
	}
	return service
}

func TestNewServiceValidatesDependenciesAndPolicy(t *testing.T) {
	runs := &fakeScopedRepository{}
	steps := &fakeStepRepository{}
	if _, err := NewService(nil, steps, RunPolicy{MaxSteps: 5, MaxTotalTokens: 1, RunTimeout: time.Second}); !errors.Is(err, ErrServiceDependencies) {
		t.Fatalf("NewService(nil) error = %v", err)
	}
	if _, err := NewService(runs, steps, RunPolicy{}); !errors.Is(err, ErrInvalidRunPolicy) {
		t.Fatalf("NewService(invalid policy) error = %v", err)
	}
	if _, err := NewService(runs, steps, RunPolicy{
		MaxSteps: agentdomain.MaxPersistedSteps + 1, MaxTotalTokens: 1, RunTimeout: time.Second,
	}); !errors.Is(err, ErrInvalidRunPolicy) {
		t.Fatalf("NewService(oversized step policy) error = %v", err)
	}
}

func TestServiceQueueNormalizesAndAppliesServerBudget(t *testing.T) {
	runs := &fakeScopedRepository{createResult: QueueResult{
		RunSnapshot: RunSnapshot{Run: agentdomain.Run{ID: 7}, Steps: []agentdomain.Step{}},
		Created:     true,
	}}
	service := testService(t, runs, &fakeStepRepository{})
	now := time.Date(2026, 11, 15, 8, 0, 0, 0, time.UTC)
	service.now = func() time.Time { return now }
	documentID := int64(9)

	result, err := service.Queue(t.Context(), testScope(t), QueueInput{
		ConversationID:      3,
		Content:             "question",
		ClientRunID:         "  run-1  ",
		RequestedDocumentID: &documentID,
		ResponseLanguage:    " EN ",
	})
	if err != nil || result.Run.ID != 7 {
		t.Fatalf("Queue() = %+v, %v", result, err)
	}
	got := runs.createInput
	if got.ClientRunID != "run-1" || got.ResponseLanguage != agentdomain.ResponseLanguageEnglish {
		t.Fatalf("CreateRun input = %+v", got)
	}
	if got.Budget.MaxSteps != 5 || got.Budget.MaxTotalTokens != 48_000 || !got.Budget.DeadlineAt.Equal(now.Add(2*time.Minute)) {
		t.Fatalf("CreateRun budget = %+v", got.Budget)
	}
}

func TestServiceQueueRejectsInvalidInputBeforeRepository(t *testing.T) {
	tests := []struct {
		name  string
		input QueueInput
		want  error
	}{
		{name: "conversation", input: QueueInput{ClientRunID: "x", Content: "x"}, want: ErrInvalidConversationID},
		{name: "content", input: QueueInput{ConversationID: 1, ClientRunID: "x", Content: "  "}, want: ErrInvalidContent},
		{name: "client ID", input: QueueInput{ConversationID: 1, Content: "x"}, want: ErrInvalidClientRunID},
		{name: "language", input: QueueInput{ConversationID: 1, Content: "x", ClientRunID: "x", ResponseLanguage: "fr"}, want: ErrInvalidResponseLanguage},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			runs := &fakeScopedRepository{}
			service := testService(t, runs, &fakeStepRepository{})
			_, err := service.Queue(t.Context(), testScope(t), test.input)
			if !errors.Is(err, test.want) {
				t.Fatalf("Queue() error = %v, want %v", err, test.want)
			}
			if runs.calls != 0 {
				t.Fatalf("repository calls = %d, want 0", runs.calls)
			}
		})
	}
}

func TestServiceQueriesCancelAndListSteps(t *testing.T) {
	runs := &fakeScopedRepository{
		getResult:    RunSnapshot{Run: agentdomain.Run{ID: 8}, Steps: []agentdomain.Step{}},
		activeResult: RunSnapshot{Run: agentdomain.Run{ID: 8}, Steps: []agentdomain.Step{}},
		latestResult: RunSnapshot{Run: agentdomain.Run{ID: 8}, Steps: []agentdomain.Step{}},
		cancelResult: RunSnapshot{Run: agentdomain.Run{ID: 8, Status: agentdomain.RunStatusCanceled}, Steps: []agentdomain.Step{}},
	}
	service := testService(t, runs, &fakeStepRepository{})
	scope := testScope(t)

	if _, err := service.GetByID(t.Context(), scope, 0); !errors.Is(err, ErrInvalidRunID) {
		t.Fatalf("GetByID() error = %v", err)
	}
	if _, err := service.GetActiveConversationRun(t.Context(), scope, 0); !errors.Is(err, ErrInvalidConversationID) {
		t.Fatalf("GetActiveConversationRun() error = %v", err)
	}
	if _, err := service.Cancel(t.Context(), scope, 8); err != nil {
		t.Fatalf("Cancel() error = %v", err)
	}
	steps, err := service.ListSteps(t.Context(), scope, 8)
	if err != nil || steps == nil || len(steps) != 0 {
		t.Fatalf("ListSteps() = %#v, %v", steps, err)
	}
}

func TestServiceReturnsUnifiedRunSnapshotsAndPreservesLookupErrors(t *testing.T) {
	snapshot := RunSnapshot{
		Run:   agentdomain.Run{ID: 8, ConversationID: 3, StepCount: 1},
		Steps: []agentdomain.Step{{ID: 11, RunID: 8, SequenceNo: 1}},
	}
	runs := &fakeScopedRepository{
		createResult: QueueResult{RunSnapshot: snapshot, Created: true},
		getResult:    snapshot,
		activeResult: snapshot,
		latestResult: snapshot,
		cancelResult: snapshot,
	}
	service := testService(t, runs, &fakeStepRepository{})
	scope := testScope(t)

	queued, err := service.Queue(t.Context(), scope, QueueInput{
		ConversationID: 3, Content: "question", ClientRunID: "run-1",
		ResponseLanguage: agentdomain.ResponseLanguageAuto,
	})
	if err != nil || !reflect.DeepEqual(queued.RunSnapshot, snapshot) {
		t.Fatalf("Queue() snapshot = %+v, %v", queued.RunSnapshot, err)
	}
	for name, call := range map[string]func() (RunSnapshot, error){
		"get":    func() (RunSnapshot, error) { return service.GetByID(t.Context(), scope, 8) },
		"active": func() (RunSnapshot, error) { return service.GetActiveConversationRun(t.Context(), scope, 3) },
		"latest": func() (RunSnapshot, error) { return service.GetLatestConversationRun(t.Context(), scope, 3) },
		"cancel": func() (RunSnapshot, error) { return service.Cancel(t.Context(), scope, 8) },
	} {
		t.Run(name, func(t *testing.T) {
			got, err := call()
			if err != nil || !reflect.DeepEqual(got, snapshot) {
				t.Fatalf("snapshot = %+v, %v", got, err)
			}
		})
	}

	runs.activeErr = conversationdomain.ErrNotFound
	if _, err := service.GetActiveConversationRun(t.Context(), scope, 3); !errors.Is(err, conversationdomain.ErrNotFound) {
		t.Fatalf("active conversation error = %v", err)
	}
	runs.latestErr = ErrRunNotFound
	if _, err := service.GetLatestConversationRun(t.Context(), scope, 3); !errors.Is(err, ErrRunNotFound) {
		t.Fatalf("latest no-run error = %v", err)
	}
}

func TestStepInputJSONRemainsOpaqueAtPort(t *testing.T) {
	raw := json.RawMessage(`{"query":"safe"}`)
	input := PersistDecisionInput{ToolName: "search_documents", Arguments: raw}
	if string(input.Arguments) != string(raw) {
		t.Fatalf("arguments = %s", input.Arguments)
	}
}
