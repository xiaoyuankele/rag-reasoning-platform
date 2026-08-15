package config

import (
	"fmt"
	"os"
	"strconv"
	"strings"
)

const (
	// 默认存储目录相对于 APP_ROOT，不再相对于进程当前工作目录。
	defaultStorageRoot = "storage"

	// 默认单个上传文件最大为 200 MiB。
	defaultStorageMaxFileSizeBytes int64 = 200 * 1024 * 1024
)

// StorageConfig 保存本地文档存储需要的配置。
type StorageConfig struct {
	RootDir          string
	MaxFileSizeBytes int64
}

// LoadStorage 从操作系统环境变量中读取并校验文件存储配置。
func LoadStorage(appRoot string) (StorageConfig, error) {
	rootDir, configured := os.LookupEnv("STORAGE_ROOT")
	if !configured || rootDir == "" {
		rootDir = defaultStorageRoot
	}

	resolvedRootDir, err := resolveResourcePath(
		appRoot,
		rootDir,
		"STORAGE_ROOT",
	)
	if err != nil {
		return StorageConfig{}, err
	}

	maxFileSizeValue := os.Getenv(
		"STORAGE_MAX_FILE_SIZE_BYTES",
	)
	if maxFileSizeValue == "" {
		maxFileSizeValue = strconv.FormatInt(
			defaultStorageMaxFileSizeBytes,
			10,
		)
	}

	maxFileSizeValue = strings.TrimSpace(maxFileSizeValue)

	maxFileSizeBytes, err := strconv.ParseInt(
		maxFileSizeValue,
		10,
		64,
	)
	if err != nil {
		return StorageConfig{}, fmt.Errorf(
			"STORAGE_MAX_FILE_SIZE_BYTES must be an integer: %w",
			err,
		)
	}

	if maxFileSizeBytes <= 0 {
		return StorageConfig{}, fmt.Errorf(
			"STORAGE_MAX_FILE_SIZE_BYTES must be greater than zero",
		)
	}

	return StorageConfig{
		RootDir:          resolvedRootDir,
		MaxFileSizeBytes: maxFileSizeBytes,
	}, nil
}
