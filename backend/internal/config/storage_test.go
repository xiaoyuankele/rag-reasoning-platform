package config

import (
	"path/filepath"
	"testing"
)

// TestLoadStorageUsesDefaults 验证没有设置环境变量时使用项目默认值。
func TestLoadStorageUsesDefaults(t *testing.T) {
	appRoot := t.TempDir()
	t.Setenv("STORAGE_ROOT", "")
	t.Setenv("STORAGE_MAX_FILE_SIZE_BYTES", "")

	storageConfig, err := LoadStorage(appRoot)
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}

	expected := StorageConfig{
		RootDir:          filepath.Join(appRoot, "storage"),
		MaxFileSizeBytes: 200 * 1024 * 1024,
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

	storageConfig, err := LoadStorage(appRoot)
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}

	expected := StorageConfig{
		RootDir:          filepath.Join(appRoot, "custom-storage"),
		MaxFileSizeBytes: 1048576,
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
