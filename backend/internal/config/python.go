package config

import (
	"fmt"
	"os"
	"strconv"
	"strings"
)

const (
	defaultPythonExecutable          = "python"
	defaultPythonSourceRoot          = "ai/src"
	defaultPythonPDFMaxFileSizeBytes = 50 * 1024 * 1024
	defaultPythonPDFMaxPages         = 500
	defaultPythonProcessMode         = PythonProcessModeOneShot
	defaultPythonProcessPoolSize     = 2
	defaultPythonProcessMaxDocuments = 20
	maximumPythonPDFMaxFileSizeBytes = 1024 * 1024 * 1024
	maximumPythonPDFMaxPages         = 10000
	maximumPythonProcessPoolSize     = 4
	maximumPythonProcessMaxDocuments = 10000

	// PythonProcessModeOneShot 表示每份复杂文档启动一个独立 Python 进程。
	PythonProcessModeOneShot = "oneshot"

	// PythonProcessModePool 表示通过固定大小进程池复用 Python 进程。
	PythonProcessModePool = "pool"
)

// PythonConfig 保存 Go 启动 Python 文档处理子进程所需的配置。
type PythonConfig struct {
	Executable          string
	SourceRoot          string
	PDFMaxFileSizeBytes int64
	PDFMaxPages         int
	ProcessMode         string
	ProcessPoolSize     int
	ProcessMaxDocuments int
}

// LoadPython 从环境变量读取 Python 子进程配置。
//
// 这里只负责提供配置值；可执行文件是否存在、源码目录是否有效，
// 由真正使用这些值的 PythonProcessor 在构造时检查。
func LoadPython(appRoot string) (PythonConfig, error) {
	executable := strings.TrimSpace(os.Getenv("PYTHON_EXECUTABLE"))
	if executable == "" {
		executable = defaultPythonExecutable
	}

	sourceRoot, configured := os.LookupEnv("PYTHON_SOURCE_ROOT")
	if !configured || sourceRoot == "" {
		sourceRoot = defaultPythonSourceRoot
	}
	resolvedSourceRoot, err := resolveResourcePath(
		appRoot,
		sourceRoot,
		"PYTHON_SOURCE_ROOT",
	)
	if err != nil {
		return PythonConfig{}, err
	}

	maxFileSizeBytes, err := loadPositiveBoundedInt64(
		"PYTHON_PDF_MAX_FILE_SIZE_BYTES",
		defaultPythonPDFMaxFileSizeBytes,
		maximumPythonPDFMaxFileSizeBytes,
	)
	if err != nil {
		return PythonConfig{}, fmt.Errorf(
			"load Python PDF file size limit: %w",
			err,
		)
	}

	maxPages, err := loadPositiveBoundedInt(
		"PYTHON_PDF_MAX_PAGES",
		defaultPythonPDFMaxPages,
		maximumPythonPDFMaxPages,
	)
	if err != nil {
		return PythonConfig{}, fmt.Errorf(
			"load Python PDF page limit: %w",
			err,
		)
	}

	processMode := strings.ToLower(strings.TrimSpace(
		os.Getenv("PYTHON_PROCESS_MODE"),
	))
	if processMode == "" {
		processMode = defaultPythonProcessMode
	}
	if processMode != PythonProcessModeOneShot &&
		processMode != PythonProcessModePool {
		return PythonConfig{}, fmt.Errorf(
			"PYTHON_PROCESS_MODE must be %q or %q",
			PythonProcessModeOneShot,
			PythonProcessModePool,
		)
	}

	processPoolSize, err := loadPositiveBoundedInt(
		"PYTHON_PROCESS_POOL_SIZE",
		defaultPythonProcessPoolSize,
		maximumPythonProcessPoolSize,
	)
	if err != nil {
		return PythonConfig{}, fmt.Errorf(
			"load Python process pool size: %w",
			err,
		)
	}

	processMaxDocuments, err := loadPositiveBoundedInt(
		"PYTHON_PROCESS_MAX_DOCUMENTS",
		defaultPythonProcessMaxDocuments,
		maximumPythonProcessMaxDocuments,
	)
	if err != nil {
		return PythonConfig{}, fmt.Errorf(
			"load Python process recycle limit: %w",
			err,
		)
	}

	return PythonConfig{
		Executable:          executable,
		SourceRoot:          resolvedSourceRoot,
		PDFMaxFileSizeBytes: maxFileSizeBytes,
		PDFMaxPages:         maxPages,
		ProcessMode:         processMode,
		ProcessPoolSize:     processPoolSize,
		ProcessMaxDocuments: processMaxDocuments,
	}, nil
}

func loadPositiveBoundedInt64(
	environmentName string,
	defaultValue int64,
	maximumValue int64,
) (int64, error) {
	value := strings.TrimSpace(os.Getenv(environmentName))
	if value == "" {
		return defaultValue, nil
	}

	parsed, err := strconv.ParseInt(value, 10, 64)
	if err != nil {
		return 0, fmt.Errorf(
			"%s must be an integer: %w",
			environmentName,
			err,
		)
	}
	if parsed <= 0 || parsed > maximumValue {
		return 0, fmt.Errorf(
			"%s must be between 1 and %d",
			environmentName,
			maximumValue,
		)
	}

	return parsed, nil
}

func loadPositiveBoundedInt(
	environmentName string,
	defaultValue int,
	maximumValue int,
) (int, error) {
	value := strings.TrimSpace(os.Getenv(environmentName))
	if value == "" {
		return defaultValue, nil
	}

	parsed, err := strconv.Atoi(value)
	if err != nil {
		return 0, fmt.Errorf(
			"%s must be an integer: %w",
			environmentName,
			err,
		)
	}
	if parsed <= 0 || parsed > maximumValue {
		return 0, fmt.Errorf(
			"%s must be between 1 and %d",
			environmentName,
			maximumValue,
		)
	}

	return parsed, nil
}
