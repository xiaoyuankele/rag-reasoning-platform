package config

import (
	"errors"
	"fmt"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"
)

const (
	// StorageDriverLocal 把正式文件保存在单机文件系统。
	StorageDriverLocal = "local"

	// StorageDriverOSS 把正式文件保存在阿里云 OSS，本机目录只用于暂存。
	StorageDriverOSS = "oss"

	// OSSCredentialModeEnvironment 使用 OSS SDK 标准环境变量读取凭证。
	OSSCredentialModeEnvironment = "environment"

	// OSSCredentialModeECSRAMRole 使用绑定到 ECS 的 RAM Role 临时凭证。
	OSSCredentialModeECSRAMRole = "ecs_ram_role"

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

// OSSConfig 保存连接阿里云 OSS 所需的非敏感配置。
//
// AccessKey 不进入该结构体。environment 模式由 OSS SDK 读取
// OSS_ACCESS_KEY_ID、OSS_ACCESS_KEY_SECRET 和可选 OSS_SESSION_TOKEN；
// ecs_ram_role 模式则由 SDK 自动取得并刷新 ECS 临时凭证。
type OSSConfig struct {
	Bucket         string
	Region         string
	Endpoint       string
	CredentialMode string
	ECSRAMRole     string
}

// StorageConfig 保存文档存储驱动、暂存目录和上传容量配置。
type StorageConfig struct {
	Driver                      string
	RootDir                     string
	MaxFileSizeBytes            int64
	UploadMaxConcurrencyPerUser int
	UploadMaxConcurrencyGlobal  int
	UploadQueueWaitTimeout      time.Duration
	OSS                         OSSConfig
}

// LoadStorage 从操作系统环境变量中读取并校验文件存储配置。
func LoadStorage(appRoot string) (StorageConfig, error) {
	driver := strings.ToLower(strings.TrimSpace(os.Getenv("FILE_STORAGE_DRIVER")))
	if driver == "" {
		driver = StorageDriverLocal
	}
	if driver != StorageDriverLocal && driver != StorageDriverOSS {
		return StorageConfig{}, fmt.Errorf(
			"FILE_STORAGE_DRIVER must be %q or %q",
			StorageDriverLocal,
			StorageDriverOSS,
		)
	}

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

	var ossConfig OSSConfig
	if driver == StorageDriverOSS {
		ossConfig, err = loadOSSConfig()
		if err != nil {
			return StorageConfig{}, err
		}
	}

	return StorageConfig{
		Driver:                      driver,
		RootDir:                     resolvedRootDir,
		MaxFileSizeBytes:            maxFileSizeBytes,
		UploadMaxConcurrencyPerUser: uploadMaxConcurrencyPerUser,
		UploadMaxConcurrencyGlobal:  uploadMaxConcurrencyGlobal,
		UploadQueueWaitTimeout:      uploadQueueWaitTimeout,
		OSS:                         ossConfig,
	}, nil
}

func loadOSSConfig() (OSSConfig, error) {
	bucket, err := loadRequiredStorageValue("OSS_BUCKET")
	if err != nil {
		return OSSConfig{}, err
	}
	region, err := loadRequiredStorageValue("OSS_REGION")
	if err != nil {
		return OSSConfig{}, err
	}
	endpoint, err := loadRequiredStorageValue("OSS_ENDPOINT")
	if err != nil {
		return OSSConfig{}, err
	}
	if err := validateOSSEndpoint(endpoint); err != nil {
		return OSSConfig{}, err
	}

	credentialMode := strings.ToLower(strings.TrimSpace(
		os.Getenv("OSS_CREDENTIAL_MODE"),
	))
	if credentialMode == "" {
		credentialMode = OSSCredentialModeEnvironment
	}

	ecsRAMRole := strings.TrimSpace(os.Getenv("OSS_ECS_RAM_ROLE"))
	switch credentialMode {
	case OSSCredentialModeEnvironment:
		if strings.TrimSpace(os.Getenv("OSS_ACCESS_KEY_ID")) == "" ||
			strings.TrimSpace(os.Getenv("OSS_ACCESS_KEY_SECRET")) == "" {
			return OSSConfig{}, errors.New(
				"OSS_ACCESS_KEY_ID and OSS_ACCESS_KEY_SECRET must be provided when OSS_CREDENTIAL_MODE=environment",
			)
		}
	case OSSCredentialModeECSRAMRole:
		if ecsRAMRole == "" {
			return OSSConfig{}, errors.New(
				"OSS_ECS_RAM_ROLE must be provided when OSS_CREDENTIAL_MODE=ecs_ram_role",
			)
		}
	default:
		return OSSConfig{}, fmt.Errorf(
			"OSS_CREDENTIAL_MODE must be %q or %q",
			OSSCredentialModeEnvironment,
			OSSCredentialModeECSRAMRole,
		)
	}

	return OSSConfig{
		Bucket:         bucket,
		Region:         region,
		Endpoint:       endpoint,
		CredentialMode: credentialMode,
		ECSRAMRole:     ecsRAMRole,
	}, nil
}

func loadRequiredStorageValue(name string) (string, error) {
	value := strings.TrimSpace(os.Getenv(name))
	if value == "" {
		return "", fmt.Errorf("%s must be provided when FILE_STORAGE_DRIVER=oss", name)
	}
	return value, nil
}

func validateOSSEndpoint(endpoint string) error {
	parsed, err := url.Parse(endpoint)
	if err != nil || parsed.Scheme != "https" || parsed.Host == "" ||
		parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" ||
		(parsed.Path != "" && parsed.Path != "/") {
		return fmt.Errorf(
			"OSS_ENDPOINT must be an HTTPS endpoint without path, query, fragment, or user information",
		)
	}
	return nil
}
