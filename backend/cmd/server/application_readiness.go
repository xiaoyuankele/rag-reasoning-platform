package main

import (
	"fmt"
	"os"

	"rag-reasoning-platform/backend/internal/config"
)

// writeApplicationReadyFile 在专用 Worker 完成全部启动步骤后发布就绪标记。
//
// 文件只属于部署生命周期，不进入业务层。返回的清理函数必须在进程退出前
// 调用，使健康检查先停止接纳该实例，再等待 Worker goroutine 完成清理。
func writeApplicationReadyFile(
	path string,
	role config.ApplicationRole,
) (func() error, error) {
	if path == "" {
		return func() error { return nil }, nil
	}

	if err := os.WriteFile(path, []byte(string(role)+"\n"), 0o600); err != nil {
		return nil, fmt.Errorf("write application ready file %q: %w", path, err)
	}

	return func() error {
		err := os.Remove(path)
		if err == nil || os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("remove application ready file %q: %w", path, err)
	}, nil
}
