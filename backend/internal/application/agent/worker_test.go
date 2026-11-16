package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	accessdomain "rag-reasoning-platform/backend/internal/domain/access"
	agentdomain "rag-reasoning-platform/backend/internal/domain/agent"
)

type traceRecorder struct {
	mu     sync.Mutex
	events []string
}

func (r *traceRecorder) add(event string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.events = append(r.events, event)
}

func (r *traceRecorder) snapshot() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.events...)
}

type memoryExecutionRepository struct {
	mu         sync.Mutex
	run        agentdomain.Run
	steps      []agentdomain.Step
	claimed    bool
	nextStepID int64
	trace      *traceRecorder
	renewErr   error
	failCode   agentdomain.ErrorCode
	requeued   bool
	finalInput *CommitFinalInput
}

func newMemoryExecutionRepository(run agentdomain.Run, steps []agentdomain.Step, trace *traceRecorder) *memoryExecutionRepository {
	return &memoryExecutionRepository{run: run, steps: append([]agentdomain.Step(nil), steps...), nextStepID: 100, trace: trace}
}

func (r *memoryExecutionRepository) RequeueExpiredRuns(context.Context, int, agentdomain.ErrorCode, string) (int64, error) {
	return 0, nil
}

func (r *memoryExecutionRepository) ClaimNextRun(context.Context) (agentdomain.Run, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.claimed {
		return agentdomain.Run{}, ErrNoQueuedRun
	}
	r.claimed = true
	return r.run, nil
}

func (r *memoryExecutionRepository) RenewRunLease(_ context.Context, runID int64, leaseToken string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.renewErr == nil && (runID != r.run.ID || leaseToken != r.run.LeaseToken || r.run.Status != agentdomain.RunStatusProcessing) {
		return ErrRunLeaseLost
	}
	return r.renewErr
}

func (r *memoryExecutionRepository) RequeueRun(
	_ context.Context,
	runID int64,
	leaseToken string,
	_ time.Time,
	code agentdomain.ErrorCode,
	message string,
) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if runID != r.run.ID || leaseToken != r.run.LeaseToken || r.run.Status != agentdomain.RunStatusProcessing {
		return ErrRunLeaseLost
	}
	r.requeued = true
	r.run.Status = agentdomain.RunStatusQueued
	r.run.ErrorCode = code
	r.run.ErrorMessage = &message
	r.run.LeaseToken = ""
	for index := range r.steps {
		if r.steps[index].Status == agentdomain.StepStatusRunning {
			r.steps[index].Status = agentdomain.StepStatusPending
		}
	}
	r.trace.add("requeue")
	return nil
}

func (r *memoryExecutionRepository) MarkRunFailed(
	_ context.Context,
	runID int64,
	leaseToken string,
	code agentdomain.ErrorCode,
	message string,
) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if runID != r.run.ID || leaseToken != r.run.LeaseToken || r.run.Status != agentdomain.RunStatusProcessing {
		return ErrRunLeaseLost
	}
	r.failCode = code
	r.run.Status = agentdomain.RunStatusFailed
	r.run.ErrorCode = code
	r.run.ErrorMessage = &message
	r.run.LeaseToken = ""
	for index := range r.steps {
		if r.steps[index].Status == agentdomain.StepStatusPending || r.steps[index].Status == agentdomain.StepStatusRunning {
			r.steps[index].Status = agentdomain.StepStatusFailed
		}
	}
	r.trace.add("fail:" + string(code))
	return nil
}

func (r *memoryExecutionRepository) CommitFinal(
	_ context.Context,
	runID int64,
	leaseToken string,
	input CommitFinalInput,
) (agentdomain.Step, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if runID != r.run.ID || leaseToken != r.run.LeaseToken || r.run.Status != agentdomain.RunStatusProcessing {
		return agentdomain.Step{}, ErrRunLeaseLost
	}
	if r.run.StepCount >= r.run.Budget.MaxSteps {
		return agentdomain.Step{}, ErrStepLimitExceeded
	}
	if input.TotalTokens > r.run.Budget.MaxTotalTokens-r.run.TotalTokens {
		return agentdomain.Step{}, ErrTokenBudgetExceeded
	}
	r.nextStepID++
	raw, _ := json.Marshal(input.Result)
	step := agentdomain.Step{
		ID: r.nextStepID, RunID: runID, SequenceNo: r.run.StepCount + 1,
		Kind: agentdomain.StepKindFinal, Status: agentdomain.StepStatusSucceeded,
		Result: raw, PromptTokens: input.PromptTokens,
		CompletionTokens: input.CompletionTokens, TotalTokens: input.TotalTokens,
	}
	r.steps = append(r.steps, step)
	r.run.StepCount++
	r.run.PromptTokens += input.PromptTokens
	r.run.CompletionTokens += input.CompletionTokens
	r.run.TotalTokens += input.TotalTokens
	r.run.Status = agentdomain.RunStatusSucceeded
	r.run.LeaseToken = ""
	r.run.Result = &agentdomain.Result{
		Answer: input.Result.Answer, ResponseLanguage: input.Result.ResponseLanguage,
		Payload: input.Result.Payload, PromptTokens: r.run.PromptTokens,
		CompletionTokens: r.run.CompletionTokens, TotalTokens: r.run.TotalTokens,
	}
	copy := input
	r.finalInput = &copy
	r.trace.add("commit:final")
	return step, nil
}

func (r *memoryExecutionRepository) ListStepsForWorker(_ context.Context, runID int64, leaseToken string) ([]agentdomain.Step, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if runID != r.run.ID || leaseToken != r.run.LeaseToken || r.run.Status != agentdomain.RunStatusProcessing {
		return nil, ErrRunLeaseLost
	}
	return append([]agentdomain.Step(nil), r.steps...), nil
}

func (r *memoryExecutionRepository) ClaimPendingStep(_ context.Context, runID int64, leaseToken string) (agentdomain.Step, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if runID != r.run.ID || leaseToken != r.run.LeaseToken || r.run.Status != agentdomain.RunStatusProcessing {
		return agentdomain.Step{}, ErrRunLeaseLost
	}
	for index := range r.steps {
		if r.steps[index].Status == agentdomain.StepStatusPending {
			r.steps[index].Status = agentdomain.StepStatusRunning
			r.trace.add("claim:pending")
			return r.steps[index], nil
		}
	}
	return agentdomain.Step{}, ErrNoPendingStep
}

func (r *memoryExecutionRepository) PersistDecision(
	_ context.Context,
	runID int64,
	leaseToken string,
	input PersistDecisionInput,
) (agentdomain.Step, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if runID != r.run.ID || leaseToken != r.run.LeaseToken || r.run.Status != agentdomain.RunStatusProcessing {
		return agentdomain.Step{}, ErrRunLeaseLost
	}
	if r.run.StepCount >= r.run.Budget.MaxSteps-1 {
		return agentdomain.Step{}, ErrStepLimitExceeded
	}
	if input.TotalTokens > r.run.Budget.MaxTotalTokens-r.run.TotalTokens {
		return agentdomain.Step{}, ErrTokenBudgetExceeded
	}
	r.nextStepID++
	toolName := input.ToolName
	step := agentdomain.Step{
		ID: r.nextStepID, RunID: runID, SequenceNo: r.run.StepCount + 1,
		Kind: agentdomain.StepKindToolCall, Status: agentdomain.StepStatusRunning,
		ToolName: &toolName, Arguments: append(json.RawMessage(nil), input.Arguments...),
		PromptTokens: input.PromptTokens, CompletionTokens: input.CompletionTokens, TotalTokens: input.TotalTokens,
	}
	r.steps = append(r.steps, step)
	r.run.StepCount++
	r.run.PromptTokens += input.PromptTokens
	r.run.CompletionTokens += input.CompletionTokens
	r.run.TotalTokens += input.TotalTokens
	r.trace.add("persist:" + input.ToolName)
	return step, nil
}

func (r *memoryExecutionRepository) MarkStepSucceeded(
	_ context.Context,
	runID int64,
	stepID int64,
	leaseToken string,
	input CompleteStepInput,
) (agentdomain.Step, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if runID != r.run.ID || leaseToken != r.run.LeaseToken || r.run.Status != agentdomain.RunStatusProcessing {
		return agentdomain.Step{}, ErrRunLeaseLost
	}
	for index := range r.steps {
		if r.steps[index].ID == stepID && r.steps[index].Status == agentdomain.StepStatusRunning {
			r.steps[index].Status = agentdomain.StepStatusSucceeded
			r.steps[index].Result = append(json.RawMessage(nil), input.Result...)
			r.trace.add("complete:" + *r.steps[index].ToolName)
			return r.steps[index], nil
		}
	}
	return agentdomain.Step{}, ErrInvalidStep
}

func (r *memoryExecutionRepository) MarkStepFailed(
	_ context.Context,
	runID int64,
	stepID int64,
	leaseToken string,
	input FailStepInput,
) (agentdomain.Step, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if runID != r.run.ID || leaseToken != r.run.LeaseToken || r.run.Status != agentdomain.RunStatusProcessing {
		return agentdomain.Step{}, ErrRunLeaseLost
	}
	for index := range r.steps {
		if r.steps[index].ID == stepID && r.steps[index].Status == agentdomain.StepStatusRunning {
			r.steps[index].Status = agentdomain.StepStatusFailed
			r.steps[index].ErrorCode = input.ErrorCode
			r.steps[index].ErrorMessage = &input.ErrorMessage
			r.trace.add("step-fail:" + string(input.ErrorCode))
			return r.steps[index], nil
		}
	}
	return agentdomain.Step{}, ErrInvalidStep
}

func (r *memoryExecutionRepository) snapshot() (agentdomain.Run, []agentdomain.Step) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.run, append([]agentdomain.Step(nil), r.steps...)
}

type contextProviderFunc func(context.Context, accessdomain.OwnerScope, agentdomain.Run) (AgentContext, error)

func (f contextProviderFunc) LoadAgentContext(
	ctx context.Context,
	scope accessdomain.OwnerScope,
	run agentdomain.Run,
) (AgentContext, error) {
	return f(ctx, scope, run)
}

type scriptedModel struct {
	mu        sync.Mutex
	decisions []ModelDecision
	errors    []error
	requests  []ModelRequest
	trace     *traceRecorder
	started   chan struct{}
	release   chan struct{}
	delay     time.Duration
}

func (m *scriptedModel) Decide(ctx context.Context, request ModelRequest) (ModelDecision, error) {
	m.mu.Lock()
	callIndex := len(m.requests)
	m.requests = append(m.requests, request)
	m.mu.Unlock()
	if m.trace != nil {
		m.trace.add(fmt.Sprintf("model:%d", callIndex+1))
	}
	if m.started != nil {
		select {
		case m.started <- struct{}{}:
		default:
		}
	}
	if m.release != nil {
		select {
		case <-m.release:
		case <-ctx.Done():
			return ModelDecision{}, ctx.Err()
		}
	}
	if m.delay > 0 {
		timer := time.NewTimer(m.delay)
		defer timer.Stop()
		select {
		case <-timer.C:
		case <-ctx.Done():
			return ModelDecision{}, ctx.Err()
		}
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if callIndex < len(m.errors) && m.errors[callIndex] != nil {
		return ModelDecision{}, m.errors[callIndex]
	}
	if callIndex >= len(m.decisions) {
		return ModelDecision{}, errors.New("unexpected model call")
	}
	return m.decisions[callIndex], nil
}

func (m *scriptedModel) callCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.requests)
}

func testWorkerRun() agentdomain.Run {
	return agentdomain.Run{
		ID: 1, OwnerUserID: 42, ConversationID: 7, UserMessageID: 8, AssistantMessageID: 9,
		ResponseLanguage: agentdomain.ResponseLanguageEnglish,
		Status:           agentdomain.RunStatusProcessing,
		Budget:           agentdomain.Budget{MaxSteps: 5, MaxTotalTokens: 48_000, DeadlineAt: time.Now().Add(time.Minute)},
		AttemptCount:     1, LeaseToken: "lease-1",
	}
}

func testWorkerPolicy() WorkerPolicy {
	return WorkerPolicy{
		ModelTimeout: time.Second, ToolTimeout: time.Second,
		HeartbeatInterval: time.Hour, RetryDelay: time.Second, RecoveryBatchSize: 10,
	}
}

func validContextProvider(t *testing.T) ContextProvider {
	t.Helper()
	return contextProviderFunc(func(_ context.Context, scope accessdomain.OwnerScope, run agentdomain.Run) (AgentContext, error) {
		if scope.OwnerUserID() != run.OwnerUserID {
			t.Fatalf("context scope=%d run owner=%d", scope.OwnerUserID(), run.OwnerUserID)
		}
		return AgentContext{
			SystemInstruction: "Use only trusted evidence.",
			CurrentQuestion:   "What does the paper say?",
			History:           []ModelMessage{{Role: "user", Content: "Earlier question"}},
		}, nil
	})
}

func TestWorkerRunsDeterministicSearchReadFinalChain(t *testing.T) {
	trace := &traceRecorder{}
	repository := newMemoryExecutionRepository(testWorkerRun(), nil, trace)
	model := &scriptedModel{trace: trace, decisions: []ModelDecision{
		{
			ToolCall: &ModelToolCall{
				Name:      ToolSearchDocuments,
				Arguments: json.RawMessage(`{"query":"bridge","strategy":"semantic","top_k":2}`),
			},
			Usage: TokenUsage{PromptTokens: 2, CompletionTokens: 1, TotalTokens: 3},
		},
		{
			ToolCall: &ModelToolCall{
				Name:      ToolReadDocumentEvidence,
				Arguments: json.RawMessage(`{"evidence_refs":["step-1:item-1"]}`),
			},
			Usage: TokenUsage{PromptTokens: 2, CompletionTokens: 1, TotalTokens: 3},
		},
		{
			Final: &ModelFinalAnswer{Text: "The bridge response is bounded.", EvidenceRefs: []string{"step-1:item-1"}},
			Usage: TokenUsage{PromptTokens: 1, CompletionTokens: 2, TotalTokens: 3},
		},
	}}
	page := 4
	registry, err := NewToolRegistry(
		searchDocumentsPortFunc(func(_ context.Context, _ accessdomain.OwnerScope, input SearchDocumentsInput) ([]SearchDocumentsHit, error) {
			trace.add("tool:search")
			return []SearchDocumentsHit{{
				DocumentID: 10, ChunkID: 20, Title: "Bridge", PageStart: &page, PageEnd: &page,
				Snippet: "bounded snippet", Score: 0.9,
			}}, nil
		}),
		readEvidencePortFunc(func(_ context.Context, _ accessdomain.OwnerScope, locators []EvidenceLocator) ([]ReadEvidenceItem, error) {
			trace.add("tool:read")
			if len(locators) != 1 || locators[0].EvidenceRef != "step-1:item-1" {
				t.Fatalf("read locators = %+v", locators)
			}
			return []ReadEvidenceItem{{
				DocumentID: 10, ChunkID: 20, Title: "Bridge", PageStart: &page, PageEnd: &page,
				Excerpt: "bounded evidence", ContentHash: "sha256:bridge",
			}}, nil
		}),
	)
	if err != nil {
		t.Fatalf("NewToolRegistry() error = %v", err)
	}
	worker, err := NewWorker(repository, validContextProvider(t), model, registry, testWorkerPolicy())
	if err != nil {
		t.Fatalf("NewWorker() error = %v", err)
	}
	handled, err := worker.RunOnce(t.Context())
	if err != nil || !handled {
		t.Fatalf("RunOnce() = %t, %v", handled, err)
	}
	run, steps := repository.snapshot()
	if run.Status != agentdomain.RunStatusSucceeded || run.StepCount != 3 || run.TotalTokens != 9 ||
		run.Result == nil || run.Result.Answer != "The bridge response is bounded." {
		t.Fatalf("completed Run = %+v", run)
	}
	if len(steps) != 3 || steps[0].Status != agentdomain.StepStatusSucceeded ||
		steps[1].Status != agentdomain.StepStatusSucceeded || steps[2].Kind != agentdomain.StepKindFinal {
		t.Fatalf("completed Steps = %+v", steps)
	}
	wantTrace := []string{
		"model:1", "persist:search_documents", "tool:search", "complete:search_documents",
		"model:2", "persist:read_document_evidence", "tool:read", "complete:read_document_evidence",
		"model:3", "commit:final",
	}
	if got := trace.snapshot(); fmt.Sprint(got) != fmt.Sprint(wantTrace) {
		t.Fatalf("execution trace = %v, want %v", got, wantTrace)
	}
	if len(model.requests) != 3 || len(model.requests[0].Observations) != 0 ||
		len(model.requests[1].Observations) != 1 || len(model.requests[2].Observations) != 2 {
		t.Fatalf("model requests = %+v", model.requests)
	}
	if !strings.Contains(string(model.requests[1].Observations[0].Arguments), `"query":"bridge"`) ||
		!strings.Contains(string(model.requests[2].Observations[1].Arguments), `"step-1:item-1"`) {
		t.Fatalf("model observation arguments = %+v", model.requests)
	}
	if repository.finalInput == nil {
		t.Fatal("final input was not persisted")
	}
	var payload struct {
		Sources []FinalSource `json:"sources"`
	}
	if err := json.Unmarshal(repository.finalInput.Result.Payload, &payload); err != nil ||
		len(payload.Sources) != 1 || payload.Sources[0].ContentHash != "sha256:bridge" {
		t.Fatalf("final source payload = %+v, %v", payload, err)
	}
}

type fakeToolSet struct {
	trace        *traceRecorder
	validateErr  error
	executeErr   error
	result       json.RawMessage
	sources      []FinalSource
	resolveErr   error
	started      chan struct{}
	release      chan struct{}
	delay        time.Duration
	executeCalls atomic.Int64
}

func (f *fakeToolSet) Definitions() []ToolDefinition {
	return []ToolDefinition{{Name: ToolSearchDocuments, Parameters: json.RawMessage(`{}`)}}
}

func (f *fakeToolSet) ValidateCall(agentdomain.Run, []agentdomain.Step, ModelToolCall) (json.RawMessage, error) {
	if f.validateErr != nil {
		return nil, f.validateErr
	}
	return json.RawMessage(`{"query":"safe","strategy":"keyword","top_k":1}`), nil
}

func (f *fakeToolSet) Execute(
	ctx context.Context,
	_ accessdomain.OwnerScope,
	_ agentdomain.Run,
	_ []agentdomain.Step,
	step agentdomain.Step,
) (json.RawMessage, error) {
	f.executeCalls.Add(1)
	if f.trace != nil && step.ToolName != nil {
		f.trace.add("tool:" + *step.ToolName)
	}
	if f.started != nil {
		select {
		case f.started <- struct{}{}:
		default:
		}
	}
	if f.release != nil {
		select {
		case <-f.release:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	if f.delay > 0 {
		timer := time.NewTimer(f.delay)
		defer timer.Stop()
		select {
		case <-timer.C:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	if f.executeErr != nil {
		return nil, f.executeErr
	}
	if len(f.result) == 0 {
		return json.RawMessage(`{"items":[]}`), nil
	}
	return append(json.RawMessage(nil), f.result...), nil
}

func (f *fakeToolSet) ResolveFinalSources(agentdomain.Run, []agentdomain.Step, []string) ([]FinalSource, error) {
	if f.resolveErr != nil {
		return nil, f.resolveErr
	}
	return append([]FinalSource(nil), f.sources...), nil
}

func TestWorkerExecutesRecoveredPendingActionBeforeCallingModel(t *testing.T) {
	trace := &traceRecorder{}
	run := testWorkerRun()
	run.StepCount = 1
	run.PromptTokens = 2
	run.CompletionTokens = 1
	run.TotalTokens = 3
	toolName := ToolSearchDocuments
	pending := agentdomain.Step{
		ID: 50, RunID: run.ID, SequenceNo: 1, Kind: agentdomain.StepKindToolCall,
		Status: agentdomain.StepStatusPending, ToolName: &toolName,
		Arguments:    json.RawMessage(`{"query":"safe","strategy":"keyword","top_k":1}`),
		PromptTokens: 2, CompletionTokens: 1, TotalTokens: 3,
	}
	repository := newMemoryExecutionRepository(run, []agentdomain.Step{pending}, trace)
	model := &scriptedModel{trace: trace, decisions: []ModelDecision{{
		Final: &ModelFinalAnswer{Text: "recovered", EvidenceRefs: []string{"step-1:item-1"}},
		Usage: TokenUsage{PromptTokens: 1, CompletionTokens: 1, TotalTokens: 2},
	}}}
	started := make(chan struct{}, 1)
	release := make(chan struct{})
	tools := &fakeToolSet{
		trace: trace, started: started, release: release,
		sources: []FinalSource{{EvidenceRef: "step-1:item-1", DocumentID: 1, ChunkID: 2, ContentHash: "hash"}},
	}
	worker, err := NewWorker(repository, validContextProvider(t), model, tools, testWorkerPolicy())
	if err != nil {
		t.Fatalf("NewWorker() error = %v", err)
	}
	done := make(chan error, 1)
	go func() {
		_, runErr := worker.RunOnce(t.Context())
		done <- runErr
	}()
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("recovered Tool did not start")
	}
	if calls := model.callCount(); calls != 0 {
		t.Fatalf("model calls before recovered Tool = %d, want 0", calls)
	}
	close(release)
	if err := <-done; err != nil {
		t.Fatalf("RunOnce() after recovered Tool error = %v", err)
	}
	if calls := model.callCount(); calls != 1 {
		t.Fatalf("model calls after recovered Tool = %d, want 1 final call", calls)
	}
	if len(model.requests) != 1 || len(model.requests[0].Observations) != 1 ||
		string(model.requests[0].Observations[0].Arguments) != string(pending.Arguments) {
		t.Fatalf("recovered observation arguments = %+v", model.requests)
	}
	if got := trace.snapshot(); len(got) < 3 || got[0] != "claim:pending" || got[1] != "tool:search_documents" {
		t.Fatalf("recovery trace = %v", got)
	}
}

func TestModelObservationsDefensivelyCopyArgumentsAndResult(t *testing.T) {
	toolName := ToolSearchDocuments
	steps := []agentdomain.Step{{
		ID: 1, RunID: 1, SequenceNo: 1, Kind: agentdomain.StepKindToolCall,
		Status: agentdomain.StepStatusSucceeded, ToolName: &toolName,
		Arguments: json.RawMessage(`{"query":"original"}`),
		Result:    json.RawMessage(`{"items":[]}`),
	}}
	observations := modelObservations(steps)
	steps[0].Arguments[0] = 'x'
	steps[0].Result[0] = 'x'
	if len(observations) != 1 || string(observations[0].Arguments) != `{"query":"original"}` ||
		string(observations[0].Result) != `{"items":[]}` {
		t.Fatalf("observations changed with source Step: %+v", observations)
	}
}
