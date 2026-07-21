package config

import (
	"fmt"
	"os"
	"strconv"
	"strings"
)

const (
	// 默认从 backend 目录运行服务，因此 ../storage 指向项目根目录的 storage。
	defaultStorageRoot = "../storage"

	// 默认单个上传文件最大为 200 MiB。
	defaultStorageMaxFileSizeBytes int64 = 200 * 1024 * 1024
)

// StorageConfig 保存本地文档存储需要的配置。
type StorageConfig struct {
	RootDir          string
	MaxFileSizeBytes int64
}

// LoadStorage 从操作系统环境变量中读取并校验文件存储配置。
func LoadStorage() (StorageConfig, error) {
	rootDir := os.Getenv("STORAGE_ROOT")
	if rootDir == "" {
		rootDir = defaultStorageRoot
	}

	rootDir = strings.TrimSpace(rootDir)
	if rootDir == "" {
		return StorageConfig{}, fmt.Errorf(
			"STORAGE_ROOT must not be blank",
		)
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
		RootDir:          rootDir,
		MaxFileSizeBytes: maxFileSizeBytes,
	}, nil
}
