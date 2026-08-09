package database_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"fmt"
	"io/fs"
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

	var textChunksTable string
	if err := testPool.QueryRow(
		ctx,
		"SELECT to_regclass('text_chunks')::text",
	).Scan(&textChunksTable); err != nil {
		t.Fatalf("query text_chunks table: %v", err)
	}
	if textChunksTable != "text_chunks" {
		t.Fatalf(
			"text_chunks table = %q, want %q",
			textChunksTable,
			"text_chunks",
		)
	}

	var hasDocumentChunkIndexConstraint bool
	if err := testPool.QueryRow(
		ctx,
		`
			SELECT EXISTS (
				SELECT 1
				FROM pg_constraint
				WHERE conname = 'uq_text_chunks_document_index'
				  AND conrelid = 'text_chunks'::regclass
			)
		`,
	).Scan(&hasDocumentChunkIndexConstraint); err != nil {
		t.Fatalf("query text_chunks unique constraint: %v", err)
	}
	if !hasDocumentChunkIndexConstraint {
		t.Fatal("text_chunks document/index unique constraint was not created")
	}

	var hasCascadeForeignKey bool
	if err := testPool.QueryRow(
		ctx,
		`
			SELECT EXISTS (
				SELECT 1
				FROM pg_constraint
				WHERE conrelid = 'text_chunks'::regclass
				  AND confrelid = 'documents'::regclass
				  AND contype = 'f'
				  AND confdeltype = 'c'
			)
		`,
	).Scan(&hasCascadeForeignKey); err != nil {
		t.Fatalf("query text_chunks cascade foreign key: %v", err)
	}
	if !hasCascadeForeignKey {
		t.Fatal("text_chunks cascading document foreign key was not created")
	}

	// 期望数量直接来自编译进后端的正向迁移文件，避免每次新增迁移后
	// 还要手工同步一个容易遗忘的数字常量。
	embeddedMigrationNames, err := fs.Glob(
		migrations.Files,
		"*.up.sql",
	)
	if err != nil {
		t.Fatalf("list embedded migrations: %v", err)
	}
	expectedAppliedCount := len(embeddedMigrationNames)

	var appliedCount int
	if err := testPool.QueryRow(
		ctx,
		"SELECT COUNT(*) FROM schema_migrations",
	).Scan(&appliedCount); err != nil {
		t.Fatalf("count schema migrations: %v", err)
	}
	if appliedCount != expectedAppliedCount {
		t.Fatalf(
			"applied migration count = %d, want %d",
			appliedCount,
			expectedAppliedCount,
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

	// 模拟旧版迁移器曾按 CRLF 原始字节保存校验值。新版必须先严格确认
	// 该旧值与当前源文件一致，再把数据库记录升级为规范化 LF 校验值。
	legacyContent := []byte(
		"CREATE TABLE legacy_line_endings (id BIGINT);\r\n",
	)
	legacyMigration := fstest.MapFS{
		"000100_legacy_line_endings.up.sql": {
			Data: legacyContent,
		},
	}
	if err := database.Migrate(ctx, testPool, legacyMigration); err != nil {
		t.Fatalf("apply legacy-line-ending migration: %v", err)
	}

	legacyChecksum := fmt.Sprintf("%x", sha256.Sum256(legacyContent))
	if _, err := testPool.Exec(
		ctx,
		"UPDATE schema_migrations SET checksum = $1 WHERE version = 100",
		legacyChecksum,
	); err != nil {
		t.Fatalf("set simulated legacy checksum: %v", err)
	}

	if err := database.Migrate(ctx, testPool, legacyMigration); err != nil {
		t.Fatalf("upgrade legacy-line-ending checksum: %v", err)
	}

	normalizedLegacyContent := bytes.ReplaceAll(
		legacyContent,
		[]byte("\r\n"),
		[]byte("\n"),
	)
	expectedNormalizedChecksum := fmt.Sprintf(
		"%x",
		sha256.Sum256(normalizedLegacyContent),
	)
	var upgradedChecksum string
	if err := testPool.QueryRow(
		ctx,
		"SELECT checksum FROM schema_migrations WHERE version = 100",
	).Scan(&upgradedChecksum); err != nil {
		t.Fatalf("query upgraded legacy checksum: %v", err)
	}
	if upgradedChecksum != expectedNormalizedChecksum {
		t.Fatalf(
			"upgraded checksum = %q, want %q",
			upgradedChecksum,
			expectedNormalizedChecksum,
		)
	}
}
