package database_test

import (
	"context"
	"crypto/sha256"
	"fmt"
	"os"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"rag-reasoning-platform/backend/internal/config"
	"rag-reasoning-platform/backend/internal/infrastructure/database"
)

func TestMigrateCanRetryWrappedSQLAfterVersionInsertFails(t *testing.T) {
	if os.Getenv("RUN_DATABASE_TESTS") != "1" {
		t.Skip("set RUN_DATABASE_TESTS=1 to run PostgreSQL integration tests")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	databaseConfig, err := config.LoadDatabase()
	if err != nil {
		t.Fatal(err)
	}
	adminPool, err := pgxpool.New(ctx, databaseConfig.ConnectionString())
	if err != nil {
		t.Fatal(err)
	}
	defer adminPool.Close()

	schema := fmt.Sprintf("migration_atomicity_%d", time.Now().UnixNano())
	quotedSchema := pgx.Identifier{schema}.Sanitize()
	if _, err := adminPool.Exec(ctx, "CREATE SCHEMA "+quotedSchema); err != nil {
		t.Fatal(err)
	}
	defer func() {
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if _, err := adminPool.Exec(cleanupCtx, "DROP SCHEMA "+quotedSchema+" CASCADE"); err != nil {
			t.Errorf("clean up migration test schema: %v", err)
		}
	}()

	poolConfig, err := pgxpool.ParseConfig(databaseConfig.ConnectionString())
	if err != nil {
		t.Fatal(err)
	}
	poolConfig.ConnConfig.RuntimeParams["search_path"] = schema
	pool, err := pgxpool.NewWithConfig(ctx, poolConfig)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()

	const wrappedSQL = "BEGIN;\nCREATE TABLE migration_probe (id INTEGER PRIMARY KEY);\n" +
		"INSERT INTO migration_probe VALUES (42);\nCOMMIT;\n"
	files := fstest.MapFS{"000001_migration_probe.up.sql": {Data: []byte(wrappedSQL)}}
	if _, err := pool.Exec(ctx, `
		CREATE TABLE schema_migrations (
			version BIGINT PRIMARY KEY CONSTRAINT reject_probe_version CHECK (version <> 1),
			name TEXT NOT NULL,
			checksum TEXT NOT NULL
		)
	`); err != nil {
		t.Fatal(err)
	}

	if err := database.Migrate(ctx, pool, files); err == nil || !strings.Contains(err.Error(), "record migration") {
		t.Fatalf("Migrate() error = %v, want version-registration failure", err)
	}
	var tableExists, versionExists bool
	if err := pool.QueryRow(ctx, `
		SELECT to_regclass('migration_probe') IS NOT NULL,
		       EXISTS (SELECT 1 FROM schema_migrations WHERE version = 1)
	`).Scan(&tableExists, &versionExists); err != nil {
		t.Fatal(err)
	}
	if tableExists || versionExists {
		t.Fatalf("after failed registration: table exists=%v version exists=%v, want both false", tableExists, versionExists)
	}

	if _, err := pool.Exec(ctx, "ALTER TABLE schema_migrations DROP CONSTRAINT reject_probe_version"); err != nil {
		t.Fatal(err)
	}
	for attempt := 1; attempt <= 2; attempt++ {
		if err := database.Migrate(ctx, pool, files); err != nil {
			t.Fatalf("Migrate() after removing the failure, attempt %d: %v", attempt, err)
		}
	}
	var value int
	var checksum string
	if err := pool.QueryRow(ctx, `
		SELECT migration_probe.id, schema_migrations.checksum
		FROM migration_probe CROSS JOIN schema_migrations
		WHERE schema_migrations.version = 1
	`).Scan(&value, &checksum); err != nil {
		t.Fatal(err)
	}
	wantChecksum := fmt.Sprintf("%x", sha256.Sum256([]byte(wrappedSQL)))
	if value != 42 || checksum != wantChecksum {
		t.Fatalf("retry stored value=%d checksum=%q, want 42 and original checksum %q", value, checksum, wantChecksum)
	}
}
