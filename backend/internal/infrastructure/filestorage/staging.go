package filestorage

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"unicode/utf8"

	applicationdocument "rag-reasoning-platform/backend/internal/application/document"
)

const pdfHeader = "%PDF-"

// fileFormat 描述一种允许上传的文件格式。
type fileFormat struct {
	storageExtension string
	mimeType         string
}

// stagedDocument 是已经完整读取、校验并关闭的临时文件。
// 调用方取得它后必须把文件移动为正式本地文件，或在上传对象后执行 Remove。
type stagedDocument struct {
	path      string
	format    fileFormat
	sizeBytes int64
	sha256    string
}

// Remove 幂等删除临时文件。
func (s stagedDocument) Remove() error {
	err := os.Remove(s.path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("remove staged document file: %w", err)
	}

	return nil
}

// resolveFileFormat 根据原始文件名确定规范化扩展名和可信 MIME。
// 文件名只选择预期格式；stageDocumentUpload 仍会校验真实内容。
func resolveFileFormat(originalName string) (fileFormat, error) {
	extension := strings.ToLower(
		filepath.Ext(strings.TrimSpace(originalName)),
	)

	switch extension {
	case ".pdf":
		return fileFormat{
			storageExtension: ".pdf",
			mimeType:         "application/pdf",
		}, nil

	case ".md", ".markdown":
		return fileFormat{
			storageExtension: ".md",
			mimeType:         "text/markdown",
		}, nil

	case ".txt":
		return fileFormat{
			storageExtension: ".txt",
			mimeType:         "text/plain",
		}, nil

	default:
		return fileFormat{}, applicationdocument.ErrUnsupportedFileType
	}
}

// contextReader 在每次读取前检查请求是否已取消。
type contextReader struct {
	ctx    context.Context
	reader io.Reader
}

// Read 实现 io.Reader 接口。
func (r *contextReader) Read(p []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}

	return r.reader.Read(p)
}

// validatePDFHeader 检查文件开头是否包含 PDF 标识。
// 返回 bufio.Reader，保证 Peek 读取过的字节仍能写入临时文件。
func validatePDFHeader(content io.Reader) (*bufio.Reader, error) {
	if content == nil {
		return nil, applicationdocument.ErrInvalidPDFContent
	}

	bufferedContent := bufio.NewReader(content)
	header, err := bufferedContent.Peek(len(pdfHeader))
	if errors.Is(err, io.EOF) {
		return nil, applicationdocument.ErrInvalidPDFContent
	}
	if err != nil {
		return nil, fmt.Errorf("read PDF header: %w", err)
	}
	if !bytes.Equal(header, []byte(pdfHeader)) {
		return nil, applicationdocument.ErrInvalidPDFContent
	}

	return bufferedContent, nil
}

// validateUTF8File 流式检查临时文本文件是否为合法 UTF-8。
func validateUTF8File(ctx context.Context, path string) error {
	file, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("open temporary text file for validation: %w", err)
	}
	defer func() {
		_ = file.Close()
	}()

	const readBufferSize = 32 * 1024
	buffer := make([]byte, readBufferSize+utf8.UTFMax)
	pendingBytes := 0

	for {
		if err := ctx.Err(); err != nil {
			return fmt.Errorf("validate text file: %w", err)
		}

		bytesRead, readErr := file.Read(buffer[pendingBytes:])
		data := buffer[:pendingBytes+bytesRead]
		offset := 0

		for offset < len(data) {
			remaining := data[offset:]
			if !utf8.FullRune(remaining) {
				break
			}

			runeValue, runeSize := utf8.DecodeRune(remaining)
			if runeValue == utf8.RuneError && runeSize == 1 {
				return applicationdocument.ErrInvalidTextContent
			}

			offset += runeSize
		}

		pendingBytes = copy(buffer, data[offset:])

		if errors.Is(readErr, io.EOF) {
			if pendingBytes != 0 {
				return applicationdocument.ErrInvalidTextContent
			}

			return nil
		}
		if readErr != nil {
			return fmt.Errorf("read temporary text file for validation: %w", readErr)
		}
	}
}

// stageDocumentUpload 统一完成 LocalStorage 与 ObjectStorage 的上传前处理：
// 格式选择、上下文取消、流式限长、SHA-256 和真实内容校验。
func stageDocumentUpload(
	ctx context.Context,
	directory string,
	maxSizeBytes int64,
	originalName string,
	content io.Reader,
) (result stagedDocument, resultErr error) {
	if err := ctx.Err(); err != nil {
		return stagedDocument{}, fmt.Errorf("stage document upload: %w", err)
	}

	format, err := resolveFileFormat(originalName)
	if err != nil {
		return stagedDocument{}, err
	}
	if content == nil {
		if format.storageExtension == ".pdf" {
			return stagedDocument{}, applicationdocument.ErrInvalidPDFContent
		}

		return stagedDocument{}, applicationdocument.ErrInvalidTextContent
	}

	contextContent := &contextReader{ctx: ctx, reader: content}
	var contentToStore io.Reader = contextContent
	if format.storageExtension == ".pdf" {
		bufferedContent, err := validatePDFHeader(contextContent)
		if err != nil {
			return stagedDocument{}, err
		}
		contentToStore = bufferedContent
	}

	temporaryFile, err := os.CreateTemp(directory, "document-*.tmp")
	if err != nil {
		return stagedDocument{}, fmt.Errorf("create temporary file: %w", err)
	}
	temporaryPath := temporaryFile.Name()
	temporaryFileOpen := true
	keepTemporaryFile := false
	defer func() {
		var cleanupErrors []error
		if temporaryFileOpen {
			if err := temporaryFile.Close(); err != nil {
				cleanupErrors = append(
					cleanupErrors,
					fmt.Errorf("close unfinished staged document file: %w", err),
				)
			}
		}
		if !keepTemporaryFile {
			removeErr := os.Remove(temporaryPath)
			if removeErr != nil && !errors.Is(removeErr, os.ErrNotExist) {
				cleanupErrors = append(
					cleanupErrors,
					fmt.Errorf("remove unfinished staged document file: %w", removeErr),
				)
			}
		}
		if len(cleanupErrors) != 0 {
			result = stagedDocument{}
			resultErr = errors.Join(
				append([]error{resultErr}, cleanupErrors...)...,
			)
		}
	}()

	hasher := sha256.New()
	limitedContent := io.LimitReader(contentToStore, maxSizeBytes+1)
	sizeBytes, err := io.Copy(
		io.MultiWriter(temporaryFile, hasher),
		limitedContent,
	)
	if err != nil {
		return stagedDocument{}, fmt.Errorf("write temporary document file: %w", err)
	}
	if sizeBytes > maxSizeBytes {
		return stagedDocument{}, applicationdocument.ErrFileTooLarge
	}
	if err := temporaryFile.Close(); err != nil {
		return stagedDocument{}, fmt.Errorf("close temporary document file: %w", err)
	}
	temporaryFileOpen = false
	if format.storageExtension != ".pdf" {
		if err := validateUTF8File(ctx, temporaryPath); err != nil {
			return stagedDocument{}, err
		}
	}

	keepTemporaryFile = true
	result = stagedDocument{
		path:      temporaryPath,
		format:    format,
		sizeBytes: sizeBytes,
		sha256:    hex.EncodeToString(hasher.Sum(nil)),
	}
	return result, nil
}
