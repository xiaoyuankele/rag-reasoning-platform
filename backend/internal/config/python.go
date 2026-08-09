package config

import (
	"fmt"
	"os"
	"strconv"
	"strings"
)

const (
	defaultPythonExecutable          = "python"
	defaultPythonSourceRoot          = "../ai/src"
	defaultPythonPDFMaxFileSizeBytes = 50 * 1024 * 1024
	defaultPythonPDFMaxPages         = 500
	maximumPythonPDFMaxFileSizeBytes = 1024 * 1024 * 1024
	maximumPythonPDFMaxPages         = 10000
)

// PythonConfig 保存 Go 启动 Python 文档处理子进程所需的配置。
type PythonConfig struct {
	Executable          string
	SourceRoot          string
	PDFMaxFileSizeBytes int64
	PDFMaxPages         int
}

// LoadPython 从环境变量读取 Python 子进程配置。
//
// 这里只负责提供配置值；可执行文件是否存在、源码目录是否有效，
// 由真正使用这些值的 PythonProcessor 在构造时检查。
func LoadPython() (PythonConfig, error) {
	executable := strings.TrimSpace(os.Getenv("PYTHON_EXECUTABLE"))
	if executable == "" {
		executable = defaultPythonExecutable
	}

	sourceRoot := strings.TrimSpace(os.Getenv("PYTHON_SOURCE_ROOT"))
	if sourceRoot == "" {
		sourceRoot = defaultPythonSourceRoot
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

	return PythonConfig{
		Executable:          executable,
		SourceRoot:          sourceRoot,
		PDFMaxFileSizeBytes: maxFileSizeBytes,
		PDFMaxPages:         maxPages,
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
