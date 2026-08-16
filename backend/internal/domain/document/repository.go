package document

import (
	"context"
	"errors"

	accessdomain "rag-reasoning-platform/backend/internal/domain/access"
)

// ErrNotFound 表示指定文档不存在。
// 业务层只判断这个领域错误，不需要认识 pgx.ErrNoRows。
var ErrNotFound = errors.New("document not found")

// CreateInput 保存创建文档时由调用者提供的数据。
// ID、状态和时间由数据库生成，因此不放入创建参数。
type CreateInput struct {
	OriginalName string
	StoragePath  string
	MIMEType     string
	SizeBytes    int64
	SHA256       string
}

// Creator 定义创建文档所需的仓储能力
type Creator interface {
	Create(ctx context.Context, input CreateInput) (Document, error)
}

// Finder 定义按 ID 查询文档所需的仓储能力。
type Finder interface {
	GetByID(ctx context.Context, id int64) (Document, error)
}

// Deleter 定义删除文档的能力
type Deleter interface {
	Delete(ctx context.Context, id int64) error
}

// ListOptions 表示仓储查询文档列表时使用的分页参数。
//
// Limit 是最多返回多少条记录；Offset 是跳过多少条记录。
// 这里不使用 HTTP 层的 page 和 page_size，避免领域仓储依赖接口表现形式。
type ListOptions struct {
	Limit  int64
	Offset int64
}

// ListResult 表示一次文档列表查询的结果。
//
// Documents 是当前页数据；Total 是不考虑 Limit 和 Offset 时的总记录数。
// Total 用于上层计算总页数。
type ListResult struct {
	Documents []Document
	Total     int64
}

// Lister 定义查询文档列表所需的仓储能力。
type Lister interface {
	List(ctx context.Context, options ListOptions) (ListResult, error)
}

// Repository 定义文档持久化需要提供的能力。
// 这里只规定“能做什么”，不规定使用 PostgreSQL、内存还是其他存储。
type Repository interface {
	Creator
	Finder
	Lister
	Deleter
}

// ScopedCreator 定义必须在可信所有者范围内创建文档的仓储能力。
type ScopedCreator interface {
	Create(
		ctx context.Context,
		scope accessdomain.OwnerScope,
		input CreateInput,
	) (Document, error)
}

// ScopedFinder 定义只能在可信所有者范围内按 ID 查询文档的仓储能力。
// 文档不存在和属于其他用户都必须返回 ErrNotFound。
type ScopedFinder interface {
	GetByID(
		ctx context.Context,
		scope accessdomain.OwnerScope,
		id int64,
	) (Document, error)
}

// ScopedLister 定义只能列出可信所有者文档的仓储能力。
type ScopedLister interface {
	List(
		ctx context.Context,
		scope accessdomain.OwnerScope,
		options ListOptions,
	) (ListResult, error)
}

// ScopedDeleter 定义只能删除可信所有者文档的仓储能力。
type ScopedDeleter interface {
	Delete(
		ctx context.Context,
		scope accessdomain.OwnerScope,
		id int64,
	) error
}

// ScopedRepository 组合面向已认证个人用户的文档持久化能力。
type ScopedRepository interface {
	ScopedCreator
	ScopedFinder
	ScopedLister
	ScopedDeleter
}
