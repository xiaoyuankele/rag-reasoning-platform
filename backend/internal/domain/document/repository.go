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

// Finder 定义按 ID 查询文档所需的仓储能力。
// 该无作用域能力仅供后台 Worker 按任务中的文档 ID 读取文档。
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

// ScopedCreator 定义必须在可信所有者范围内创建文档的仓储能力。
type ScopedCreator interface {
	Create(
		ctx context.Context,
		scope accessdomain.OwnerScope,
		input CreateInput,
	) (Document, error)
}

// CreateOrGetResult 表示按“所有者 + 内容哈希”保存文档的结果。
//
// Created 为 true 时，Document 是本次新建的记录；为 false 时，Document
// 是同一用户之前已经保存的相同内容。领域层只描述这个稳定事实，不决定 HTTP
// 应该返回 200 还是 201。
type CreateOrGetResult struct {
	Document Document
	Created  bool
}

// ScopedCreateOrGetter 定义同一用户内原子查重并创建文档的仓储能力。
// PostgreSQL 实现必须依靠唯一约束处理并发上传，不能只做“先查再插”。
type ScopedCreateOrGetter interface {
	CreateOrGetBySHA256(
		ctx context.Context,
		scope accessdomain.OwnerScope,
		input CreateInput,
	) (CreateOrGetResult, error)
}

// ScopedContentFinder 定义在可信所有者范围内按照内容指纹查找文档的能力。
//
// SHA-256 与字节数同时匹配才表示二进制内容完全相同。未命中时返回
// ErrNotFound；不同用户的相同内容不能互相命中。
type ScopedContentFinder interface {
	FindBySHA256AndSize(
		ctx context.Context,
		scope accessdomain.OwnerScope,
		sha256 string,
		sizeBytes int64,
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
	ScopedCreateOrGetter
	ScopedContentFinder
	ScopedFinder
	ScopedLister
	ScopedDeleter
}
