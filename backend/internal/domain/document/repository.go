package document

import (
	"context"
	"errors"
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

// Repository 定义文档持久化需要提供的能力。
// 这里只规定“能做什么”，不规定使用 PostgreSQL、内存还是其他存储。
type Repository interface {
	Creator
	Finder
}
