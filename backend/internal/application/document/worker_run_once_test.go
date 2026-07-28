package document

import (
	"context"
	"errors"
	"testing"

	documentdomain "rag-reasoning-platform/backend/internal/domain/document"
)

type fakeWorkerDocumentFinder struct {
	getByIDFunc  func(context.Context, int64) (documentdomain.Document, error)
	getByIDCalls int
}

func (f *fakeWorkerDocumentFinder) GetByID(
	ctx context.Context,
	documentID int64,
) (documentdomain.Document, error) {
	f.getByIDCalls++
	return f.getByIDFunc(ctx, documentID)
}

type fakeDocumentProcessor struct {
	processFunc  func(context.Context, documentdomain.Document) error
	processCalls int
}

func (f *fakeDocumentProcessor) Process(
	ctx context.Context,
	document documentdomain.Document,
) error {
	f.processCalls++
	return f.processFunc(ctx, document)
}

func TestWorkerRunOnceReturnsIdleWhenQueueIsEmpty(t *testing.T) {
	jobs := &fakeProcessingJobClaimer{
		claimNextFunc: func(
			context.Context,
		) (documentdomain.ProcessingJob, error) {
			return documentdomain.ProcessingJob{},
				documentdomain.ErrNoQueuedProcessingJob
		},
	}
	documents := &fakeWorkerDocumentFinder{
		getByIDFunc: func(
			context.Context,
			int64,
		) (documentdomain.Document, error) {
			t.Fatal("GetByID() must not be called for an empty queue")
			return documentdomain.Document{}, nil
		},
	}
	processor := &fakeDocumentProcessor{
		processFunc: func(
			context.Context,
			documentdomain.Document,
		) error {
			t.Fatal("Process() must not be called for an empty queue")
			return nil
		},
	}
	worker := NewWorker(jobs, documents, processor)

	handled, err := worker.RunOnce(context.Background())

	if err != nil {
		t.Fatalf("RunOnce() error = %v, want nil", err)
	}
	if handled {
		t.Fatal("RunOnce() handled = true, want false")
	}
	if jobs.claimNextCalls != 1 {
		t.Fatalf(
			"ClaimNextProcessingJob() calls = %d, want 1",
			jobs.claimNextCalls,
		)
	}
	if documents.getByIDCalls != 0 || processor.processCalls != 0 {
		t.Fatal("empty queue must not query or process a document")
	}
	if jobs.markSucceededCalls != 0 || jobs.markFailedCalls != 0 {
		t.Fatal("empty queue must not finalize a processing job")
	}
}

func TestWorkerRunOnceMarksSuccessfulProcessing(t *testing.T) {
	expectedJob := documentdomain.ProcessingJob{
		ID:         17,
		DocumentID: 7,
		Status:     documentdomain.ProcessingJobStatusProcessing,
	}
	expectedDocument := documentdomain.Document{
		ID:          expectedJob.DocumentID,
		StoragePath: "documents/example.pdf",
		Status:      documentdomain.StatusProcessing,
	}
	jobs := &fakeProcessingJobClaimer{
		claimNextFunc: func(
			context.Context,
		) (documentdomain.ProcessingJob, error) {
			return expectedJob, nil
		},
		markSucceededFunc: func(
			_ context.Context,
			jobID int64,
		) error {
			if jobID != expectedJob.ID {
				t.Fatalf(
					"MarkProcessingJobSucceeded() jobID = %d, want %d",
					jobID,
					expectedJob.ID,
				)
			}
			return nil
		},
	}
	documents := &fakeWorkerDocumentFinder{
		getByIDFunc: func(
			_ context.Context,
			documentID int64,
		) (documentdomain.Document, error) {
			if documentID != expectedJob.DocumentID {
				t.Fatalf(
					"GetByID() documentID = %d, want %d",
					documentID,
					expectedJob.DocumentID,
				)
			}
			return expectedDocument, nil
		},
	}
	processor := &fakeDocumentProcessor{
		processFunc: func(
			_ context.Context,
			document documentdomain.Document,
		) error {
			if document != expectedDocument {
				t.Fatalf(
					"Process() document = %+v, want %+v",
					document,
					expectedDocument,
				)
			}
			return nil
		},
	}
	worker := NewWorker(jobs, documents, processor)

	handled, err := worker.RunOnce(context.Background())

	if err != nil {
		t.Fatalf("RunOnce() error = %v, want nil", err)
	}
	if !handled {
		t.Fatal("RunOnce() handled = false, want true")
	}
	if documents.getByIDCalls != 1 || processor.processCalls != 1 {
		t.Fatal("successful run must query and process one document")
	}
	if jobs.markSucceededCalls != 1 {
		t.Fatalf(
			"MarkProcessingJobSucceeded() calls = %d, want 1",
			jobs.markSucceededCalls,
		)
	}
	if jobs.markFailedCalls != 0 {
		t.Fatalf(
			"MarkProcessingJobFailed() calls = %d, want 0",
			jobs.markFailedCalls,
		)
	}
}

func TestWorkerRunOncePreservesDocumentLookupError(t *testing.T) {
	lookupError := errors.New("document query failed")
	expectedJob := documentdomain.ProcessingJob{
		ID:         18,
		DocumentID: 8,
		Status:     documentdomain.ProcessingJobStatusProcessing,
	}
	jobs := &fakeProcessingJobClaimer{
		claimNextFunc: func(
			context.Context,
		) (documentdomain.ProcessingJob, error) {
			return expectedJob, nil
		},
	}
	documents := &fakeWorkerDocumentFinder{
		getByIDFunc: func(
			context.Context,
			int64,
		) (documentdomain.Document, error) {
			return documentdomain.Document{}, lookupError
		},
	}
	processor := &fakeDocumentProcessor{
		processFunc: func(
			context.Context,
			documentdomain.Document,
		) error {
			t.Fatal("Process() must not be called after document lookup fails")
			return nil
		},
	}
	worker := NewWorker(jobs, documents, processor)

	handled, err := worker.RunOnce(context.Background())

	if !errors.Is(err, lookupError) {
		t.Fatalf(
			"RunOnce() error = %v, want wrapped lookup error",
			err,
		)
	}
	if !handled {
		t.Fatal("RunOnce() handled = false, want true")
	}
	if processor.processCalls != 0 {
		t.Fatalf(
			"Process() calls = %d, want 0",
			processor.processCalls,
		)
	}
	if jobs.markSucceededCalls != 0 || jobs.markFailedCalls != 0 {
		t.Fatal("lookup failure must not finalize the processing job")
	}
}

func TestWorkerRunOnceMarksFailedProcessing(t *testing.T) {
	processingError := errors.New("python process exited with code 1")
	expectedJob := documentdomain.ProcessingJob{
		ID:         19,
		DocumentID: 9,
		Status:     documentdomain.ProcessingJobStatusProcessing,
	}
	expectedDocument := documentdomain.Document{
		ID:          expectedJob.DocumentID,
		StoragePath: "documents/failing.pdf",
		Status:      documentdomain.StatusProcessing,
	}
	jobs := &fakeProcessingJobClaimer{
		claimNextFunc: func(
			context.Context,
		) (documentdomain.ProcessingJob, error) {
			return expectedJob, nil
		},
		markFailedFunc: func(
			_ context.Context,
			jobID int64,
			errorMessage string,
		) error {
			if jobID != expectedJob.ID {
				t.Fatalf(
					"MarkProcessingJobFailed() jobID = %d, want %d",
					jobID,
					expectedJob.ID,
				)
			}
			if errorMessage != safeProcessingFailureMessage {
				t.Fatalf(
					"failure message = %q, want %q",
					errorMessage,
					safeProcessingFailureMessage,
				)
			}
			return nil
		},
	}
	documents := &fakeWorkerDocumentFinder{
		getByIDFunc: func(
			context.Context,
			int64,
		) (documentdomain.Document, error) {
			return expectedDocument, nil
		},
	}
	processor := &fakeDocumentProcessor{
		processFunc: func(
			context.Context,
			documentdomain.Document,
		) error {
			return processingError
		},
	}
	worker := NewWorker(jobs, documents, processor)

	handled, err := worker.RunOnce(context.Background())

	if !errors.Is(err, processingError) {
		t.Fatalf(
			"RunOnce() error = %v, want wrapped processing error",
			err,
		)
	}
	if !handled {
		t.Fatal("RunOnce() handled = false, want true")
	}
	if jobs.markFailedCalls != 1 {
		t.Fatalf(
			"MarkProcessingJobFailed() calls = %d, want 1",
			jobs.markFailedCalls,
		)
	}
	if jobs.markSucceededCalls != 0 {
		t.Fatalf(
			"MarkProcessingJobSucceeded() calls = %d, want 0",
			jobs.markSucceededCalls,
		)
	}
}

func TestWorkerRunOncePreservesProcessingAndFailureFinalizationErrors(
	t *testing.T,
) {
	processingError := errors.New("python process failed")
	finalizationError := errors.New("database unavailable")
	expectedJob := documentdomain.ProcessingJob{
		ID:         21,
		DocumentID: 10,
		Status:     documentdomain.ProcessingJobStatusProcessing,
	}
	jobs := &fakeProcessingJobClaimer{
		claimNextFunc: func(
			context.Context,
		) (documentdomain.ProcessingJob, error) {
			return expectedJob, nil
		},
		markFailedFunc: func(
			context.Context,
			int64,
			string,
		) error {
			return finalizationError
		},
	}
	documents := &fakeWorkerDocumentFinder{
		getByIDFunc: func(
			context.Context,
			int64,
		) (documentdomain.Document, error) {
			return documentdomain.Document{
				ID:     expectedJob.DocumentID,
				Status: documentdomain.StatusProcessing,
			}, nil
		},
	}
	processor := &fakeDocumentProcessor{
		processFunc: func(
			context.Context,
			documentdomain.Document,
		) error {
			return processingError
		},
	}
	worker := NewWorker(jobs, documents, processor)

	handled, err := worker.RunOnce(context.Background())

	if !errors.Is(err, processingError) {
		t.Fatalf(
			"RunOnce() error = %v, want processing error",
			err,
		)
	}
	if !errors.Is(err, finalizationError) {
		t.Fatalf(
			"RunOnce() error = %v, want finalization error",
			err,
		)
	}
	if !handled {
		t.Fatal("RunOnce() handled = false, want true")
	}
	if jobs.markFailedCalls != 1 {
		t.Fatalf(
			"MarkProcessingJobFailed() calls = %d, want 1",
			jobs.markFailedCalls,
		)
	}
}

func TestWorkerRunOncePreservesFinalizationError(t *testing.T) {
	finalizationError := errors.New("database unavailable")
	expectedJob := documentdomain.ProcessingJob{
		ID:         23,
		DocumentID: 11,
		Status:     documentdomain.ProcessingJobStatusProcessing,
	}
	jobs := &fakeProcessingJobClaimer{
		claimNextFunc: func(
			context.Context,
		) (documentdomain.ProcessingJob, error) {
			return expectedJob, nil
		},
		markSucceededFunc: func(
			context.Context,
			int64,
		) error {
			return finalizationError
		},
	}
	documents := &fakeWorkerDocumentFinder{
		getByIDFunc: func(
			context.Context,
			int64,
		) (documentdomain.Document, error) {
			return documentdomain.Document{
				ID:     expectedJob.DocumentID,
				Status: documentdomain.StatusProcessing,
			}, nil
		},
	}
	processor := &fakeDocumentProcessor{
		processFunc: func(
			context.Context,
			documentdomain.Document,
		) error {
			return nil
		},
	}
	worker := NewWorker(jobs, documents, processor)

	handled, err := worker.RunOnce(context.Background())

	if !errors.Is(err, finalizationError) {
		t.Fatalf(
			"RunOnce() error = %v, want wrapped finalization error",
			err,
		)
	}
	if !handled {
		t.Fatal("RunOnce() handled = false, want true")
	}
}
