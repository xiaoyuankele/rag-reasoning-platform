package pythonprocessor

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	documentdomain "rag-reasoning-platform/backend/internal/domain/document"
)

func TestNewProcessRequestBuildsVersionedRequest(t *testing.T) {
	sourcePath := filepath.Join(t.TempDir(), "document.pdf")
	document := documentdomain.Document{
		ID:           42,
		OriginalName: "research.pdf",
		MIMEType:     "application/pdf",
	}

	request, err := newProcessRequest(
		"job-123",
		document,
		sourcePath,
		1000,
	)
	if err != nil {
		t.Fatalf("newProcessRequest() error = %v, want nil", err)
	}

	if request.ContractVersion != contractVersionV1 {
		t.Fatalf(
			"contract version = %q, want %q",
			request.ContractVersion,
			contractVersionV1,
		)
	}
	if request.RequestID != "job-123" {
		t.Fatalf("request ID = %q, want job-123", request.RequestID)
	}
	if request.Document.ID != document.ID ||
		request.Document.OriginalName != document.OriginalName ||
		request.Document.SourcePath != sourcePath ||
		request.Document.MIMEType != document.MIMEType {
		t.Fatalf("request document = %+v, want source document", request.Document)
	}
	if request.Options.MaxChunkCharacters != 1000 {
		t.Fatalf(
			"max chunk characters = %d, want 1000",
			request.Options.MaxChunkCharacters,
		)
	}
}

func TestNewProcessRequestRejectsInvalidInput(t *testing.T) {
	validDocument := documentdomain.Document{
		ID:           42,
		OriginalName: "research.pdf",
		MIMEType:     "application/pdf",
	}
	validSourcePath := filepath.Join(t.TempDir(), "document.pdf")

	tests := []struct {
		name               string
		requestID          string
		document           documentdomain.Document
		sourcePath         string
		maxChunkCharacters int
	}{
		{
			name:               "missing request ID",
			document:           validDocument,
			sourcePath:         validSourcePath,
			maxChunkCharacters: 1000,
		},
		{
			name:               "invalid request ID characters",
			requestID:          "job 123",
			document:           validDocument,
			sourcePath:         validSourcePath,
			maxChunkCharacters: 1000,
		},
		{
			name:               "invalid document ID",
			requestID:          "job-123",
			document:           documentdomain.Document{OriginalName: "research.pdf", MIMEType: "application/pdf"},
			sourcePath:         validSourcePath,
			maxChunkCharacters: 1000,
		},
		{
			name:               "relative source path",
			requestID:          "job-123",
			document:           validDocument,
			sourcePath:         "documents/research.pdf",
			maxChunkCharacters: 1000,
		},
		{
			name:       "invalid chunk limit",
			requestID:  "job-123",
			document:   validDocument,
			sourcePath: validSourcePath,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			request, err := newProcessRequest(
				test.requestID,
				test.document,
				test.sourcePath,
				test.maxChunkCharacters,
			)

			if !errors.Is(err, ErrInvalidProcessRequest) {
				t.Fatalf(
					"newProcessRequest() error = %v, want ErrInvalidProcessRequest",
					err,
				)
			}
			if !reflect.DeepEqual(request, processRequest{}) {
				t.Fatalf("request = %+v, want empty", request)
			}
		})
	}
}

func TestEncodeProcessRequestWritesContractJSON(t *testing.T) {
	request := processRequest{
		ContractVersion: contractVersionV1,
		RequestID:       "job-123",
		Document: processDocument{
			ID:           42,
			OriginalName: "research.pdf",
			SourcePath:   filepath.Join(t.TempDir(), "document.pdf"),
			MIMEType:     "application/pdf",
		},
		Options: processOptions{MaxChunkCharacters: 1000},
	}
	var output bytes.Buffer

	if err := encodeProcessRequest(
		context.Background(),
		&output,
		request,
	); err != nil {
		t.Fatalf("encodeProcessRequest() error = %v, want nil", err)
	}

	var decoded processRequest
	if err := json.Unmarshal(output.Bytes(), &decoded); err != nil {
		t.Fatalf("decode encoded request: %v", err)
	}
	if !reflect.DeepEqual(decoded, request) {
		t.Fatalf("decoded request = %+v, want %+v", decoded, request)
	}
}

func TestDecodeProcessResponseReturnsChunks(t *testing.T) {
	responseJSON := `{
		"contract_version":"v1",
		"request_id":"job-123",
		"status":"succeeded",
		"chunks":[
			{"index":0,"content":"first"},
			{"index":1,"content":"second"}
		]
	}`

	result, err := decodeProcessResponse(
		context.Background(),
		strings.NewReader(responseJSON),
		"job-123",
	)
	if err != nil {
		t.Fatalf("decodeProcessResponse() error = %v, want nil", err)
	}
	wantChunks := []documentdomain.ChunkInput{
		{Index: 0, Content: "first"},
		{Index: 1, Content: "second"},
	}
	if !reflect.DeepEqual(result.Chunks, wantChunks) {
		t.Fatalf("chunks = %+v, want %+v", result.Chunks, wantChunks)
	}
}

func TestDecodeProcessResponseReturnsStructuredFailure(t *testing.T) {
	responseJSON := `{
		"contract_version":"v1",
		"request_id":"job-123",
		"status":"failed",
		"error":{
			"code":"unsupported_format",
			"message":"document format is not supported",
			"retryable":false
		}
	}`

	result, err := decodeProcessResponse(
		context.Background(),
		strings.NewReader(responseJSON),
		"job-123",
	)
	var failure *ProcessingFailureError
	if !errors.As(err, &failure) {
		t.Fatalf("error = %v, want ProcessingFailureError", err)
	}
	if failure.Code != "unsupported_format" ||
		failure.Message != "document format is not supported" ||
		failure.Retryable {
		t.Fatalf("failure = %+v, want structured unsupported error", failure)
	}
	if len(result.Chunks) != 0 {
		t.Fatalf("chunks = %+v, want empty", result.Chunks)
	}
}

func TestDecodeProcessResponseRejectsInvalidEnvelope(t *testing.T) {
	tests := []struct {
		name         string
		responseJSON string
	}{
		{
			name: "unknown contract version",
			responseJSON: `{
				"contract_version":"v2",
				"request_id":"job-123",
				"status":"succeeded",
				"chunks":[{"index":0,"content":"content"}]
			}`,
		},
		{
			name: "mismatched request ID",
			responseJSON: `{
				"contract_version":"v1",
				"request_id":"job-999",
				"status":"succeeded",
				"chunks":[{"index":0,"content":"content"}]
			}`,
		},
		{
			name: "unknown status",
			responseJSON: `{
				"contract_version":"v1",
				"request_id":"job-123",
				"status":"working"
			}`,
		},
		{
			name: "unknown field",
			responseJSON: `{
				"contract_version":"v1",
				"request_id":"job-123",
				"status":"succeeded",
				"chunks":[{"index":0,"content":"content"}],
				"debug":"not allowed"
			}`,
		},
		{
			name: "trailing JSON value",
			responseJSON: `{
				"contract_version":"v1",
				"request_id":"job-123",
				"status":"succeeded",
				"chunks":[{"index":0,"content":"content"}]
			} {"extra":true}`,
		},
		{
			name: "success contains error",
			responseJSON: `{
				"contract_version":"v1",
				"request_id":"job-123",
				"status":"succeeded",
				"chunks":[{"index":0,"content":"content"}],
				"error":{"code":"parse_failed","message":"failure","retryable":false}
			}`,
		},
		{
			name: "failure contains chunks",
			responseJSON: `{
				"contract_version":"v1",
				"request_id":"job-123",
				"status":"failed",
				"chunks":[{"index":0,"content":"content"}],
				"error":{"code":"parse_failed","message":"failure","retryable":false}
			}`,
		},
		{
			name: "failure has unsupported code",
			responseJSON: `{
				"contract_version":"v1",
				"request_id":"job-123",
				"status":"failed",
				"error":{"code":"unknown","message":"failure","retryable":false}
			}`,
		},
		{
			name: "failure is missing retryable",
			responseJSON: `{
				"contract_version":"v1",
				"request_id":"job-123",
				"status":"failed",
				"error":{"code":"parse_failed","message":"failure"}
			}`,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			result, err := decodeProcessResponse(
				context.Background(),
				strings.NewReader(test.responseJSON),
				"job-123",
			)

			if !errors.Is(err, ErrInvalidProcessResponse) {
				t.Fatalf(
					"error = %v, want ErrInvalidProcessResponse",
					err,
				)
			}
			if len(result.Chunks) != 0 {
				t.Fatalf("chunks = %+v, want empty", result.Chunks)
			}
		})
	}
}

func TestValidateProcessChunks(t *testing.T) {
	tests := []struct {
		name    string
		chunks  []processChunk
		wantErr bool
	}{
		{
			name:    "chunks are required",
			wantErr: true,
		},
		{
			name: "first index must be zero",
			chunks: []processChunk{
				{Index: 1, Content: "content"},
			},
			wantErr: true,
		},
		{
			name: "indexes must remain continuous",
			chunks: []processChunk{
				{Index: 0, Content: "first"},
				{Index: 2, Content: "second"},
			},
			wantErr: true,
		},
		{
			name: "content must not be blank",
			chunks: []processChunk{
				{Index: 0, Content: " \r\n\t "},
			},
			wantErr: true,
		},
		{
			name: "valid chunks",
			chunks: []processChunk{
				{Index: 0, Content: "first"},
				{Index: 1, Content: "second"},
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := validateProcessChunks(test.chunks)

			if test.wantErr {
				if !errors.Is(err, ErrInvalidProcessResponse) {
					t.Fatalf(
						"validateProcessChunks() error = %v, want ErrInvalidProcessResponse",
						err,
					)
				}
				return
			}

			if err != nil {
				t.Fatalf("validateProcessChunks() error = %v, want nil", err)
			}
		})
	}
}
