package database_test

import (
	"context"
	"fmt"
	"os"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"rag-reasoning-platform/backend/internal/config"
	"rag-reasoning-platform/backend/internal/infrastructure/database"
	"rag-reasoning-platform/backend/migrations"
)

func TestMigrateAppliesEmbeddedMigrationsOnce(t *testing.T) {
	if os.Getenv("RUN_DATABASE_TESTS") != "1" {
		t.Skip("set RUN_DATABASE_TESTS=1 to run PostgreSQL integration tests")
	}

	ctx, cancel := context.WithTimeout(
		context.Background(),
		20*time.Second,
	)
	defer cancel()

	databaseConfig, err := config.LoadDatabase()
	if err != nil {
		t.Fatalf("load database configuration: %v", err)
	}

	adminPool, err := database.Open(
		ctx,
		databaseConfig.ConnectionString(),
	)
	if err != nil {
		t.Fatalf("open admin database connection: %v", err)
	}
	defer adminPool.Close()

	// 每次测试使用独立 schema，不接触开发库中的真实 documents 表。
	schemaName := fmt.Sprintf(
		"migration_test_%d",
		time.Now().UnixNano(),
	)
	if _, err := adminPool.Exec(
		ctx,
		fmt.Sprintf(`CREATE SCHEMA "%s"`, schemaName),
	); err != nil {
		t.Fatalf("create migration test schema: %v", err)
	}
	defer func() {
		cleanupContext, cleanupCancel := context.WithTimeout(
			context.Background(),
			5*time.Second,
		)
		defer cleanupCancel()

		if _, cleanupErr := adminPool.Exec(
			cleanupContext,
			fmt.Sprintf(`DROP SCHEMA "%s" CASCADE`, schemaName),
		); cleanupErr != nil {
			t.Errorf("drop migration test schema: %v", cleanupErr)
		}
	}()

	poolConfig, err := pgxpool.ParseConfig(
		databaseConfig.ConnectionString(),
	)
	if err != nil {
		t.Fatalf("parse test pool configuration: %v", err)
	}
	poolConfig.MaxConns = 2
	poolConfig.ConnConfig.RuntimeParams["search_path"] = schemaName

	testPool, err := pgxpool.NewWithConfig(ctx, poolConfig)
	if err != nil {
		t.Fatalf("create migration test pool: %v", err)
	}
	defer testPool.Close()

	if err := testPool.Ping(ctx); err != nil {
		t.Fatalf("ping migration test pool: %v", err)
	}

	if err := database.Migrate(ctx, testPool, migrations.Files); err != nil {
		t.Fatalf("first Migrate() error = %v, want nil", err)
	}

	// 第二次执行必须安全跳过已经应用且校验和一致的迁移。
	if err := database.Migrate(ctx, testPool, migrations.Files); err != nil {
		t.Fatalf("second Migrate() error = %v, want nil", err)
	}

	var documentsTable string
	if err := testPool.QueryRow(
		ctx,
		"SELECT to_regclass('documents')::text",
	).Scan(&documentsTable); err != nil {
		t.Fatalf("query documents table: %v", err)
	}
	if documentsTable != "documents" {
		t.Fatalf(
			"documents table = %q, want %q",
			documentsTable,
			"documents",
		)
	}

	var appliedCount int
	if err := testPool.QueryRow(
		ctx,
		"SELECT COUNT(*) FROM schema_migrations",
	).Scan(&appliedCount); err != nil {
		t.Fatalf("count schema migrations: %v", err)
	}
	if appliedCount != 2 {
		t.Fatalf(
			"applied migration count = %d, want 2",
			appliedCount,
		)
	}

	changedMigration := fstest.MapFS{
		"000001_create_documents.up.sql": {
			Data: []byte("SELECT 1;"),
		},
	}
	err = database.Migrate(ctx, testPool, changedMigration)
	if err == nil || !strings.Contains(err.Error(), "changed after it was applied") {
		t.Fatalf(
			"Migrate() changed-file error = %v, want checksum error",
			err,
		)
	}
}
