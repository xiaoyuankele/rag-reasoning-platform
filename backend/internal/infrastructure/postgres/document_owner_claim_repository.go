package postgres

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	userdomain "rag-reasoning-platform/backend/internal/domain/user"
	documentowner "rag-reasoning-platform/backend/internal/maintenance/documentowner"
)

// DocumentOwnerClaimRepository 使用 PostgreSQL 完成一次性的历史文档归属迁移。
type DocumentOwnerClaimRepository struct {
	pool *pgxpool.Pool
}

// NewDocumentOwnerClaimRepository 创建历史文档归属仓储。
func NewDocumentOwnerClaimRepository(
	pool *pgxpool.Pool,
) *DocumentOwnerClaimRepository {
	return &DocumentOwnerClaimRepository{pool: pool}
}

// PreviewOwnerClaim 在一致的只读事务中读取目标用户和无主文档数量。
func (r *DocumentOwnerClaimRepository) PreviewOwnerClaim(
	ctx context.Context,
	ownerUserID int64,
) (documentowner.Preview, error) {
	transaction, err := r.pool.BeginTx(
		ctx,
		pgx.TxOptions{
			IsoLevel:   pgx.RepeatableRead,
			AccessMode: pgx.ReadOnly,
		},
	)
	if err != nil {
		return documentowner.Preview{}, fmt.Errorf(
			"begin owner claim preview transaction: %w",
			err,
		)
	}
	defer func() { _ = transaction.Rollback(ctx) }()

	target, err := loadOwnerClaimTarget(ctx, transaction, ownerUserID, false)
	if err != nil {
		return documentowner.Preview{}, err
	}

	unownedDocuments, err := countUnownedDocuments(ctx, transaction)
	if err != nil {
		return documentowner.Preview{}, err
	}

	if err := transaction.Commit(ctx); err != nil {
		return documentowner.Preview{}, fmt.Errorf(
			"commit owner claim preview transaction: %w",
			err,
		)
	}

	return documentowner.Preview{
		Target:           target,
		UnownedDocuments: unownedDocuments,
	}, nil
}

// ClaimUnownedDocuments 在同一个事务中锁定写入、核对数量并更新全部无主文档。
// 任一步失败都会回滚，不会留下部分文档已经认领的状态。
func (r *DocumentOwnerClaimRepository) ClaimUnownedDocuments(
	ctx context.Context,
	ownerUserID int64,
	expectedUnowned int64,
) (documentowner.Result, error) {
	transaction, err := r.pool.Begin(ctx)
	if err != nil {
		return documentowner.Result{}, fmt.Errorf(
			"begin owner claim transaction: %w",
			err,
		)
	}
	defer func() { _ = transaction.Rollback(ctx) }()

	// KEY SHARE 防止目标用户在事务提交前被删除，但不阻碍普通用户读取。
	target, err := loadOwnerClaimTarget(ctx, transaction, ownerUserID, true)
	if err != nil {
		return documentowner.Result{}, err
	}

	// 这是一次性运维操作。短暂阻止 documents 并发写入，保证“先数后改”期间
	// 无主文档集合不会变化，预计数量才真正具有防误操作意义。
	if _, err := transaction.Exec(
		ctx,
		"LOCK TABLE documents IN SHARE ROW EXCLUSIVE MODE",
	); err != nil {
		return documentowner.Result{}, fmt.Errorf(
			"lock documents for owner claim: %w",
			err,
		)
	}

	actualUnowned, err := countUnownedDocuments(ctx, transaction)
	if err != nil {
		return documentowner.Result{}, err
	}
	if actualUnowned != expectedUnowned {
		return documentowner.Result{},
			&documentowner.CountMismatchError{
				Expected: expectedUnowned,
				Actual:   actualUnowned,
			}
	}

	commandTag, err := transaction.Exec(
		ctx,
		`UPDATE documents
		 SET owner_user_id = $1
		 WHERE owner_user_id IS NULL`,
		ownerUserID,
	)
	if err != nil {
		return documentowner.Result{}, fmt.Errorf(
			"update unowned document owners: %w",
			err,
		)
	}
	claimedDocuments := commandTag.RowsAffected()
	if claimedDocuments != expectedUnowned {
		return documentowner.Result{}, fmt.Errorf(
			"owner claim updated %d documents, expected %d",
			claimedDocuments,
			expectedUnowned,
		)
	}

	remainingUnowned, err := countUnownedDocuments(ctx, transaction)
	if err != nil {
		return documentowner.Result{}, err
	}
	if err := transaction.Commit(ctx); err != nil {
		return documentowner.Result{}, fmt.Errorf(
			"commit owner claim transaction: %w",
			err,
		)
	}

	return documentowner.Result{
		Target:           target,
		ClaimedDocuments: claimedDocuments,
		RemainingUnowned: remainingUnowned,
	}, nil
}

// ownerClaimQueryRow 描述 pgx.Tx 在本文件中需要的最小查询能力。
type ownerClaimQueryRow interface {
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

func loadOwnerClaimTarget(
	ctx context.Context,
	querier ownerClaimQueryRow,
	ownerUserID int64,
	lockUser bool,
) (documentowner.Target, error) {
	query := `
		SELECT id, display_name, status
		FROM users
		WHERE id = $1
	`
	if lockUser {
		query += " FOR KEY SHARE"
	}

	var target documentowner.Target
	var status string
	err := querier.QueryRow(ctx, query, ownerUserID).Scan(
		&target.UserID,
		&target.DisplayName,
		&status,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return documentowner.Target{},
			documentowner.ErrTargetNotFound
	}
	if err != nil {
		return documentowner.Target{}, fmt.Errorf(
			"load owner claim target: %w",
			err,
		)
	}

	target.Status = userdomain.Status(status)
	if target.Status != userdomain.StatusActive {
		return documentowner.Target{},
			documentowner.ErrTargetInactive
	}
	return target, nil
}

func countUnownedDocuments(
	ctx context.Context,
	querier ownerClaimQueryRow,
) (int64, error) {
	var count int64
	if err := querier.QueryRow(
		ctx,
		"SELECT COUNT(*) FROM documents WHERE owner_user_id IS NULL",
	).Scan(&count); err != nil {
		return 0, fmt.Errorf("count unowned documents: %w", err)
	}
	return count, nil
}
