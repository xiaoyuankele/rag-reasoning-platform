package pythonprocessor

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"

	documentapplication "rag-reasoning-platform/backend/internal/application/document"
)

var (
	// ErrSourceMaterializerRequired 表示没有提供把存储对象准备为本地文件的能力。
	ErrSourceMaterializerRequired = errors.New(
		"Python processor source materializer is required",
	)

	// ErrMaterializedSourcePathInvalid 表示实现没有返回 Python 可读取的绝对路径。
	ErrMaterializedSourcePathInvalid = errors.New(
		"materialized Python source path must be absolute",
	)

	// ErrMaterializedSourceReleaseRequired 表示实现没有返回配套清理函数。
	ErrMaterializedSourceReleaseRequired = errors.New(
		"materialized Python source release function is required",
	)
)

// StoredFileMaterializer 把不透明存储键转换为本次 Python 调用可读取的
// 本地绝对路径。
//
// LocalStorage 可以直接返回受控绝对路径；未来对象存储实现可以下载到临时
// 文件。release 必须非 nil，并负责删除临时文件或释放其他本地资源。
type StoredFileMaterializer interface {
	Materialize(
		ctx context.Context,
		storagePath string,
	) (localPath string, release func() error, err error)
}

func materializeSourceFile(
	ctx context.Context,
	files StoredFileMaterializer,
	storagePath string,
) (string, func() error, error) {
	if err := ctx.Err(); err != nil {
		return "", nil, err
	}

	localPath, release, err := files.Materialize(ctx, storagePath)
	if err != nil {
		return "", nil, fmt.Errorf(
			"materialize Python processing source: %w",
			err,
		)
	}
	if release == nil {
		return "", nil, ErrMaterializedSourceReleaseRequired
	}

	localPath = strings.TrimSpace(localPath)
	if localPath == "" || !filepath.IsAbs(localPath) {
		// 实现已经创建了本地资源时，即使路径契约无效也必须尝试清理。
		releaseErr := release()
		pathErr := fmt.Errorf(
			"%w: %q",
			ErrMaterializedSourcePathInvalid,
			localPath,
		)
		if releaseErr != nil {
			return "", nil, errors.Join(
				pathErr,
				fmt.Errorf("release invalid materialized source: %w", releaseErr),
			)
		}
		return "", nil, pathErr
	}

	return filepath.Clean(localPath), release, nil
}

func releaseMaterializedSource(
	result *documentapplication.ProcessingResult,
	processingErr *error,
	release func() error,
) {
	releaseErr := release()
	if releaseErr == nil {
		return
	}

	wrappedReleaseErr := fmt.Errorf(
		"release materialized Python processing source: %w",
		releaseErr,
	)
	*result = documentapplication.ProcessingResult{}
	if *processingErr != nil {
		*processingErr = errors.Join(*processingErr, wrappedReleaseErr)
		return
	}
	*processingErr = wrappedReleaseErr
}
