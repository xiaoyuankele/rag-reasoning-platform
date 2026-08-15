package answer

import (
	"context"
	"errors"
	"testing"

	documentdomain "rag-reasoning-platform/backend/internal/domain/document"
	generationdomain "rag-reasoning-platform/backend/internal/domain/generation"
)

type recordingGenerationEventObserver struct {
	events []GenerationEvent
}

func newRecordingGenerationEventObserver() *recordingGenerationEventObserver {
	return &recordingGenerationEventObserver{
		events: make([]GenerationEvent, 0, 2),
	}
}

// ObserveGenerationEvent 实现测试用事件观察器，只在内存中保留事件。
func (o *recordingGenerationEventObserver) ObserveGenerationEvent(
	_ context.Context,
	event GenerationEvent,
) {
	o.events = append(o.events, event)
}

func TestServiceObservesSkippedGenerationWithoutEvidence(t *testing.T) {
	observer := newRecordingGenerationEventObserver()
	service, err := NewService(
		successfulSearcher(nil),
		successfulGenerator(),
		observer,
		"test-generation-model",
		512,
		0.1,
	)
	if err != nil {
		t.Fatalf("NewService() error = %v, want nil", err)
	}

	_, err = service.Answer(context.Background(), Input{
		Query:            "No evidence question",
		TopK:             5,
		ResponseLanguage: ResponseLanguageEnglish,
	})
	if err != nil {
		t.Fatalf("Answer() error = %v, want nil", err)
	}

	if len(observer.events) != 1 {
		t.Fatalf("event count = %d, want 1", len(observer.events))
	}
	event := observer.events[0]
	if event.Type != GenerationEventSkipped ||
		event.SkipReason != GenerationSkipReasonInsufficientEvidence ||
		event.EvidenceCount != 0 || event.RequestedTopK != 5 {
		t.Fatalf("event = %+v, want insufficient-evidence skipped event", event)
	}
}

func TestServiceObservesSuccessfulGenerationAndTokenUsage(t *testing.T) {
	observer := newRecordingGenerationEventObserver()
	documentID := int64(42)
	service, err := NewService(
		successfulSearcher([]documentdomain.SemanticSearchHit{{
			ChunkID:    11,
			DocumentID: documentID,
			Content:    "grounded evidence",
		}}),
		&fakeGenerator{generateFunc: func(
			context.Context,
			generationdomain.GenerateRequest,
		) (generationdomain.GenerateResult, error) {
			return generationdomain.GenerateResult{
				Text:             "answer [1]",
				PromptTokens:     80,
				CompletionTokens: 12,
				TotalTokens:      92,
			}, nil
		}},
		observer,
		"test-generation-model",
		512,
		0.1,
	)
	if err != nil {
		t.Fatalf("NewService() error = %v, want nil", err)
	}

	_, err = service.Answer(context.Background(), Input{
		Query:      "question",
		DocumentID: &documentID,
		TopK:       5,
	})
	if err != nil {
		t.Fatalf("Answer() error = %v, want nil", err)
	}

	if len(observer.events) != 2 {
		t.Fatalf("event count = %d, want 2", len(observer.events))
	}
	if observer.events[0].Type != GenerationEventStarted {
		t.Fatalf("first event = %q, want %q", observer.events[0].Type, GenerationEventStarted)
	}
	succeeded := observer.events[1]
	if succeeded.Type != GenerationEventSucceeded ||
		succeeded.DocumentID == nil || *succeeded.DocumentID != documentID ||
		succeeded.EvidenceCount != 1 ||
		succeeded.PromptTokens != 80 ||
		succeeded.CompletionTokens != 12 ||
		succeeded.TotalTokens != 92 {
		t.Fatalf("succeeded event = %+v, want call metadata and token usage", succeeded)
	}
}

func TestServiceObservesClassifiedGenerationFailure(t *testing.T) {
	observer := newRecordingGenerationEventObserver()
	providerError := fmtGenerationTestError(generationdomain.ErrGenerationRateLimited)
	service, err := NewService(
		successfulSearcher([]documentdomain.SemanticSearchHit{{Content: "evidence"}}),
		&fakeGenerator{generateFunc: func(
			context.Context,
			generationdomain.GenerateRequest,
		) (generationdomain.GenerateResult, error) {
			return generationdomain.GenerateResult{}, providerError
		}},
		observer,
		"test-generation-model",
		512,
		0.1,
	)
	if err != nil {
		t.Fatalf("NewService() error = %v, want nil", err)
	}

	_, err = service.Answer(context.Background(), Input{Query: "question", TopK: 5})
	if !errors.Is(err, generationdomain.ErrGenerationRateLimited) {
		t.Fatalf("Answer() error = %v, want wrapped rate-limit error", err)
	}

	if len(observer.events) != 2 {
		t.Fatalf("event count = %d, want 2", len(observer.events))
	}
	failed := observer.events[1]
	if failed.Type != GenerationEventFailed ||
		failed.ErrorCategory != GenerationErrorCategoryProviderRateLimit ||
		!errors.Is(failed.Err, generationdomain.ErrGenerationRateLimited) {
		t.Fatalf("failed event = %+v, want classified provider error", failed)
	}
}

func fmtGenerationTestError(err error) error {
	return errors.Join(errors.New("provider call failed"), err)
}

func TestClassifyGenerationError(t *testing.T) {
	testCases := []struct {
		name string
		err  error
		want GenerationErrorCategory
	}{
		{name: "authentication", err: generationdomain.ErrGenerationAuthentication, want: GenerationErrorCategoryProviderAuthentication},
		{name: "quota", err: generationdomain.ErrGenerationQuotaExceeded, want: GenerationErrorCategoryProviderQuota},
		{name: "rate limit", err: generationdomain.ErrGenerationRateLimited, want: GenerationErrorCategoryProviderRateLimit},
		{name: "request", err: generationdomain.ErrGenerationRequestRejected, want: GenerationErrorCategoryProviderRequest},
		{name: "unavailable", err: generationdomain.ErrGenerationUnavailable, want: GenerationErrorCategoryProviderUnavailable},
		{name: "response", err: generationdomain.ErrInvalidGenerationResponse, want: GenerationErrorCategoryProviderResponse},
		{name: "timeout", err: context.DeadlineExceeded, want: GenerationErrorCategoryTimeout},
		{name: "canceled", err: context.Canceled, want: GenerationErrorCategoryCanceled},
		{name: "internal", err: errors.New("unexpected"), want: GenerationErrorCategoryInternal},
	}

	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			if got := classifyGenerationError(testCase.err); got != testCase.want {
				t.Fatalf("classifyGenerationError() = %q, want %q", got, testCase.want)
			}
		})
	}
}
