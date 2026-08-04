package integration_test

import (
	"context"
	"reflect"
	"strings"
	"testing"

	applicationdocument "rag-reasoning-platform/backend/internal/application/document"
	documentdomain "rag-reasoning-platform/backend/internal/domain/document"
	"rag-reasoning-platform/backend/internal/infrastructure/filestorage"
)

// TestTextProcessorWithLocalStorage 验证应用层文本处理器可以通过
// StoredFileOpener 接口使用真实本地文件存储，而不依赖具体磁盘类型。
func TestTextProcessorWithLocalStorage(t *testing.T) {
	ctx := context.Background()
	storage, err := filestorage.NewLocalStorage(t.TempDir(), 1024)
	if err != nil {
		t.Fatalf("create local storage: %v", err)
	}

	storedFile, err := storage.Save(
		ctx,
		"notes.md",
		strings.NewReader("\uFEFF# 标题\r\n\r\n第一段\t第二段"),
	)
	if err != nil {
		t.Fatalf("save Markdown file: %v", err)
	}

	processor := applicationdocument.NewTextProcessor(storage)
	result, err := processor.Process(ctx, documentdomain.Document{
		StoragePath: storedFile.StoragePath,
		MIMEType:    storedFile.MIMEType,
	})
	if err != nil {
		t.Fatalf("process stored Markdown file: %v", err)
	}

	want := []documentdomain.ChunkInput{
		{Index: 0, Content: "# 标题 第一段 第二段"},
	}
	if !reflect.DeepEqual(result.Chunks, want) {
		t.Fatalf("Process() chunks = %+v, want %+v", result.Chunks, want)
	}

	// Windows 不允许删除仍被占用的文件；删除成功也证明 Process 已经关闭文件。
	if err := storage.Delete(ctx, storedFile.StoragePath); err != nil {
		t.Fatalf("delete processed Markdown file: %v", err)
	}
}
