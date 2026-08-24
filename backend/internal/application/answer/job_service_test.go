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
	listScope    accessdomain.OwnerScope
	listOptions  JobListOptions
	listResult   JobListResult
	listErr      error
	cancelResult Job
	cancelErr    error
	callCount    int
}

func (f *fakeScopedAnswerJobRepository) ListAnswerJobs(
	_ context.Context,
	scope accessdomain.OwnerScope,
	options JobListOptions,
) (JobListResult, error) {
	f.callCount++
	f.listScope = scope
	f.listOptions = options
	return f.listResult, f.listErr
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

func TestJobServiceListCalculatesPagination(t *testing.T) {
	repository := &fakeScopedAnswerJobRepository{
		listResult: JobListResult{
			Jobs:  []Job{{ID: 3}, {ID: 2}},
			Total: 41,
		},
	}
	service, _ := NewJobService(repository)
	scope := testAnswerOwnerScope(t)

	result, err := service.List(t.Context(), scope, JobListInput{
		Page:     2,
		PageSize: 20,
	})
	if err != nil {
		t.Fatalf("List() error = %v", err)
	}
	if repository.listScope.OwnerUserID() != scope.OwnerUserID() ||
		repository.listOptions.Limit != 20 || repository.listOptions.Offset != 20 {
		t.Fatalf(
			"repository input = scope %d, %+v; want scope %d, limit 20, offset 20",
			repository.listScope.OwnerUserID(),
			repository.listOptions,
			scope.OwnerUserID(),
		)
	}
	if len(result.Jobs) != 2 || result.Total != 41 || result.TotalPages != 3 ||
		result.Page != 2 || result.PageSize != 20 {
		t.Fatalf("List() result = %+v, want page 2 of 3", result)
	}
}

func TestJobServiceListRejectsInvalidPaginationBeforePersistence(t *testing.T) {
	testCases := []struct {
		name  string
		input JobListInput
		want  error
	}{
		{name: "zero page", input: JobListInput{Page: 0, PageSize: 20}, want: ErrInvalidAnswerJobPage},
		{name: "zero page size", input: JobListInput{Page: 1, PageSize: 0}, want: ErrInvalidAnswerJobPageSize},
		{name: "oversized page", input: JobListInput{Page: 1, PageSize: 101}, want: ErrInvalidAnswerJobPageSize},
		{name: "offset overflow", input: JobListInput{Page: 1<<62 + 1, PageSize: 20}, want: ErrInvalidAnswerJobPage},
	}

	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			repository := &fakeScopedAnswerJobRepository{}
			service, _ := NewJobService(repository)
			_, err := service.List(t.Context(), testAnswerOwnerScope(t), testCase.input)
			if !errors.Is(err, testCase.want) {
				t.Fatalf("List() error = %v, want %v", err, testCase.want)
			}
			if repository.callCount != 0 {
				t.Fatalf("repository calls = %d, want 0", repository.callCount)
			}
		})
	}
}

func TestJobServiceListReturnsNonNilEmptyJobs(t *testing.T) {
	repository := &fakeScopedAnswerJobRepository{
		listResult: JobListResult{Jobs: nil, Total: 0},
	}
	service, _ := NewJobService(repository)

	result, err := service.List(t.Context(), testAnswerOwnerScope(t), JobListInput{
		Page:     1,
		PageSize: 20,
	})
	if err != nil {
		t.Fatalf("List() error = %v", err)
	}
	if result.Jobs == nil || len(result.Jobs) != 0 || result.TotalPages != 0 {
		t.Fatalf("List() result = %+v, want non-nil empty jobs and zero pages", result)
	}
}
