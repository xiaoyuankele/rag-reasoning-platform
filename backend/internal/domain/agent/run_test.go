package agent

import (
	"testing"
	"time"
)

func TestRunAndStepStatuses(t *testing.T) {
	for _, status := range []RunStatus{
		RunStatusQueued,
		RunStatusProcessing,
		RunStatusSucceeded,
		RunStatusFailed,
		RunStatusCanceled,
	} {
		if !status.IsValid() {
			t.Fatalf("RunStatus(%q) must be valid", status)
		}
	}
	if RunStatus("unknown").IsValid() {
		t.Fatal("unknown RunStatus must be invalid")
	}
	if !RunStatusQueued.IsActive() || !RunStatusProcessing.IsActive() || RunStatusSucceeded.IsActive() {
		t.Fatal("RunStatus active classification is incorrect")
	}
	if !RunStatusQueued.CanCancel() {
		t.Fatal("queued run must be cancelable")
	}

	for _, status := range []StepStatus{
		StepStatusPending,
		StepStatusRunning,
		StepStatusSucceeded,
		StepStatusFailed,
	} {
		if !status.IsValid() {
			t.Fatalf("StepStatus(%q) must be valid", status)
		}
	}
	if StepStatus("unknown").IsValid() {
		t.Fatal("unknown StepStatus must be invalid")
	}
}

func TestBudgetChecksBeforeNextCall(t *testing.T) {
	now := time.Now()
	budget := Budget{MaxSteps: 5, MaxTotalTokens: 48_000, DeadlineAt: now.Add(time.Minute)}
	if !budget.IsValid() || !budget.AllowsNextCall(4, 47_999, now) {
		t.Fatal("valid remaining budget must allow next call")
	}
	if budget.AllowsNextCall(5, 0, now) ||
		budget.AllowsNextCall(0, 48_000, now) ||
		budget.AllowsNextCall(-1, 0, now) ||
		budget.AllowsNextCall(0, -1, now) ||
		budget.AllowsNextCall(0, 0, now.Add(2*time.Minute)) {
		t.Fatal("exhausted budget must reject next call")
	}
	if (Budget{MaxSteps: MaxPersistedSteps + 1, MaxTotalTokens: 1, DeadlineAt: now.Add(time.Minute)}).IsValid() {
		t.Fatal("budget above the frozen step bound must be invalid")
	}
	if (Budget{MaxSteps: 1, MaxTotalTokens: MaxRunTotalTokens + 1, DeadlineAt: now.Add(time.Minute)}).IsValid() {
		t.Fatal("budget above the frozen token bound must be invalid")
	}
}

func TestQueueAndLeasePolicies(t *testing.T) {
	if !(AdmissionLimits{MaxQueuedRunsPerOwner: 1, MaxQueuedRunsGlobal: 2}).IsValid() {
		t.Fatal("valid admission limits must be accepted")
	}
	if (AdmissionLimits{MaxQueuedRunsPerOwner: 2, MaxQueuedRunsGlobal: 1}).IsValid() {
		t.Fatal("owner capacity above global capacity must be rejected")
	}
	if !(SchedulingPolicy{
		MaxInFlightPerOwner:         1,
		MaxBorrowedInFlightPerOwner: 2,
		StarvationThreshold:         time.Second,
	}).IsValid() {
		t.Fatal("valid scheduling policy must be accepted")
	}
	if !(LeasePolicy{
		WorkerID:      "agent-worker",
		LeaseDuration: time.Minute,
		MaxAttempts:   MaxAutomaticAttempts,
	}).IsValid() {
		t.Fatal("valid lease policy must be accepted")
	}
	if (LeasePolicy{
		WorkerID:      "agent-worker",
		LeaseDuration: time.Minute,
		MaxAttempts:   MaxAutomaticAttempts + 1,
	}).IsValid() {
		t.Fatal("lease policy above the frozen attempt bound must be rejected")
	}
}

func TestResultValidation(t *testing.T) {
	if !(Result{Answer: "done", ResponseLanguage: ResponseLanguageEnglish, PromptTokens: 2, CompletionTokens: 3, TotalTokens: 5}).IsValid() {
		t.Fatal("valid Result must be accepted")
	}
	if (Result{ResponseLanguage: ResponseLanguageEnglish}).IsValid() {
		t.Fatal("empty answer must be rejected")
	}
	if (Result{Answer: "done", ResponseLanguage: ResponseLanguageEnglish, PromptTokens: 2, CompletionTokens: 3, TotalTokens: 6}).IsValid() {
		t.Fatal("inconsistent token total must be rejected")
	}
	if (Result{Answer: "done", ResponseLanguage: ResponseLanguageEnglish, PromptTokens: 0, CompletionTokens: 0, TotalTokens: -1}).IsValid() {
		t.Fatal("negative token total must be rejected")
	}
	if (Result{Answer: "done", ResponseLanguage: ResponseLanguageAuto}).IsValid() {
		t.Fatal("successful Result must reject unresolved auto language")
	}
	if !ResponseLanguageChinese.IsResolved() || !ResponseLanguageEnglish.IsResolved() || ResponseLanguageAuto.IsResolved() {
		t.Fatal("resolved response language classification is incorrect")
	}
}
