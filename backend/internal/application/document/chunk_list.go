package document

import (
	"context"
	"errors"
	"fmt"

	accessdomain "rag-reasoning-platform/backend/internal/domain/access"
	documentdomain "rag-reasoning-platform/backend/internal/domain/document"
)

// ErrDocumentChunksNotReady 表示文档存在，但其文本块还不能作为正式结果读取。
var ErrDocumentChunksNotReady = errors.New("document chunks are not ready")

// ChunkListInput 是分页浏览一份文档文本块的应用输入。
type ChunkListInput struct {
	DocumentID int64
	Page       int64
	PageSize   int64
}

// ChunkListOutput 是分页浏览文本块的应用输出。
type ChunkListOutput struct {
	DocumentID int64
	Chunks     []documentdomain.TextChunk
	Page       int64
	PageSize   int64
	Total      int64
	TotalPages int64
}

// ChunkListService 编排“确认文档可读 → 分页查询 chunks”用例。
type ChunkListService struct {
	documents documentdomain.ScopedFinder
	chunks    documentdomain.ScopedChunkPageLister
}

// NewChunkListService 创建文档文本块分页服务。
func NewChunkListService(
	documents documentdomain.ScopedFinder,
	chunks documentdomain.ScopedChunkPageLister,
) *ChunkListService {
	return &ChunkListService{
		documents: documents,
		chunks:    chunks,
	}
}

// List 校验参数，只允许 ready 文档读取正式 chunks，并计算总页数。
func (s *ChunkListService) List(
	ctx context.Context,
	scope accessdomain.OwnerScope,
	input ChunkListInput,
) (ChunkListOutput, error) {
	if input.DocumentID <= 0 {
		return ChunkListOutput{}, ErrInvalidID
	}
	if input.Page <= 0 {
		return ChunkListOutput{}, ErrInvalidPage
	}
	if input.PageSize <= 0 || input.PageSize > MaxPageSize {
		return ChunkListOutput{}, ErrInvalidPageSize
	}

	foundDocument, err := s.documents.GetByID(ctx, scope, input.DocumentID)
	if err != nil {
		return ChunkListOutput{}, fmt.Errorf(
			"get document before listing chunks: %w",
			err,
		)
	}
	if foundDocument.Status != documentdomain.StatusReady {
		return ChunkListOutput{}, ErrDocumentChunksNotReady
	}

	offset := (input.Page - 1) * input.PageSize
	pageResult, err := s.chunks.ListPageByDocumentID(
		ctx,
		scope,
		foundDocument.ID,
		documentdomain.ChunkPageOptions{
			Limit:  input.PageSize,
			Offset: offset,
		},
	)
	if err != nil {
		return ChunkListOutput{}, fmt.Errorf(
			"list document chunk page: %w",
			err,
		)
	}

	totalPages := pageResult.Total / input.PageSize
	if pageResult.Total%input.PageSize != 0 {
		totalPages++
	}

	return ChunkListOutput{
		DocumentID: foundDocument.ID,
		Chunks:     pageResult.Chunks,
		Page:       input.Page,
		PageSize:   input.PageSize,
		Total:      pageResult.Total,
		TotalPages: totalPages,
	}, nil
}
