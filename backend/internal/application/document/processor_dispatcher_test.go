package document

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	documentdomain "rag-reasoning-platform/backend/internal/domain/document"
)

type fakeDispatchProcessor struct {
	processFunc func(
		context.Context,
		documentdomain.Document,
	) (ProcessingResult, error)
	processCalls int
}

func (f *fakeDispatchProcessor) Process(
	ctx context.Context,
	document documentdomain.Document,
) (ProcessingResult, error) {
	f.processCalls++
	return f.processFunc(ctx, document)
}

func TestNewProcessorDispatcherRejectsInvalidConfiguration(t *testing.T) {
	tests := []struct {
		name       string
		processors map[string]DocumentProcessor
		wantErr    error
	}{
		{
			name:    "processors are required",
			wantErr: ErrDocumentProcessorsRequired,
		},
		{
			name: "MIME type is required",
			processors: map[string]DocumentProcessor{
				"  ": &fakeDispatchProcessor{},
			},
			wantErr: ErrDocumentProcessorMIMETypeRequired,
		},
		{
			name: "processor is required",
			processors: map[string]DocumentProcessor{
				"application/pdf": nil,
			},
			wantErr: ErrDocumentProcessorRequired,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			dispatcher, err := NewProcessorDispatcher(test.processors)

			if !errors.Is(err, test.wantErr) {
				t.Fatalf(
					"NewProcessorDispatcher() error = %v, want %v",
					err,
					test.wantErr,
				)
			}
			if dispatcher != nil {
				t.Fatalf(
					"NewProcessorDispatcher() dispatcher = %#v, want nil",
					dispatcher,
				)
			}
		})
	}
}

func TestProcessorDispatcherRoutesByMIMEType(t *testing.T) {
	type contextKey string
	const requestIDKey contextKey = "request-id"

	ctx := context.WithValue(
		context.Background(),
		requestIDKey,
		"dispatcher-test",
	)
	wantDocument := documentdomain.Document{
		ID:          42,
		StoragePath: "documents/example.md",
		MIMEType:    "text/markdown",
	}
	wantResult := ProcessingResult{
		Chunks: []documentdomain.ChunkInput{
			{Index: 0, Content: "dispatched content"},
		},
	}

	markdownProcessor := &fakeDispatchProcessor{
		processFunc: func(
			gotContext context.Context,
			gotDocument documentdomain.Document,
		) (ProcessingResult, error) {
			if gotContext.Value(requestIDKey) != "dispatcher-test" {
				t.Fatal("Process() did not forward the original context")
			}
			if !reflect.DeepEqual(gotDocument, wantDocument) {
				t.Fatalf(
					"Process() document = %+v, want %+v",
					gotDocument,
					wantDocument,
				)
			}
			return wantResult, nil
		},
	}
	plainTextProcessor := &fakeDispatchProcessor{
		processFunc: func(
			context.Context,
			documentdomain.Document,
		) (ProcessingResult, error) {
			t.Fatal("plain text processor must not handle Markdown")
			return ProcessingResult{}, nil
		},
	}

	dispatcher, err := NewProcessorDispatcher(
		map[string]DocumentProcessor{
			"text/markdown": markdownProcessor,
			"text/plain":    plainTextProcessor,
		},
	)
	if err != nil {
		t.Fatalf("NewProcessorDispatcher() error = %v, want nil", err)
	}

	result, err := dispatcher.Process(ctx, wantDocument)
	if err != nil {
		t.Fatalf("Process() error = %v, want nil", err)
	}
	if !reflect.DeepEqual(result, wantResult) {
		t.Fatalf("Process() result = %+v, want %+v", result, wantResult)
	}
	if markdownProcessor.processCalls != 1 {
		t.Fatalf(
			"Markdown Process() calls = %d, want 1",
			markdownProcessor.processCalls,
		)
	}
	if plainTextProcessor.processCalls != 0 {
		t.Fatalf(
			"plain text Process() calls = %d, want 0",
			plainTextProcessor.processCalls,
		)
	}
}

func TestProcessorDispatcherRejectsUnsupportedMIMEType(t *testing.T) {
	registeredProcessor := &fakeDispatchProcessor{
		processFunc: func(
			context.Context,
			documentdomain.Document,
		) (ProcessingResult, error) {
			t.Fatal("registered processor must not handle an unsupported MIME type")
			return ProcessingResult{}, nil
		},
	}
	dispatcher, err := NewProcessorDispatcher(
		map[string]DocumentProcessor{
			"text/markdown": registeredProcessor,
		},
	)
	if err != nil {
		t.Fatalf("NewProcessorDispatcher() error = %v, want nil", err)
	}

	result, err := dispatcher.Process(
		context.Background(),
		documentdomain.Document{MIMEType: "application/pdf"},
	)

	if !errors.Is(err, ErrDocumentProcessorNotFound) {
		t.Fatalf(
			"Process() error = %v, want ErrDocumentProcessorNotFound",
			err,
		)
	}
	if !strings.Contains(err.Error(), "application/pdf") {
		t.Fatalf("Process() error = %q, want MIME type", err.Error())
	}
	if len(result.Chunks) != 0 {
		t.Fatalf("Process() chunks = %+v, want empty", result.Chunks)
	}
	if registeredProcessor.processCalls != 0 {
		t.Fatalf(
			"registered Process() calls = %d, want 0",
			registeredProcessor.processCalls,
		)
	}
}

func TestProcessorDispatcherPreservesProcessorError(t *testing.T) {
	processorErr := errors.New("processor failure")
	processor := &fakeDispatchProcessor{
		processFunc: func(
			context.Context,
			documentdomain.Document,
		) (ProcessingResult, error) {
			return ProcessingResult{}, processorErr
		},
	}
	dispatcher, err := NewProcessorDispatcher(
		map[string]DocumentProcessor{
			"text/plain": processor,
		},
	)
	if err != nil {
		t.Fatalf("NewProcessorDispatcher() error = %v, want nil", err)
	}

	result, err := dispatcher.Process(
		context.Background(),
		documentdomain.Document{MIMEType: "text/plain"},
	)

	if !errors.Is(err, processorErr) {
		t.Fatalf("Process() error = %v, want processor error", err)
	}
	if !strings.Contains(err.Error(), "text/plain") {
		t.Fatalf("Process() error = %q, want MIME type", err.Error())
	}
	if len(result.Chunks) != 0 {
		t.Fatalf("Process() chunks = %+v, want empty", result.Chunks)
	}
}
