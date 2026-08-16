package document

import (
	"context"
	"errors"
	"fmt"

	accessdomain "rag-reasoning-platform/backend/internal/domain/access"
	documentdomain "rag-reasoning-platform/backend/internal/domain/document"
)

// ErrInvalidProcessingJobID 表示解析任务 ID 不是正整数。
var ErrInvalidProcessingJobID = errors.New(
	"document processing job ID must be positive",
)

// ProcessingJobService 提供解析任务查询用例。
type ProcessingJobService struct {
	jobs documentdomain.ScopedProcessingJobFinder
}

// NewProcessingJobService 创建解析任务查询服务。
func NewProcessingJobService(
	jobs documentdomain.ScopedProcessingJobFinder,
) *ProcessingJobService {
	return &ProcessingJobService{
		jobs: jobs,
	}
}

// GetByID 校验任务 ID，并查询解析任务。
func (s *ProcessingJobService) GetByID(
	ctx context.Context,
	scope accessdomain.OwnerScope,
	jobID int64,
) (documentdomain.ProcessingJob, error) {
	if jobID <= 0 {
		return documentdomain.ProcessingJob{}, ErrInvalidProcessingJobID
	}

	foundJob, err := s.jobs.GetProcessingJobByID(ctx, scope, jobID)
	if err != nil {
		return documentdomain.ProcessingJob{}, fmt.Errorf(
			"get processing job by ID: %w",
			err,
		)
	}

	return foundJob, nil
}
