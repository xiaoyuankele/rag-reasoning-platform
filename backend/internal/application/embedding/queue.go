// Package embedding 编排文本向量化相关的应用用例。
package embedding

import (
	"context"
	"errors"
	"fmt"

	accessdomain "rag-reasoning-platform/backend/internal/domain/access"
	embeddingdomain "rag-reasoning-platform/backend/internal/domain/embedding"
)

var (
	// ErrInvalidDocumentID 表示文档 ID 不是正整数。
	ErrInvalidDocumentID = errors.New("document ID must be a positive integer")

	// ErrEmptyEmbeddingBatch 表示批量申请没有包含任何文档。
	ErrEmptyEmbeddingBatch = errors.New("document_ids must contain at least one document ID")

	// ErrEmbeddingBatchTooLarge 表示单次批量申请超过保护上限。
	ErrEmbeddingBatchTooLarge = errors.New("document_ids must contain at most 100 document IDs")
)

// MaxEmbeddingBatchDocumentCount 限制单个请求最多申请多少份文档。
// 它与 Worker 每次发给远程 API 的 chunk batch size 是两个不同概念。
const MaxEmbeddingBatchDocumentCount = 100

// BatchQueueItem 保存一份文档在批量申请中的独立结果。
// Err 只影响当前 DocumentID，其他文件仍然继续申请。
type BatchQueueItem struct {
	DocumentID int64
	Result     embeddingdomain.JobRequestResult
	Err        error
}

// BatchQueueOutput 是批量申请的应用层输出。
type BatchQueueOutput struct {
	Items []BatchQueueItem
}

// QueueService 编排“保存文档向量化意图”的用例。
type QueueService struct {
	jobs       embeddingdomain.ScopedJobRequester
	modelName  string
	dimensions int
}

// NewQueueService 创建向量任务排队服务。
func NewQueueService(
	jobs embeddingdomain.ScopedJobRequester,
	modelName string,
	dimensions int,
) *QueueService {
	return &QueueService{
		jobs:       jobs,
		modelName:  modelName,
		dimensions: dimensions,
	}
}

// Queue 保存当前用户对文档的向量化意图。
//
// 文档已经 ready 时，仓储返回 queued 任务；文档仍在上传、解析或解析失败时，
// 仓储返回 waiting_document 任务。状态选择必须和文档行锁位于同一事务，避免
// “解析刚完成但向量任务永远等待”的竞态。
func (s *QueueService) Queue(
	ctx context.Context,
	scope accessdomain.OwnerScope,
	documentID int64,
) (embeddingdomain.JobRequestResult, error) {
	if documentID <= 0 {
		return embeddingdomain.JobRequestResult{}, ErrInvalidDocumentID
	}

	result, err := s.jobs.RequestEmbeddingJob(
		ctx,
		scope,
		documentID,
		s.modelName,
		s.dimensions,
	)
	if err != nil {
		return embeddingdomain.JobRequestResult{}, fmt.Errorf(
			"request embedding job: %w",
			err,
		)
	}

	return result, nil
}

// QueueBatch 按文件逐一保存向量化意图。
//
// 输入先去重并保持第一次出现的顺序。每次 Queue 都由仓储开启独立事务，
// 因而某个文件失败不会撤销其他文件已经创建或复用的任务。
func (s *QueueService) QueueBatch(
	ctx context.Context,
	scope accessdomain.OwnerScope,
	documentIDs []int64,
) (BatchQueueOutput, error) {
	if len(documentIDs) == 0 {
		return BatchQueueOutput{}, ErrEmptyEmbeddingBatch
	}
	if len(documentIDs) > MaxEmbeddingBatchDocumentCount {
		return BatchQueueOutput{}, ErrEmbeddingBatchTooLarge
	}
	for _, documentID := range documentIDs {
		if documentID <= 0 {
			return BatchQueueOutput{}, ErrInvalidDocumentID
		}
	}

	seen := make(map[int64]struct{}, len(documentIDs))
	items := make([]BatchQueueItem, 0, len(documentIDs))
	for _, documentID := range documentIDs {
		if _, duplicated := seen[documentID]; duplicated {
			continue
		}
		seen[documentID] = struct{}{}

		result, err := s.Queue(ctx, scope, documentID)
		items = append(items, BatchQueueItem{
			DocumentID: documentID,
			Result:     result,
			Err:        err,
		})
	}

	return BatchQueueOutput{Items: items}, nil
}
