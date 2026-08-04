package document

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	documentdomain "rag-reasoning-platform/backend/internal/domain/document"
)

func TestChunkTextNormalizesAndIndexesContent(t *testing.T) {
	chunks, err := chunkText(
		context.Background(),
		strings.NewReader("\uFEFF  first\r\n\r\nsecond\tpart  "),
		100,
	)
	if err != nil {
		t.Fatalf("chunkText() error = %v, want nil", err)
	}

	want := []documentdomain.ChunkInput{
		{Index: 0, Content: "first second part"},
	}
	if !reflect.DeepEqual(chunks, want) {
		t.Fatalf("chunkText() = %+v, want %+v", chunks, want)
	}
}

func TestChunkTextSplitsByUnicodeRunes(t *testing.T) {
	chunks, err := chunkText(
		context.Background(),
		strings.NewReader("你好世界测试"),
		4,
	)
	if err != nil {
		t.Fatalf("chunkText() error = %v, want nil", err)
	}

	want := []documentdomain.ChunkInput{
		{Index: 0, Content: "你好世界"},
		{Index: 1, Content: "测试"},
	}
	if !reflect.DeepEqual(chunks, want) {
		t.Fatalf("chunkText() = %+v, want %+v", chunks, want)
	}
}

func TestChunkTextAvoidsBoundaryWhitespace(t *testing.T) {
	chunks, err := chunkText(
		context.Background(),
		strings.NewReader("one two three"),
		7,
	)
	if err != nil {
		t.Fatalf("chunkText() error = %v, want nil", err)
	}

	want := []documentdomain.ChunkInput{
		{Index: 0, Content: "one two"},
		{Index: 1, Content: "three"},
	}
	if !reflect.DeepEqual(chunks, want) {
		t.Fatalf("chunkText() = %+v, want %+v", chunks, want)
	}
}

func TestChunkTextRejectsEmptyContent(t *testing.T) {
	chunks, err := chunkText(
		context.Background(),
		strings.NewReader(" \r\n\t "),
		100,
	)
	if chunks != nil {
		t.Fatalf("chunkText() chunks = %+v, want nil", chunks)
	}
	if !errors.Is(err, ErrEmptyTextDocument) {
		t.Fatalf("chunkText() error = %v, want ErrEmptyTextDocument", err)
	}
}

func TestChunkTextRejectsInvalidUTF8(t *testing.T) {
	chunks, err := chunkText(
		context.Background(),
		strings.NewReader(string([]byte{0xff})),
		100,
	)
	if chunks != nil {
		t.Fatalf("chunkText() chunks = %+v, want nil", chunks)
	}
	if !errors.Is(err, ErrInvalidTextContent) {
		t.Fatalf("chunkText() error = %v, want ErrInvalidTextContent", err)
	}
}

func TestChunkTextHonorsContextCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	chunks, err := chunkText(ctx, strings.NewReader("content"), 100)
	if chunks != nil {
		t.Fatalf("chunkText() chunks = %+v, want nil", chunks)
	}
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("chunkText() error = %v, want context.Canceled", err)
	}
}

func TestChunkTextRejectsInvalidRuneLimit(t *testing.T) {
	chunks, err := chunkText(
		context.Background(),
		strings.NewReader("content"),
		0,
	)
	if chunks != nil {
		t.Fatalf("chunkText() chunks = %+v, want nil", chunks)
	}
	if !errors.Is(err, errInvalidTextChunkRuneLimit) {
		t.Fatalf("chunkText() error = %v, want errInvalidTextChunkRuneLimit", err)
	}
}
