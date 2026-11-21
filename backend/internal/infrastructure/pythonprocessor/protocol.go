// Package pythonprocessor 提供 Go 调用 Python 文档处理子进程的基础设施适配器。
package pythonprocessor

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	documentapplication "rag-reasoning-platform/backend/internal/application/document"
	documentdomain "rag-reasoning-platform/backend/internal/domain/document"
)

const (
	contractVersionV1         = "v1"
	processStatusSucceeded    = "succeeded"
	processStatusFailed       = "failed"
	maxRequestIDLength        = 128
	maxChunkCharactersMinimum = 1
	maxChunkCharactersMaximum = 100000
	maxPDFFileBytesMaximum    = 1024 * 1024 * 1024
	maxPDFPagesMaximum        = 10000
	maxDetectedTitleRunes     = 500
)

var (
	requestIDPattern = regexp.MustCompile(`^[A-Za-z0-9._:-]+$`)

	// ErrInvalidProcessRequest 表示 Go 准备发送给 Python 的请求不符合 v1 契约。
	ErrInvalidProcessRequest = errors.New(
		"invalid Python document processing request",
	)

	// ErrInvalidProcessResponse 表示 Python stdout 不符合 v1 契约。
	ErrInvalidProcessResponse = errors.New(
		"invalid Python document processing response",
	)
)

var supportedFailureCodes = map[string]struct{}{
	"invalid_request":          {},
	"unsupported_format":       {},
	"source_not_found":         {},
	"source_access_denied":     {},
	"password_required":        {},
	"extraction_not_permitted": {},
	"ocr_required":             {},
	"invalid_content":          {},
	"resource_limit_exceeded":  {},
	"parse_failed":             {},
	"internal_error":           {},
}

type processRequest struct {
	ContractVersion string          `json:"contract_version"`
	RequestID       string          `json:"request_id"`
	Document        processDocument `json:"document"`
	Options         processOptions  `json:"options"`
}

type processDocument struct {
	ID           int64  `json:"id"`
	OriginalName string `json:"original_name"`
	SourcePath   string `json:"source_path"`
	MIMEType     string `json:"mime_type"`
}

type processOptions struct {
	MaxChunkCharacters int   `json:"max_chunk_characters"`
	MaxPDFFileBytes    int64 `json:"max_pdf_file_bytes"`
	MaxPDFPages        int   `json:"max_pdf_pages"`
}

type processResponse struct {
	ContractVersion string           `json:"contract_version"`
	RequestID       string           `json:"request_id"`
	Status          string           `json:"status"`
	Metadata        *processMetadata `json:"metadata,omitempty"`
	Metrics         *processMetrics  `json:"metrics,omitempty"`
	Chunks          []processChunk   `json:"chunks,omitempty"`
	Error           *processFailure  `json:"error,omitempty"`
}

type processMetrics struct {
	PythonTotalMS     int64 `json:"python_total_ms"`
	SourceOpenMS      int64 `json:"source_open_ms"`
	MetadataReadMS    int64 `json:"metadata_read_ms"`
	TextExtractMS     int64 `json:"text_extract_ms"`
	TextSplitMS       int64 `json:"text_split_ms"`
	PageCount         int   `json:"page_count"`
	SlowestPageNumber int   `json:"slowest_page_number"`
	SlowestPageMS     int64 `json:"slowest_page_ms"`
}

type processMetadata struct {
	Title string `json:"title"`
}

type processChunk struct {
	Index     int    `json:"index"`
	Content   string `json:"content"`
	PageStart *int   `json:"page_start,omitempty"`
	PageEnd   *int   `json:"page_end,omitempty"`
}

type processFailure struct {
	Code      string `json:"code"`
	Message   string `json:"message"`
	Retryable *bool  `json:"retryable"`
}

// ProcessingFailureError 表示 Python 正常完成协议，但文档处理结果为失败。
//
// 它不同于进程崩溃和 JSON 协议错误；Retryable 为后续有限重试策略保留。
type ProcessingFailureError struct {
	Code      string
	Message   string
	Retryable bool
}

func (e *ProcessingFailureError) Error() string {
	return fmt.Sprintf(
		"Python document processing failed with code %q: %s",
		e.Code,
		e.Message,
	)
}

func newProcessRequest(
	requestID string,
	document documentdomain.Document,
	sourcePath string,
	maxChunkCharacters int,
	maxPDFFileBytes int64,
	maxPDFPages int,
) (processRequest, error) {
	requestID = strings.TrimSpace(requestID)
	sourcePath = strings.TrimSpace(sourcePath)

	switch {
	case requestID == "":
		return processRequest{}, fmt.Errorf(
			"%w: request ID is required",
			ErrInvalidProcessRequest,
		)
	case len(requestID) > maxRequestIDLength ||
		!requestIDPattern.MatchString(requestID):
		return processRequest{}, fmt.Errorf(
			"%w: request ID %q has an invalid format",
			ErrInvalidProcessRequest,
			requestID,
		)
	case document.ID <= 0:
		return processRequest{}, fmt.Errorf(
			"%w: document ID must be positive",
			ErrInvalidProcessRequest,
		)
	case strings.TrimSpace(document.OriginalName) == "":
		return processRequest{}, fmt.Errorf(
			"%w: original name is required",
			ErrInvalidProcessRequest,
		)
	case sourcePath == "":
		return processRequest{}, fmt.Errorf(
			"%w: source path is required",
			ErrInvalidProcessRequest,
		)
	case !filepath.IsAbs(sourcePath):
		return processRequest{}, fmt.Errorf(
			"%w: source path must be absolute",
			ErrInvalidProcessRequest,
		)
	case strings.TrimSpace(document.MIMEType) == "":
		return processRequest{}, fmt.Errorf(
			"%w: MIME type is required",
			ErrInvalidProcessRequest,
		)
	case maxChunkCharacters < maxChunkCharactersMinimum ||
		maxChunkCharacters > maxChunkCharactersMaximum:
		return processRequest{}, fmt.Errorf(
			"%w: max chunk characters must be between %d and %d",
			ErrInvalidProcessRequest,
			maxChunkCharactersMinimum,
			maxChunkCharactersMaximum,
		)
	case maxPDFFileBytes < 1 || maxPDFFileBytes > maxPDFFileBytesMaximum:
		return processRequest{}, fmt.Errorf(
			"%w: max PDF file bytes must be between 1 and %d",
			ErrInvalidProcessRequest,
			maxPDFFileBytesMaximum,
		)
	case maxPDFPages < 1 || maxPDFPages > maxPDFPagesMaximum:
		return processRequest{}, fmt.Errorf(
			"%w: max PDF pages must be between 1 and %d",
			ErrInvalidProcessRequest,
			maxPDFPagesMaximum,
		)
	}

	return processRequest{
		ContractVersion: contractVersionV1,
		RequestID:       requestID,
		Document: processDocument{
			ID:           document.ID,
			OriginalName: document.OriginalName,
			SourcePath:   sourcePath,
			MIMEType:     document.MIMEType,
		},
		Options: processOptions{
			MaxChunkCharacters: maxChunkCharacters,
			MaxPDFFileBytes:    maxPDFFileBytes,
			MaxPDFPages:        maxPDFPages,
		},
	}, nil
}

func encodeProcessRequest(
	ctx context.Context,
	writer io.Writer,
	request processRequest,
) error {
	if err := ctx.Err(); err != nil {
		return err
	}

	if err := json.NewEncoder(writer).Encode(request); err != nil {
		return fmt.Errorf("encode Python processing request: %w", err)
	}

	return nil
}

func decodeProcessResponse(
	ctx context.Context,
	reader io.Reader,
	expectedRequestID string,
) (documentapplication.ProcessingResult, error) {
	if err := ctx.Err(); err != nil {
		return documentapplication.ProcessingResult{}, err
	}

	decoder := json.NewDecoder(reader)
	decoder.DisallowUnknownFields()

	var response processResponse
	if err := decoder.Decode(&response); err != nil {
		return documentapplication.ProcessingResult{}, fmt.Errorf(
			"%w: decode JSON: %w",
			ErrInvalidProcessResponse,
			err,
		)
	}

	var trailingValue any
	trailingErr := decoder.Decode(&trailingValue)
	if !errors.Is(trailingErr, io.EOF) {
		if trailingErr == nil {
			trailingErr = errors.New("multiple JSON values are not allowed")
		}
		return documentapplication.ProcessingResult{}, fmt.Errorf(
			"%w: trailing output: %w",
			ErrInvalidProcessResponse,
			trailingErr,
		)
	}

	if response.ContractVersion != contractVersionV1 {
		return documentapplication.ProcessingResult{}, fmt.Errorf(
			"%w: contract version = %q, want %q",
			ErrInvalidProcessResponse,
			response.ContractVersion,
			contractVersionV1,
		)
	}
	if response.RequestID != expectedRequestID {
		return documentapplication.ProcessingResult{}, fmt.Errorf(
			"%w: request ID = %q, want %q",
			ErrInvalidProcessResponse,
			response.RequestID,
			expectedRequestID,
		)
	}

	switch response.Status {
	case processStatusSucceeded:
		if response.Error != nil {
			return documentapplication.ProcessingResult{}, fmt.Errorf(
				"%w: succeeded response must not contain error",
				ErrInvalidProcessResponse,
			)
		}
		if err := validateProcessChunks(response.Chunks); err != nil {
			return documentapplication.ProcessingResult{}, err
		}
		detectedTitle, err := decodeDetectedTitle(response.Metadata)
		if err != nil {
			return documentapplication.ProcessingResult{}, err
		}
		processorMetrics, err := decodeProcessMetrics(response.Metrics)
		if err != nil {
			return documentapplication.ProcessingResult{}, err
		}

		chunks := make(
			[]documentdomain.ChunkInput,
			len(response.Chunks),
		)
		for index, chunk := range response.Chunks {
			chunks[index] = documentdomain.ChunkInput{
				Index:     chunk.Index,
				Content:   chunk.Content,
				PageStart: chunk.PageStart,
				PageEnd:   chunk.PageEnd,
			}
		}

		return documentapplication.ProcessingResult{
			DetectedTitle: detectedTitle,
			Chunks:        chunks,
			Metrics:       processorMetrics,
		}, nil

	case processStatusFailed:
		if response.Metadata != nil {
			return documentapplication.ProcessingResult{}, fmt.Errorf(
				"%w: failed response must not contain metadata",
				ErrInvalidProcessResponse,
			)
		}
		if response.Metrics != nil {
			return documentapplication.ProcessingResult{}, fmt.Errorf(
				"%w: failed response must not contain metrics",
				ErrInvalidProcessResponse,
			)
		}
		if len(response.Chunks) != 0 {
			return documentapplication.ProcessingResult{}, fmt.Errorf(
				"%w: failed response must not contain chunks",
				ErrInvalidProcessResponse,
			)
		}
		if err := validateProcessFailure(response.Error); err != nil {
			return documentapplication.ProcessingResult{}, err
		}

		return documentapplication.ProcessingResult{}, &ProcessingFailureError{
			Code:      response.Error.Code,
			Message:   response.Error.Message,
			Retryable: *response.Error.Retryable,
		}

	default:
		return documentapplication.ProcessingResult{}, fmt.Errorf(
			"%w: response status = %q",
			ErrInvalidProcessResponse,
			response.Status,
		)
	}
}

// decodeProcessMetrics 校验可选的 Python 内部阶段指标并转换为领域值。
// 整个 metrics 缺失表示旧处理器没有观测能力，不属于协议错误。
func decodeProcessMetrics(
	metrics *processMetrics,
) (*documentdomain.ProcessorStageMetrics, error) {
	if metrics == nil {
		return nil, nil
	}

	durations := []int64{
		metrics.PythonTotalMS,
		metrics.SourceOpenMS,
		metrics.MetadataReadMS,
		metrics.TextExtractMS,
		metrics.TextSplitMS,
		metrics.SlowestPageMS,
	}
	for _, duration := range durations {
		if duration < 0 {
			return nil, fmt.Errorf(
				"%w: processing metric duration must be non-negative",
				ErrInvalidProcessResponse,
			)
		}
	}
	if metrics.PageCount < 1 ||
		metrics.SlowestPageNumber < 1 ||
		metrics.SlowestPageNumber > metrics.PageCount {
		return nil, fmt.Errorf(
			"%w: processing page metrics are invalid",
			ErrInvalidProcessResponse,
		)
	}

	return &documentdomain.ProcessorStageMetrics{
		TotalDuration:        time.Duration(metrics.PythonTotalMS) * time.Millisecond,
		SourceOpenDuration:   time.Duration(metrics.SourceOpenMS) * time.Millisecond,
		MetadataReadDuration: time.Duration(metrics.MetadataReadMS) * time.Millisecond,
		TextExtractDuration:  time.Duration(metrics.TextExtractMS) * time.Millisecond,
		TextSplitDuration:    time.Duration(metrics.TextSplitMS) * time.Millisecond,
		PageCount:            metrics.PageCount,
		SlowestPageNumber:    metrics.SlowestPageNumber,
		SlowestPageDuration:  time.Duration(metrics.SlowestPageMS) * time.Millisecond,
	}, nil
}

// decodeDetectedTitle 校验 Python 返回的可选标题候选。
// 标题缺失是正常情况；一旦存在，就必须已经完成空白规范化并满足长度限制。
func decodeDetectedTitle(metadata *processMetadata) (*string, error) {
	if metadata == nil {
		return nil, nil
	}

	title := metadata.Title
	if !utf8.ValidString(title) ||
		title == "" ||
		title != strings.TrimSpace(title) ||
		utf8.RuneCountInString(title) > maxDetectedTitleRunes {
		return nil, fmt.Errorf(
			"%w: metadata title is invalid",
			ErrInvalidProcessResponse,
		)
	}

	return &title, nil
}

// validateProcessChunks 校验成功响应中的统一文本块。
func validateProcessChunks(chunks []processChunk) error {
	if len(chunks) == 0 {
		return fmt.Errorf(
			"%w: succeeded response must contain at least one chunk",
			ErrInvalidProcessResponse,
		)
	}

	for expectedIndex, chunk := range chunks {
		if chunk.Index != expectedIndex {
			return fmt.Errorf(
				"%w: chunk index = %d, want %d",
				ErrInvalidProcessResponse,
				chunk.Index,
				expectedIndex,
			)
		}

		if strings.TrimSpace(chunk.Content) == "" {
			return fmt.Errorf(
				"%w: chunk %d content must not be blank",
				ErrInvalidProcessResponse,
				expectedIndex,
			)
		}

		chunkInput := documentdomain.ChunkInput{
			Index:     chunk.Index,
			Content:   chunk.Content,
			PageStart: chunk.PageStart,
			PageEnd:   chunk.PageEnd,
		}
		if !chunkInput.HasValidPageRange() {
			return fmt.Errorf(
				"%w: chunk %d has an invalid page range",
				ErrInvalidProcessResponse,
				expectedIndex,
			)
		}
	}

	return nil
}

func validateProcessFailure(failure *processFailure) error {
	if failure == nil {
		return fmt.Errorf(
			"%w: failed response must contain error",
			ErrInvalidProcessResponse,
		)
	}
	if _, supported := supportedFailureCodes[failure.Code]; !supported {
		return fmt.Errorf(
			"%w: unsupported error code %q",
			ErrInvalidProcessResponse,
			failure.Code,
		)
	}
	if strings.TrimSpace(failure.Message) == "" {
		return fmt.Errorf(
			"%w: failure message is required",
			ErrInvalidProcessResponse,
		)
	}
	if failure.Retryable == nil {
		return fmt.Errorf(
			"%w: retryable is required",
			ErrInvalidProcessResponse,
		)
	}

	return nil
}
