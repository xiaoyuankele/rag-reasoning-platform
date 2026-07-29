package document

import (
	"context"
	"errors"
	"reflect"
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
	processFunc func(
		context.Context,
		documentdomain.Document,
	) (ProcessingResult, error)
	processCalls int
}

func (f *fakeDocumentProcessor) Process(
	ctx context.Context,
	document documentdomain.Document,
) (ProcessingResult, error) {
	f.processCalls++
	return f.processFunc(ctx, document)
}

type fakeWorkerChunkReplacer struct {
	replaceFunc func(
		context.Context,
		int64,
		[]documentdomain.ChunkInput,
	) error
	replaceCalls int
}

func (f *fakeWorkerChunkReplacer) ReplaceForDocument(
	ctx context.Context,
	documentID int64,
	chunks []documentdomain.ChunkInput,
) error {
	f.replaceCalls++
	if f.replaceFunc == nil {
		return nil
	}

	return f.replaceFunc(ctx, documentID, chunks)
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
		) (ProcessingResult, error) {
			t.Fatal("Process() must not be called for an empty queue")
			return ProcessingResult{}, nil
		},
	}
	chunks := &fakeWorkerChunkReplacer{}
	worker := NewWorker(jobs, documents, processor, chunks)

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
	if documents.getByIDCalls != 0 ||
		processor.processCalls != 0 ||
		chunks.replaceCalls != 0 {
		t.Fatal("empty queue must not query, process, or store a document")
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
	expectedChunks := []documentdomain.ChunkInput{
		{Index: 0, Content: "first normalized chunk"},
		{Index: 1, Content: "second normalized chunk"},
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
		) (ProcessingResult, error) {
			if document != expectedDocument {
				t.Fatalf(
					"Process() document = %+v, want %+v",
					document,
					expectedDocument,
				)
			}
			return ProcessingResult{
				Chunks: expectedChunks,
			}, nil
		},
	}
	chunks := &fakeWorkerChunkReplacer{
		replaceFunc: func(
			_ context.Context,
			documentID int64,
			foundChunks []documentdomain.ChunkInput,
		) error {
			if documentID != expectedDocument.ID {
				t.Fatalf(
					"ReplaceForDocument() documentID = %d, want %d",
					documentID,
					expectedDocument.ID,
				)
			}
			if !reflect.DeepEqual(foundChunks, expectedChunks) {
				t.Fatalf(
					"ReplaceForDocument() chunks = %+v, want %+v",
					foundChunks,
					expectedChunks,
				)
			}
			return nil
		},
	}
	worker := NewWorker(jobs, documents, processor, chunks)

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
	if chunks.replaceCalls != 1 {
		t.Fatalf(
			"ReplaceForDocument() calls = %d, want 1",
			chunks.replaceCalls,
		)
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
		) (ProcessingResult, error) {
			t.Fatal("Process() must not be called after document lookup fails")
			return ProcessingResult{}, nil
		},
	}
	chunks := &fakeWorkerChunkReplacer{}
	worker := NewWorker(jobs, documents, processor, chunks)

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
	if chunks.replaceCalls != 0 {
		t.Fatal("document lookup failure must not store chunks")
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
		) (ProcessingResult, error) {
			return ProcessingResult{}, processingError
		},
	}
	chunks := &fakeWorkerChunkReplacer{}
	worker := NewWorker(jobs, documents, processor, chunks)

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
	if chunks.replaceCalls != 0 {
		t.Fatal("processing failure must not store chunks")
	}
}

func TestWorkerRunOnceMarksFailedWhenChunkReplacementFails(t *testing.T) {
	chunkReplacementError := errors.New("replace chunks failed")
	expectedJob := documentdomain.ProcessingJob{
		ID:         20,
		DocumentID: 12,
		Status:     documentdomain.ProcessingJobStatusProcessing,
	}
	expectedDocument := documentdomain.Document{
		ID:          expectedJob.DocumentID,
		StoragePath: "documents/chunk-failure.md",
		Status:      documentdomain.StatusProcessing,
	}
	expectedChunks := []documentdomain.ChunkInput{
		{Index: 0, Content: "normalized content"},
	}
	steps := make([]string, 0, 2)

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
			steps = append(steps, "mark failed")
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
		) (ProcessingResult, error) {
			return ProcessingResult{
				Chunks: expectedChunks,
			}, nil
		},
	}
	chunks := &fakeWorkerChunkReplacer{
		replaceFunc: func(
			_ context.Context,
			documentID int64,
			foundChunks []documentdomain.ChunkInput,
		) error {
			steps = append(steps, "replace chunks")
			if documentID != expectedDocument.ID {
				t.Fatalf(
					"ReplaceForDocument() documentID = %d, want %d",
					documentID,
					expectedDocument.ID,
				)
			}
			if !reflect.DeepEqual(foundChunks, expectedChunks) {
				t.Fatalf(
					"ReplaceForDocument() chunks = %+v, want %+v",
					foundChunks,
					expectedChunks,
				)
			}
			return chunkReplacementError
		},
	}
	worker := NewWorker(jobs, documents, processor, chunks)

	handled, err := worker.RunOnce(context.Background())

	if !errors.Is(err, chunkReplacementError) {
		t.Fatalf(
			"RunOnce() error = %v, want chunk replacement error",
			err,
		)
	}
	if !handled {
		t.Fatal("RunOnce() handled = false, want true")
	}
	if chunks.replaceCalls != 1 {
		t.Fatalf(
			"ReplaceForDocument() calls = %d, want 1",
			chunks.replaceCalls,
		)
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
	expectedSteps := []string{"replace chunks", "mark failed"}
	if !reflect.DeepEqual(steps, expectedSteps) {
		t.Fatalf(
			"steps = %v, want %v",
			steps,
			expectedSteps,
		)
	}
}

func TestWorkerRunOncePreservesChunkAndFailureFinalizationErrors(
	t *testing.T,
) {
	chunkReplacementError := errors.New("replace chunks failed")
	finalizationError := errors.New("mark failed unavailable")
	expectedJob := documentdomain.ProcessingJob{
		ID:         24,
		DocumentID: 13,
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
		) (ProcessingResult, error) {
			return ProcessingResult{
				Chunks: []documentdomain.ChunkInput{
					{Index: 0, Content: "normalized content"},
				},
			}, nil
		},
	}
	chunks := &fakeWorkerChunkReplacer{
		replaceFunc: func(
			context.Context,
			int64,
			[]documentdomain.ChunkInput,
		) error {
			return chunkReplacementError
		},
	}
	worker := NewWorker(jobs, documents, processor, chunks)

	handled, err := worker.RunOnce(context.Background())

	if !errors.Is(err, chunkReplacementError) {
		t.Fatalf(
			"RunOnce() error = %v, want chunk replacement error",
			err,
		)
	}
	if !errors.Is(err, finalizationError) {
		t.Fatalf(
			"RunOnce() error = %v, want failure finalization error",
			err,
		)
	}
	if !handled {
		t.Fatal("RunOnce() handled = false, want true")
	}
	if chunks.replaceCalls != 1 || jobs.markFailedCalls != 1 {
		t.Fatal("chunk and failure finalization paths must each run once")
	}
	if jobs.markSucceededCalls != 0 {
		t.Fatal("failed chunk replacement must not mark the job succeeded")
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
		) (ProcessingResult, error) {
			return ProcessingResult{}, processingError
		},
	}
	worker := NewWorker(
		jobs,
		documents,
		processor,
		&fakeWorkerChunkReplacer{},
	)

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
		) (ProcessingResult, error) {
			return ProcessingResult{
				Chunks: []documentdomain.ChunkInput{
					{Index: 0, Content: "normalized content"},
				},
			}, nil
		},
	}
	worker := NewWorker(
		jobs,
		documents,
		processor,
		&fakeWorkerChunkReplacer{},
	)

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
