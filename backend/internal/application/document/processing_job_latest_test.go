package document

import (
	"context"
	"errors"
	"testing"

	accessdomain "rag-reasoning-platform/backend/internal/domain/access"
	documentdomain "rag-reasoning-platform/backend/internal/domain/document"
)

type fakeLatestProcessingJobFinder struct {
	findFunc  func(context.Context, accessdomain.OwnerScope, []int64) ([]documentdomain.ProcessingJob, error)
	findCalls int
}

func (f *fakeLatestProcessingJobFinder) FindLatestProcessingJobsByDocumentIDs(
	ctx context.Context,
	scope accessdomain.OwnerScope,
	documentIDs []int64,
) ([]documentdomain.ProcessingJob, error) {
	f.findCalls++
	return f.findFunc(ctx, scope, documentIDs)
}

func TestProcessingJobLatestServicePreservesInputOrderAndMissingItems(t *testing.T) {
	jobForSeven := documentdomain.ProcessingJob{
		ID: 41, DocumentID: 7, Status: documentdomain.ProcessingJobStatusSucceeded,
	}
	jobForThree := documentdomain.ProcessingJob{
		ID: 52, DocumentID: 3, Status: documentdomain.ProcessingJobStatusProcessing,
	}
	finder := &fakeLatestProcessingJobFinder{
		findFunc: func(
			_ context.Context,
			scope accessdomain.OwnerScope,
			documentIDs []int64,
		) ([]documentdomain.ProcessingJob, error) {
			if scope.OwnerUserID() != testOwnerUserID {
				t.Fatalf("owner = %d, want %d", scope.OwnerUserID(), testOwnerUserID)
			}
			wantIDs := []int64{7, 9, 3}
			if len(documentIDs) != len(wantIDs) {
				t.Fatalf("document IDs = %v, want %v", documentIDs, wantIDs)
			}
			for index := range wantIDs {
				if documentIDs[index] != wantIDs[index] {
					t.Fatalf("document IDs = %v, want %v", documentIDs, wantIDs)
				}
			}
			return []documentdomain.ProcessingJob{jobForThree, jobForSeven}, nil
		},
	}

	output, err := NewProcessingJobLatestService(finder).GetLatestByDocumentIDs(
		context.Background(),
		testOwnerScope(t),
		[]int64{7, 9, 7, 3},
	)
	if err != nil {
		t.Fatalf("GetLatestByDocumentIDs() error = %v", err)
	}
	if len(output.Items) != 3 {
		t.Fatalf("items = %d, want 3", len(output.Items))
	}
	if output.Items[0].DocumentID != 7 || output.Items[0].Job == nil || *output.Items[0].Job != jobForSeven {
		t.Fatalf("item 0 = %+v, want document 7 job", output.Items[0])
	}
	if output.Items[1].DocumentID != 9 || output.Items[1].Job != nil {
		t.Fatalf("item 1 = %+v, want document 9 nil job", output.Items[1])
	}
	if output.Items[2].DocumentID != 3 || output.Items[2].Job == nil || *output.Items[2].Job != jobForThree {
		t.Fatalf("item 2 = %+v, want document 3 job", output.Items[2])
	}
}

func TestProcessingJobLatestServiceRejectsInvalidInput(t *testing.T) {
	tests := []struct {
		name        string
		documentIDs []int64
		wantErr     error
	}{
		{name: "empty", documentIDs: nil, wantErr: ErrEmptyProcessingJobLookup},
		{name: "too large", documentIDs: make([]int64, MaxProcessingJobLookupDocumentCount+1), wantErr: ErrProcessingJobLookupTooLarge},
		{name: "invalid ID", documentIDs: []int64{7, 0}, wantErr: ErrInvalidProcessingJobDocumentID},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			finder := &fakeLatestProcessingJobFinder{
				findFunc: func(context.Context, accessdomain.OwnerScope, []int64) ([]documentdomain.ProcessingJob, error) {
					t.Fatal("repository must not be called for invalid input")
					return nil, nil
				},
			}
			_, err := NewProcessingJobLatestService(finder).GetLatestByDocumentIDs(
				context.Background(),
				testOwnerScope(t),
				test.documentIDs,
			)
			if !errors.Is(err, test.wantErr) {
				t.Fatalf("error = %v, want %v", err, test.wantErr)
			}
			if finder.findCalls != 0 {
				t.Fatalf("find calls = %d, want 0", finder.findCalls)
			}
		})
	}
}

func TestProcessingJobLatestServicePreservesRepositoryError(t *testing.T) {
	repositoryErr := errors.New("database unavailable")
	finder := &fakeLatestProcessingJobFinder{
		findFunc: func(context.Context, accessdomain.OwnerScope, []int64) ([]documentdomain.ProcessingJob, error) {
			return nil, repositoryErr
		},
	}
	_, err := NewProcessingJobLatestService(finder).GetLatestByDocumentIDs(
		context.Background(),
		testOwnerScope(t),
		[]int64{7},
	)
	if !errors.Is(err, repositoryErr) {
		t.Fatalf("error = %v, want wrapped repository error", err)
	}
}
