package answer

import (
	"context"
	"errors"
	"fmt"

	embeddingapplication "rag-reasoning-platform/backend/internal/application/embedding"
	accessdomain "rag-reasoning-platform/backend/internal/domain/access"
)

var (
	// ErrAnswerJobServiceDependencies 表示异步任务服务缺少持久化端口。
	ErrAnswerJobServiceDependencies = errors.New(
		"answer job service dependencies must be provided",
	)

	// ErrInvalidAnswerJobID 表示路径中的任务 ID 不是正整数。
	ErrInvalidAnswerJobID = errors.New("answer job ID must be positive")
)

// JobService 编排当前用户创建、查询和取消异步问答任务。
type JobService struct {
	jobs ScopedJobRepository
}

// NewJobService 创建异步问答任务应用服务。
func NewJobService(jobs ScopedJobRepository) (*JobService, error) {
	if jobs == nil {
		return nil, ErrAnswerJobServiceDependencies
	}
	return &JobService{jobs: jobs}, nil
}

// Queue 先完成零费用输入校验，再把规范化后的任务交给 Repository。
func (s *JobService) Queue(
	ctx context.Context,
	scope accessdomain.OwnerScope,
	input Input,
) (Job, error) {
	query, err := embeddingapplication.ValidateSemanticSearchInput(
		embeddingapplication.SemanticSearchInput{
			Query:      input.Query,
			DocumentID: input.DocumentID,
			TopK:       input.TopK,
		},
	)
	if err != nil {
		return Job{}, err
	}
	language, err := normalizeResponseLanguagePreference(input.ResponseLanguage)
	if err != nil {
		return Job{}, err
	}

	input.Query = query
	input.ResponseLanguage = language
	job, err := s.jobs.CreateAnswerJob(ctx, scope, input)
	if err != nil {
		return Job{}, fmt.Errorf("create answer job: %w", err)
	}
	return job, nil
}

// GetByID 查询当前用户可见的任务快照。
func (s *JobService) GetByID(
	ctx context.Context,
	scope accessdomain.OwnerScope,
	jobID int64,
) (Job, error) {
	if jobID <= 0 {
		return Job{}, ErrInvalidAnswerJobID
	}
	job, err := s.jobs.GetAnswerJobByID(ctx, scope, jobID)
	if err != nil {
		return Job{}, fmt.Errorf("get answer job: %w", err)
	}
	return job, nil
}

// Cancel 原子取消 queued 任务；processing 和终态由 Repository 返回稳定错误。
func (s *JobService) Cancel(
	ctx context.Context,
	scope accessdomain.OwnerScope,
	jobID int64,
) (Job, error) {
	if jobID <= 0 {
		return Job{}, ErrInvalidAnswerJobID
	}
	job, err := s.jobs.CancelAnswerJob(ctx, scope, jobID)
	if err != nil {
		return Job{}, fmt.Errorf("cancel answer job: %w", err)
	}
	return job, nil
}
