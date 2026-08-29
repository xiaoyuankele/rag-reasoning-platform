package config

import (
	"errors"
	"fmt"
	"os"
	"strings"
)

const (
	// ApplicationRoleAll 保留当前单进程行为：API 与所有后台 Worker 一起运行。
	ApplicationRoleAll ApplicationRole = "all"

	// ApplicationRoleAPI 只运行 HTTP API；具体条件组装在下一阶段实现。
	ApplicationRoleAPI ApplicationRole = "api"

	// ApplicationRoleDocumentWorker 只运行文档解析 Worker 与 Python Pool。
	ApplicationRoleDocumentWorker ApplicationRole = "document-worker"

	// ApplicationRoleEmbeddingWorker 只运行后台向量任务 Worker。
	ApplicationRoleEmbeddingWorker ApplicationRole = "embedding-worker"

	// ApplicationRoleAnswerWorker 只运行持久化异步问答 Worker。
	ApplicationRoleAnswerWorker ApplicationRole = "answer-worker"
)

var (
	// ErrUnsupportedApplicationRole 表示 APP_ROLE 不是当前契约允许的角色。
	ErrUnsupportedApplicationRole = errors.New("unsupported application role")
)

// ApplicationRole 是同一后端二进制在本次启动中承担的部署角色。
//
// 它只决定组合根应当启动哪些组件，不进入 Domain/Application 业务规则，
// 也不会作为 HTTP 请求参数暴露给前端。
type ApplicationRole string

// LoadApplicationRole 读取并规范化 APP_ROLE。
//
// 空值默认 all，保持现有本地开发和单容器部署行为。输入会去除首尾空白
// 并转成小写，方便 Windows PowerShell 和容器环境配置。
func LoadApplicationRole() (ApplicationRole, error) {
	rawValue := os.Getenv("APP_ROLE")
	role := ApplicationRole(strings.ToLower(strings.TrimSpace(rawValue)))
	if role == "" {
		return ApplicationRoleAll, nil
	}

	switch role {
	case ApplicationRoleAll,
		ApplicationRoleAPI,
		ApplicationRoleDocumentWorker,
		ApplicationRoleEmbeddingWorker,
		ApplicationRoleAnswerWorker:
		return role, nil
	default:
		return "", fmt.Errorf(
			"%w: APP_ROLE must be one of all, api, document-worker, embedding-worker, answer-worker: %q",
			ErrUnsupportedApplicationRole,
			rawValue,
		)
	}
}
