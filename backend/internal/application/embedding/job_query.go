package embedding

import (
	"context"
	"errors"
	"fmt"

	embeddingdomain "rag-reasoning-platform/backend/internal/domain/embedding"
)

// ErrInvalidEmbeddingJobID 表示调用者提供的向量任务 ID 不是正整数。
var ErrInvalidEmbeddingJobID = errors.New("embedding job ID must be positive")

// JobQueryService 编排按照 ID 查询向量任务的应用用例。
type JobQueryService struct {
	jobs embeddingdomain.JobFinder
}

// NewJobQueryService 创建向量任务查询服务。
func NewJobQueryService(
	jobs embeddingdomain.JobFinder,
) *JobQueryService {
	return &JobQueryService{jobs: jobs}
}

// GetByID 校验业务参数，再通过领域端口查询向量任务。
func (s *JobQueryService) GetByID(
	ctx context.Context,
	jobID int64,
) (embeddingdomain.Job, error) {
	if jobID <= 0 {
		return embeddingdomain.Job{}, ErrInvalidEmbeddingJobID
	}

	foundJob, err := s.jobs.GetEmbeddingJobByID(ctx, jobID)
	if err != nil {
		return embeddingdomain.Job{}, fmt.Errorf(
			"get embedding job by ID: %w",
			err,
		)
	}

	return foundJob, nil
}
