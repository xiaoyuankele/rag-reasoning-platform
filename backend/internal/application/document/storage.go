package document

import (
	"context"
	"io"
)

// StoredFile 表示存储实现完整读取并校验文件后返回的可信元数据。
//
// StoragePath 是由存储实现生成的不透明存储键。Application 和 Domain 可以
// 持久化、传递这个值，但不能使用 filepath 拼接它，也不能假设它一定是本机路径。
type StoredFile struct {
	StoragePath string
	MIMEType    string
	SizeBytes   int64
	SHA256      string
}

// FileSaver 定义上传用例保存文件所需的最小能力。
type FileSaver interface {
	// Save 流式保存文件，并返回最终存储键、可信 MIME、大小和 SHA-256。
	//
	// 文件超限时返回 ErrFileTooLarge；扩展名不受支持时返回
	// ErrUnsupportedFileType；内容不符合声明格式时返回对应的稳定内容错误。
	Save(
		ctx context.Context,
		originalName string,
		content io.Reader,
	) (StoredFile, error)
}

// StoredFileOpener 定义流式读取一份已存储文件所需的最小能力。
type StoredFileOpener interface {
	// Open 返回的读取器必须由调用方关闭。
	Open(ctx context.Context, storagePath string) (io.ReadCloser, error)
}

// StoredFileDeleter 定义幂等删除已存储文件所需的最小能力。
type StoredFileDeleter interface {
	// Delete 必须保持幂等：目标已经不存在时仍视为删除成功。
	Delete(ctx context.Context, storagePath string) error
}

// UploadFileStorage 是上传用例实际需要的端口：保存失败补偿和重复文件清理
// 都要求同一个实现同时支持 Save 与 Delete。
type UploadFileStorage interface {
	FileSaver
	StoredFileDeleter
}

// FileStorage 描述一份完整的文档内容存储实现。
//
// 具体用例仍应依赖上面的最小接口；这个组合接口主要用于编译期证明 Local、
// 未来 Object Storage 等实现具备完整契约。
type FileStorage interface {
	FileSaver
	StoredFileOpener
	StoredFileDeleter
}
