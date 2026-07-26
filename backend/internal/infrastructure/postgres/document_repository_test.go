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

// TestDocumentRepositoryDelete 使用真实 PostgreSQL 验证删除成功，
// 并验证重复删除同一 ID 时返回稳定的领域错误。
func TestDocumentRepositoryDelete(t *testing.T) {
	if os.Getenv("RUN_DATABASE_TESTS") != "1" {
		t.Skip("set RUN_DATABASE_TESTS=1 to run PostgreSQL integration tests")
	}

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
	storagePath := fmt.Sprintf(
		"integration-tests/delete-%d.pdf",
		time.Now().UnixNano(),
	)

	savedDocument, err := repository.Create(
		ctx,
		document.CreateInput{
			OriginalName: "integration-delete.pdf",
			StoragePath:  storagePath,
			MIMEType:     "application/pdf",
			SizeBytes:    2048,
			SHA256:       strings.Repeat("d", 64),
		},
	)
	if err != nil {
		t.Fatalf("create document for deletion: %v", err)
	}

	// 即使测试中途失败，也只清理本测试创建的记录。
	defer func() {
		cleanupContext, cleanupCancel := context.WithTimeout(
			context.Background(),
			5*time.Second,
		)
		defer cleanupCancel()

		if _, cleanupErr := pool.Exec(
			cleanupContext,
			"DELETE FROM documents WHERE id = $1",
			savedDocument.ID,
		); cleanupErr != nil {
			t.Errorf("clean up delete test document: %v", cleanupErr)
		}
	}()

	if err := repository.Delete(ctx, savedDocument.ID); err != nil {
		t.Fatalf("delete document: %v", err)
	}

	_, err = repository.GetByID(ctx, savedDocument.ID)
	if !errors.Is(err, document.ErrNotFound) {
		t.Fatalf(
			"GetByID() after Delete() error = %v, want ErrNotFound",
			err,
		)
	}

	err = repository.Delete(ctx, savedDocument.ID)
	if !errors.Is(err, document.ErrNotFound) {
		t.Fatalf(
			"second Delete() error = %v, want ErrNotFound",
			err,
		)
	}
}

// TestDocumentRepositoryList 使用真实 PostgreSQL 验证总数、分页、稳定排序
// 以及超过末页时返回空切片。测试只清理自己创建的记录。
func TestDocumentRepositoryList(t *testing.T) {
	if os.Getenv("RUN_DATABASE_TESTS") != "1" {
		t.Skip("set RUN_DATABASE_TESTS=1 to run PostgreSQL integration tests")
	}

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

	// 先读取已有记录数，使测试不依赖数据库必须为空。
	var originalTotal int64
	if err := pool.QueryRow(
		ctx,
		"SELECT COUNT(*) FROM documents",
	).Scan(&originalTotal); err != nil {
		t.Fatalf("count existing documents: %v", err)
	}

	createdDocuments := make([]document.Document, 0, 3)
	defer func() {
		cleanupContext, cleanupCancel := context.WithTimeout(
			context.Background(),
			5*time.Second,
		)
		defer cleanupCancel()

		for _, createdDocument := range createdDocuments {
			_, cleanupErr := pool.Exec(
				cleanupContext,
				"DELETE FROM documents WHERE id = $1",
				createdDocument.ID,
			)
			if cleanupErr != nil {
				t.Errorf(
					"clean up test document %d: %v",
					createdDocument.ID,
					cleanupErr,
				)
			}
		}
	}()

	uniquePrefix := time.Now().UnixNano()
	for index := 0; index < 3; index++ {
		createdDocument, err := repository.Create(
			ctx,
			document.CreateInput{
				OriginalName: fmt.Sprintf(
					"integration-list-%d.pdf",
					index+1,
				),
				StoragePath: fmt.Sprintf(
					"integration-tests/list-%d-%d.pdf",
					uniquePrefix,
					index+1,
				),
				MIMEType:  "application/pdf",
				SizeBytes: int64(index + 1),
				SHA256: strings.Repeat(
					string(rune('a'+index)),
					64,
				),
			},
		)
		if err != nil {
			t.Fatalf("create list test document %d: %v", index+1, err)
		}

		createdDocuments = append(createdDocuments, createdDocument)
	}

	firstPage, err := repository.List(
		ctx,
		document.ListOptions{Limit: 2, Offset: 0},
	)
	if err != nil {
		t.Fatalf("list first page: %v", err)
	}

	expectedTotal := originalTotal + int64(len(createdDocuments))
	if firstPage.Total != expectedTotal {
		t.Fatalf(
			"expected total %d, got %d",
			expectedTotal,
			firstPage.Total,
		)
	}

	if len(firstPage.Documents) != 2 {
		t.Fatalf(
			"expected 2 documents on first page, got %d",
			len(firstPage.Documents),
		)
	}

	// 三条测试记录最后创建的 ID 最大；created_at 相同的情况下，
	// SQL 中的 id DESC 仍会保证顺序稳定。
	if firstPage.Documents[0].ID != createdDocuments[2].ID ||
		firstPage.Documents[1].ID != createdDocuments[1].ID {
		t.Fatalf(
			"unexpected first-page order: got IDs %d, %d",
			firstPage.Documents[0].ID,
			firstPage.Documents[1].ID,
		)
	}

	emptyPage, err := repository.List(
		ctx,
		document.ListOptions{
			Limit:  2,
			Offset: expectedTotal + 10,
		},
	)
	if err != nil {
		t.Fatalf("list page beyond end: %v", err)
	}

	if emptyPage.Total != expectedTotal {
		t.Fatalf(
			"expected empty-page total %d, got %d",
			expectedTotal,
			emptyPage.Total,
		)
	}

	if emptyPage.Documents == nil {
		t.Fatal("expected an empty slice, got nil")
	}

	if len(emptyPage.Documents) != 0 {
		t.Fatalf(
			"expected no documents beyond last page, got %d",
			len(emptyPage.Documents),
		)
	}
}
