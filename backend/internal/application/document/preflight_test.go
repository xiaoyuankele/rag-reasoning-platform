package document

import (
	"context"
	"errors"
	"strings"
	"testing"

	accessdomain "rag-reasoning-platform/backend/internal/domain/access"
	documentdomain "rag-reasoning-platform/backend/internal/domain/document"
)

type fakePreflightRepository struct {
	findFunc  func(context.Context, accessdomain.OwnerScope, string, int64) (documentdomain.Document, error)
	findCalls int
}

func (f *fakePreflightRepository) FindBySHA256AndSize(
	ctx context.Context,
	scope accessdomain.OwnerScope,
	sha256 string,
	sizeBytes int64,
) (documentdomain.Document, error) {
	f.findCalls++
	return f.findFunc(ctx, scope, sha256, sizeBytes)
}

func TestPreflightServiceReturnsExistingDocument(t *testing.T) {
	const ownerID int64 = 17
	expectedHash := strings.Repeat("a", 64)
	expectedDocument := documentdomain.Document{
		ID:           91,
		OwnerUserID:  ownerID,
		OriginalName: "existing.pdf",
		SizeBytes:    2048,
		SHA256:       expectedHash,
		Status:       documentdomain.StatusReady,
	}
	repository := &fakePreflightRepository{
		findFunc: func(
			_ context.Context,
			scope accessdomain.OwnerScope,
			sha256 string,
			sizeBytes int64,
		) (documentdomain.Document, error) {
			if scope.OwnerUserID() != ownerID {
				t.Fatalf("owner ID = %d, want %d", scope.OwnerUserID(), ownerID)
			}
			if sha256 != expectedHash || sizeBytes != expectedDocument.SizeBytes {
				t.Fatalf("lookup = (%q, %d), want (%q, %d)", sha256, sizeBytes, expectedHash, expectedDocument.SizeBytes)
			}
			return expectedDocument, nil
		},
	}
	scope, err := accessdomain.NewOwnerScope(ownerID)
	if err != nil {
		t.Fatalf("create owner scope: %v", err)
	}

	result, err := NewPreflightService(repository, 4096).Check(
		context.Background(),
		scope,
		PreflightInput{SHA256: expectedHash, SizeBytes: 2048},
	)
	if err != nil {
		t.Fatalf("Check() error = %v", err)
	}
	if !result.Exists || result.Document.ID != expectedDocument.ID {
		t.Fatalf("Check() result = %+v, want existing document", result)
	}
	if repository.findCalls != 1 {
		t.Fatalf("FindBySHA256AndSize() calls = %d, want 1", repository.findCalls)
	}
}

func TestPreflightServiceReturnsNotExistingForRepositoryMiss(t *testing.T) {
	repository := &fakePreflightRepository{
		findFunc: func(
			context.Context,
			accessdomain.OwnerScope,
			string,
			int64,
		) (documentdomain.Document, error) {
			return documentdomain.Document{}, documentdomain.ErrNotFound
		},
	}
	scope, err := accessdomain.NewOwnerScope(21)
	if err != nil {
		t.Fatalf("create owner scope: %v", err)
	}

	result, err := NewPreflightService(repository, 4096).Check(
		context.Background(),
		scope,
		PreflightInput{SHA256: strings.Repeat("b", 64), SizeBytes: 1024},
	)
	if err != nil {
		t.Fatalf("Check() error = %v", err)
	}
	if result.Exists || result.Document.ID != 0 {
		t.Fatalf("Check() result = %+v, want exists=false", result)
	}
}

func TestPreflightServiceRejectsInvalidInputBeforeRepository(t *testing.T) {
	tests := []struct {
		name      string
		input     PreflightInput
		wantError error
	}{
		{name: "empty hash", input: PreflightInput{SizeBytes: 1}, wantError: ErrInvalidPreflightSHA256},
		{name: "short hash", input: PreflightInput{SHA256: "abcd", SizeBytes: 1}, wantError: ErrInvalidPreflightSHA256},
		{name: "uppercase hash", input: PreflightInput{SHA256: strings.Repeat("A", 64), SizeBytes: 1}, wantError: ErrInvalidPreflightSHA256},
		{name: "non hexadecimal hash", input: PreflightInput{SHA256: strings.Repeat("g", 64), SizeBytes: 1}, wantError: ErrInvalidPreflightSHA256},
		{name: "zero size", input: PreflightInput{SHA256: strings.Repeat("c", 64)}, wantError: ErrInvalidPreflightSize},
		{name: "negative size", input: PreflightInput{SHA256: strings.Repeat("c", 64), SizeBytes: -1}, wantError: ErrInvalidPreflightSize},
		{name: "file too large", input: PreflightInput{SHA256: strings.Repeat("c", 64), SizeBytes: 4097}, wantError: ErrFileTooLarge},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			repository := &fakePreflightRepository{
				findFunc: func(context.Context, accessdomain.OwnerScope, string, int64) (documentdomain.Document, error) {
					t.Fatal("repository must not be called for invalid input")
					return documentdomain.Document{}, nil
				},
			}

			_, err := NewPreflightService(repository, 4096).Check(
				context.Background(),
				accessdomain.OwnerScope{},
				test.input,
			)
			if !errors.Is(err, test.wantError) {
				t.Fatalf("Check() error = %v, want %v", err, test.wantError)
			}
			if repository.findCalls != 0 {
				t.Fatalf("repository calls = %d, want 0", repository.findCalls)
			}
		})
	}
}

func TestPreflightServiceWrapsRepositoryError(t *testing.T) {
	repositoryError := errors.New("database unavailable")
	repository := &fakePreflightRepository{
		findFunc: func(context.Context, accessdomain.OwnerScope, string, int64) (documentdomain.Document, error) {
			return documentdomain.Document{}, repositoryError
		},
	}
	scope, err := accessdomain.NewOwnerScope(23)
	if err != nil {
		t.Fatalf("create owner scope: %v", err)
	}

	_, err = NewPreflightService(repository, 4096).Check(
		context.Background(),
		scope,
		PreflightInput{SHA256: strings.Repeat("d", 64), SizeBytes: 1},
	)
	if !errors.Is(err, repositoryError) {
		t.Fatalf("Check() error = %v, want wrapped repository error", err)
	}
}
