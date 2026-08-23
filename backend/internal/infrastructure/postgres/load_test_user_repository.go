package postgres

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	userdomain "rag-reasoning-platform/backend/internal/domain/user"
	loadtestuser "rag-reasoning-platform/backend/internal/maintenance/loadtestuser"
)

// LoadTestUserRepository 在受控测试数据库中查询和创建压测账号。
// 它不更新既有账号，也不创建 Session。
type LoadTestUserRepository struct {
	pool *pgxpool.Pool
}

var _ loadtestuser.Repository = (*LoadTestUserRepository)(nil)

// NewLoadTestUserRepository 创建测试账号预置仓储。
func NewLoadTestUserRepository(pool *pgxpool.Pool) *LoadTestUserRepository {
	return &LoadTestUserRepository{pool: pool}
}

// FindExistingAccounts 一次读取计划邮箱中已经存在的账号及密码摘要。
func (r *LoadTestUserRepository) FindExistingAccounts(
	ctx context.Context,
	emails []string,
) ([]loadtestuser.ExistingAccount, error) {
	if len(emails) == 0 {
		return []loadtestuser.ExistingAccount{}, nil
	}

	rows, err := r.pool.Query(
		ctx,
		"SELECT id, email, display_name, password_hash, status, created_at "+
			"FROM users "+
			"WHERE email = ANY($1::text[]) "+
			"ORDER BY email",
		emails,
	)
	if err != nil {
		return nil, fmt.Errorf("query existing load-test accounts: %w", err)
	}
	defer rows.Close()

	accounts := make([]loadtestuser.ExistingAccount, 0)
	for rows.Next() {
		var account loadtestuser.ExistingAccount
		var status string
		if err := rows.Scan(
			&account.ID,
			&account.Email,
			&account.DisplayName,
			&account.PasswordHash,
			&status,
			&account.CreatedAt,
		); err != nil {
			return nil, fmt.Errorf("scan existing load-test account: %w", err)
		}
		account.Status = userdomain.Status(status)
		accounts = append(accounts, account)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate existing load-test accounts: %w", err)
	}
	return accounts, nil
}

// CreateAccounts 在一个事务中创建全部缺失账号。任一插入失败都会整体回滚。
func (r *LoadTestUserRepository) CreateAccounts(
	ctx context.Context,
	accounts []loadtestuser.NewAccount,
	createdAt time.Time,
) ([]loadtestuser.StoredAccount, error) {
	if len(accounts) == 0 {
		return []loadtestuser.StoredAccount{}, nil
	}

	transaction, err := r.pool.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("begin load-test account transaction: %w", err)
	}
	defer func() { _ = transaction.Rollback(ctx) }()

	created := make([]loadtestuser.StoredAccount, 0, len(accounts))
	for _, account := range accounts {
		var stored loadtestuser.StoredAccount
		if err := transaction.QueryRow(
			ctx,
			"INSERT INTO users ("+
				"email, email_verified_at, display_name, password_hash, "+
				"status, created_at, updated_at"+
				") VALUES ($1, $2, $3, $4, 'active', $2, $2) "+
				"RETURNING id, email, display_name, created_at",
			account.Email,
			createdAt,
			account.DisplayName,
			account.PasswordHash,
		).Scan(
			&stored.ID,
			&stored.Email,
			&stored.DisplayName,
			&stored.CreatedAt,
		); err != nil {
			return nil, fmt.Errorf("insert load-test account %s: %w", account.Email, err)
		}
		created = append(created, stored)
	}

	if err := transaction.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit load-test accounts: %w", err)
	}
	return created, nil
}
