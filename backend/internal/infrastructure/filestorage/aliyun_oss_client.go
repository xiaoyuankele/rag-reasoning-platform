package filestorage

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/aliyun/alibabacloud-oss-go-sdk-v2/oss"
	"github.com/aliyun/alibabacloud-oss-go-sdk-v2/oss/credentials"
)

const (
	// OSSCredentialModeEnvironment 使用 OSS SDK 标准环境变量凭证。
	OSSCredentialModeEnvironment = "environment"

	// OSSCredentialModeECSRAMRole 使用 ECS 实例绑定的 RAM Role 临时凭证。
	OSSCredentialModeECSRAMRole = "ecs_ram_role"
)

var (
	// ErrAliyunOSSAPIRequired 表示测试或生产构造时没有提供 OSS API。
	ErrAliyunOSSAPIRequired = errors.New("Aliyun OSS API is required")

	// ErrAliyunOSSBucketRequired 表示没有配置目标 Bucket。
	ErrAliyunOSSBucketRequired = errors.New("Aliyun OSS bucket is required")

	// ErrAliyunOSSRegionRequired 表示没有配置 Bucket 所在 Region。
	ErrAliyunOSSRegionRequired = errors.New("Aliyun OSS region is required")

	// ErrAliyunOSSEndpointRequired 表示没有配置访问 Endpoint。
	ErrAliyunOSSEndpointRequired = errors.New("Aliyun OSS endpoint is required")

	// ErrAliyunOSSCredentialModeInvalid 表示凭证来源不受支持。
	ErrAliyunOSSCredentialModeInvalid = errors.New(
		"Aliyun OSS credential mode is invalid",
	)
)

// AliyunOSSClientConfig 保存创建官方 OSS SDK 客户端所需的非敏感配置。
type AliyunOSSClientConfig struct {
	Bucket         string
	Region         string
	Endpoint       string
	CredentialMode string
	ECSRAMRole     string
}

type aliyunOSSAPI interface {
	PutObject(
		ctx context.Context,
		request *oss.PutObjectRequest,
		optFns ...func(*oss.Options),
	) (*oss.PutObjectResult, error)
	GetObject(
		ctx context.Context,
		request *oss.GetObjectRequest,
		optFns ...func(*oss.Options),
	) (*oss.GetObjectResult, error)
	DeleteObject(
		ctx context.Context,
		request *oss.DeleteObjectRequest,
		optFns ...func(*oss.Options),
	) (*oss.DeleteObjectResult, error)
}

// AliyunOSSObjectClient 把官方阿里云 OSS SDK 适配为项目稳定的 ObjectClient。
type AliyunOSSObjectClient struct {
	api    aliyunOSSAPI
	bucket string
}

var _ ObjectClient = (*AliyunOSSObjectClient)(nil)

// NewAliyunOSSObjectClient 创建真实 OSS 对象客户端。
//
// environment 模式由 SDK 在请求时读取 OSS_ACCESS_KEY_ID、
// OSS_ACCESS_KEY_SECRET 和可选 OSS_SESSION_TOKEN；ecs_ram_role 模式自动
// 获取并刷新 ECS 实例临时凭证。构造过程不会向 OSS 发起网络请求。
func NewAliyunOSSObjectClient(
	config AliyunOSSClientConfig,
) (*AliyunOSSObjectClient, error) {
	bucket := strings.TrimSpace(config.Bucket)
	if bucket == "" {
		return nil, ErrAliyunOSSBucketRequired
	}
	region := strings.TrimSpace(config.Region)
	if region == "" {
		return nil, ErrAliyunOSSRegionRequired
	}
	endpoint := strings.TrimSpace(config.Endpoint)
	if endpoint == "" {
		return nil, ErrAliyunOSSEndpointRequired
	}

	var provider credentials.CredentialsProvider
	switch strings.ToLower(strings.TrimSpace(config.CredentialMode)) {
	case OSSCredentialModeEnvironment:
		provider = credentials.NewEnvironmentVariableCredentialsProvider()
	case OSSCredentialModeECSRAMRole:
		role := strings.TrimSpace(config.ECSRAMRole)
		if role == "" {
			return nil, fmt.Errorf(
				"%w: ECS RAM role is required",
				ErrAliyunOSSCredentialModeInvalid,
			)
		}
		provider = credentials.NewEcsRoleCredentialsProvider(
			credentials.EcsRamRole(role),
		)
	default:
		return nil, ErrAliyunOSSCredentialModeInvalid
	}

	sdkConfig := oss.LoadDefaultConfig().
		WithCredentialsProvider(provider).
		WithRegion(region).
		WithEndpoint(endpoint)

	return newAliyunOSSObjectClient(
		oss.NewClient(sdkConfig),
		bucket,
	)
}

func newAliyunOSSObjectClient(
	api aliyunOSSAPI,
	bucket string,
) (*AliyunOSSObjectClient, error) {
	if api == nil {
		return nil, ErrAliyunOSSAPIRequired
	}
	bucket = strings.TrimSpace(bucket)
	if bucket == "" {
		return nil, ErrAliyunOSSBucketRequired
	}

	return &AliyunOSSObjectClient{
		api:    api,
		bucket: bucket,
	}, nil
}

// PutObject 把文档流写入私有 Bucket，并保存内容类型、长度和 SHA-256 元数据。
func (c *AliyunOSSObjectClient) PutObject(
	ctx context.Context,
	key string,
	content io.Reader,
	metadata ObjectMetadata,
) error {
	forbidOverwrite := "true"
	request := &oss.PutObjectRequest{
		Bucket:          oss.Ptr(c.bucket),
		Key:             oss.Ptr(key),
		ContentType:     oss.Ptr(metadata.ContentType),
		ContentLength:   oss.Ptr(metadata.ContentLength),
		ForbidOverwrite: &forbidOverwrite,
		Body:            content,
	}
	if metadata.SHA256 != "" {
		request.Metadata = map[string]string{
			"sha256": metadata.SHA256,
		}
	}

	if _, err := c.api.PutObject(ctx, request); err != nil {
		return fmt.Errorf("put Aliyun OSS object: %w", normalizeOSSObjectError(err))
	}
	return nil
}

// GetObject 流式读取一个 OSS 对象；调用方负责关闭返回值。
func (c *AliyunOSSObjectClient) GetObject(
	ctx context.Context,
	key string,
) (io.ReadCloser, error) {
	result, err := c.api.GetObject(ctx, &oss.GetObjectRequest{
		Bucket: oss.Ptr(c.bucket),
		Key:    oss.Ptr(key),
	})
	if err != nil {
		return nil, fmt.Errorf(
			"get Aliyun OSS object: %w",
			normalizeOSSObjectError(err),
		)
	}
	if result == nil || result.Body == nil {
		return nil, ErrObjectReaderRequired
	}
	return result.Body, nil
}

// DeleteObject 幂等删除一个 OSS 对象。
func (c *AliyunOSSObjectClient) DeleteObject(
	ctx context.Context,
	key string,
) error {
	_, err := c.api.DeleteObject(ctx, &oss.DeleteObjectRequest{
		Bucket: oss.Ptr(c.bucket),
		Key:    oss.Ptr(key),
	})
	if err != nil {
		return fmt.Errorf(
			"delete Aliyun OSS object: %w",
			normalizeOSSObjectError(err),
		)
	}
	return nil
}

func normalizeOSSObjectError(err error) error {
	if err == nil {
		return nil
	}

	var serviceError *oss.ServiceError
	if errors.As(err, &serviceError) &&
		(serviceError.HttpStatusCode() == 404 ||
			serviceError.ErrorCode() == "NoSuchKey" ||
			serviceError.ErrorCode() == "NoSuchObject") {
		return errors.Join(ErrObjectNotFound, err)
	}

	return err
}
