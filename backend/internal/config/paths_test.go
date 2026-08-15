package config

import (
	"os"
	"path/filepath"
	"testing"
)

// TestLoadRuntimePathsUsesConfiguredAbsoluteRoot 验证部署环境可以通过
// APP_ROOT 明确指定应用根目录，而不依赖进程的当前工作目录。
func TestLoadRuntimePathsUsesConfiguredAbsoluteRoot(t *testing.T) {
	appRoot := t.TempDir()
	t.Setenv("APP_ROOT", appRoot)

	pathsConfig, err := LoadRuntimePaths()
	if err != nil {
		t.Fatalf("LoadRuntimePaths() error = %v, want nil", err)
	}

	if pathsConfig.AppRoot != filepath.Clean(appRoot) {
		t.Fatalf(
			"AppRoot = %q, want %q",
			pathsConfig.AppRoot,
			filepath.Clean(appRoot),
		)
	}
}

// TestLoadRuntimePathsRejectsRelativeConfiguredRoot 验证 APP_ROOT 必须明确为
// 绝对路径，避免显式配置仍然随启动目录改变含义。
func TestLoadRuntimePathsRejectsRelativeConfiguredRoot(t *testing.T) {
	t.Setenv("APP_ROOT", "../project")

	_, err := LoadRuntimePaths()
	if err == nil {
		t.Fatal("LoadRuntimePaths() error = nil, want relative APP_ROOT rejection")
	}
}

// TestDiscoverProjectRootFromDescendant 验证开发环境没有设置 APP_ROOT 时，
// 可以从项目根目录及其后代目录向上找到稳定的项目根。
func TestDiscoverProjectRootFromDescendant(t *testing.T) {
	projectRoot := createProjectRootFixture(t)

	tests := []struct {
		name      string
		startPath string
	}{
		{name: "project root", startPath: projectRoot},
		{name: "backend directory", startPath: filepath.Join(projectRoot, "backend")},
		{
			name:      "nested backend directory",
			startPath: filepath.Join(projectRoot, "backend", "cmd", "server"),
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			actualRoot, err := discoverProjectRoot(test.startPath)
			if err != nil {
				t.Fatalf("discoverProjectRoot() error = %v, want nil", err)
			}
			if actualRoot != filepath.Clean(projectRoot) {
				t.Fatalf(
					"discoverProjectRoot() = %q, want %q",
					actualRoot,
					filepath.Clean(projectRoot),
				)
			}
		})
	}
}

// TestDiscoverProjectRootRejectsDirectoryWithoutMarkers 验证自动发现不会把
// 任意普通目录误判成项目根；部署目录需要显式配置 APP_ROOT。
func TestDiscoverProjectRootRejectsDirectoryWithoutMarkers(t *testing.T) {
	_, err := discoverProjectRoot(t.TempDir())
	if err == nil {
		t.Fatal("discoverProjectRoot() error = nil, want missing marker error")
	}
}

func createProjectRootFixture(t *testing.T) string {
	t.Helper()

	projectRoot := t.TempDir()
	backendRoot := filepath.Join(projectRoot, "backend")
	pythonPackageRoot := filepath.Join(projectRoot, "ai", "src", "rag_ai")

	if err := os.MkdirAll(filepath.Join(backendRoot, "cmd", "server"), 0o755); err != nil {
		t.Fatalf("create backend fixture: %v", err)
	}
	if err := os.MkdirAll(pythonPackageRoot, 0o755); err != nil {
		t.Fatalf("create Python fixture: %v", err)
	}
	if err := os.WriteFile(filepath.Join(backendRoot, "go.mod"), []byte("module test\n"), 0o600); err != nil {
		t.Fatalf("create go.mod fixture: %v", err)
	}

	return projectRoot
}
