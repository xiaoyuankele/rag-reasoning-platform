package database

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io/fs"
	"path"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

const migrationAdvisoryLockID int64 = 726031551

type migration struct {
	version        int64
	name           string
	filename       string
	checksum       string
	sourceChecksum string
	sql            string
}

// Migrate 按版本顺序执行尚未应用的 PostgreSQL 正向迁移。
//
// 每条迁移都在独立事务中执行；已应用迁移会校验文件名和规范化后的
// SHA-256，既防止历史 SQL 被静默修改，也避免操作系统换行造成误报。
func Migrate(
	ctx context.Context,
	pool *pgxpool.Pool,
	migrationFiles fs.FS,
) error {
	loadedMigrations, err := loadMigrations(migrationFiles)
	if err != nil {
		return fmt.Errorf("load migrations: %w", err)
	}

	connection, err := pool.Acquire(ctx)
	if err != nil {
		return fmt.Errorf("acquire migration connection: %w", err)
	}
	defer connection.Release()

	// 会话级 advisory lock 保证多个服务实例不会同时执行迁移。
	if _, err := connection.Exec(
		ctx,
		"SELECT pg_advisory_lock($1)",
		migrationAdvisoryLockID,
	); err != nil {
		return fmt.Errorf("lock migrations: %w", err)
	}
	defer unlockMigrations(connection)

	if _, err := connection.Exec(ctx, `
		CREATE TABLE IF NOT EXISTS schema_migrations (
			version BIGINT PRIMARY KEY,
			name TEXT NOT NULL,
			checksum TEXT NOT NULL,
			applied_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP
		)
	`); err != nil {
		return fmt.Errorf("create schema_migrations table: %w", err)
	}

	for _, currentMigration := range loadedMigrations {
		applied, err := migrationIsApplied(
			ctx,
			connection,
			currentMigration,
		)
		if err != nil {
			return err
		}
		if applied {
			continue
		}

		if err := applyMigration(
			ctx,
			connection,
			currentMigration,
		); err != nil {
			return err
		}
	}

	return nil
}

func loadMigrations(migrationFiles fs.FS) ([]migration, error) {
	filenames, err := fs.Glob(migrationFiles, "*.up.sql")
	if err != nil {
		return nil, fmt.Errorf("find migration files: %w", err)
	}
	if len(filenames) == 0 {
		return nil, errors.New("no .up.sql migration files found")
	}

	sort.Strings(filenames)
	loaded := make([]migration, 0, len(filenames))
	seenVersions := make(map[int64]string, len(filenames))

	for _, filename := range filenames {
		currentMigration, err := readMigration(
			migrationFiles,
			filename,
		)
		if err != nil {
			return nil, err
		}

		if previousFilename, exists := seenVersions[currentMigration.version]; exists {
			return nil, fmt.Errorf(
				"duplicate migration version %d in %q and %q",
				currentMigration.version,
				previousFilename,
				filename,
			)
		}

		seenVersions[currentMigration.version] = filename
		loaded = append(loaded, currentMigration)
	}

	sort.Slice(loaded, func(left, right int) bool {
		return loaded[left].version < loaded[right].version
	})

	return loaded, nil
}

func readMigration(
	migrationFiles fs.FS,
	filename string,
) (migration, error) {
	const suffix = ".up.sql"

	baseName := path.Base(filename)
	separatorIndex := strings.IndexByte(baseName, '_')
	if separatorIndex <= 0 || !strings.HasSuffix(baseName, suffix) {
		return migration{}, fmt.Errorf(
			"invalid migration filename %q; want VERSION_name.up.sql",
			filename,
		)
	}

	version, err := strconv.ParseInt(
		baseName[:separatorIndex],
		10,
		64,
	)
	if err != nil || version <= 0 {
		return migration{}, fmt.Errorf(
			"invalid migration version in %q",
			filename,
		)
	}

	name := strings.TrimSuffix(
		baseName[separatorIndex+1:],
		suffix,
	)
	if strings.TrimSpace(name) == "" {
		return migration{}, fmt.Errorf(
			"migration name is empty in %q",
			filename,
		)
	}

	content, err := fs.ReadFile(migrationFiles, filename)
	if err != nil {
		return migration{}, fmt.Errorf(
			"read migration %q: %w",
			filename,
			err,
		)
	}
	if strings.TrimSpace(string(content)) == "" {
		return migration{}, fmt.Errorf(
			"migration %q is empty",
			filename,
		)
	}

	// SQL 换行不改变数据库语义，因此校验和统一使用 LF。sourceChecksum
	// 只用于识别并安全升级旧版迁移器保存的原始字节校验值。
	normalizedContent := normalizeMigrationLineEndings(content)
	checksum := migrationChecksum(normalizedContent)
	sourceChecksum := migrationChecksum(content)

	return migration{
		version:        version,
		name:           name,
		filename:       filename,
		checksum:       checksum,
		sourceChecksum: sourceChecksum,
		sql:            string(normalizedContent),
	}, nil
}

// normalizeMigrationLineEndings 把 Windows CRLF 和旧式 CR 统一成 LF。
//
// 必须先替换 CRLF，再替换剩余 CR，否则一个 CRLF 可能被错误处理成两次换行。
func normalizeMigrationLineEndings(content []byte) []byte {
	normalized := bytes.ReplaceAll(content, []byte("\r\n"), []byte("\n"))
	return bytes.ReplaceAll(normalized, []byte("\r"), []byte("\n"))
}

func migrationChecksum(content []byte) string {
	return fmt.Sprintf("%x", sha256.Sum256(content))
}

func migrationIsApplied(
	ctx context.Context,
	connection *pgxpool.Conn,
	currentMigration migration,
) (bool, error) {
	var appliedName string
	var appliedChecksum string

	err := connection.QueryRow(
		ctx,
		`
			SELECT name, checksum
			FROM schema_migrations
			WHERE version = $1
		`,
		currentMigration.version,
	).Scan(&appliedName, &appliedChecksum)

	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf(
			"query migration version %d: %w",
			currentMigration.version,
			err,
		)
	}

	if appliedName != currentMigration.name {
		return false, fmt.Errorf(
			"migration version %d changed after it was applied",
			currentMigration.version,
		)
	}

	if appliedChecksum == currentMigration.checksum {
		return true, nil
	}

	// 旧版迁移器直接对工作区原始字节计算校验和。只有数据库保存值与
	// 当前原始文件完全一致时，才能证明 SQL 没有被修改，并把记录升级
	// 为跨平台稳定的 LF 校验值。整个过程处于 advisory lock 保护下。
	if appliedChecksum == currentMigration.sourceChecksum {
		commandTag, err := connection.Exec(
			ctx,
			`
				UPDATE schema_migrations
				SET checksum = $1
				WHERE version = $2
				  AND checksum = $3
			`,
			currentMigration.checksum,
			currentMigration.version,
			appliedChecksum,
		)
		if err != nil {
			return false, fmt.Errorf(
				"upgrade migration version %d checksum: %w",
				currentMigration.version,
				err,
			)
		}
		if commandTag.RowsAffected() != 1 {
			return false, fmt.Errorf(
				"upgrade migration version %d checksum: expected one updated row",
				currentMigration.version,
			)
		}

		return true, nil
	}

	return false, fmt.Errorf(
		"migration version %d changed after it was applied",
		currentMigration.version,
	)
}

func applyMigration(
	ctx context.Context,
	connection *pgxpool.Conn,
	currentMigration migration,
) error {
	transaction, err := connection.Begin(ctx)
	if err != nil {
		return fmt.Errorf(
			"begin migration %q: %w",
			currentMigration.filename,
			err,
		)
	}
	defer func() {
		_ = transaction.Rollback(context.Background())
	}()

	if _, err := transaction.Exec(ctx, currentMigration.sql); err != nil {
		return fmt.Errorf(
			"execute migration %q: %w",
			currentMigration.filename,
			err,
		)
	}

	if _, err := transaction.Exec(
		ctx,
		`
			INSERT INTO schema_migrations (
				version,
				name,
				checksum
			)
			VALUES ($1, $2, $3)
		`,
		currentMigration.version,
		currentMigration.name,
		currentMigration.checksum,
	); err != nil {
		return fmt.Errorf(
			"record migration %q: %w",
			currentMigration.filename,
			err,
		)
	}

	if err := transaction.Commit(ctx); err != nil {
		return fmt.Errorf(
			"commit migration %q: %w",
			currentMigration.filename,
			err,
		)
	}

	return nil
}

func unlockMigrations(connection *pgxpool.Conn) {
	unlockContext, cancel := context.WithTimeout(
		context.Background(),
		5*time.Second,
	)
	defer cancel()

	_, _ = connection.Exec(
		unlockContext,
		"SELECT pg_advisory_unlock($1)",
		migrationAdvisoryLockID,
	)
}
