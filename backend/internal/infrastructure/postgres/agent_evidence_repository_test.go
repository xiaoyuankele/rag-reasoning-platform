package postgres_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	agentapplication "rag-reasoning-platform/backend/internal/application/agent"
	accessdomain "rag-reasoning-platform/backend/internal/domain/access"
	documentdomain "rag-reasoning-platform/backend/internal/domain/document"
	postgresrepository "rag-reasoning-platform/backend/internal/infrastructure/postgres"
)

func TestAgentEvidenceRepositoryReadsBoundedEvidenceInInputOrder(t *testing.T) {
	if os.Getenv("RUN_DATABASE_TESTS") != "1" {
		t.Skip("set RUN_DATABASE_TESTS=1 to run PostgreSQL integration tests")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	pool := openIsolatedDocumentTestPool(t, ctx)
	documents := newOwnedDocumentFixture(t, ctx, pool)
	chunkRepository := postgresrepository.NewChunkRepository(pool)
	evidenceRepository, err := agentapplication.NewRuntimeEvidenceReader(
		postgresrepository.NewAgentEvidenceRepository(pool),
	)
	if err != nil {
		t.Fatalf("NewRuntimeEvidenceReader() error = %v", err)
	}
	document, err := documents.Create(ctx, documentdomain.CreateInput{
		OriginalName: "fallback.pdf",
		StoragePath:  fmt.Sprintf("agent-evidence/%d.pdf", time.Now().UnixNano()),
		MIMEType:     "application/pdf",
		SizeBytes:    1,
		SHA256:       strings.Repeat("a", 64),
	})
	if err != nil {
		t.Fatalf("create evidence document: %v", err)
	}
	pageOne := 1
	pageTwo := 2
	shortContent := "short trusted evidence"
	longContent := strings.Repeat("证据", 2500)
	if err := chunkRepository.ReplaceForDocument(ctx, document.ID, []documentdomain.ChunkInput{
		{Index: 0, Content: shortContent, PageStart: &pageOne, PageEnd: &pageOne},
		{Index: 1, Content: longContent, PageStart: &pageTwo, PageEnd: &pageTwo},
	}); err != nil {
		t.Fatalf("write evidence chunks: %v", err)
	}
	if _, err := pool.Exec(
		ctx,
		`UPDATE documents SET title = 'Trusted title', status = 'ready' WHERE id = $1`,
		document.ID,
	); err != nil {
		t.Fatalf("ready evidence document: %v", err)
	}
	rows, err := pool.Query(ctx, `SELECT id FROM text_chunks WHERE document_id = $1 ORDER BY chunk_index`, document.ID)
	if err != nil {
		t.Fatalf("query evidence chunk IDs: %v", err)
	}
	chunkIDs := make([]int64, 0, 2)
	for rows.Next() {
		var chunkID int64
		if err := rows.Scan(&chunkID); err != nil {
			rows.Close()
			t.Fatalf("scan evidence chunk ID: %v", err)
		}
		chunkIDs = append(chunkIDs, chunkID)
	}
	rows.Close()
	if len(chunkIDs) != 2 {
		t.Fatalf("chunk IDs = %v", chunkIDs)
	}
	items, err := evidenceRepository.ReadDocumentEvidence(ctx, documents.scope, []agentapplication.EvidenceLocator{
		{EvidenceRef: "step-1:item-2", DocumentID: document.ID, ChunkID: chunkIDs[1]},
		{EvidenceRef: "step-1:item-1", DocumentID: document.ID, ChunkID: chunkIDs[0]},
	})
	if err != nil || len(items) != 2 {
		t.Fatalf("ReadDocumentEvidence() = %+v, %v", items, err)
	}
	longHash := sha256.Sum256([]byte(longContent))
	shortHash := sha256.Sum256([]byte(shortContent))
	if items[0].ChunkID != chunkIDs[1] || items[0].Title != "Trusted title" ||
		items[0].PageStart == nil || *items[0].PageStart != pageTwo ||
		items[0].ContentHash != hex.EncodeToString(longHash[:]) ||
		utf8.RuneCountInString(items[0].Excerpt) > agentapplication.MaxEvidenceExcerptRunes ||
		!strings.HasSuffix(items[0].Excerpt, "…") {
		t.Fatalf("long evidence item = %+v", items[0])
	}
	if items[1].ChunkID != chunkIDs[0] || items[1].Excerpt != shortContent ||
		items[1].ContentHash != hex.EncodeToString(shortHash[:]) {
		t.Fatalf("short evidence item = %+v", items[1])
	}
}

func TestAgentEvidenceRepositoryRejectsUnavailableOrMismatchedEvidence(t *testing.T) {
	if os.Getenv("RUN_DATABASE_TESTS") != "1" {
		t.Skip("set RUN_DATABASE_TESTS=1 to run PostgreSQL integration tests")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	pool := openIsolatedDocumentTestPool(t, ctx)
	ownerA := newOwnedDocumentFixture(t, ctx, pool)
	ownerB := newOwnedDocumentFixture(t, ctx, pool)
	chunkRepository := postgresrepository.NewChunkRepository(pool)
	evidenceRepository, err := agentapplication.NewRuntimeEvidenceReader(
		postgresrepository.NewAgentEvidenceRepository(pool),
	)
	if err != nil {
		t.Fatalf("NewRuntimeEvidenceReader() error = %v", err)
	}
	createDocument := func(owner *ownedDocumentFixture, marker string, ready bool) (int64, int64) {
		t.Helper()
		document, err := owner.Create(ctx, documentdomain.CreateInput{
			OriginalName: marker + ".pdf",
			StoragePath:  fmt.Sprintf("agent-evidence/%s-%d.pdf", marker, time.Now().UnixNano()),
			MIMEType:     "application/pdf",
			SizeBytes:    1,
			SHA256:       strings.Repeat(marker, 64)[:64],
		})
		if err != nil {
			t.Fatalf("create %s document: %v", marker, err)
		}
		if err := chunkRepository.ReplaceForDocument(ctx, document.ID, []documentdomain.ChunkInput{{Index: 0, Content: marker + " evidence"}}); err != nil {
			t.Fatalf("write %s chunk: %v", marker, err)
		}
		if ready {
			if _, err := pool.Exec(ctx, `UPDATE documents SET status = 'ready' WHERE id = $1`, document.ID); err != nil {
				t.Fatalf("ready %s document: %v", marker, err)
			}
		}
		var chunkID int64
		if err := pool.QueryRow(ctx, `SELECT id FROM text_chunks WHERE document_id = $1`, document.ID).Scan(&chunkID); err != nil {
			t.Fatalf("read %s chunk ID: %v", marker, err)
		}
		return document.ID, chunkID
	}
	documentA, chunkA := createDocument(ownerA, "a", true)
	documentB, chunkB := createDocument(ownerA, "b", true)
	nonReadyDocument, nonReadyChunk := createDocument(ownerA, "c", false)
	valid := agentapplication.EvidenceLocator{EvidenceRef: "step-1:item-1", DocumentID: documentA, ChunkID: chunkA}
	validItems, err := evidenceRepository.ReadDocumentEvidence(ctx, ownerA.scope, []agentapplication.EvidenceLocator{valid})
	if err != nil || len(validItems) != 1 || validItems[0].Title != "a.pdf" {
		t.Fatalf("fallback title evidence = %+v, %v", validItems, err)
	}

	for _, test := range []struct {
		name        string
		scopeOwnerB bool
		locator     agentapplication.EvidenceLocator
	}{
		{name: "cross owner", scopeOwnerB: true, locator: valid},
		{name: "wrong document chunk pair", locator: agentapplication.EvidenceLocator{EvidenceRef: "step-1:item-1", DocumentID: documentA, ChunkID: chunkB}},
		{name: "non ready", locator: agentapplication.EvidenceLocator{EvidenceRef: "step-1:item-1", DocumentID: nonReadyDocument, ChunkID: nonReadyChunk}},
		{name: "missing", locator: agentapplication.EvidenceLocator{EvidenceRef: "step-1:item-1", DocumentID: 999999, ChunkID: 999999}},
	} {
		t.Run(test.name, func(t *testing.T) {
			scope := ownerA.scope
			if test.scopeOwnerB {
				scope = ownerB.scope
			}
			if _, err := evidenceRepository.ReadDocumentEvidence(ctx, scope, []agentapplication.EvidenceLocator{test.locator}); !errors.Is(err, agentapplication.ErrEvidenceRefNotFound) {
				t.Fatalf("unavailable evidence error = %v", err)
			}
		})
	}
	if _, err := evidenceRepository.ReadDocumentEvidence(ctx, ownerA.scope, []agentapplication.EvidenceLocator{
		valid,
		{EvidenceRef: "step-1:item-2", DocumentID: documentB, ChunkID: 999999},
	}); !errors.Is(err, agentapplication.ErrEvidenceRefNotFound) {
		t.Fatalf("partial batch error = %v", err)
	}
}

func TestAgentEvidenceRepositoryValidatesBatchInput(t *testing.T) {
	if os.Getenv("RUN_DATABASE_TESTS") != "1" {
		t.Skip("set RUN_DATABASE_TESTS=1 to run PostgreSQL integration tests")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	pool := openIsolatedDocumentTestPool(t, ctx)
	documents := newOwnedDocumentFixture(t, ctx, pool)
	repository, err := agentapplication.NewRuntimeEvidenceReader(
		postgresrepository.NewAgentEvidenceRepository(pool),
	)
	if err != nil {
		t.Fatalf("NewRuntimeEvidenceReader() error = %v", err)
	}
	if _, err := repository.ReadDocumentEvidence(ctx, accessdomain.OwnerScope{}, []agentapplication.EvidenceLocator{{
		EvidenceRef: "step-1:item-1", DocumentID: 1, ChunkID: 1,
	}}); !errors.Is(err, accessdomain.ErrInvalidOwnerScope) {
		t.Fatalf("invalid OwnerScope error = %v", err)
	}
	if _, err := repository.ReadDocumentEvidence(ctx, documents.scope, nil); !errors.Is(err, agentapplication.ErrInvalidToolCall) {
		t.Fatalf("empty batch error = %v", err)
	}
	tooMany := make([]agentapplication.EvidenceLocator, agentapplication.MaxReadEvidenceRefs+1)
	for index := range tooMany {
		tooMany[index] = agentapplication.EvidenceLocator{
			EvidenceRef: fmt.Sprintf("step-1:item-%d", index+1), DocumentID: 1, ChunkID: int64(index + 1),
		}
	}
	if _, err := repository.ReadDocumentEvidence(ctx, documents.scope, tooMany); !errors.Is(err, agentapplication.ErrInvalidToolCall) {
		t.Fatalf("oversized batch error = %v", err)
	}
	if _, err := repository.ReadDocumentEvidence(ctx, documents.scope, []agentapplication.EvidenceLocator{
		{EvidenceRef: "duplicate", DocumentID: 1, ChunkID: 1},
		{EvidenceRef: "duplicate", DocumentID: 1, ChunkID: 2},
	}); !errors.Is(err, agentapplication.ErrInvalidToolCall) {
		t.Fatalf("duplicate ref error = %v", err)
	}
	if _, err := repository.ReadDocumentEvidence(ctx, documents.scope, []agentapplication.EvidenceLocator{
		{EvidenceRef: "invalid", DocumentID: 0, ChunkID: 1},
	}); !errors.Is(err, agentapplication.ErrInvalidToolCall) {
		t.Fatalf("invalid ID error = %v", err)
	}
	if _, err := repository.ReadDocumentEvidence(ctx, documents.scope, []agentapplication.EvidenceLocator{
		{EvidenceRef: " step-1:item-1 ", DocumentID: 1, ChunkID: 1},
	}); !errors.Is(err, agentapplication.ErrInvalidToolCall) {
		t.Fatalf("whitespace ref error = %v", err)
	}
}
