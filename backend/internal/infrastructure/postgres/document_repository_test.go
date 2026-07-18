package postgres_test

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"rag-reasoning-platform/backend/internal/config"
	"rag-reasoning-platform/backend/internal/domain/document"
	"rag-reasoning-platform/backend/internal/infrastructure/database"
	postgresrepository "rag-reasoning-platform/backend/internal/infrastructure/postgres"
)

// TestDocumentRepositoryCreateAndGetByID 使用真实 PostgreSQL 验证仓储。
func TestDocumentRepositoryCreateAndGetByID(t *testing.T) {
	// 普通 go test 默认跳过真实数据库测试。
	// 只有显式设置 RUN_DATABASE_TESTS=1 时才执行。
	if os.Getenv("RUN_DATABASE_TESTS") != "1" {
		t.Skip("set RUN_DATABASE_TESTS=1 to run PostgreSQL integration tests")
	}

	// 为整项集成测试设置 10 秒上限。
	ctx, cancel := context.WithTimeout(
		context.Background(),
		10*time.Second,
	)
	defer cancel()

	databaseConfig, err := config.LoadDatabase()
	if err != nil {
		t.Fatalf("load database configuration: %v", err)
	}

	pool, err := database.Open(
		ctx,
		databaseConfig.ConnectionString(),
	)
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	defer pool.Close()

	repository := postgresrepository.NewDocumentRepository(pool)

	// 使用纳秒时间生成唯一存储路径，避免多次运行测试时冲突。
	storagePath := fmt.Sprintf(
		"integration-tests/document-%d.pdf",
		time.Now().UnixNano(),
	)

	input := document.CreateInput{
		OriginalName: "integration-test.pdf",
		StoragePath:  storagePath,
		MIMEType:     "application/pdf",
		SizeBytes:    1024,
		SHA256:       strings.Repeat("a", 64),
	}

	savedDocument, err := repository.Create(ctx, input)
	if err != nil {
		t.Fatalf("create document: %v", err)
	}

	// 测试结束前删除本次创建的数据。
	// 这个 defer 比 pool.Close 后注册，因此会先删除，再关闭连接池。
	defer func() {
		cleanupContext, cleanupCancel := context.WithTimeout(
			context.Background(),
			5*time.Second,
		)
		defer cleanupCancel()

		_, cleanupErr := pool.Exec(
			cleanupContext,
			"DELETE FROM documents WHERE id = $1",
			savedDocument.ID,
		)
		if cleanupErr != nil {
			t.Errorf("clean up test document: %v", cleanupErr)
		}
	}()

	if savedDocument.ID <= 0 {
		t.Fatalf("expected a generated ID, got %d", savedDocument.ID)
	}

	if savedDocument.Status != document.StatusUploaded {
		t.Fatalf(
			"expected status %q, got %q",
			document.StatusUploaded,
			savedDocument.Status,
		)
	}

	if savedDocument.ErrorMessage != nil {
		t.Fatal("expected error message to be nil")
	}

	if savedDocument.CreatedAt.IsZero() || savedDocument.UpdatedAt.IsZero() {
		t.Fatal("expected database-generated timestamps")
	}

	foundDocument, err := repository.GetByID(ctx, savedDocument.ID)
	if err != nil {
		t.Fatalf("get document by ID: %v", err)
	}

	if foundDocument.ID != savedDocument.ID {
		t.Fatalf(
			"expected document ID %d, got %d",
			savedDocument.ID,
			foundDocument.ID,
		)
	}

	if foundDocument.StoragePath != input.StoragePath {
		t.Fatalf(
			"expected storage path %q, got %q",
			input.StoragePath,
			foundDocument.StoragePath,
		)
	}

	// 使用不存在的负数 ID 验证 pgx.ErrNoRows 已转换为领域错误。
	_, err = repository.GetByID(ctx, -1)
	if !errors.Is(err, document.ErrNotFound) {
		t.Fatalf(
			"expected ErrNotFound, got %v",
			err,
		)
	}
}
