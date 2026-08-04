package document

import (
	"context"
	"errors"
	"io"
	"reflect"
	"strings"
	"testing"

	documentdomain "rag-reasoning-platform/backend/internal/domain/document"
)

type fakeStoredFileOpener struct {
	openFunc  func(context.Context, string) (io.ReadCloser, error)
	openCalls int
}

func (f *fakeStoredFileOpener) Open(
	ctx context.Context,
	storagePath string,
) (io.ReadCloser, error) {
	f.openCalls++
	return f.openFunc(ctx, storagePath)
}

type trackingReadCloser struct {
	io.Reader
	closed   bool
	closeErr error
}

func (r *trackingReadCloser) Close() error {
	r.closed = true
	return r.closeErr
}

func TestValidateTextDocument(t *testing.T) {
	tests := []struct {
		name     string
		mimeType string
		wantErr  bool
	}{
		{
			name:     "Markdown is supported",
			mimeType: "text/markdown",
		},
		{
			name:     "plain text is supported",
			mimeType: "text/plain",
		},
		{
			name:     "PDF is rejected",
			mimeType: "application/pdf",
			wantErr:  true,
		},
		{
			name:     "empty MIME type is rejected",
			mimeType: "",
			wantErr:  true,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := validateTextDocument(documentdomain.Document{
				MIMEType: test.mimeType,
			})

			if !test.wantErr {
				if err != nil {
					t.Fatalf("validateTextDocument() error = %v, want nil", err)
				}
				return
			}

			if !errors.Is(err, ErrUnsupportedTextDocumentType) {
				t.Fatalf(
					"validateTextDocument() error = %v, want ErrUnsupportedTextDocumentType",
					err,
				)
			}
		})
	}
}

func TestTextProcessorRejectsUnsupportedTypeBeforeOpeningFile(t *testing.T) {
	files := &fakeStoredFileOpener{
		openFunc: func(context.Context, string) (io.ReadCloser, error) {
			t.Fatal("Open() must not be called for an unsupported document")
			return nil, nil
		},
	}
	processor := NewTextProcessor(files)

	result, err := processor.Process(
		context.Background(),
		documentdomain.Document{
			StoragePath: "documents/example.pdf",
			MIMEType:    "application/pdf",
		},
	)

	if !errors.Is(err, ErrUnsupportedTextDocumentType) {
		t.Fatalf("Process() error = %v, want ErrUnsupportedTextDocumentType", err)
	}
	if len(result.Chunks) != 0 {
		t.Fatalf("Process() chunks = %+v, want empty", result.Chunks)
	}
	if files.openCalls != 0 {
		t.Fatalf("Open() calls = %d, want 0", files.openCalls)
	}
}

func TestTextProcessorPreservesOpenError(t *testing.T) {
	openErr := errors.New("open failure")
	files := &fakeStoredFileOpener{
		openFunc: func(
			_ context.Context,
			storagePath string,
		) (io.ReadCloser, error) {
			if storagePath != "documents/example.md" {
				t.Fatalf("Open() storagePath = %q", storagePath)
			}
			return nil, openErr
		},
	}
	processor := NewTextProcessor(files)

	result, err := processor.Process(
		context.Background(),
		documentdomain.Document{
			StoragePath: "documents/example.md",
			MIMEType:    "text/markdown",
		},
	)

	if !errors.Is(err, openErr) {
		t.Fatalf("Process() error = %v, want open error", err)
	}
	if len(result.Chunks) != 0 {
		t.Fatalf("Process() chunks = %+v, want empty", result.Chunks)
	}
}

func TestTextProcessorReturnsNormalizedChunksAndClosesFile(t *testing.T) {
	openedFile := &trackingReadCloser{
		Reader: strings.NewReader("first\n\nsecond"),
	}
	files := &fakeStoredFileOpener{
		openFunc: func(
			_ context.Context,
			storagePath string,
		) (io.ReadCloser, error) {
			if storagePath != "documents/example.md" {
				t.Fatalf("Open() storagePath = %q", storagePath)
			}
			return openedFile, nil
		},
	}
	processor := NewTextProcessor(files)

	result, err := processor.Process(
		context.Background(),
		documentdomain.Document{
			StoragePath: "documents/example.md",
			MIMEType:    "text/markdown",
		},
	)

	if err != nil {
		t.Fatalf("Process() error = %v, want nil", err)
	}
	want := []documentdomain.ChunkInput{
		{Index: 0, Content: "first second"},
	}
	if !reflect.DeepEqual(result.Chunks, want) {
		t.Fatalf("Process() chunks = %+v, want %+v", result.Chunks, want)
	}
	if !openedFile.closed {
		t.Fatal("Process() did not close the opened file")
	}
}

func TestTextProcessorPreservesChunkErrorAndClosesFile(t *testing.T) {
	openedFile := &trackingReadCloser{
		Reader: strings.NewReader(" \r\n\t "),
	}
	files := &fakeStoredFileOpener{
		openFunc: func(
			context.Context,
			string,
		) (io.ReadCloser, error) {
			return openedFile, nil
		},
	}
	processor := NewTextProcessor(files)

	result, err := processor.Process(
		context.Background(),
		documentdomain.Document{
			StoragePath: "documents/empty.txt",
			MIMEType:    "text/plain",
		},
	)

	if !errors.Is(err, ErrEmptyTextDocument) {
		t.Fatalf("Process() error = %v, want ErrEmptyTextDocument", err)
	}
	if len(result.Chunks) != 0 {
		t.Fatalf("Process() chunks = %+v, want empty", result.Chunks)
	}
	if !openedFile.closed {
		t.Fatal("Process() did not close the opened file after a chunk error")
	}
}

func TestTextProcessorReturnsCloseErrorAfterSuccessfulChunking(t *testing.T) {
	closeErr := errors.New("close failure")
	openedFile := &trackingReadCloser{
		Reader:   strings.NewReader("content"),
		closeErr: closeErr,
	}
	files := &fakeStoredFileOpener{
		openFunc: func(
			context.Context,
			string,
		) (io.ReadCloser, error) {
			return openedFile, nil
		},
	}
	processor := NewTextProcessor(files)

	result, err := processor.Process(
		context.Background(),
		documentdomain.Document{
			StoragePath: "documents/example.txt",
			MIMEType:    "text/plain",
		},
	)

	if !errors.Is(err, closeErr) {
		t.Fatalf("Process() error = %v, want close error", err)
	}
	if len(result.Chunks) != 0 {
		t.Fatalf("Process() chunks = %+v, want empty", result.Chunks)
	}
}

func TestTextProcessorPreservesChunkAndCloseErrors(t *testing.T) {
	closeErr := errors.New("close failure")
	openedFile := &trackingReadCloser{
		Reader:   strings.NewReader(" \r\n\t "),
		closeErr: closeErr,
	}
	files := &fakeStoredFileOpener{
		openFunc: func(
			context.Context,
			string,
		) (io.ReadCloser, error) {
			return openedFile, nil
		},
	}
	processor := NewTextProcessor(files)

	_, err := processor.Process(
		context.Background(),
		documentdomain.Document{
			StoragePath: "documents/empty.txt",
			MIMEType:    "text/plain",
		},
	)

	if !errors.Is(err, ErrEmptyTextDocument) {
		t.Fatalf("Process() error = %v, want ErrEmptyTextDocument", err)
	}
	if !errors.Is(err, closeErr) {
		t.Fatalf("Process() error = %v, want close error", err)
	}
}
