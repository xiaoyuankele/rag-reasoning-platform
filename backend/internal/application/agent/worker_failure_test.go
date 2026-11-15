package agent

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	accessdomain "rag-reasoning-platform/backend/internal/domain/access"
	agentdomain "rag-reasoning-platform/backend/internal/domain/agent"
)

func TestNewWorkerValidatesDependenciesAndPolicy(t *testing.T) {
	validPolicy := testWorkerPolicy()
	repository := newMemoryExecutionRepository(testWorkerRun(), nil, &traceRecorder{})
	contextProvider := validContextProvider(t)
	model := &scriptedModel{}
	tools := &fakeToolSet{}
	if _, err := NewWorker(nil, contextProvider, model, tools, validPolicy); !errors.Is(err, ErrWorkerDependencies) {
		t.Fatalf("NewWorker(nil) error = %v", err)
	}
	if _, err := NewWorker(repository, contextProvider, model, tools, WorkerPolicy{}); !errors.Is(err, ErrInvalidWorkerPolicy) {
		t.Fatalf("NewWorker(invalid policy) error = %v", err)
	}
	repository.claimed = true
	worker, err := NewWorker(repository, contextProvider, model, tools, validPolicy)
	if err != nil {
		t.Fatalf("NewWorker() error = %v", err)
	}
	if handled, err := worker.RunOnce(t.Context()); err != nil || handled {
		t.Fatalf("empty RunOnce() = %t, %v", handled, err)
	}
}

func TestWorkerPreflightExhaustionStopsBeforeModelAndTool(t *testing.T) {
	for _, test := range []struct {
		name   string
		mutate func(*agentdomain.Run)
		want   agentdomain.ErrorCode
	}{
		{
			name:   "step limit",
			mutate: func(run *agentdomain.Run) { run.StepCount = run.Budget.MaxSteps },
			want:   agentdomain.ErrorCodeStepLimitExceeded,
		},
		{
			name:   "token budget",
			mutate: func(run *agentdomain.Run) { run.TotalTokens = run.Budget.MaxTotalTokens },
			want:   agentdomain.ErrorCodeTokenBudgetExceeded,
		},
		{
			name:   "deadline",
			mutate: func(run *agentdomain.Run) { run.Budget.DeadlineAt = time.Now().Add(-time.Second) },
			want:   agentdomain.ErrorCodeDeadlineExceeded,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			run := testWorkerRun()
			test.mutate(&run)
			repository := newMemoryExecutionRepository(run, nil, &traceRecorder{})
			model := &scriptedModel{}
			tools := &fakeToolSet{}
			worker, err := NewWorker(repository, validContextProvider(t), model, tools, testWorkerPolicy())
			if err != nil {
				t.Fatalf("NewWorker() error = %v", err)
			}
			handled, err := worker.RunOnce(t.Context())
			if err != nil || !handled {
				t.Fatalf("RunOnce() = %t, %v", handled, err)
			}
			if model.callCount() != 0 || tools.executeCalls.Load() != 0 || repository.failCode != test.want {
				t.Fatalf("preflight calls model=%d tool=%d fail=%s, want %s", model.callCount(), tools.executeCalls.Load(), repository.failCode, test.want)
			}
		})
	}
}

func TestWorkerMapsInvalidDecisionsAndEvidenceToStableFailure(t *testing.T) {
	validUsage := TokenUsage{PromptTokens: 1, CompletionTokens: 1, TotalTokens: 2}
	for _, test := range []struct {
		name      string
		decision  ModelDecision
		tools     *fakeToolSet
		wantCalls int
	}{
		{
			name:     "unknown tool",
			decision: ModelDecision{ToolCall: &ModelToolCall{Name: "write_document", Arguments: json.RawMessage(`{}`)}, Usage: validUsage},
			tools:    &fakeToolSet{validateErr: ErrInvalidToolCall},
		},
		{
			name: "cross run ref",
			decision: ModelDecision{ToolCall: &ModelToolCall{
				Name: ToolReadDocumentEvidence, Arguments: json.RawMessage(`{"evidence_refs":["step-9:item-1"]}`),
			}, Usage: validUsage},
			tools: &fakeToolSet{validateErr: ErrEvidenceRefNotFound},
		},
		{
			name:     "no final evidence",
			decision: ModelDecision{Final: &ModelFinalAnswer{Text: "unsupported"}, Usage: validUsage},
			tools:    &fakeToolSet{resolveErr: ErrFinalEvidenceRequired},
		},
		{
			name: "invalid tagged union",
			decision: ModelDecision{
				ToolCall: &ModelToolCall{Name: ToolSearchDocuments, Arguments: json.RawMessage(`{}`)},
				Final:    &ModelFinalAnswer{Text: "also final"}, Usage: validUsage,
			},
			tools: &fakeToolSet{},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			repository := newMemoryExecutionRepository(testWorkerRun(), nil, &traceRecorder{})
			model := &scriptedModel{decisions: []ModelDecision{test.decision}}
			worker, err := NewWorker(repository, validContextProvider(t), model, test.tools, testWorkerPolicy())
			if err != nil {
				t.Fatalf("NewWorker() error = %v", err)
			}
			if handled, err := worker.RunOnce(t.Context()); err != nil || !handled {
				t.Fatalf("RunOnce() = %t, %v", handled, err)
			}
			if repository.failCode != agentdomain.ErrorCodeInvalidToolCall || test.tools.executeCalls.Load() != int64(test.wantCalls) {
				t.Fatalf("invalid decision fail=%s tool_calls=%d", repository.failCode, test.tools.executeCalls.Load())
			}
		})
	}
}

func TestWorkerMapsToolFailureAndTemporaryFailures(t *testing.T) {
	toolDecision := ModelDecision{
		ToolCall: &ModelToolCall{
			Name:      ToolSearchDocuments,
			Arguments: json.RawMessage(`{"query":"safe","strategy":"keyword","top_k":1}`),
		},
		Usage: TokenUsage{PromptTokens: 2, CompletionTokens: 1, TotalTokens: 3},
	}
	t.Run("permanent tool failure", func(t *testing.T) {
		repository := newMemoryExecutionRepository(testWorkerRun(), nil, &traceRecorder{})
		model := &scriptedModel{decisions: []ModelDecision{toolDecision}}
		tools := &fakeToolSet{executeErr: errors.New("provider detail must not persist")}
		worker, _ := NewWorker(repository, validContextProvider(t), model, tools, testWorkerPolicy())
		if handled, err := worker.RunOnce(t.Context()); err != nil || !handled {
			t.Fatalf("RunOnce() = %t, %v", handled, err)
		}
		run, steps := repository.snapshot()
		if repository.failCode != agentdomain.ErrorCodeToolFailed || run.Status != agentdomain.RunStatusFailed ||
			len(steps) != 1 || steps[0].ErrorCode != agentdomain.ErrorCodeToolFailed ||
			steps[0].ErrorMessage == nil || *steps[0].ErrorMessage != safeToolFailureMessage {
			t.Fatalf("permanent Tool failure run=%+v steps=%+v", run, steps)
		}
	})

	t.Run("temporary tool failure", func(t *testing.T) {
		repository := newMemoryExecutionRepository(testWorkerRun(), nil, &traceRecorder{})
		model := &scriptedModel{decisions: []ModelDecision{toolDecision}}
		tools := &fakeToolSet{executeErr: ErrToolTemporarilyUnavailable}
		worker, _ := NewWorker(repository, validContextProvider(t), model, tools, testWorkerPolicy())
		if handled, err := worker.RunOnce(t.Context()); err != nil || !handled {
			t.Fatalf("RunOnce() = %t, %v", handled, err)
		}
		run, steps := repository.snapshot()
		if !repository.requeued || run.Status != agentdomain.RunStatusQueued || len(steps) != 1 ||
			steps[0].Status != agentdomain.StepStatusPending || steps[0].TotalTokens != 3 {
			t.Fatalf("temporary Tool failure run=%+v steps=%+v", run, steps)
		}
	})

	t.Run("temporary model failure", func(t *testing.T) {
		repository := newMemoryExecutionRepository(testWorkerRun(), nil, &traceRecorder{})
		model := &scriptedModel{errors: []error{ErrModelTemporarilyUnavailable}}
		tools := &fakeToolSet{}
		worker, _ := NewWorker(repository, validContextProvider(t), model, tools, testWorkerPolicy())
		if handled, err := worker.RunOnce(t.Context()); err != nil || !handled {
			t.Fatalf("RunOnce() = %t, %v", handled, err)
		}
		run, steps := repository.snapshot()
		if !repository.requeued || run.Status != agentdomain.RunStatusQueued || len(steps) != 0 {
			t.Fatalf("temporary Model failure run=%+v steps=%+v", run, steps)
		}
	})

	t.Run("direct model deadline", func(t *testing.T) {
		repository := newMemoryExecutionRepository(testWorkerRun(), nil, &traceRecorder{})
		model := &scriptedModel{errors: []error{context.DeadlineExceeded}}
		worker, _ := NewWorker(repository, validContextProvider(t), model, &fakeToolSet{}, testWorkerPolicy())
		if handled, err := worker.RunOnce(t.Context()); err != nil || !handled {
			t.Fatalf("RunOnce() = %t, %v", handled, err)
		}
		if !repository.requeued {
			t.Fatal("direct Model deadline was not requeued")
		}
	})

	t.Run("direct tool deadline", func(t *testing.T) {
		repository := newMemoryExecutionRepository(testWorkerRun(), nil, &traceRecorder{})
		model := &scriptedModel{decisions: []ModelDecision{toolDecision}}
		tools := &fakeToolSet{executeErr: context.DeadlineExceeded}
		worker, _ := NewWorker(repository, validContextProvider(t), model, tools, testWorkerPolicy())
		if handled, err := worker.RunOnce(t.Context()); err != nil || !handled {
			t.Fatalf("RunOnce() = %t, %v", handled, err)
		}
		if !repository.requeued {
			t.Fatal("direct Tool deadline was not requeued")
		}
	})
}

func TestWorkerMapsContextAndModelContractErrors(t *testing.T) {
	t.Run("context unavailable requeues", func(t *testing.T) {
		repository := newMemoryExecutionRepository(testWorkerRun(), nil, &traceRecorder{})
		contexts := contextProviderFunc(func(context.Context, accessdomain.OwnerScope, agentdomain.Run) (AgentContext, error) {
			return AgentContext{}, ErrAgentContextUnavailable
		})
		worker, _ := NewWorker(repository, contexts, &scriptedModel{}, &fakeToolSet{}, testWorkerPolicy())
		if handled, err := worker.RunOnce(t.Context()); err != nil || !handled {
			t.Fatalf("RunOnce() = %t, %v", handled, err)
		}
		if !repository.requeued {
			t.Fatal("context unavailable was not requeued")
		}
	})

	t.Run("invalid model response is stable invalid action", func(t *testing.T) {
		repository := newMemoryExecutionRepository(testWorkerRun(), nil, &traceRecorder{})
		model := &scriptedModel{errors: []error{ErrInvalidModelDecision}}
		worker, _ := NewWorker(repository, validContextProvider(t), model, &fakeToolSet{}, testWorkerPolicy())
		if handled, err := worker.RunOnce(t.Context()); err != nil || !handled {
			t.Fatalf("RunOnce() = %t, %v", handled, err)
		}
		if repository.failCode != agentdomain.ErrorCodeInvalidToolCall {
			t.Fatalf("invalid Model response fail code = %s", repository.failCode)
		}
	})
}

func TestWorkerRejectsDecisionTokenOverflowBeforePersistOrTool(t *testing.T) {
	run := testWorkerRun()
	run.Budget.MaxTotalTokens = 5
	run.TotalTokens = 4
	repository := newMemoryExecutionRepository(run, nil, &traceRecorder{})
	model := &scriptedModel{decisions: []ModelDecision{{
		ToolCall: &ModelToolCall{
			Name:      ToolSearchDocuments,
			Arguments: json.RawMessage(`{"query":"safe","strategy":"keyword","top_k":1}`),
		},
		Usage: TokenUsage{PromptTokens: 1, CompletionTokens: 1, TotalTokens: 2},
	}}}
	tools := &fakeToolSet{}
	worker, _ := NewWorker(repository, validContextProvider(t), model, tools, testWorkerPolicy())
	if handled, err := worker.RunOnce(t.Context()); err != nil || !handled {
		t.Fatalf("RunOnce() = %t, %v", handled, err)
	}
	_, steps := repository.snapshot()
	if repository.failCode != agentdomain.ErrorCodeTokenBudgetExceeded || len(steps) != 0 || tools.executeCalls.Load() != 0 {
		t.Fatalf("overflow fail=%s steps=%+v tool_calls=%d", repository.failCode, steps, tools.executeCalls.Load())
	}
}

func TestWorkerRejectsFifthToolDecisionAndPreservesFinalSlot(t *testing.T) {
	run := testWorkerRun()
	run.StepCount = run.Budget.MaxSteps - 1
	repository := newMemoryExecutionRepository(run, nil, &traceRecorder{})
	model := &scriptedModel{decisions: []ModelDecision{{
		ToolCall: &ModelToolCall{
			Name:      ToolSearchDocuments,
			Arguments: json.RawMessage(`{"query":"one too many","strategy":"keyword","top_k":1}`),
		},
		Usage: TokenUsage{PromptTokens: 1, CompletionTokens: 1, TotalTokens: 2},
	}}}
	tools := &fakeToolSet{}
	worker, _ := NewWorker(repository, validContextProvider(t), model, tools, testWorkerPolicy())
	if handled, err := worker.RunOnce(t.Context()); err != nil || !handled {
		t.Fatalf("RunOnce() = %t, %v", handled, err)
	}
	_, steps := repository.snapshot()
	if repository.failCode != agentdomain.ErrorCodeStepLimitExceeded || len(steps) != 0 || tools.executeCalls.Load() != 0 {
		t.Fatalf("fifth Tool fail=%s steps=%+v tool_calls=%d", repository.failCode, steps, tools.executeCalls.Load())
	}
}

func TestWorkerHeartbeatFailureCancelsBlockedModelWithoutStateWrite(t *testing.T) {
	repository := newMemoryExecutionRepository(testWorkerRun(), nil, &traceRecorder{})
	repository.renewErr = ErrRunLeaseLost
	started := make(chan struct{}, 1)
	model := &scriptedModel{started: started, release: make(chan struct{})}
	policy := testWorkerPolicy()
	policy.HeartbeatInterval = 5 * time.Millisecond
	worker, _ := NewWorker(repository, validContextProvider(t), model, &fakeToolSet{}, policy)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() {
		_, err := worker.RunOnce(ctx)
		done <- err
	}()
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("blocked Model did not start")
	}
	err := <-done
	if !errors.Is(err, ErrRunLeaseLost) {
		t.Fatalf("heartbeat failure error = %v", err)
	}
	run, steps := repository.snapshot()
	if run.Status != agentdomain.RunStatusProcessing || repository.requeued || repository.failCode != agentdomain.ErrorCodeNone || len(steps) != 0 {
		t.Fatalf("heartbeat failure changed state run=%+v steps=%+v", run, steps)
	}
}
