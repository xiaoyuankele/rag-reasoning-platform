// Package document 定义文档领域模型和业务状态。
package document

import "time"

// Status 是文档处理状态。
// 使用独立类型可以避免在业务代码中到处传递无约束的普通字符串。
type Status string

const (
	// StatusUploaded 表示文件已经上传，但尚未开始解析。
	StatusUploaded Status = "uploaded"

	// StatusProcessing 表示文档正在解析或分块。
	StatusProcessing Status = "processing"

	// StatusReady 表示文档已经处理完成，可以参与检索。
	StatusReady Status = "ready"

	// StatusFailed 表示文档处理失败，可以记录原因并重试。
	StatusFailed Status = "failed"
)

// IsValid 判断状态是否属于系统支持的四种状态。
func (s Status) IsValid() bool {
	switch s {
	case StatusUploaded,
		StatusProcessing,
		StatusReady,
		StatusFailed:
		return true
	default:
		return false
	}
}

// CanTransitionTo 判断当前状态是否允许转换到目标状态。
func (s Status) CanTransitionTo(next Status) bool {
	// 来源或目标状态不合法时，直接拒绝转换。
	if !s.IsValid() || !next.IsValid() {
		return false
	}

	switch s {
	case StatusUploaded:
		// 已上传的文档只能开始处理。
		return next == StatusProcessing
	case StatusProcessing:
		// 处理中的文档只能成功完成或失败。
		return next == StatusReady || next == StatusFailed
	case StatusFailed:
		// 失败后允许重新进入处理状态。
		return next == StatusProcessing
	case StatusReady:
		// 已完成是终态，当前不允许继续转换。
		return false
	default:
		return false
	}
}

// Document 表示系统中的一份文档。
// 该结构体只描述业务数据，不包含 SQL 和 JSON 处理逻辑。
type Document struct {
	ID int64
	// OwnerUserID 是这份文档在个人用户域中的数据所有者。
	// 正常业务返回的文档必须具有正整数 OwnerUserID；迁移期数据库中的
	// NULL 历史记录不会进入面向用户的作用域查询。
	OwnerUserID int64

	// Title 是从文档元数据识别或由用户确认的文献标题。
	// nil 表示尚未获得标题，展示层应回退到 OriginalName。
	Title        *string
	OriginalName string

	// StoragePath 是文件存储实现生成的不透明键，不保证是操作系统路径。
	// Domain 只保存和传递它；路径校验、对象下载和本地物化属于 Infrastructure。
	StoragePath string
	MIMEType    string
	SizeBytes   int64
	SHA256      string
	Status      Status

	// ErrorMessage 使用指针表示数据库中的可空字段：
	// nil 表示没有错误信息，非 nil 表示存在错误内容。
	ErrorMessage *string

	CreatedAt time.Time
	UpdatedAt time.Time
}
