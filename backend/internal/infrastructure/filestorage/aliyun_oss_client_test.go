package filestorage

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/aliyun/alibabacloud-oss-go-sdk-v2/oss"
)

type fakeAliyunOSSAPI struct {
	putRequest    *oss.PutObjectRequest
	putContent    string
	putErr        error
	getRequest    *oss.GetObjectRequest
	getResult     *oss.GetObjectResult
	getErr        error
	deleteRequest *oss.DeleteObjectRequest
	deleteErr     error
}

func (f *fakeAliyunOSSAPI) PutObject(
	_ context.Context,
	request *oss.PutObjectRequest,
	_ ...func(*oss.Options),
) (*oss.PutObjectResult, error) {
	f.putRequest = request
	if request != nil && request.Body != nil {
		content, err := io.ReadAll(request.Body)
		if err != nil {
			return nil, err
		}
		f.putContent = string(content)
	}
	if f.putErr != nil {
		return nil, f.putErr
	}
	return &oss.PutObjectResult{}, nil
}

func (f *fakeAliyunOSSAPI) GetObject(
	_ context.Context,
	request *oss.GetObjectRequest,
	_ ...func(*oss.Options),
) (*oss.GetObjectResult, error) {
	f.getRequest = request
	return f.getResult, f.getErr
}

func (f *fakeAliyunOSSAPI) DeleteObject(
	_ context.Context,
	request *oss.DeleteObjectRequest,
	_ ...func(*oss.Options),
) (*oss.DeleteObjectResult, error) {
	f.deleteRequest = request
	if f.deleteErr != nil {
		return nil, f.deleteErr
	}
	return &oss.DeleteObjectResult{}, nil
}

func TestAliyunOSSObjectClientPutObjectMapsMetadata(t *testing.T) {
	api := &fakeAliyunOSSAPI{}
	client, err := newAliyunOSSObjectClient(api, "private-bucket")
	if err != nil {
		t.Fatalf("newAliyunOSSObjectClient() error = %v, want nil", err)
	}

	err = client.PutObject(
		context.Background(),
		"documents/document-a.pdf",
		strings.NewReader("pdf-content"),
		ObjectMetadata{
			ContentType:   "application/pdf",
			ContentLength: 11,
			SHA256:        strings.Repeat("a", 64),
		},
	)
	if err != nil {
		t.Fatalf("PutObject() error = %v, want nil", err)
	}
	if api.putRequest == nil {
		t.Fatal("PutObject() did not call OSS API")
	}
	if got := oss.ToString(api.putRequest.Bucket); got != "private-bucket" {
		t.Fatalf("Bucket = %q, want private-bucket", got)
	}
	if got := oss.ToString(api.putRequest.Key); got != "documents/document-a.pdf" {
		t.Fatalf("Key = %q, want document object key", got)
	}
	if api.putContent != "pdf-content" {
		t.Fatalf("Body = %q, want pdf-content", api.putContent)
	}
	if api.putRequest.ContentLength == nil || *api.putRequest.ContentLength != 11 {
		t.Fatalf("ContentLength = %v, want 11", api.putRequest.ContentLength)
	}
	if got := oss.ToString(api.putRequest.ContentType); got != "application/pdf" {
		t.Fatalf("ContentType = %q, want application/pdf", got)
	}
	if api.putRequest.Metadata["sha256"] != strings.Repeat("a", 64) {
		t.Fatalf("SHA256 metadata = %q, want trusted digest", api.putRequest.Metadata["sha256"])
	}
	if got := oss.ToString(api.putRequest.ForbidOverwrite); got != "true" {
		t.Fatalf("ForbidOverwrite = %q, want true", got)
	}
}

func TestAliyunOSSObjectClientGetAndDeleteObject(t *testing.T) {
	api := &fakeAliyunOSSAPI{
		getResult: &oss.GetObjectResult{
			Body: io.NopCloser(strings.NewReader("stored-content")),
		},
	}
	client, err := newAliyunOSSObjectClient(api, "private-bucket")
	if err != nil {
		t.Fatalf("newAliyunOSSObjectClient() error = %v, want nil", err)
	}

	reader, err := client.GetObject(
		context.Background(),
		"documents/document-a.pdf",
	)
	if err != nil {
		t.Fatalf("GetObject() error = %v, want nil", err)
	}
	content, err := io.ReadAll(reader)
	if err != nil {
		t.Fatalf("ReadAll() error = %v, want nil", err)
	}
	if err := reader.Close(); err != nil {
		t.Fatalf("Close() error = %v, want nil", err)
	}
	if string(content) != "stored-content" {
		t.Fatalf("content = %q, want stored-content", content)
	}

	if err := client.DeleteObject(
		context.Background(),
		"documents/document-a.pdf",
	); err != nil {
		t.Fatalf("DeleteObject() error = %v, want nil", err)
	}
	if got := oss.ToString(api.getRequest.Bucket); got != "private-bucket" {
		t.Fatalf("GetObject bucket = %q, want private-bucket", got)
	}
	if got := oss.ToString(api.deleteRequest.Key); got != "documents/document-a.pdf" {
		t.Fatalf("DeleteObject key = %q, want document object key", got)
	}
}

func TestAliyunOSSObjectClientNormalizesNotFound(t *testing.T) {
	api := &fakeAliyunOSSAPI{
		getErr: &oss.ServiceError{
			StatusCode: 404,
			Code:       "NoSuchKey",
		},
		deleteErr: &oss.ServiceError{
			StatusCode: 404,
			Code:       "NoSuchKey",
		},
	}
	client, err := newAliyunOSSObjectClient(api, "private-bucket")
	if err != nil {
		t.Fatalf("newAliyunOSSObjectClient() error = %v, want nil", err)
	}

	if _, err := client.GetObject(
		context.Background(),
		"documents/missing.pdf",
	); !errors.Is(err, ErrObjectNotFound) {
		t.Fatalf("GetObject() error = %v, want ErrObjectNotFound", err)
	}
	if err := client.DeleteObject(
		context.Background(),
		"documents/missing.pdf",
	); !errors.Is(err, ErrObjectNotFound) {
		t.Fatalf("DeleteObject() error = %v, want ErrObjectNotFound", err)
	}
}

func TestNewAliyunOSSObjectClientRejectsInvalidConfiguration(t *testing.T) {
	testCases := []struct {
		name   string
		config AliyunOSSClientConfig
	}{
		{name: "missing bucket", config: AliyunOSSClientConfig{}},
		{
			name: "missing region",
			config: AliyunOSSClientConfig{
				Bucket: "bucket",
			},
		},
		{
			name: "missing endpoint",
			config: AliyunOSSClientConfig{
				Bucket: "bucket",
				Region: "cn-shanghai",
			},
		},
		{
			name: "invalid credential mode",
			config: AliyunOSSClientConfig{
				Bucket:         "bucket",
				Region:         "cn-shanghai",
				Endpoint:       "https://oss-cn-shanghai.aliyuncs.com",
				CredentialMode: "unknown",
			},
		},
		{
			name: "missing ECS RAM role",
			config: AliyunOSSClientConfig{
				Bucket:         "bucket",
				Region:         "cn-shanghai",
				Endpoint:       "https://oss-cn-shanghai-internal.aliyuncs.com",
				CredentialMode: OSSCredentialModeECSRAMRole,
			},
		},
	}

	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			if _, err := NewAliyunOSSObjectClient(testCase.config); err == nil {
				t.Fatal("NewAliyunOSSObjectClient() error = nil, want invalid configuration error")
			}
		})
	}
}
