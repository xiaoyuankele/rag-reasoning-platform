package pythonprocessor

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	documentapplication "rag-reasoning-platform/backend/internal/application/document"
	documentdomain "rag-reasoning-platform/backend/internal/domain/document"
)

type storedFileMaterializerFunc func(
	ctx context.Context,
	storagePath string,
) (string, func() error, error)

func (f storedFileMaterializerFunc) Materialize(
	ctx context.Context,
	storagePath string,
) (string, func() error, error) {
	return f(ctx, storagePath)
}

func TestMaterializeSourceFileRejectsInvalidImplementationContract(t *testing.T) {
	tests := []struct {
		name             string
		localPath        string
		release          func() error
		wantErr          error
		wantReleaseCalls int
	}{
		{
			name:             "relative path is rejected and released",
			localPath:        "temporary/document.pdf",
			wantErr:          ErrMaterializedSourcePathInvalid,
			wantReleaseCalls: 1,
		},
		{
			name:             "release function is required",
			localPath:        filepath.Join(t.TempDir(), "document.pdf"),
			release:          nil,
			wantErr:          ErrMaterializedSourceReleaseRequired,
			wantReleaseCalls: 0,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			releaseCalls := 0
			release := test.release
			if test.wantReleaseCalls > 0 {
				release = func() error {
					releaseCalls++
					return nil
				}
			}

			materializer := storedFileMaterializerFunc(func(
				context.Context,
				string,
			) (string, func() error, error) {
				return test.localPath, release, nil
			})

			_, _, err := materializeSourceFile(
				context.Background(),
				materializer,
				"documents/source.pdf",
			)
			if !errors.Is(err, test.wantErr) {
				t.Fatalf("materializeSourceFile() error = %v, want %v", err, test.wantErr)
			}
			if releaseCalls != test.wantReleaseCalls {
				t.Fatalf(
					"release calls = %d, want %d",
					releaseCalls,
					test.wantReleaseCalls,
				)
			}
		})
	}
}

func TestReleaseMaterializedSourceJoinsProcessingAndCleanupErrors(t *testing.T) {
	processingFailure := errors.New("processing failed")
	cleanupFailure := errors.New("cleanup failed")
	result := documentapplication.ProcessingResult{
		Chunks: []documentdomain.ChunkInput{{Index: 0, Content: "content"}},
	}
	processingErr := processingFailure

	releaseMaterializedSource(
		&result,
		&processingErr,
		func() error { return cleanupFailure },
	)

	if !errors.Is(processingErr, processingFailure) {
		t.Fatalf("error = %v, want processing failure", processingErr)
	}
	if !errors.Is(processingErr, cleanupFailure) {
		t.Fatalf("error = %v, want cleanup failure", processingErr)
	}
	if len(result.Chunks) != 0 {
		t.Fatalf("chunks = %+v, want empty after cleanup failure", result.Chunks)
	}
}
