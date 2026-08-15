// Package observability 提供日志、追踪等跨层技术信息的最小公共契约。
// 它不定义文档领域规则，也不依赖 Gin、PostgreSQL 等具体框架。
package observability

import "context"

// requestIDContextKey 使用包内私有类型，避免与其他包写入 context 的键冲突。
type requestIDContextKey struct{}

// WithRequestID 返回携带请求 ID 的子 context。
//
// context 是不可变的：该函数不会修改 parent，而是创建一个能够继续继承取消信号、
// 截止时间和已有值的新 context。
func WithRequestID(parent context.Context, requestID string) context.Context {
	return context.WithValue(parent, requestIDContextKey{}, requestID)
}

// RequestIDFromContext 读取之前写入的请求 ID。
// ok=false 表示当前调用链并非来自 HTTP 请求，或尚未经过请求 ID 中间件。
func RequestIDFromContext(ctx context.Context) (requestID string, ok bool) {
	if ctx == nil {
		return "", false
	}

	requestID, ok = ctx.Value(requestIDContextKey{}).(string)
	return requestID, ok && requestID != ""
}
