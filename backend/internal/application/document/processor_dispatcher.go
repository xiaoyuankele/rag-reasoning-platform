package document

import (
	"context"
	"errors"
	"fmt"
	"strings"

	documentdomain "rag-reasoning-platform/backend/internal/domain/document"
)

var (
	// ErrDocumentProcessorsRequired 表示 Dispatcher 没有任何可用处理器。
	ErrDocumentProcessorsRequired = errors.New(
		"at least one document processor is required",
	)

	// ErrDocumentProcessorMIMETypeRequired 表示注册项缺少 MIME 类型。
	ErrDocumentProcessorMIMETypeRequired = errors.New(
		"document processor MIME type is required",
	)

	// ErrDocumentProcessorRequired 表示 MIME 类型没有对应的处理器实例。
	ErrDocumentProcessorRequired = errors.New(
		"document processor is required",
	)

	// ErrDocumentProcessorNotFound 表示当前没有处理器支持该文档 MIME 类型。
	ErrDocumentProcessorNotFound = errors.New(
		"document processor not found",
	)
)

// ProcessorDispatcher 根据文档的可信 MIME 类型选择具体处理器。
//
// 它本身不解析文档，只负责格式路由。所有处理器都返回统一的
// ProcessingResult，因此 Worker 后半段不需要区分源文件格式。
type ProcessorDispatcher struct {
	processors map[string]DocumentProcessor
}

var _ DocumentProcessor = (*ProcessorDispatcher)(nil)

// NewProcessorDispatcher 创建处理器调度器。
//
// 参数 map 的 key 是 LocalStorage 写入数据库的可信 MIME 类型，value 是
// 对应处理器。构造函数复制 map，避免调用方后续修改影响 Dispatcher。
func NewProcessorDispatcher(
	processors map[string]DocumentProcessor,
) (*ProcessorDispatcher, error) {
	if len(processors) == 0 {
		return nil, ErrDocumentProcessorsRequired
	}

	copiedProcessors := make(
		map[string]DocumentProcessor,
		len(processors),
	)
	for mimeType, processor := range processors {
		normalizedMIMEType := strings.TrimSpace(mimeType)
		if normalizedMIMEType == "" {
			return nil, ErrDocumentProcessorMIMETypeRequired
		}
		if processor == nil {
			return nil, fmt.Errorf(
				"%w for MIME type %q",
				ErrDocumentProcessorRequired,
				normalizedMIMEType,
			)
		}

		copiedProcessors[normalizedMIMEType] = processor
	}

	return &ProcessorDispatcher{
		processors: copiedProcessors,
	}, nil
}

// Process 根据 document.MIMEType 选择处理器并转发调用。
func (d *ProcessorDispatcher) Process(
	ctx context.Context,
	document documentdomain.Document,
) (ProcessingResult, error) {
	// processor 是负责该 MIME 类型的具体处理器；found 表示 key 是否存在。
	processor, found := d.processors[document.MIMEType]

	if !found {
		return ProcessingResult{}, fmt.Errorf(
			"%w: %q",
			ErrDocumentProcessorNotFound,
			document.MIMEType,
		)
	}

	processingResult, err := processor.Process(ctx, document)

	if err != nil {
		return ProcessingResult{}, fmt.Errorf(
			"process %q document: %w",
			document.MIMEType,
			err,
		)
	}

	return processingResult, nil
}
