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
	activeScope  accessdomain.OwnerScope
	activeID     int64
	activeResult Job
	activeErr    error
	latestScope  accessdomain.OwnerScope
	latestID     int64
	latestResult Job
	latestErr    error
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

func (f *fakeScopedAnswerJobRepository) GetAnswerJobByUserMessageID(
	context.Context,
	accessdomain.OwnerScope,
	int64,
) (Job, error) {
	f.callCount++
	return f.getResult, f.getErr
}

func (f *fakeScopedAnswerJobRepository) GetActiveAnswerJobByConversationID(
	_ context.Context,
	scope accessdomain.OwnerScope,
	conversationID int64,
) (Job, error) {
	f.callCount++
	f.activeScope = scope
	f.activeID = conversationID
	return f.activeResult, f.activeErr
}

func (f *fakeScopedAnswerJobRepository) GetLatestAnswerJobByConversationID(
	_ context.Context,
	scope accessdomain.OwnerScope,
	conversationID int64,
) (Job, error) {
	f.callCount++
	f.latestScope = scope
	f.latestID = conversationID
	return f.latestResult, f.latestErr
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

func TestJobServiceQueueConversationReturnsExistingTerminalJob(t *testing.T) {
	repository := &fakeScopedAnswerJobRepository{
		getResult: Job{ID: 19, Status: JobStatusFailed},
	}
	service, _ := NewJobService(repository)

	job, err := service.QueueConversation(t.Context(), testAnswerOwnerScope(t), ConversationQueueInput{
		ConversationID: 7, UserMessageID: 8, AssistantMessageID: 9,
		Input: Input{Query: "question", TopK: 5},
	})
	if err != nil || job.ID != 19 || job.Status != JobStatusFailed {
		t.Fatalf("QueueConversation() = %+v, %v", job, err)
	}
	if repository.callCount != 1 || repository.createdInput.Query != "" {
		t.Fatalf("repository calls/input = %d / %+v, want existing lookup only", repository.callCount, repository.createdInput)
	}
}

func TestJobServiceQueueConversationCreatesWhenNoJobExists(t *testing.T) {
	repository := &fakeScopedAnswerJobRepository{
		getErr:       ErrAnswerJobNotFound,
		createResult: Job{ID: 20, Status: JobStatusQueued},
	}
	service, _ := NewJobService(repository)

	job, err := service.QueueConversation(t.Context(), testAnswerOwnerScope(t), ConversationQueueInput{
		ConversationID: 7, UserMessageID: 8, AssistantMessageID: 9,
		Input: Input{Query: "question", TopK: 5},
	})
	if err != nil || job.ID != 20 {
		t.Fatalf("QueueConversation() = %+v, %v", job, err)
	}
	if repository.callCount != 2 || repository.createdInput.ConversationID == nil ||
		*repository.createdInput.ConversationID != 7 || repository.createdInput.UserMessageID == nil ||
		*repository.createdInput.UserMessageID != 8 || repository.createdInput.AssistantMessageID == nil ||
		*repository.createdInput.AssistantMessageID != 9 {
		t.Fatalf("repository calls/input = %d / %+v", repository.callCount, repository.createdInput)
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
	if _, err := service.GetActiveConversationJob(t.Context(), testAnswerOwnerScope(t), 0); !errors.Is(err, ErrInvalidAnswerJobID) {
		t.Fatalf("GetActiveConversationJob() error = %v", err)
	}
	if _, err := service.GetLatestConversationJob(t.Context(), testAnswerOwnerScope(t), 0); !errors.Is(err, ErrInvalidAnswerJobID) {
		t.Fatalf("GetLatestConversationJob() error = %v", err)
	}
	if repository.callCount != 0 {
		t.Fatalf("repository calls = %d, want 0", repository.callCount)
	}
}

func TestJobServiceGetsLatestConversationJobWithinScope(t *testing.T) {
	repository := &fakeScopedAnswerJobRepository{
		latestResult: Job{ID: 18, Status: JobStatusSucceeded},
	}
	service, _ := NewJobService(repository)
	scope := testAnswerOwnerScope(t)

	job, err := service.GetLatestConversationJob(t.Context(), scope, 24)
	if err != nil {
		t.Fatalf("GetLatestConversationJob() error = %v", err)
	}
	if job.ID != 18 || repository.latestID != 24 ||
		repository.latestScope.OwnerUserID() != scope.OwnerUserID() {
		t.Fatalf("latest job/scope = %+v / %+v, want job 18, conversation 24", job, repository.latestScope)
	}
}

func TestJobServiceGetsActiveConversationJobWithinScope(t *testing.T) {
	repository := &fakeScopedAnswerJobRepository{
		activeResult: Job{ID: 17, Status: JobStatusProcessing},
	}
	service, _ := NewJobService(repository)
	scope := testAnswerOwnerScope(t)

	job, err := service.GetActiveConversationJob(t.Context(), scope, 23)
	if err != nil {
		t.Fatalf("GetActiveConversationJob() error = %v", err)
	}
	if job.ID != 17 || repository.activeID != 23 ||
		repository.activeScope.OwnerUserID() != scope.OwnerUserID() {
		t.Fatalf("active job/scope = %+v / %+v, want job 17, conversation 23", job, repository.activeScope)
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
