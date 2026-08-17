package database_test

import (
	"context"
	"fmt"
	"io/fs"
	"os"
	"testing"
	"testing/fstest"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"rag-reasoning-platform/backend/internal/config"
	"rag-reasoning-platform/backend/internal/infrastructure/database"
	"rag-reasoning-platform/backend/migrations"
)

// TestRequireDocumentOwnerMigrationRejectsLegacyUnownedRows 验证 Release B 门禁：
// 只要旧数据库仍有无主文档，000012 就必须失败且不能登记为已应用。
func TestRequireDocumentOwnerMigrationRejectsLegacyUnownedRows(t *testing.T) {
	if os.Getenv("RUN_DATABASE_TESTS") != "1" {
		t.Skip("set RUN_DATABASE_TESTS=1 to run PostgreSQL integration tests")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	databaseConfig, err := config.LoadDatabase()
	if err != nil {
		t.Fatalf("load database configuration: %v", err)
	}

	adminPool, err := database.Open(ctx, databaseConfig.ConnectionString())
	if err != nil {
		t.Fatalf("open admin database connection: %v", err)
	}
	// Cleanup 按后进先出执行：测试连接池关闭、schema 删除，最后关闭管理连接池。
	t.Cleanup(adminPool.Close)

	schemaName := fmt.Sprintf("owner_required_migration_test_%d", time.Now().UnixNano())
	if _, err := adminPool.Exec(
		ctx,
		fmt.Sprintf(`CREATE SCHEMA "%s"`, schemaName),
	); err != nil {
		t.Fatalf("create owner migration test schema: %v", err)
	}
	t.Cleanup(func() {
		cleanupContext, cleanupCancel := context.WithTimeout(
			context.Background(),
			5*time.Second,
		)
		defer cleanupCancel()
		if _, cleanupErr := adminPool.Exec(
			cleanupContext,
			fmt.Sprintf(`DROP SCHEMA "%s" CASCADE`, schemaName),
		); cleanupErr != nil {
			t.Errorf("drop owner migration test schema: %v", cleanupErr)
		}
	})

	poolConfig, err := pgxpool.ParseConfig(databaseConfig.ConnectionString())
	if err != nil {
		t.Fatalf("parse owner migration test pool: %v", err)
	}
	poolConfig.MaxConns = 1
	poolConfig.ConnConfig.RuntimeParams["search_path"] = schemaName
	testPool, err := pgxpool.NewWithConfig(ctx, poolConfig)
	if err != nil {
		t.Fatalf("create owner migration test pool: %v", err)
	}
	t.Cleanup(testPool.Close)

	if _, err := testPool.Exec(
		ctx,
		`CREATE TABLE documents (
			id BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
			owner_user_id BIGINT
		)`,
	); err != nil {
		t.Fatalf("create simulated legacy documents table: %v", err)
	}
	if _, err := testPool.Exec(
		ctx,
		"INSERT INTO documents (owner_user_id) VALUES (NULL)",
	); err != nil {
		t.Fatalf("insert simulated legacy unowned document: %v", err)
	}

	migrationSQL, err := fs.ReadFile(
		migrations.Files,
		"000012_require_document_owner.up.sql",
	)
	if err != nil {
		t.Fatalf("read embedded owner requirement migration: %v", err)
	}
	onlyReleaseB := fstest.MapFS{
		"000012_require_document_owner.up.sql": &fstest.MapFile{Data: migrationSQL},
	}

	if err := database.Migrate(ctx, testPool, onlyReleaseB); err == nil {
		t.Fatal("Migrate() accepted legacy owner_user_id NULL, want failure")
	}

	var migrationRecorded bool
	if err := testPool.QueryRow(
		ctx,
		"SELECT EXISTS (SELECT 1 FROM schema_migrations WHERE version = 12)",
	).Scan(&migrationRecorded); err != nil {
		t.Fatalf("query failed migration record: %v", err)
	}
	if migrationRecorded {
		t.Fatal("failed owner requirement migration was recorded as applied")
	}

	var isNullable string
	if err := testPool.QueryRow(
		ctx,
		`
			SELECT is_nullable
			FROM information_schema.columns
			WHERE table_schema = current_schema()
			  AND table_name = 'documents'
			  AND column_name = 'owner_user_id'
		`,
	).Scan(&isNullable); err != nil {
		t.Fatalf("query owner column after failed migration: %v", err)
	}
	if isNullable != "YES" {
		t.Fatalf("owner_user_id nullable = %q after failure, want YES", isNullable)
	}

	var documentCount int64
	if err := testPool.QueryRow(
		ctx,
		"SELECT COUNT(*) FROM documents WHERE owner_user_id IS NULL",
	).Scan(&documentCount); err != nil {
		t.Fatalf("count legacy document after failed migration: %v", err)
	}
	if documentCount != 1 {
		t.Fatalf("unowned documents after failed migration = %d, want 1", documentCount)
	}
}
