package filestorage

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	applicationdocument "rag-reasoning-platform/backend/internal/application/document"
)

// cancelAfterHeaderReader 第一次读取返回合法 PDF 文件头，
// 随后立即取消上下文，用于模拟上传过程中被客户端取消。
type cancelAfterHeaderReader struct {
	cancel     context.CancelFunc
	headerSent bool
}

func (r *cancelAfterHeaderReader) Read(p []byte) (int, error) {
	if r.headerSent {
		return 0, io.EOF
	}

	r.headerSent = true
	n := copy(p, []byte("%PDF-"))
	r.cancel()

	return n, nil
}

// TestNewLocalStorageCreatesDocumentsDirectory 验证构造本地文件存储时，
// 会在指定根目录下准备 documents 子目录。
func TestNewLocalStorageCreatesDocumentsDirectory(t *testing.T) {
	// TempDir 为当前测试创建独立临时目录。
	// 测试结束后，Go 会自动删除该目录及其内容。
	rootDir := t.TempDir()

	storage, err := NewLocalStorage(rootDir, 1024)
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}

	if storage == nil {
		t.Fatal("expected LocalStorage, got nil")
	}

	documentsDir := filepath.Join(rootDir, "documents")
	info, err := os.Stat(documentsDir)
	if err != nil {
		t.Fatalf("stat documents directory: %v", err)
	}

	if !info.IsDir() {
		t.Fatalf("expected %q to be a directory", documentsDir)
	}
}

// TestNewLocalStorageRejectsInvalidConfiguration 验证构造函数会拒绝
// 空根目录和非正数的最大文件大小。
func TestNewLocalStorageRejectsInvalidConfiguration(t *testing.T) {
	tests := []struct {
		name          string
		rootDir       string
		maxSizeBytes  int64
		expectedError error
	}{
		{
			name:          "blank root directory",
			rootDir:       "   ",
			maxSizeBytes:  1024,
			expectedError: ErrRootDirectoryRequired,
		},
		{
			name:          "zero maximum size",
			rootDir:       t.TempDir(),
			maxSizeBytes:  0,
			expectedError: ErrInvalidMaxFileSize,
		},
		{
			name:          "negative maximum size",
			rootDir:       t.TempDir(),
			maxSizeBytes:  -1,
			expectedError: ErrInvalidMaxFileSize,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := NewLocalStorage(
				test.rootDir,
				test.maxSizeBytes,
			)
			if !errors.Is(err, test.expectedError) {
				t.Fatalf(
					"expected error %v, got %v",
					test.expectedError,
					err,
				)
			}
		})
	}
}

// TestLocalStorageSaveWritesFileAndCalculatesMetadata 验证成功保存的主流程。
func TestLocalStorageSaveWritesFileAndCalculatesMetadata(t *testing.T) {
	rootDir := t.TempDir()
	storage, err := NewLocalStorage(rootDir, 1024)
	if err != nil {
		t.Fatalf("create local storage: %v", err)
	}

	content := []byte("%PDF-1.7\ntest document")
	storedFile, err := storage.Save(
		context.Background(),
		"example.pdf",
		bytes.NewReader(content),
	)
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}

	if storedFile.SizeBytes != int64(len(content)) {
		t.Fatalf(
			"expected size %d, got %d",
			len(content),
			storedFile.SizeBytes,
		)
	}

	if storedFile.MIMEType != "application/pdf" {
		t.Fatalf(
			"expected MIME type %q, got %q",
			"application/pdf",
			storedFile.MIMEType,
		)
	}

	expectedHash := sha256.Sum256(content)
	expectedSHA256 := hex.EncodeToString(expectedHash[:])
	if storedFile.SHA256 != expectedSHA256 {
		t.Fatalf(
			"expected SHA-256 %q, got %q",
			expectedSHA256,
			storedFile.SHA256,
		)
	}

	normalizedStoragePath := filepath.ToSlash(storedFile.StoragePath)
	if !strings.HasPrefix(normalizedStoragePath, "documents/") {
		t.Fatalf(
			"expected path inside documents directory, got %q",
			storedFile.StoragePath,
		)
	}

	if filepath.Ext(storedFile.StoragePath) != ".pdf" {
		t.Fatalf(
			"expected .pdf storage path, got %q",
			storedFile.StoragePath,
		)
	}

	if filepath.IsAbs(storedFile.StoragePath) {
		t.Fatalf(
			"expected relative storage path, got %q",
			storedFile.StoragePath,
		)
	}

	absoluteStoragePath := filepath.Join(
		rootDir,
		filepath.FromSlash(storedFile.StoragePath),
	)
	savedContent, err := os.ReadFile(absoluteStoragePath)
	if err != nil {
		t.Fatalf("read saved file: %v", err)
	}

	if !bytes.Equal(savedContent, content) {
		t.Fatalf(
			"expected saved content %q, got %q",
			string(content),
			string(savedContent),
		)
	}

	entries, err := os.ReadDir(filepath.Join(rootDir, "documents"))
	if err != nil {
		t.Fatalf("read documents directory: %v", err)
	}

	if len(entries) != 1 {
		t.Fatalf(
			"expected one finalized file, got %d entries",
			len(entries),
		)
	}

	if filepath.Ext(entries[0].Name()) != ".pdf" {
		t.Fatalf(
			"expected only a .pdf file, got %q",
			entries[0].Name(),
		)
	}
}

// TestLocalStorageSaveSupportsTextFormats 验证 Markdown 和纯文本会使用
// 规范化扩展名保存，并向应用层返回可信 MIME 类型。
func TestLocalStorageSaveSupportsTextFormats(t *testing.T) {
	tests := []struct {
		name              string
		originalName      string
		content           []byte
		expectedExtension string
		expectedMIME      string
	}{
		{
			name:              "MD file",
			originalName:      "notes.md",
			content:           []byte("# 标题\nMarkdown 内容"),
			expectedExtension: ".md",
			expectedMIME:      "text/markdown",
		},
		{
			name:              "Markdown extension is normalized",
			originalName:      "notes.markdown",
			content:           []byte("# Normalized extension"),
			expectedExtension: ".md",
			expectedMIME:      "text/markdown",
		},
		{
			name:              "TXT file",
			originalName:      "notes.txt",
			content:           []byte("plain text 内容"),
			expectedExtension: ".txt",
			expectedMIME:      "text/plain",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			rootDir := t.TempDir()
			storage, err := NewLocalStorage(rootDir, 1024)
			if err != nil {
				t.Fatalf("create local storage: %v", err)
			}

			storedFile, err := storage.Save(
				context.Background(),
				test.originalName,
				bytes.NewReader(test.content),
			)
			if err != nil {
				t.Fatalf("save text document: %v", err)
			}

			if filepath.Ext(storedFile.StoragePath) != test.expectedExtension {
				t.Fatalf(
					"expected extension %q, got path %q",
					test.expectedExtension,
					storedFile.StoragePath,
				)
			}

			if storedFile.MIMEType != test.expectedMIME {
				t.Fatalf(
					"expected MIME type %q, got %q",
					test.expectedMIME,
					storedFile.MIMEType,
				)
			}

			absolutePath := filepath.Join(
				rootDir,
				filepath.FromSlash(storedFile.StoragePath),
			)
			savedContent, err := os.ReadFile(absolutePath)
			if err != nil {
				t.Fatalf("read saved text document: %v", err)
			}

			if !bytes.Equal(savedContent, test.content) {
				t.Fatalf(
					"expected content %q, got %q",
					string(test.content),
					string(savedContent),
				)
			}
		})
	}
}

// TestLocalStorageSaveAcceptsUTF8RuneAcrossReadBoundary 验证多字节 UTF-8
// 字符跨越验证缓冲区边界时不会被误判为非法文本。
func TestLocalStorageSaveAcceptsUTF8RuneAcrossReadBoundary(t *testing.T) {
	rootDir := t.TempDir()
	content := append(bytes.Repeat([]byte("a"), 32*1024+3), []byte("中")...)
	storage, err := NewLocalStorage(rootDir, int64(len(content)))
	if err != nil {
		t.Fatalf("create local storage: %v", err)
	}

	_, err = storage.Save(
		context.Background(),
		"boundary.txt",
		bytes.NewReader(content),
	)
	if err != nil {
		t.Fatalf("expected valid UTF-8 to be accepted, got %v", err)
	}
}

// TestLocalStorageSaveRejectsFileLargerThanLimit 验证超限文件会被拒绝，
// 并且未完成的临时文件会被清理。
func TestLocalStorageSaveRejectsFileLargerThanLimit(t *testing.T) {
	rootDir := t.TempDir()
	storage, err := NewLocalStorage(rootDir, 8)
	if err != nil {
		t.Fatalf("create local storage: %v", err)
	}

	_, err = storage.Save(
		context.Background(),
		"too-large.pdf",
		bytes.NewReader([]byte("%PDF-1234")),
	)
	if !errors.Is(err, applicationdocument.ErrFileTooLarge) {
		t.Fatalf("expected application ErrFileTooLarge, got %v", err)
	}

	entries, err := os.ReadDir(filepath.Join(rootDir, "documents"))
	if err != nil {
		t.Fatalf("read documents directory: %v", err)
	}

	if len(entries) != 0 {
		t.Fatalf(
			"expected no files after rejected upload, got %d",
			len(entries),
		)
	}
}

// TestLocalStorageSaveAcceptsFileAtExactSizeLimit 防止大小判断出现
// 大于和大于等于之间的边界错误。
func TestLocalStorageSaveAcceptsFileAtExactSizeLimit(t *testing.T) {
	content := []byte("%PDF-123")
	storage, err := NewLocalStorage(t.TempDir(), int64(len(content)))
	if err != nil {
		t.Fatalf("create local storage: %v", err)
	}

	storedFile, err := storage.Save(
		context.Background(),
		"exact-limit.pdf",
		bytes.NewReader(content),
	)
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}

	if storedFile.SizeBytes != int64(len(content)) {
		t.Fatalf(
			"expected size %d, got %d",
			len(content),
			storedFile.SizeBytes,
		)
	}
}

// TestLocalStorageSaveUsesUniqueStoragePaths 验证相同内容重复上传时，
// 每个文件仍然获得独立存储路径。
func TestLocalStorageSaveUsesUniqueStoragePaths(t *testing.T) {
	storage, err := NewLocalStorage(t.TempDir(), 1024)
	if err != nil {
		t.Fatalf("create local storage: %v", err)
	}

	content := []byte("%PDF-1.7\nsame content")
	first, err := storage.Save(
		context.Background(),
		"first.pdf",
		bytes.NewReader(content),
	)
	if err != nil {
		t.Fatalf("save first document: %v", err)
	}

	second, err := storage.Save(
		context.Background(),
		"second.pdf",
		bytes.NewReader(content),
	)
	if err != nil {
		t.Fatalf("save second document: %v", err)
	}

	if first.StoragePath == second.StoragePath {
		t.Fatalf(
			"expected unique paths, both were %q",
			first.StoragePath,
		)
	}

	if first.SHA256 != second.SHA256 {
		t.Fatalf(
			"expected identical content hashes, got %q and %q",
			first.SHA256,
			second.SHA256,
		)
	}
}

// TestLocalStorageSaveRejectsUnsupportedFileType 验证扩展名白名单会在
// 创建临时文件前拒绝不支持的格式。
func TestLocalStorageSaveRejectsUnsupportedFileType(t *testing.T) {
	rootDir := t.TempDir()
	storage, err := NewLocalStorage(rootDir, 1024)
	if err != nil {
		t.Fatalf("create local storage: %v", err)
	}

	_, err = storage.Save(
		context.Background(),
		"report.docx",
		bytes.NewReader([]byte("DOCX content")),
	)
	if !errors.Is(err, applicationdocument.ErrUnsupportedFileType) {
		t.Fatalf(
			"expected ErrUnsupportedFileType, got %v",
			err,
		)
	}

	entries, readErr := os.ReadDir(filepath.Join(rootDir, "documents"))
	if readErr != nil {
		t.Fatalf("read documents directory: %v", readErr)
	}

	if len(entries) != 0 {
		t.Fatalf(
			"expected no files after unsupported upload, got %d",
			len(entries),
		)
	}
}

// TestLocalStorageSaveRejectsInvalidUTF8 验证 Markdown 和 TXT 中的非法
// UTF-8 字节会被拒绝，并且失败后不会留下临时文件。
func TestLocalStorageSaveRejectsInvalidUTF8(t *testing.T) {
	tests := []struct {
		name         string
		originalName string
	}{
		{name: "invalid Markdown", originalName: "invalid.md"},
		{name: "invalid plain text", originalName: "invalid.txt"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			rootDir := t.TempDir()
			storage, err := NewLocalStorage(rootDir, 1024)
			if err != nil {
				t.Fatalf("create local storage: %v", err)
			}

			invalidContent := []byte{'v', 'a', 'l', 'i', 'd', 0xff}
			_, err = storage.Save(
				context.Background(),
				test.originalName,
				bytes.NewReader(invalidContent),
			)
			if !errors.Is(err, applicationdocument.ErrInvalidTextContent) {
				t.Fatalf(
					"expected ErrInvalidTextContent, got %v",
					err,
				)
			}

			entries, readErr := os.ReadDir(filepath.Join(rootDir, "documents"))
			if readErr != nil {
				t.Fatalf("read documents directory: %v", readErr)
			}

			if len(entries) != 0 {
				t.Fatalf(
					"expected no files after invalid UTF-8, got %d",
					len(entries),
				)
			}
		})
	}
}

// TestLocalStorageSaveRejectsNonPDFContent 验证不能只相信文件名；
// 不含 PDF 文件头的内容必须被拒绝且不能留下文件。
func TestLocalStorageSaveRejectsNonPDFContent(t *testing.T) {
	rootDir := t.TempDir()
	storage, err := NewLocalStorage(rootDir, 1024)
	if err != nil {
		t.Fatalf("create local storage: %v", err)
	}

	_, err = storage.Save(
		context.Background(),
		"fake.pdf",
		bytes.NewReader([]byte("this is plain text")),
	)
	if !errors.Is(err, applicationdocument.ErrInvalidPDFContent) {
		t.Errorf(
			"expected application ErrInvalidPDFContent, got %v",
			err,
		)
	}

	entries, readErr := os.ReadDir(filepath.Join(rootDir, "documents"))
	if readErr != nil {
		t.Fatalf("read documents directory: %v", readErr)
	}

	if len(entries) != 0 {
		t.Fatalf(
			"expected no files after invalid PDF, got %d",
			len(entries),
		)
	}
}

// TestLocalStorageSaveRejectsTruncatedPDFHeader 验证不足以组成 %PDF-
// 文件头的短内容同样会被识别为无效 PDF。
func TestLocalStorageSaveRejectsTruncatedPDFHeader(t *testing.T) {
	rootDir := t.TempDir()
	storage, err := NewLocalStorage(rootDir, 1024)
	if err != nil {
		t.Fatalf("create local storage: %v", err)
	}

	_, err = storage.Save(
		context.Background(),
		"truncated.pdf",
		bytes.NewReader([]byte("%PD")),
	)
	if !errors.Is(err, applicationdocument.ErrInvalidPDFContent) {
		t.Fatalf(
			"expected application ErrInvalidPDFContent, got %v",
			err,
		)
	}
}

// TestLocalStorageSaveStopsWhenContextCanceled 验证请求已取消时，
// Save 不会继续创建或写入文件。
func TestLocalStorageSaveStopsWhenContextCanceled(t *testing.T) {
	rootDir := t.TempDir()
	storage, err := NewLocalStorage(rootDir, 1024)
	if err != nil {
		t.Fatalf("create local storage: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err = storage.Save(
		ctx,
		"canceled.pdf",
		bytes.NewReader([]byte("%PDF-1.7\ntest document")),
	)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context.Canceled, got %v", err)
	}

	entries, readErr := os.ReadDir(filepath.Join(rootDir, "documents"))
	if readErr != nil {
		t.Fatalf("read documents directory: %v", readErr)
	}

	if len(entries) != 0 {
		t.Fatalf(
			"expected no files after canceled save, got %d",
			len(entries),
		)
	}
}

// TestLocalStorageSaveStopsWhenContextCanceledDuringRead 验证文件头读取后
// 才发生取消时，流式复制会停止并清理临时文件。
func TestLocalStorageSaveStopsWhenContextCanceledDuringRead(t *testing.T) {
	rootDir := t.TempDir()
	storage, err := NewLocalStorage(rootDir, 1024)
	if err != nil {
		t.Fatalf("create local storage: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	content := &cancelAfterHeaderReader{cancel: cancel}

	_, err = storage.Save(ctx, "canceled-during-read.pdf", content)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context.Canceled, got %v", err)
	}

	entries, readErr := os.ReadDir(filepath.Join(rootDir, "documents"))
	if readErr != nil {
		t.Fatalf("read documents directory: %v", readErr)
	}

	if len(entries) != 0 {
		t.Fatalf(
			"expected no files after canceled read, got %d",
			len(entries),
		)
	}
}

// TestLocalStorageDeleteRemovesStoredFile 验证 Delete 能根据相对存储路径
// 删除之前成功保存的文件。
func TestLocalStorageDeleteRemovesStoredFile(t *testing.T) {
	rootDir := t.TempDir()
	storage, err := NewLocalStorage(rootDir, 1024)
	if err != nil {
		t.Fatalf("create local storage: %v", err)
	}

	storedFile, err := storage.Save(
		context.Background(),
		"example.md",
		bytes.NewReader([]byte("# document to delete")),
	)
	if err != nil {
		t.Fatalf("save document: %v", err)
	}

	err = storage.Delete(
		context.Background(),
		storedFile.StoragePath,
	)
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}

	absoluteStoragePath := filepath.Join(
		rootDir,
		filepath.FromSlash(storedFile.StoragePath),
	)
	_, err = os.Stat(absoluteStoragePath)
	if !errors.Is(err, os.ErrNotExist) {
		t.Fatalf(
			"expected stored file to be deleted, stat error: %v",
			err,
		)
	}
}

// TestLocalStorageDeleteRejectsPathTraversal 验证相对路径不能通过 ..
// 逃离 storage/documents 目录并删除其他文件。
func TestLocalStorageDeleteRejectsPathTraversal(t *testing.T) {
	testDir := t.TempDir()
	rootDir := filepath.Join(testDir, "storage")

	storage, err := NewLocalStorage(rootDir, 1024)
	if err != nil {
		t.Fatalf("create local storage: %v", err)
	}

	outsidePath := filepath.Join(testDir, "outside.txt")
	outsideContent := []byte("must not be deleted")
	if err := os.WriteFile(outsidePath, outsideContent, 0o600); err != nil {
		t.Fatalf("create outside file: %v", err)
	}

	err = storage.Delete(
		context.Background(),
		"../outside.txt",
	)
	if !errors.Is(err, ErrInvalidStoragePath) {
		t.Errorf(
			"expected ErrInvalidStoragePath, got %v",
			err,
		)
	}

	remainingContent, readErr := os.ReadFile(outsidePath)
	if readErr != nil {
		t.Fatalf("outside file was removed or became unreadable: %v", readErr)
	}

	if !bytes.Equal(remainingContent, outsideContent) {
		t.Fatalf(
			"expected outside content %q, got %q",
			string(outsideContent),
			string(remainingContent),
		)
	}
}

// TestLocalStorageDeleteIsIdempotent 验证同一路径重复删除不会报错。
func TestLocalStorageDeleteIsIdempotent(t *testing.T) {
	rootDir := t.TempDir()
	storage, err := NewLocalStorage(rootDir, 1024)
	if err != nil {
		t.Fatalf("create local storage: %v", err)
	}

	storedFile, err := storage.Save(
		context.Background(),
		"example.pdf",
		bytes.NewReader([]byte("%PDF-1.7\ntest document")),
	)
	if err != nil {
		t.Fatalf("save document: %v", err)
	}

	for attempt := 1; attempt <= 2; attempt++ {
		if err := storage.Delete(
			context.Background(),
			storedFile.StoragePath,
		); err != nil {
			t.Fatalf(
				"delete attempt %d returned error: %v",
				attempt,
				err,
			)
		}
	}
}

// TestLocalStorageDeleteStopsWhenContextCanceled 验证删除开始前请求已取消时，
// Delete 返回 context.Canceled 并保留原文件。
func TestLocalStorageDeleteStopsWhenContextCanceled(t *testing.T) {
	rootDir := t.TempDir()
	storage, err := NewLocalStorage(rootDir, 1024)
	if err != nil {
		t.Fatalf("create local storage: %v", err)
	}

	storedFile, err := storage.Save(
		context.Background(),
		"example.pdf",
		bytes.NewReader([]byte("%PDF-1.7\ntest document")),
	)
	if err != nil {
		t.Fatalf("save document: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	err = storage.Delete(ctx, storedFile.StoragePath)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context.Canceled, got %v", err)
	}

	absoluteStoragePath := filepath.Join(
		rootDir,
		filepath.FromSlash(storedFile.StoragePath),
	)
	if _, err := os.Stat(absoluteStoragePath); err != nil {
		t.Fatalf("expected stored file to remain, stat error: %v", err)
	}
}

// TestLocalStorageDeleteRejectsInvalidPaths 验证 Delete 只接受
// documents 目录下具有受支持扩展名的单层相对路径。
func TestLocalStorageDeleteRejectsInvalidPaths(t *testing.T) {
	rootDir := t.TempDir()
	storage, err := NewLocalStorage(rootDir, 1024)
	if err != nil {
		t.Fatalf("create local storage: %v", err)
	}

	tests := []struct {
		name        string
		storagePath string
	}{
		{name: "empty path", storagePath: ""},
		{
			name: "absolute path",
			storagePath: filepath.Join(
				rootDir,
				"documents",
				"absolute.pdf",
			),
		},
		{name: "wrong directory", storagePath: "other/file.pdf"},
		{
			name:        "nested directory",
			storagePath: "documents/nested/file.pdf",
		},
		{name: "unsupported extension", storagePath: "documents/file.exe"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := storage.Delete(
				context.Background(),
				test.storagePath,
			)
			if !errors.Is(err, ErrInvalidStoragePath) {
				t.Fatalf(
					"expected ErrInvalidStoragePath, got %v",
					err,
				)
			}
		})
	}
}

// TestResolveFileFormat 验证上传文件名能够映射为固定的存储扩展名和 MIME，
// 同时拒绝白名单以外的格式。
func TestResolveFileFormat(t *testing.T) {
	tests := []struct {
		name           string
		originalName   string
		expectedFormat fileFormat
		expectedError  error
	}{
		{
			name:         "PDF extension is supported",
			originalName: "report.pdf",
			expectedFormat: fileFormat{
				storageExtension: ".pdf",
				mimeType:         "application/pdf",
			},
		},
		{
			name:         "uppercase PDF extension is normalized",
			originalName: "report.PDF",
			expectedFormat: fileFormat{
				storageExtension: ".pdf",
				mimeType:         "application/pdf",
			},
		},
		{
			name:         "MD extension is supported",
			originalName: "notes.md",
			expectedFormat: fileFormat{
				storageExtension: ".md",
				mimeType:         "text/markdown",
			},
		},
		{
			name:         "Markdown extension is normalized to MD",
			originalName: "notes.markdown",
			expectedFormat: fileFormat{
				storageExtension: ".md",
				mimeType:         "text/markdown",
			},
		},
		{
			name:         "TXT extension is supported",
			originalName: "notes.txt",
			expectedFormat: fileFormat{
				storageExtension: ".txt",
				mimeType:         "text/plain",
			},
		},
		{
			name:          "DOCX extension is rejected",
			originalName:  "report.docx",
			expectedError: applicationdocument.ErrUnsupportedFileType,
		},
		{
			name:          "missing extension is rejected",
			originalName:  "README",
			expectedError: applicationdocument.ErrUnsupportedFileType,
		},
		{
			name:          "double extension executable is rejected",
			originalName:  "report.pdf.exe",
			expectedError: applicationdocument.ErrUnsupportedFileType,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			actualFormat, err := resolveFileFormat(test.originalName)

			if test.expectedError != nil {
				if !errors.Is(err, test.expectedError) {
					t.Fatalf(
						"expected error %v, got %v",
						test.expectedError,
						err,
					)
				}

				return
			}

			if err != nil {
				t.Fatalf("expected no error, got %v", err)
			}

			if actualFormat != test.expectedFormat {
				t.Fatalf(
					"expected format %+v, got %+v",
					test.expectedFormat,
					actualFormat,
				)
			}
		})
	}
}
