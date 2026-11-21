package document

import (
	"context"
	"errors"
	"fmt"

	accessdomain "rag-reasoning-platform/backend/internal/domain/access"
	documentdomain "rag-reasoning-platform/backend/internal/domain/document"
)

const (
	// MaxProcessingJobLookupDocumentCount 限制一次状态恢复查询的文档数量。
	MaxProcessingJobLookupDocumentCount = 100
)

var (
	// ErrEmptyProcessingJobLookup 表示状态恢复请求没有提供文档 ID。
	ErrEmptyProcessingJobLookup = errors.New(
		"document_ids must contain at least one document ID",
	)

	// ErrProcessingJobLookupTooLarge 表示一次状态恢复请求超过100份文档。
	ErrProcessingJobLookupTooLarge = errors.New(
		"document_ids must contain at most 100 document IDs",
	)

	// ErrInvalidProcessingJobDocumentID 表示查询列表中存在非正整数文档 ID。
	ErrInvalidProcessingJobDocumentID = errors.New(
		"every document ID must be a positive integer",
	)
)

// LatestProcessingJobItem 表示一份文档的最新解析任务快照。
// Job=nil 同时覆盖文档不可见和该文档从未创建解析任务两种情况。
type LatestProcessingJobItem struct {
	DocumentID int64
	Job        *documentdomain.ProcessingJob
}

// LatestProcessingJobsOutput 按调用者第一次提供文档 ID 的顺序返回结果。
type LatestProcessingJobsOutput struct {
	Items []LatestProcessingJobItem
}

// ProcessingJobLatestService 编排批量恢复解析任务状态的查询用例。
type ProcessingJobLatestService struct {
	jobs documentdomain.ScopedLatestProcessingJobFinder
}

// NewProcessingJobLatestService 创建批量解析任务状态查询服务。
func NewProcessingJobLatestService(
	jobs documentdomain.ScopedLatestProcessingJobFinder,
) *ProcessingJobLatestService {
	return &ProcessingJobLatestService{jobs: jobs}
}

// GetLatestByDocumentIDs 校验、去重并查询每份文档最新的解析任务。
func (s *ProcessingJobLatestService) GetLatestByDocumentIDs(
	ctx context.Context,
	scope accessdomain.OwnerScope,
	documentIDs []int64,
) (LatestProcessingJobsOutput, error) {
	if len(documentIDs) == 0 {
		return LatestProcessingJobsOutput{}, ErrEmptyProcessingJobLookup
	}
	if len(documentIDs) > MaxProcessingJobLookupDocumentCount {
		return LatestProcessingJobsOutput{}, ErrProcessingJobLookupTooLarge
	}
	for _, documentID := range documentIDs {
		if documentID <= 0 {
			return LatestProcessingJobsOutput{},
				ErrInvalidProcessingJobDocumentID
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

	foundJobs, err := s.jobs.FindLatestProcessingJobsByDocumentIDs(
		ctx,
		scope,
		uniqueDocumentIDs,
	)
	if err != nil {
		return LatestProcessingJobsOutput{}, fmt.Errorf(
			"find latest processing jobs by document IDs: %w",
			err,
		)
	}

	jobsByDocumentID := make(
		map[int64]documentdomain.ProcessingJob,
		len(foundJobs),
	)
	for _, job := range foundJobs {
		jobsByDocumentID[job.DocumentID] = job
	}

	items := make([]LatestProcessingJobItem, 0, len(uniqueDocumentIDs))
	for _, documentID := range uniqueDocumentIDs {
		item := LatestProcessingJobItem{DocumentID: documentID}
		if foundJob, ok := jobsByDocumentID[documentID]; ok {
			jobCopy := foundJob
			item.Job = &jobCopy
		}
		items = append(items, item)
	}

	return LatestProcessingJobsOutput{Items: items}, nil
}
