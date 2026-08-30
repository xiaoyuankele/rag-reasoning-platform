package document

import (
	"context"
	"errors"
	"fmt"

	documentdomain "rag-reasoning-platform/backend/internal/domain/document"
)

// ErrUnsupportedTextDocumentType 表示文档格式不能由 TextProcessor 处理。
var ErrUnsupportedTextDocumentType = errors.New(
	"text processor supports only Markdown and plain text documents",
)

// TextProcessor 负责处理可以直接读取为 UTF-8 文本的文档。
type TextProcessor struct {
	// files 是接口类型字段，运行时可以保存 LocalStorage 或测试 Fake。
	files StoredFileOpener
}

var _ DocumentProcessor = (*TextProcessor)(nil)

// NewTextProcessor 创建文本处理器。
func NewTextProcessor(files StoredFileOpener) *TextProcessor {
	return &TextProcessor{files: files}
}

// validateTextDocument 检查文档能否交给 TextProcessor。
func validateTextDocument(document documentdomain.Document) error {
	switch document.MIMEType {
	case "text/markdown", "text/plain":
		return nil
	default:
		return fmt.Errorf(
			"%w: %q",
			ErrUnsupportedTextDocumentType,
			document.MIMEType,
		)
	}
}

// Process 读取一份 Markdown 或纯文本文档，并返回统一文本块。
func (p *TextProcessor) Process(
	ctx context.Context,
	document documentdomain.Document,
) (result ProcessingResult, err error) {
	if err := validateTextDocument(document); err != nil {
		return ProcessingResult{}, err
	}

	openedFile, err := p.files.Open(ctx, document.StoragePath)
	if err != nil {
		return ProcessingResult{}, fmt.Errorf(
			"open text document file: %w",
			err,
		)
	}

	// 使用命名返回值，让延迟关闭既能保留处理错误，
	// 又能在关闭本身失败时把关闭错误返回给 Worker。
	defer func() {
		closeErr := openedFile.Close()
		if closeErr == nil {
			return
		}

		wrappedCloseErr := fmt.Errorf(
			"close text document file: %w",
			closeErr,
		)
		if err != nil {
			err = errors.Join(err, wrappedCloseErr)
			return
		}

		result = ProcessingResult{}
		err = wrappedCloseErr
	}()

	chunks, err := chunkText(ctx, openedFile, defaultTextChunkRuneLimit)
	if err != nil {
		return ProcessingResult{}, fmt.Errorf(
			"process text document content: %w",
			err,
		)
	}

	return ProcessingResult{Chunks: chunks}, nil
}
