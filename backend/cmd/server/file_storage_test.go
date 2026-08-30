package main

import (
	"testing"

	"rag-reasoning-platform/backend/internal/config"
	"rag-reasoning-platform/backend/internal/infrastructure/filestorage"
)

func TestNewRuntimeFileStorageBuildsLocalStorage(t *testing.T) {
	storage, err := newRuntimeFileStorage(config.StorageConfig{
		Driver:           config.StorageDriverLocal,
		RootDir:          t.TempDir(),
		MaxFileSizeBytes: 1024,
	})
	if err != nil {
		t.Fatalf("newRuntimeFileStorage() error = %v, want nil", err)
	}
	if _, ok := storage.(*filestorage.LocalStorage); !ok {
		t.Fatalf("storage type = %T, want *filestorage.LocalStorage", storage)
	}
}

func TestNewRuntimeFileStorageBuildsAliyunOSSStorage(t *testing.T) {
	storage, err := newRuntimeFileStorage(config.StorageConfig{
		Driver:           config.StorageDriverOSS,
		RootDir:          t.TempDir(),
		MaxFileSizeBytes: 1024,
		OSS: config.OSSConfig{
			Bucket:         "private-bucket",
			Region:         "cn-shanghai",
			Endpoint:       "https://oss-cn-shanghai.aliyuncs.com",
			CredentialMode: config.OSSCredentialModeEnvironment,
		},
	})
	if err != nil {
		t.Fatalf("newRuntimeFileStorage() error = %v, want nil", err)
	}
	if _, ok := storage.(*filestorage.ObjectStorage); !ok {
		t.Fatalf("storage type = %T, want *filestorage.ObjectStorage", storage)
	}
}

func TestNewRuntimeFileStorageRejectsUnknownDriver(t *testing.T) {
	if _, err := newRuntimeFileStorage(config.StorageConfig{
		Driver: "unknown",
	}); err == nil {
		t.Fatal("newRuntimeFileStorage() error = nil, want unsupported driver error")
	}
}
