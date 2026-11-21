package embedding

import (
	"context"
	"errors"
	"fmt"

	accessdomain "rag-reasoning-platform/backend/internal/domain/access"
	embeddingdomain "rag-reasoning-platform/backend/internal/domain/embedding"
)

var (
	// ErrInvalidEmbeddingJobID 表示调用者提供的向量任务 ID 不是正整数。
	ErrInvalidEmbeddingJobID = errors.New("embedding job ID must be positive")

	// ErrEmptyEmbeddingJobLookup 表示批量发现请求没有文档 ID。
	ErrEmptyEmbeddingJobLookup = errors.New("document_ids must contain at least one document ID")

	// ErrEmbeddingJobLookupTooLarge 表示一次发现请求超过 100 份文档。
	ErrEmbeddingJobLookupTooLarge = errors.New("document_ids must contain at most 100 document IDs")
)

// LatestJobItem 表示一份文档的最新向量任务快照。
// Job=nil 同时覆盖当前用户看不见该文档以及该文档从未创建任务两种情况。
type LatestJobItem struct {
	DocumentID int64
	Job        *embeddingdomain.Job
}

// LatestJobsOutput 按调用者第一次提供文档 ID 的顺序返回查询结果。
type LatestJobsOutput struct {
	Items []LatestJobItem
}

// JobQueryService 编排按照 ID 查询向量任务的应用用例。
type JobQueryService struct {
	jobs       embeddingdomain.ScopedJobFinder
	latestJobs embeddingdomain.ScopedLatestJobFinder
}

// NewJobQueryService 创建向量任务查询服务。
func NewJobQueryService(
	jobs interface {
		embeddingdomain.ScopedJobFinder
		embeddingdomain.ScopedLatestJobFinder
	},
) *JobQueryService {
	return &JobQueryService{jobs: jobs, latestJobs: jobs}
}

// GetLatestByDocumentIDs 一次恢复多份文档的最近向量任务状态。
//
// 输入先整体校验，再去重并保持第一次出现顺序。Repository 只返回实际可见
// 的任务，Application 负责为其余 ID 补齐 job=nil，形成稳定的一一对应契约。
func (s *JobQueryService) GetLatestByDocumentIDs(
	ctx context.Context,
	scope accessdomain.OwnerScope,
	documentIDs []int64,
) (LatestJobsOutput, error) {
	if len(documentIDs) == 0 {
		return LatestJobsOutput{}, ErrEmptyEmbeddingJobLookup
	}
	if len(documentIDs) > MaxEmbeddingBatchDocumentCount {
		return LatestJobsOutput{}, ErrEmbeddingJobLookupTooLarge
	}
	for _, documentID := range documentIDs {
		if documentID <= 0 {
			return LatestJobsOutput{}, ErrInvalidDocumentID
		}
	}

	uniqueDocumentIDs := make([]int64, 0, len(documentIDs))
	seen := make(map[int64]struct{}, len(documentIDs))
	for _, documentID := range documentIDs {
		if _, duplicated := seen[documentID]; duplicated {
			continue
		}
		seen[documentID] = struct{}{}
		uniqueDocumentIDs = append(uniqueDocumentIDs, documentID)
	}

	foundJobs, err := s.latestJobs.FindLatestEmbeddingJobsByDocumentIDs(
		ctx,
		scope,
		uniqueDocumentIDs,
	)
	if err != nil {
		return LatestJobsOutput{}, fmt.Errorf("find latest embedding jobs by document IDs: %w", err)
	}

	jobsByDocumentID := make(map[int64]embeddingdomain.Job, len(foundJobs))
	for _, job := range foundJobs {
		jobsByDocumentID[job.DocumentID] = job
	}

	items := make([]LatestJobItem, 0, len(uniqueDocumentIDs))
	for _, documentID := range uniqueDocumentIDs {
		item := LatestJobItem{DocumentID: documentID}
		if foundJob, ok := jobsByDocumentID[documentID]; ok {
			jobCopy := foundJob
			item.Job = &jobCopy
		}
		items = append(items, item)
	}

	return LatestJobsOutput{Items: items}, nil
}

// GetByID 校验业务参数，再通过领域端口查询向量任务。
func (s *JobQueryService) GetByID(
	ctx context.Context,
	scope accessdomain.OwnerScope,
	jobID int64,
) (embeddingdomain.Job, error) {
	if jobID <= 0 {
		return embeddingdomain.Job{}, ErrInvalidEmbeddingJobID
	}

	foundJob, err := s.jobs.GetEmbeddingJobByID(ctx, scope, jobID)
	if err != nil {
		return embeddingdomain.Job{}, fmt.Errorf(
			"get embedding job by ID: %w",
			err,
		)
	}

	return foundJob, nil
}
