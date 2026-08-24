package answer

import (
	"context"
	"errors"
	"testing"

	embeddingapplication "rag-reasoning-platform/backend/internal/application/embedding"
	accessdomain "rag-reasoning-platform/backend/internal/domain/access"
)

type fakeScopedAnswerJobRepository struct {
	createdInput Input
	createdScope accessdomain.OwnerScope
	createResult Job
	createErr    error
	getResult    Job
	getErr       error
	cancelResult Job
	cancelErr    error
	callCount    int
}

func (f *fakeScopedAnswerJobRepository) CreateAnswerJob(
	_ context.Context,
	scope accessdomain.OwnerScope,
	input Input,
) (Job, error) {
	f.callCount++
	f.createdScope = scope
	f.createdInput = input
	return f.createResult, f.createErr
}

func (f *fakeScopedAnswerJobRepository) GetAnswerJobByID(
	context.Context,
	accessdomain.OwnerScope,
	int64,
) (Job, error) {
	f.callCount++
	return f.getResult, f.getErr
}

func (f *fakeScopedAnswerJobRepository) CancelAnswerJob(
	context.Context,
	accessdomain.OwnerScope,
	int64,
) (Job, error) {
	f.callCount++
	return f.cancelResult, f.cancelErr
}

func TestJobServiceQueueNormalizesInputBeforePersistence(t *testing.T) {
	documentID := int64(9)
	repository := &fakeScopedAnswerJobRepository{
		createResult: Job{ID: 7, Status: JobStatusQueued},
	}
	service, err := NewJobService(repository)
	if err != nil {
		t.Fatalf("NewJobService() error = %v", err)
	}

	job, err := service.Queue(t.Context(), testAnswerOwnerScope(t), Input{
		Query:            "  磁悬浮如何控制？  ",
		DocumentID:       &documentID,
		TopK:             5,
		ResponseLanguage: " ZH ",
	})
	if err != nil || job.ID != 7 {
		t.Fatalf("Queue() = %+v, %v", job, err)
	}
	if repository.createdInput.Query != "磁悬浮如何控制？" ||
		repository.createdInput.ResponseLanguage != ResponseLanguageChinese ||
		repository.createdInput.DocumentID == nil ||
		*repository.createdInput.DocumentID != documentID ||
		repository.createdScope.OwnerUserID() != testAnswerOwnerUserID {
		t.Fatalf("persisted input = %+v, scope = %+v", repository.createdInput, repository.createdScope)
	}
}

func TestJobServiceQueueRejectsInvalidInputBeforePersistence(t *testing.T) {
	testCases := []struct {
		name  string
		input Input
		want  error
	}{
		{name: "blank query", input: Input{Query: " ", TopK: 5}, want: embeddingapplication.ErrSemanticSearchQueryRequired},
		{name: "invalid top k", input: Input{Query: "question", TopK: 0}, want: embeddingapplication.ErrInvalidSemanticSearchTopK},
		{name: "invalid language", input: Input{Query: "question", TopK: 5, ResponseLanguage: "ja"}, want: ErrInvalidResponseLanguage},
	}

	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			repository := &fakeScopedAnswerJobRepository{}
			service, _ := NewJobService(repository)
			_, err := service.Queue(t.Context(), testAnswerOwnerScope(t), testCase.input)
			if !errors.Is(err, testCase.want) {
				t.Fatalf("Queue() error = %v, want %v", err, testCase.want)
			}
			if repository.callCount != 0 {
				t.Fatalf("repository calls = %d, want 0", repository.callCount)
			}
		})
	}
}

func TestJobServiceGetAndCancelValidateJobID(t *testing.T) {
	repository := &fakeScopedAnswerJobRepository{}
	service, _ := NewJobService(repository)

	if _, err := service.GetByID(t.Context(), testAnswerOwnerScope(t), 0); !errors.Is(err, ErrInvalidAnswerJobID) {
		t.Fatalf("GetByID() error = %v", err)
	}
	if _, err := service.Cancel(t.Context(), testAnswerOwnerScope(t), -1); !errors.Is(err, ErrInvalidAnswerJobID) {
		t.Fatalf("Cancel() error = %v", err)
	}
	if repository.callCount != 0 {
		t.Fatalf("repository calls = %d, want 0", repository.callCount)
	}
}
