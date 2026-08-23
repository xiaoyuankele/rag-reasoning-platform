package config

import (
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

const (
	// 默认存储目录相对于 APP_ROOT，不再相对于进程当前工作目录。
	defaultStorageRoot = "storage"

	// 默认单个上传文件最大为 200 MiB。
	defaultStorageMaxFileSizeBytes int64 = 200 * 1024 * 1024

	defaultUploadMaxConcurrencyPerUser = 2
	defaultUploadMaxConcurrencyGlobal  = 16
	maximumUploadConcurrency           = 64
	defaultUploadQueueWaitTimeout      = 2 * time.Second
)

// ErrInvalidUploadConcurrencyLimits 表示全局上传并发小于单用户并发。
var ErrInvalidUploadConcurrencyLimits = errors.New(
	"upload global concurrency must not be smaller than per-user concurrency",
)

// StorageConfig 保存本地文档存储需要的配置。
type StorageConfig struct {
	RootDir                     string
	MaxFileSizeBytes            int64
	UploadMaxConcurrencyPerUser int
	UploadMaxConcurrencyGlobal  int
	UploadQueueWaitTimeout      time.Duration
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

	uploadMaxConcurrencyPerUser, err := loadPositiveBoundedInt(
		"UPLOAD_MAX_CONCURRENCY_PER_USER",
		defaultUploadMaxConcurrencyPerUser,
		maximumUploadConcurrency,
	)
	if err != nil {
		return StorageConfig{}, fmt.Errorf(
			"load per-user upload concurrency: %w",
			err,
		)
	}

	uploadMaxConcurrencyGlobal, err := loadPositiveBoundedInt(
		"UPLOAD_MAX_CONCURRENCY_GLOBAL",
		defaultUploadMaxConcurrencyGlobal,
		maximumUploadConcurrency,
	)
	if err != nil {
		return StorageConfig{}, fmt.Errorf(
			"load global upload concurrency: %w",
			err,
		)
	}
	if uploadMaxConcurrencyGlobal < uploadMaxConcurrencyPerUser {
		return StorageConfig{}, ErrInvalidUploadConcurrencyLimits
	}

	uploadQueueWaitTimeout, err := loadPositiveDuration(
		"UPLOAD_QUEUE_WAIT_TIMEOUT",
		defaultUploadQueueWaitTimeout,
	)
	if err != nil {
		return StorageConfig{}, fmt.Errorf(
			"load upload queue wait timeout: %w",
			err,
		)
	}

	return StorageConfig{
		RootDir:                     resolvedRootDir,
		MaxFileSizeBytes:            maxFileSizeBytes,
		UploadMaxConcurrencyPerUser: uploadMaxConcurrencyPerUser,
		UploadMaxConcurrencyGlobal:  uploadMaxConcurrencyGlobal,
		UploadQueueWaitTimeout:      uploadQueueWaitTimeout,
	}, nil
}
