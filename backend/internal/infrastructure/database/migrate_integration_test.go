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

	// pg_trgm 安装在 public，属于整个数据库而不是某个测试 schema。
	// 先记录测试前状态，确保清理阶段只撤销本测试自己创建的扩展。
	var trigramExtensionExisted bool
	if err := adminPool.QueryRow(
		ctx,
		"SELECT EXISTS (SELECT 1 FROM pg_extension WHERE extname = 'pg_trgm')",
	).Scan(&trigramExtensionExisted); err != nil {
		t.Fatalf("query existing pg_trgm extension: %v", err)
	}

	var vectorExtensionExisted bool
	if err := adminPool.QueryRow(
		ctx,
		"SELECT EXISTS (SELECT 1 FROM pg_extension WHERE extname = 'vector')",
	).Scan(&vectorExtensionExisted); err != nil {
		t.Fatalf("query existing vector extension: %v", err)
	}

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

		if !trigramExtensionExisted {
			if _, cleanupErr := adminPool.Exec(
				cleanupContext,
				"DROP EXTENSION IF EXISTS pg_trgm",
			); cleanupErr != nil {
				t.Errorf("drop test-created pg_trgm extension: %v", cleanupErr)
			}
		}

		if !vectorExtensionExisted {
			if _, cleanupErr := adminPool.Exec(
				cleanupContext,
				"DROP EXTENSION IF EXISTS vector",
			); cleanupErr != nil {
				t.Errorf("drop test-created vector extension: %v", cleanupErr)
			}
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

	var hasTrigramExtension bool
	if err := testPool.QueryRow(
		ctx,
		`
			SELECT EXISTS (
				SELECT 1
				FROM pg_extension
				WHERE extname = 'pg_trgm'
				  AND extnamespace = 'public'::regnamespace
			)
		`,
	).Scan(&hasTrigramExtension); err != nil {
		t.Fatalf("query pg_trgm extension: %v", err)
	}
	if !hasTrigramExtension {
		t.Fatal("pg_trgm extension was not installed in public")
	}

	var hasVectorExtension bool
	if err := testPool.QueryRow(
		ctx,
		`
			SELECT EXISTS (
				SELECT 1
				FROM pg_extension
				WHERE extname = 'vector'
				  AND extnamespace = 'public'::regnamespace
			)
		`,
	).Scan(&hasVectorExtension); err != nil {
		t.Fatalf("query vector extension: %v", err)
	}
	if !hasVectorExtension {
		t.Fatal("vector extension was not installed in public")
	}

	var chunkEmbeddingsTable string
	if err := testPool.QueryRow(
		ctx,
		"SELECT to_regclass('chunk_embeddings')::text",
	).Scan(&chunkEmbeddingsTable); err != nil {
		t.Fatalf("query chunk_embeddings table: %v", err)
	}
	if chunkEmbeddingsTable != "chunk_embeddings" {
		t.Fatalf(
			"chunk_embeddings table = %q, want %q",
			chunkEmbeddingsTable,
			"chunk_embeddings",
		)
	}

	var embeddingType string
	if err := testPool.QueryRow(
		ctx,
		`
			SELECT format_type(attribute.atttypid, attribute.atttypmod)
			FROM pg_attribute AS attribute
			WHERE attribute.attrelid = 'chunk_embeddings'::regclass
			  AND attribute.attname = 'embedding'
			  AND NOT attribute.attisdropped
		`,
	).Scan(&embeddingType); err != nil {
		t.Fatalf("query chunk_embeddings vector type: %v", err)
	}
	if embeddingType != "public.vector(1536)" && embeddingType != "vector(1536)" {
		t.Fatalf(
			"chunk_embeddings.embedding type = %q, want vector(1536)",
			embeddingType,
		)
	}

	var hasChunkContentGINIndex bool
	if err := testPool.QueryRow(
		ctx,
		`
			SELECT EXISTS (
				SELECT 1
				FROM pg_index AS index_metadata
				JOIN pg_class AS index_relation
					ON index_relation.oid = index_metadata.indexrelid
				JOIN pg_am AS access_method
					ON access_method.oid = index_relation.relam
				JOIN pg_attribute AS indexed_attribute
					ON indexed_attribute.attrelid = index_metadata.indrelid
				   AND indexed_attribute.attnum = index_metadata.indkey[0]
				JOIN pg_opclass AS operator_class
					ON operator_class.oid = index_metadata.indclass[0]
				WHERE index_metadata.indrelid = 'text_chunks'::regclass
				  AND index_relation.relname = 'idx_text_chunks_content_trgm'
				  AND access_method.amname = 'gin'
				  AND index_metadata.indnatts = 1
				  AND index_metadata.indisvalid
				  AND index_metadata.indisready
				  AND indexed_attribute.attname = 'content'
				  AND operator_class.opcname = 'gin_trgm_ops'
				  AND operator_class.opcnamespace = 'public'::regnamespace
			)
		`,
	).Scan(&hasChunkContentGINIndex); err != nil {
		t.Fatalf("query text_chunks trigram GIN index: %v", err)
	}
	if !hasChunkContentGINIndex {
		t.Fatal("text_chunks trigram GIN index was not created")
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
