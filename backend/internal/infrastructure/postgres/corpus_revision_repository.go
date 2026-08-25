package postgres

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	accessdomain "rag-reasoning-platform/backend/internal/domain/access"
)

// CorpusRevisionRepository 读取 PostgreSQL 中的用户语料版本。
// 版本递增由同一 postgres 包内的业务事务完成，读取器本身不会修改版本。
type CorpusRevisionRepository struct {
	pool *pgxpool.Pool
}

// NewCorpusRevisionRepository 创建语料版本仓储。
func NewCorpusRevisionRepository(pool *pgxpool.Pool) *CorpusRevisionRepository {
	return &CorpusRevisionRepository{pool: pool}
}

// GetCorpusRevision 在 OwnerScope 内读取当前语料版本。
func (r *CorpusRevisionRepository) GetCorpusRevision(
	ctx context.Context,
	scope accessdomain.OwnerScope,
) (int64, error) {
	if !scope.IsValid() {
		return 0, accessdomain.ErrInvalidOwnerScope
	}

	var revision int64
	err := r.pool.QueryRow(
		ctx,
		"SELECT corpus_revision FROM users WHERE id = $1",
		scope.OwnerUserID(),
	).Scan(&revision)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, fmt.Errorf("get corpus revision: owner user does not exist")
	}
	if err != nil {
		return 0, fmt.Errorf("get corpus revision: %w", err)
	}
	if revision <= 0 {
		return 0, fmt.Errorf("get corpus revision: invalid revision %d", revision)
	}
	return revision, nil
}

// bumpOwnerCorpusRevision 必须在改变可检索语料的同一个 PostgreSQL 事务内调用。
// 事务任一步失败时，业务修改和版本递增会一起回滚。
func bumpOwnerCorpusRevision(
	ctx context.Context,
	transaction pgx.Tx,
	ownerUserID int64,
) error {
	commandTag, err := transaction.Exec(
		ctx,
		`UPDATE users
		 SET corpus_revision = corpus_revision + 1,
		     updated_at = CURRENT_TIMESTAMP
		 WHERE id = $1`,
		ownerUserID,
	)
	if err != nil {
		return fmt.Errorf("bump owner corpus revision: %w", err)
	}
	if commandTag.RowsAffected() != 1 {
		return fmt.Errorf(
			"bump owner corpus revision: expected 1 updated row, got %d",
			commandTag.RowsAffected(),
		)
	}
	return nil
}
