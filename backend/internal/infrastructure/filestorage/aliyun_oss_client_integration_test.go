package filestorage

import (
	"context"
	"errors"
	"io"
	"os"
	"strings"
	"testing"
	"time"
)

// TestAliyunOSSObjectStorageIntegration 显式验证官方 Go SDK、ObjectStorage
// 和真实私有 Bucket 的保存、读取、物化、释放与删除链路。
//
// 默认测试套件不会产生云请求；只有显式设置 RUN_OSS_INTEGRATION_TESTS=1
// 才运行。测试使用随机 documents/* 对象，并在失败时兜底清理。
func TestAliyunOSSObjectStorageIntegration(t *testing.T) {
	if os.Getenv("RUN_OSS_INTEGRATION_TESTS") != "1" {
		t.Skip("set RUN_OSS_INTEGRATION_TESTS=1 to run real Aliyun OSS integration test")
	}

	clientConfig := AliyunOSSClientConfig{
		Bucket:         requireOSSIntegrationEnvironment(t, "OSS_BUCKET"),
		Region:         requireOSSIntegrationEnvironment(t, "OSS_REGION"),
		Endpoint:       requireOSSIntegrationEnvironment(t, "OSS_ENDPOINT"),
		CredentialMode: requireOSSIntegrationEnvironment(t, "OSS_CREDENTIAL_MODE"),
		ECSRAMRole:     strings.TrimSpace(os.Getenv("OSS_ECS_RAM_ROLE")),
	}
	client, err := NewAliyunOSSObjectClient(clientConfig)
	if err != nil {
		t.Fatalf("NewAliyunOSSObjectClient() error = %v, want nil", err)
	}
	storage, err := NewObjectStorage(client, t.TempDir(), 1024*1024)
	if err != nil {
		t.Fatalf("NewObjectStorage() error = %v, want nil", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	content := "# OSS acceptance\n\nGo SDK real bucket integration.\n"
	storedFile, err := storage.Save(
		ctx,
		"oss-acceptance.md",
		strings.NewReader(content),
	)
	if err != nil {
		t.Fatalf("Save() error = %v, want nil", err)
	}
	t.Logf("created OSS acceptance object %s", storedFile.StoragePath)
	t.Cleanup(func() {
		cleanupContext, cleanupCancel := context.WithTimeout(
			context.Background(),
			15*time.Second,
		)
		defer cleanupCancel()
		if err := storage.Delete(cleanupContext, storedFile.StoragePath); err != nil {
			t.Errorf("cleanup Delete() error = %v", err)
		}
	})

	reader, err := storage.Open(ctx, storedFile.StoragePath)
	if err != nil {
		t.Fatalf("Open() error = %v, want nil", err)
	}
	readContent, readErr := io.ReadAll(reader)
	closeErr := reader.Close()
	if readErr != nil || closeErr != nil {
		t.Fatalf("read/close stored object errors = %v / %v", readErr, closeErr)
	}
	if string(readContent) != content {
		t.Fatalf("Open() content = %q, want %q", readContent, content)
	}

	localPath, release, err := storage.Materialize(ctx, storedFile.StoragePath)
	if err != nil {
		t.Fatalf("Materialize() error = %v, want nil", err)
	}
	materializedContent, err := os.ReadFile(localPath)
	if err != nil {
		t.Fatalf("ReadFile(materialized) error = %v, want nil", err)
	}
	if string(materializedContent) != content {
		t.Fatalf("materialized content = %q, want %q", materializedContent, content)
	}
	if err := release(); err != nil {
		t.Fatalf("release() error = %v, want nil", err)
	}
	if _, err := os.Stat(localPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("Stat(materialized) error = %v, want os.ErrNotExist", err)
	}

	if err := storage.Delete(ctx, storedFile.StoragePath); err != nil {
		t.Fatalf("Delete() error = %v, want nil", err)
	}
	if _, err := storage.Open(ctx, storedFile.StoragePath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("Open(after delete) error = %v, want os.ErrNotExist", err)
	}
}

func requireOSSIntegrationEnvironment(t *testing.T, name string) string {
	t.Helper()
	value := strings.TrimSpace(os.Getenv(name))
	if value == "" {
		t.Fatalf("%s must be configured for real OSS integration test", name)
	}
	return value
}
