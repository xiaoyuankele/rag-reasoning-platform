// Package embedding 编排文本向量化相关的应用用例。
package embedding

import (
	"context"
	"errors"
	"fmt"

	accessdomain "rag-reasoning-platform/backend/internal/domain/access"
	documentdomain "rag-reasoning-platform/backend/internal/domain/document"
	embeddingdomain "rag-reasoning-platform/backend/internal/domain/embedding"
)

var (
	// ErrInvalidDocumentID 表示文档 ID 不是正整数。
	ErrInvalidDocumentID = errors.New("document ID must be a positive integer")

	// ErrDocumentNotReady 表示文档还没有完成文本提取和分块。
	ErrDocumentNotReady = errors.New("document is not ready for embedding")
)

// QueueService 编排“检查文档并创建向量任务”的用例。
type QueueService struct {
	documents  documentdomain.ScopedFinder
	jobs       embeddingdomain.ScopedJobCreator
	modelName  string
	dimensions int
}

// NewQueueService 创建向量任务排队服务。
func NewQueueService(
	documents documentdomain.ScopedFinder,
	jobs embeddingdomain.ScopedJobCreator,
	modelName string,
	dimensions int,
) *QueueService {
	return &QueueService{
		documents:  documents,
		jobs:       jobs,
		modelName:  modelName,
		dimensions: dimensions,
	}
}

// Queue 为已经完成文本处理的文档创建 queued 向量任务。
//
// documents.status 只描述原始文件到文本块的生命周期；本方法不会修改它。
// 真正执行向量化时，Embedding Worker 只更新独立的 embedding_jobs 状态。
func (s *QueueService) Queue(
	ctx context.Context,
	scope accessdomain.OwnerScope,
	documentID int64,
) (embeddingdomain.Job, error) {
	if documentID <= 0 {
		return embeddingdomain.Job{}, ErrInvalidDocumentID
	}

	foundDocument, err := s.documents.GetByID(ctx, scope, documentID)
	if err != nil {
		return embeddingdomain.Job{}, fmt.Errorf(
			"get document before queuing embedding: %w",
			err,
		)
	}

	if foundDocument.Status != documentdomain.StatusReady {
		return embeddingdomain.Job{}, ErrDocumentNotReady
	}

	createdJob, err := s.jobs.CreateEmbeddingJob(
		ctx,
		scope,
		foundDocument.ID,
		s.modelName,
		s.dimensions,
	)
	if err != nil {
		return embeddingdomain.Job{}, fmt.Errorf(
			"create embedding job: %w",
			err,
		)
	}

	return createdJob, nil
}
