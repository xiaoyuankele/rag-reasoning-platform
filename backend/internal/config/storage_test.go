package config

import (
	"errors"
	"path/filepath"
	"testing"
	"time"
)

func clearUploadCapacityEnvironment(t *testing.T) {
	t.Helper()
	t.Setenv("UPLOAD_MAX_CONCURRENCY_PER_USER", "")
	t.Setenv("UPLOAD_MAX_CONCURRENCY_GLOBAL", "")
	t.Setenv("UPLOAD_QUEUE_WAIT_TIMEOUT", "")
}

func clearStorageBackendEnvironment(t *testing.T) {
	t.Helper()
	t.Setenv("FILE_STORAGE_DRIVER", "")
	t.Setenv("OSS_BUCKET", "")
	t.Setenv("OSS_REGION", "")
	t.Setenv("OSS_ENDPOINT", "")
	t.Setenv("OSS_CREDENTIAL_MODE", "")
	t.Setenv("OSS_ECS_RAM_ROLE", "")
	t.Setenv("OSS_ACCESS_KEY_ID", "")
	t.Setenv("OSS_ACCESS_KEY_SECRET", "")
	t.Setenv("OSS_SESSION_TOKEN", "")
}

// TestLoadStorageUsesDefaults 验证没有设置环境变量时使用项目默认值。
func TestLoadStorageUsesDefaults(t *testing.T) {
	appRoot := t.TempDir()
	t.Setenv("STORAGE_ROOT", "")
	t.Setenv("STORAGE_MAX_FILE_SIZE_BYTES", "")
	clearUploadCapacityEnvironment(t)
	clearStorageBackendEnvironment(t)

	storageConfig, err := LoadStorage(appRoot)
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}

	expected := StorageConfig{
		Driver:                      StorageDriverLocal,
		RootDir:                     filepath.Join(appRoot, "storage"),
		MaxFileSizeBytes:            200 * 1024 * 1024,
		UploadMaxConcurrencyPerUser: defaultUploadMaxConcurrencyPerUser,
		UploadMaxConcurrencyGlobal:  defaultUploadMaxConcurrencyGlobal,
		UploadQueueWaitTimeout:      defaultUploadQueueWaitTimeout,
	}

	if storageConfig != expected {
		t.Fatalf(
			"expected config %+v, got %+v",
			expected,
			storageConfig,
		)
	}
}

// TestLoadStorageUsesEnvironment 验证环境变量可以覆盖存储默认值。
func TestLoadStorageUsesEnvironment(t *testing.T) {
	appRoot := t.TempDir()
	t.Setenv("STORAGE_ROOT", "custom-storage")
	t.Setenv("STORAGE_MAX_FILE_SIZE_BYTES", "1048576")
	t.Setenv("UPLOAD_MAX_CONCURRENCY_PER_USER", "2")
	t.Setenv("UPLOAD_MAX_CONCURRENCY_GLOBAL", "8")
	t.Setenv("UPLOAD_QUEUE_WAIT_TIMEOUT", "750ms")
	clearStorageBackendEnvironment(t)

	storageConfig, err := LoadStorage(appRoot)
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}

	expected := StorageConfig{
		Driver:                      StorageDriverLocal,
		RootDir:                     filepath.Join(appRoot, "custom-storage"),
		MaxFileSizeBytes:            1048576,
		UploadMaxConcurrencyPerUser: 2,
		UploadMaxConcurrencyGlobal:  8,
		UploadQueueWaitTimeout:      750 * time.Millisecond,
	}

	if storageConfig != expected {
		t.Fatalf(
			"expected config %+v, got %+v",
			expected,
			storageConfig,
		)
	}
}

// TestLoadStorageRejectsInvalidMaximumSize 使用表驱动测试验证
// 非数字、零和负数最大文件大小都会被拒绝。
func TestLoadStorageRejectsInvalidMaximumSize(t *testing.T) {
	tests := []struct {
		name  string
		value string
	}{
		{name: "non-numeric size", value: "abc"},
		{name: "zero size", value: "0"},
		{name: "negative size", value: "-1"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Setenv("STORAGE_ROOT", "")
			t.Setenv("STORAGE_MAX_FILE_SIZE_BYTES", test.value)
			clearUploadCapacityEnvironment(t)
			clearStorageBackendEnvironment(t)

			_, err := LoadStorage(t.TempDir())
			if err == nil {
				t.Fatalf(
					"expected error for size %q",
					test.value,
				)
			}
		})
	}
}

// TestLoadStorageRejectsBlankRoot 验证只包含空白字符的路径不会被接受。
func TestLoadStorageRejectsBlankRoot(t *testing.T) {
	t.Setenv("STORAGE_ROOT", "   ")
	t.Setenv("STORAGE_MAX_FILE_SIZE_BYTES", "")
	clearUploadCapacityEnvironment(t)
	clearStorageBackendEnvironment(t)

	_, err := LoadStorage(t.TempDir())
	if err == nil {
		t.Fatal("expected an error for blank STORAGE_ROOT")
	}
}

// TestLoadStoragePreservesAbsoluteRoot 验证显式绝对存储路径不会被拼到 APP_ROOT 下。
func TestLoadStoragePreservesAbsoluteRoot(t *testing.T) {
	appRoot := t.TempDir()
	absoluteStorageRoot := t.TempDir()
	t.Setenv("STORAGE_ROOT", absoluteStorageRoot)
	t.Setenv("STORAGE_MAX_FILE_SIZE_BYTES", "")
	clearUploadCapacityEnvironment(t)
	clearStorageBackendEnvironment(t)

	storageConfig, err := LoadStorage(appRoot)
	if err != nil {
		t.Fatalf("LoadStorage() error = %v, want nil", err)
	}
	if storageConfig.RootDir != filepath.Clean(absoluteStorageRoot) {
		t.Fatalf(
			"RootDir = %q, want %q",
			storageConfig.RootDir,
			filepath.Clean(absoluteStorageRoot),
		)
	}
}

func TestLoadStorageRejectsInvalidUploadCapacityValues(t *testing.T) {
	testCases := []struct {
		name        string
		environment string
		value       string
	}{
		{name: "non-numeric per-user concurrency", environment: "UPLOAD_MAX_CONCURRENCY_PER_USER", value: "one"},
		{name: "zero per-user concurrency", environment: "UPLOAD_MAX_CONCURRENCY_PER_USER", value: "0"},
		{name: "per-user concurrency above maximum", environment: "UPLOAD_MAX_CONCURRENCY_PER_USER", value: "65"},
		{name: "non-numeric global concurrency", environment: "UPLOAD_MAX_CONCURRENCY_GLOBAL", value: "four"},
		{name: "zero global concurrency", environment: "UPLOAD_MAX_CONCURRENCY_GLOBAL", value: "0"},
		{name: "global concurrency above maximum", environment: "UPLOAD_MAX_CONCURRENCY_GLOBAL", value: "65"},
		{name: "invalid wait timeout", environment: "UPLOAD_QUEUE_WAIT_TIMEOUT", value: "soon"},
		{name: "zero wait timeout", environment: "UPLOAD_QUEUE_WAIT_TIMEOUT", value: "0s"},
	}

	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Setenv("STORAGE_ROOT", "")
			t.Setenv("STORAGE_MAX_FILE_SIZE_BYTES", "")
			t.Setenv("UPLOAD_MAX_CONCURRENCY_PER_USER", "1")
			t.Setenv("UPLOAD_MAX_CONCURRENCY_GLOBAL", "4")
			t.Setenv("UPLOAD_QUEUE_WAIT_TIMEOUT", "2s")
			clearStorageBackendEnvironment(t)
			t.Setenv(testCase.environment, testCase.value)

			if _, err := LoadStorage(t.TempDir()); err == nil {
				t.Fatalf(
					"LoadStorage() error = nil for %s=%q",
					testCase.environment,
					testCase.value,
				)
			}
		})
	}
}

func TestLoadStorageRejectsGlobalUploadConcurrencyBelowPerUser(t *testing.T) {
	t.Setenv("STORAGE_ROOT", "")
	t.Setenv("STORAGE_MAX_FILE_SIZE_BYTES", "")
	t.Setenv("UPLOAD_MAX_CONCURRENCY_PER_USER", "3")
	t.Setenv("UPLOAD_MAX_CONCURRENCY_GLOBAL", "2")
	t.Setenv("UPLOAD_QUEUE_WAIT_TIMEOUT", "2s")
	clearStorageBackendEnvironment(t)

	_, err := LoadStorage(t.TempDir())
	if !errors.Is(err, ErrInvalidUploadConcurrencyLimits) {
		t.Fatalf(
			"LoadStorage() error = %v, want ErrInvalidUploadConcurrencyLimits",
			err,
		)
	}
}

func TestLoadStorageUsesOSSWithEnvironmentCredentials(t *testing.T) {
	appRoot := t.TempDir()
	t.Setenv("FILE_STORAGE_DRIVER", StorageDriverOSS)
	t.Setenv("STORAGE_ROOT", "object-staging")
	t.Setenv("STORAGE_MAX_FILE_SIZE_BYTES", "")
	clearUploadCapacityEnvironment(t)
	t.Setenv("OSS_BUCKET", "example-private-bucket")
	t.Setenv("OSS_REGION", "cn-shanghai")
	t.Setenv("OSS_ENDPOINT", "https://oss-cn-shanghai.aliyuncs.com")
	t.Setenv("OSS_CREDENTIAL_MODE", OSSCredentialModeEnvironment)
	t.Setenv("OSS_ECS_RAM_ROLE", "")
	t.Setenv("OSS_ACCESS_KEY_ID", "test-access-key-id")
	t.Setenv("OSS_ACCESS_KEY_SECRET", "test-access-key-secret")

	storageConfig, err := LoadStorage(appRoot)
	if err != nil {
		t.Fatalf("LoadStorage() error = %v, want nil", err)
	}
	if storageConfig.Driver != StorageDriverOSS {
		t.Fatalf("Driver = %q, want %q", storageConfig.Driver, StorageDriverOSS)
	}
	if storageConfig.RootDir != filepath.Join(appRoot, "object-staging") {
		t.Fatalf("RootDir = %q, want object staging below APP_ROOT", storageConfig.RootDir)
	}
	expectedOSS := OSSConfig{
		Bucket:         "example-private-bucket",
		Region:         "cn-shanghai",
		Endpoint:       "https://oss-cn-shanghai.aliyuncs.com",
		CredentialMode: OSSCredentialModeEnvironment,
	}
	if storageConfig.OSS != expectedOSS {
		t.Fatalf("OSS = %+v, want %+v", storageConfig.OSS, expectedOSS)
	}
}

func TestLoadStorageUsesOSSWithECSRAMRole(t *testing.T) {
	t.Setenv("FILE_STORAGE_DRIVER", StorageDriverOSS)
	t.Setenv("STORAGE_ROOT", "")
	t.Setenv("STORAGE_MAX_FILE_SIZE_BYTES", "")
	clearUploadCapacityEnvironment(t)
	t.Setenv("OSS_BUCKET", "example-private-bucket")
	t.Setenv("OSS_REGION", "cn-shanghai")
	t.Setenv("OSS_ENDPOINT", "https://oss-cn-shanghai-internal.aliyuncs.com")
	t.Setenv("OSS_CREDENTIAL_MODE", OSSCredentialModeECSRAMRole)
	t.Setenv("OSS_ECS_RAM_ROLE", "RagReasoningPlatformTestEcsRole")
	t.Setenv("OSS_ACCESS_KEY_ID", "")
	t.Setenv("OSS_ACCESS_KEY_SECRET", "")

	storageConfig, err := LoadStorage(t.TempDir())
	if err != nil {
		t.Fatalf("LoadStorage() error = %v, want nil", err)
	}
	if storageConfig.OSS.CredentialMode != OSSCredentialModeECSRAMRole {
		t.Fatalf(
			"CredentialMode = %q, want %q",
			storageConfig.OSS.CredentialMode,
			OSSCredentialModeECSRAMRole,
		)
	}
	if storageConfig.OSS.ECSRAMRole != "RagReasoningPlatformTestEcsRole" {
		t.Fatalf("ECSRAMRole = %q, want configured role", storageConfig.OSS.ECSRAMRole)
	}
}

func TestLoadStorageRejectsInvalidDriverAndOSSConfiguration(t *testing.T) {
	testCases := []struct {
		name        string
		environment map[string]string
	}{
		{
			name: "unknown driver",
			environment: map[string]string{
				"FILE_STORAGE_DRIVER": "s3",
			},
		},
		{
			name: "missing bucket",
			environment: map[string]string{
				"FILE_STORAGE_DRIVER":   StorageDriverOSS,
				"OSS_REGION":            "cn-shanghai",
				"OSS_ENDPOINT":          "https://oss-cn-shanghai.aliyuncs.com",
				"OSS_CREDENTIAL_MODE":   OSSCredentialModeEnvironment,
				"OSS_ACCESS_KEY_ID":     "id",
				"OSS_ACCESS_KEY_SECRET": "secret",
			},
		},
		{
			name: "insecure endpoint",
			environment: map[string]string{
				"FILE_STORAGE_DRIVER":   StorageDriverOSS,
				"OSS_BUCKET":            "bucket",
				"OSS_REGION":            "cn-shanghai",
				"OSS_ENDPOINT":          "http://oss-cn-shanghai.aliyuncs.com",
				"OSS_CREDENTIAL_MODE":   OSSCredentialModeEnvironment,
				"OSS_ACCESS_KEY_ID":     "id",
				"OSS_ACCESS_KEY_SECRET": "secret",
			},
		},
		{
			name: "environment credentials missing",
			environment: map[string]string{
				"FILE_STORAGE_DRIVER": StorageDriverOSS,
				"OSS_BUCKET":          "bucket",
				"OSS_REGION":          "cn-shanghai",
				"OSS_ENDPOINT":        "https://oss-cn-shanghai.aliyuncs.com",
				"OSS_CREDENTIAL_MODE": OSSCredentialModeEnvironment,
			},
		},
		{
			name: "ECS RAM role missing",
			environment: map[string]string{
				"FILE_STORAGE_DRIVER": StorageDriverOSS,
				"OSS_BUCKET":          "bucket",
				"OSS_REGION":          "cn-shanghai",
				"OSS_ENDPOINT":        "https://oss-cn-shanghai-internal.aliyuncs.com",
				"OSS_CREDENTIAL_MODE": OSSCredentialModeECSRAMRole,
			},
		},
	}

	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Setenv("STORAGE_ROOT", "")
			t.Setenv("STORAGE_MAX_FILE_SIZE_BYTES", "")
			clearUploadCapacityEnvironment(t)
			clearStorageBackendEnvironment(t)
			for name, value := range testCase.environment {
				t.Setenv(name, value)
			}

			if _, err := LoadStorage(t.TempDir()); err == nil {
				t.Fatal("LoadStorage() error = nil, want invalid storage configuration error")
			}
		})
	}
}
