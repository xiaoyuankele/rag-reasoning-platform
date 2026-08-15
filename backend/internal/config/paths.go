package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

const appRootEnvironmentName = "APP_ROOT"

// RuntimePathsConfig 保存应用运行时资源共同使用的路径基准。
//
// AppRoot 始终是经过清理的绝对路径。Storage、Python 等配置只需把
// 自己的相对路径解析到该根目录，不再依赖进程碰巧从哪个目录启动。
type RuntimePathsConfig struct {
	AppRoot string
}

// LoadRuntimePaths 加载应用根目录。
//
// 部署环境可以通过 APP_ROOT 提供绝对路径；开发环境省略 APP_ROOT 时，
// 会从当前工作目录向上寻找 backend/go.mod 和 ai/src/rag_ai 项目标志。
func LoadRuntimePaths() (RuntimePathsConfig, error) {
	configuredRoot, configured := os.LookupEnv(appRootEnvironmentName)
	if configured && configuredRoot != "" {
		appRoot, err := validateConfiguredAppRoot(configuredRoot)
		if err != nil {
			return RuntimePathsConfig{}, err
		}

		return RuntimePathsConfig{AppRoot: appRoot}, nil
	}

	workingDirectory, err := os.Getwd()
	if err != nil {
		return RuntimePathsConfig{}, fmt.Errorf(
			"get current working directory: %w",
			err,
		)
	}

	appRoot, err := discoverProjectRoot(workingDirectory)
	if err != nil {
		return RuntimePathsConfig{}, err
	}

	return RuntimePathsConfig{AppRoot: appRoot}, nil
}

func validateConfiguredAppRoot(configuredRoot string) (string, error) {
	configuredRoot = strings.TrimSpace(configuredRoot)
	if configuredRoot == "" {
		return "", fmt.Errorf("%s must not be blank", appRootEnvironmentName)
	}
	if !filepath.IsAbs(configuredRoot) {
		return "", fmt.Errorf("%s must be an absolute path", appRootEnvironmentName)
	}

	appRoot := filepath.Clean(configuredRoot)
	rootInfo, err := os.Stat(appRoot)
	if err != nil {
		return "", fmt.Errorf("inspect %s %q: %w", appRootEnvironmentName, appRoot, err)
	}
	if !rootInfo.IsDir() {
		return "", fmt.Errorf("%s %q must be a directory", appRootEnvironmentName, appRoot)
	}

	return appRoot, nil
}

// discoverProjectRoot 从 startPath 开始逐级向上寻找开发仓库根目录。
// 部署产物不包含源码标志时，应显式设置 APP_ROOT，而不是猜测目录结构。
func discoverProjectRoot(startPath string) (string, error) {
	startPath = strings.TrimSpace(startPath)
	if startPath == "" {
		return "", fmt.Errorf("project root discovery start path must not be blank")
	}

	absoluteStart, err := filepath.Abs(startPath)
	if err != nil {
		return "", fmt.Errorf("resolve project root discovery start path: %w", err)
	}

	candidate := filepath.Clean(absoluteStart)
	for {
		if isDevelopmentProjectRoot(candidate) {
			return candidate, nil
		}

		parent := filepath.Dir(candidate)
		if parent == candidate {
			break
		}
		candidate = parent
	}

	return "", fmt.Errorf(
		"discover application root from %q: set %s to an absolute path",
		absoluteStart,
		appRootEnvironmentName,
	)
}

func isDevelopmentProjectRoot(candidate string) bool {
	goModulePath := filepath.Join(candidate, "backend", "go.mod")
	pythonPackagePath := filepath.Join(candidate, "ai", "src", "rag_ai")

	goModuleInfo, err := os.Stat(goModulePath)
	if err != nil || goModuleInfo.IsDir() {
		return false
	}

	pythonPackageInfo, err := os.Stat(pythonPackagePath)
	return err == nil && pythonPackageInfo.IsDir()
}

// resolveResourcePath 把受信配置中的资源路径统一转换成绝对路径。
// 绝对路径保持原意，相对路径固定以 APP_ROOT 为基准。
func resolveResourcePath(
	appRoot string,
	configuredPath string,
	environmentName string,
) (string, error) {
	appRoot = strings.TrimSpace(appRoot)
	if appRoot == "" || !filepath.IsAbs(appRoot) {
		return "", fmt.Errorf("application root must be a non-blank absolute path")
	}

	configuredPath = strings.TrimSpace(configuredPath)
	if configuredPath == "" {
		return "", fmt.Errorf("%s must not be blank", environmentName)
	}

	if filepath.IsAbs(configuredPath) {
		return filepath.Clean(configuredPath), nil
	}

	return filepath.Clean(filepath.Join(appRoot, configuredPath)), nil
}
