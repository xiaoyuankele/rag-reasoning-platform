package main

import (
	"fmt"

	documentapplication "rag-reasoning-platform/backend/internal/application/document"
	"rag-reasoning-platform/backend/internal/config"
	"rag-reasoning-platform/backend/internal/infrastructure/filestorage"
	"rag-reasoning-platform/backend/internal/infrastructure/pythonprocessor"
)

// runtimeFileStorage 是组合根需要的完整文件存储能力。
//
// HTTP 上传、查询和删除使用 FileStorage；Python 文档处理还需要把不透明
// 存储键转换为本次调用可读的本地文件。LocalStorage 与 ObjectStorage 都
// 必须同时满足这两组契约。
type runtimeFileStorage interface {
	documentapplication.FileStorage
	pythonprocessor.StoredFileMaterializer
}

func newRuntimeFileStorage(
	storageConfig config.StorageConfig,
) (runtimeFileStorage, error) {
	switch storageConfig.Driver {
	case config.StorageDriverLocal:
		storage, err := filestorage.NewLocalStorage(
			storageConfig.RootDir,
			storageConfig.MaxFileSizeBytes,
		)
		if err != nil {
			return nil, fmt.Errorf("create local file storage: %w", err)
		}
		return storage, nil

	case config.StorageDriverOSS:
		client, err := filestorage.NewAliyunOSSObjectClient(
			filestorage.AliyunOSSClientConfig{
				Bucket:         storageConfig.OSS.Bucket,
				Region:         storageConfig.OSS.Region,
				Endpoint:       storageConfig.OSS.Endpoint,
				CredentialMode: storageConfig.OSS.CredentialMode,
				ECSRAMRole:     storageConfig.OSS.ECSRAMRole,
			},
		)
		if err != nil {
			return nil, fmt.Errorf("create Aliyun OSS client: %w", err)
		}

		storage, err := filestorage.NewObjectStorage(
			client,
			storageConfig.RootDir,
			storageConfig.MaxFileSizeBytes,
		)
		if err != nil {
			return nil, fmt.Errorf("create object file storage: %w", err)
		}
		return storage, nil

	default:
		return nil, fmt.Errorf(
			"unsupported file storage driver %q",
			storageConfig.Driver,
		)
	}
}
