package config

import (
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

const (
	defaultEmbeddingDimensions                = 1536
	defaultEmbeddingWorkerConcurrency         = 1
	maximumEmbeddingWorkerConcurrency         = 4
	defaultEmbeddingProviderConcurrency       = 4
	maximumEmbeddingProviderConcurrency       = 32
	defaultEmbeddingWorkerProviderConcurrency = 2
	defaultEmbeddingOnlineProviderConcurrency = 2
	defaultEmbeddingOnlineWaitTimeout         = 2 * time.Second
	defaultEmbeddingHTTPTimeout               = 30 * time.Second
	defaultEmbeddingProcessingTimeout         = 5 * time.Minute
	defaultEmbeddingPollInterval              = 2 * time.Second
	defaultEmbeddingMaxAttempts               = 5
	maximumEmbeddingMaxAttempts               = 20
	defaultEmbeddingRetryBaseDelay            = 5 * time.Second
	defaultEmbeddingRetryMaxDelay             = 2 * time.Minute
	defaultEmbeddingActiveOwnerLimit          = 100
	defaultEmbeddingActiveGlobalLimit         = 500
	maximumEmbeddingActiveJobLimit            = 10000
)

// EmbeddingProvider 是远程向量服务提供方的配置枚举。
//
// 使用自定义类型而不是散落的字符串，可以让 main.go 的组装分支和配置校验共享
// 同一组合法值，减少 "dashscope" 拼写错误直到运行期才被发现的风险。
type EmbeddingProvider string

const (
	// EmbeddingProviderDashScope 表示阿里云百炼的 OpenAI 兼容 Embeddings API。
	EmbeddingProviderDashScope EmbeddingProvider = "dashscope"

	// EmbeddingProviderOpenAI 表示 OpenAI 官方 Embeddings API。
	EmbeddingProviderOpenAI EmbeddingProvider = "openai"

	defaultEmbeddingProvider = EmbeddingProviderDashScope

	defaultDashScopeEmbeddingModel     = "text-embedding-v4"
	defaultDashScopeEmbeddingEndpoint  = "https://dashscope.aliyuncs.com/compatible-mode/v1/embeddings"
	defaultDashScopeEmbeddingBatchSize = 10
	maximumDashScopeEmbeddingBatchSize = 10

	defaultOpenAIEmbeddingModel     = "text-embedding-3-small"
	defaultOpenAIEmbeddingEndpoint  = "https://api.openai.com/v1/embeddings"
	defaultOpenAIEmbeddingBatchSize = 32
	maximumOpenAIEmbeddingBatchSize = 2048
)

var (
	ErrUnsupportedEmbeddingDimensions = errors.New(
		"embedding dimensions must be 1536 until the vector schema is migrated",
	)
	ErrEmbeddingAPIKeyRequired = errors.New(
		"embedding provider API key must be provided when a remote embedding capability is enabled",
	)
	ErrUnsupportedEmbeddingProvider = errors.New(
		"embedding provider must be dashscope or openai",
	)
	ErrInvalidEmbeddingRetryDelays = errors.New(
		"embedding retry base delay must not exceed maximum delay",
	)
	ErrInvalidEmbeddingActiveJobLimits = errors.New(
		"embedding global active job limit must not be smaller than the per-user limit",
	)
	ErrInvalidEmbeddingProviderConcurrency = errors.New(
		"embedding worker provider concurrency must not be smaller than worker concurrency",
	)
	ErrInvalidEmbeddingProviderAllocation = errors.New(
		"embedding worker and online provider concurrency must not exceed global provider concurrency",
	)
)

// EmbeddingConfig 保存任务入队、后台执行和在线语义检索需要的向量配置。
//
// APIKey 只允许传给远程 HTTP 适配器，禁止写入日志、数据库或 HTTP 响应。
type EmbeddingConfig struct {
	WorkerEnabled             bool
	WorkerConcurrency         int
	ProviderMaxConcurrency    int
	WorkerProviderConcurrency int
	OnlineProviderConcurrency int
	OnlineQueueWaitTimeout    time.Duration
	SemanticSearchEnabled     bool
	Provider                  EmbeddingProvider
	APIKey                    string
	Endpoint                  string
	ModelName                 string
	Dimensions                int
	BatchSize                 int
	HTTPTimeout               time.Duration
	ProcessingTimeout         time.Duration
	PollInterval              time.Duration
	MaxAttempts               int
	RetryBaseDelay            time.Duration
	RetryMaxDelay             time.Duration
	ActiveJobsPerUserLimit    int
	ActiveJobsGlobalLimit     int
}

// LoadEmbedding 从环境变量加载向量任务、Worker 与语义检索配置。
func LoadEmbedding() (EmbeddingConfig, error) {
	workerEnabled, err := loadOptionalBool("EMBEDDING_WORKER_ENABLED", false)
	if err != nil {
		return EmbeddingConfig{}, err
	}
	semanticSearchEnabled, err := loadOptionalBool(
		"SEMANTIC_SEARCH_ENABLED",
		false,
	)
	if err != nil {
		return EmbeddingConfig{}, err
	}

	workerConcurrency, err := loadPositiveBoundedInt(
		"EMBEDDING_WORKER_CONCURRENCY",
		defaultEmbeddingWorkerConcurrency,
		maximumEmbeddingWorkerConcurrency,
	)
	if err != nil {
		return EmbeddingConfig{}, fmt.Errorf(
			"load embedding worker concurrency: %w",
			err,
		)
	}

	providerMaxConcurrency, err := loadPositiveBoundedInt(
		"EMBEDDING_PROVIDER_MAX_CONCURRENCY",
		defaultEmbeddingProviderConcurrency,
		maximumEmbeddingProviderConcurrency,
	)
	if err != nil {
		return EmbeddingConfig{}, fmt.Errorf(
			"load embedding provider max concurrency: %w",
			err,
		)
	}
	workerProviderConcurrency, err := loadPositiveBoundedInt(
		"EMBEDDING_WORKER_PROVIDER_CONCURRENCY",
		defaultEmbeddingWorkerProviderConcurrency,
		maximumEmbeddingProviderConcurrency,
	)
	if err != nil {
		return EmbeddingConfig{}, fmt.Errorf(
			"load embedding worker provider concurrency: %w",
			err,
		)
	}
	onlineProviderConcurrency, err := loadPositiveBoundedInt(
		"EMBEDDING_ONLINE_PROVIDER_CONCURRENCY",
		defaultEmbeddingOnlineProviderConcurrency,
		maximumEmbeddingProviderConcurrency,
	)
	if err != nil {
		return EmbeddingConfig{}, fmt.Errorf(
			"load embedding online provider concurrency: %w",
			err,
		)
	}
	if workerProviderConcurrency >
		providerMaxConcurrency-onlineProviderConcurrency {
		return EmbeddingConfig{}, ErrInvalidEmbeddingProviderAllocation
	}

	onlineQueueWaitTimeout, err := loadPositiveDuration(
		"EMBEDDING_ONLINE_QUEUE_WAIT_TIMEOUT",
		defaultEmbeddingOnlineWaitTimeout,
	)
	if err != nil {
		return EmbeddingConfig{}, fmt.Errorf(
			"load embedding online queue wait timeout: %w",
			err,
		)
	}
	if workerEnabled && workerProviderConcurrency < workerConcurrency {
		return EmbeddingConfig{}, ErrInvalidEmbeddingProviderConcurrency
	}

	provider, err := loadEmbeddingProvider()
	if err != nil {
		return EmbeddingConfig{}, err
	}

	providerConfig := embeddingProviderDefaults(provider)

	apiKey := strings.TrimSpace(os.Getenv(providerConfig.apiKeyEnvironment))
	// Worker 和在线语义检索都会调用远程 Embedding API。只有两项能力都关闭时，
	// 才允许不配置 API Key，从而避免基础文档功能被 AI 配置阻塞。
	if (workerEnabled || semanticSearchEnabled) && apiKey == "" {
		return EmbeddingConfig{}, fmt.Errorf(
			"%w: %s",
			ErrEmbeddingAPIKeyRequired,
			providerConfig.apiKeyEnvironment,
		)
	}

	endpoint := strings.TrimSpace(os.Getenv(providerConfig.endpointEnvironment))
	if endpoint == "" {
		endpoint = providerConfig.defaultEndpoint
	}

	modelName := strings.TrimSpace(os.Getenv("EMBEDDING_MODEL"))
	if modelName == "" {
		modelName = providerConfig.defaultModel
	}

	dimensions, err := loadPositiveBoundedInt(
		"EMBEDDING_DIMENSIONS",
		defaultEmbeddingDimensions,
		defaultEmbeddingDimensions,
	)
	if err != nil {
		return EmbeddingConfig{}, fmt.Errorf(
			"load embedding dimensions: %w",
			err,
		)
	}
	if dimensions != defaultEmbeddingDimensions {
		return EmbeddingConfig{}, ErrUnsupportedEmbeddingDimensions
	}

	batchSize, err := loadPositiveBoundedInt(
		"EMBEDDING_BATCH_SIZE",
		providerConfig.defaultBatchSize,
		providerConfig.maximumBatchSize,
	)
	if err != nil {
		return EmbeddingConfig{}, fmt.Errorf("load embedding batch size: %w", err)
	}

	httpTimeout, err := loadPositiveDuration(
		"EMBEDDING_HTTP_TIMEOUT",
		defaultEmbeddingHTTPTimeout,
	)
	if err != nil {
		return EmbeddingConfig{}, fmt.Errorf("load embedding HTTP timeout: %w", err)
	}

	processingTimeout, err := loadPositiveDuration(
		"EMBEDDING_PROCESSING_TIMEOUT",
		defaultEmbeddingProcessingTimeout,
	)
	if err != nil {
		return EmbeddingConfig{}, fmt.Errorf("load embedding processing timeout: %w", err)
	}

	pollInterval, err := loadPositiveDuration(
		"EMBEDDING_POLL_INTERVAL",
		defaultEmbeddingPollInterval,
	)
	if err != nil {
		return EmbeddingConfig{}, fmt.Errorf("load embedding poll interval: %w", err)
	}

	maxAttempts, err := loadPositiveBoundedInt(
		"EMBEDDING_MAX_ATTEMPTS",
		defaultEmbeddingMaxAttempts,
		maximumEmbeddingMaxAttempts,
	)
	if err != nil {
		return EmbeddingConfig{}, fmt.Errorf("load embedding max attempts: %w", err)
	}

	retryBaseDelay, err := loadPositiveDuration(
		"EMBEDDING_RETRY_BASE_DELAY",
		defaultEmbeddingRetryBaseDelay,
	)
	if err != nil {
		return EmbeddingConfig{}, fmt.Errorf("load embedding retry base delay: %w", err)
	}

	retryMaxDelay, err := loadPositiveDuration(
		"EMBEDDING_RETRY_MAX_DELAY",
		defaultEmbeddingRetryMaxDelay,
	)
	if err != nil {
		return EmbeddingConfig{}, fmt.Errorf("load embedding retry max delay: %w", err)
	}
	if retryBaseDelay > retryMaxDelay {
		return EmbeddingConfig{}, ErrInvalidEmbeddingRetryDelays
	}

	activeJobsPerUserLimit, err := loadPositiveBoundedInt(
		"EMBEDDING_MAX_ACTIVE_JOBS_PER_USER",
		defaultEmbeddingActiveOwnerLimit,
		maximumEmbeddingActiveJobLimit,
	)
	if err != nil {
		return EmbeddingConfig{}, fmt.Errorf("load embedding per-user active job limit: %w", err)
	}
	activeJobsGlobalLimit, err := loadPositiveBoundedInt(
		"EMBEDDING_MAX_ACTIVE_JOBS_GLOBAL",
		defaultEmbeddingActiveGlobalLimit,
		maximumEmbeddingActiveJobLimit,
	)
	if err != nil {
		return EmbeddingConfig{}, fmt.Errorf("load embedding global active job limit: %w", err)
	}
	if activeJobsGlobalLimit < activeJobsPerUserLimit {
		return EmbeddingConfig{}, ErrInvalidEmbeddingActiveJobLimits
	}

	return EmbeddingConfig{
		WorkerEnabled:             workerEnabled,
		WorkerConcurrency:         workerConcurrency,
		ProviderMaxConcurrency:    providerMaxConcurrency,
		WorkerProviderConcurrency: workerProviderConcurrency,
		OnlineProviderConcurrency: onlineProviderConcurrency,
		OnlineQueueWaitTimeout:    onlineQueueWaitTimeout,
		SemanticSearchEnabled:     semanticSearchEnabled,
		Provider:                  provider,
		APIKey:                    apiKey,
		Endpoint:                  endpoint,
		ModelName:                 modelName,
		Dimensions:                dimensions,
		BatchSize:                 batchSize,
		HTTPTimeout:               httpTimeout,
		ProcessingTimeout:         processingTimeout,
		PollInterval:              pollInterval,
		MaxAttempts:               maxAttempts,
		RetryBaseDelay:            retryBaseDelay,
		RetryMaxDelay:             retryMaxDelay,
		ActiveJobsPerUserLimit:    activeJobsPerUserLimit,
		ActiveJobsGlobalLimit:     activeJobsGlobalLimit,
	}, nil
}

// embeddingProviderConfig 保存只有 Config 层需要知道的提供方默认值。
// Domain 和 Application 不读取这些字段，因此不会依赖某家云厂商。
type embeddingProviderConfig struct {
	apiKeyEnvironment   string
	endpointEnvironment string
	defaultEndpoint     string
	defaultModel        string
	defaultBatchSize    int
	maximumBatchSize    int
}

func loadEmbeddingProvider() (EmbeddingProvider, error) {
	rawProvider := strings.ToLower(strings.TrimSpace(os.Getenv("EMBEDDING_PROVIDER")))
	if rawProvider == "" {
		return defaultEmbeddingProvider, nil
	}

	provider := EmbeddingProvider(rawProvider)
	switch provider {
	case EmbeddingProviderDashScope, EmbeddingProviderOpenAI:
		return provider, nil
	default:
		return "", fmt.Errorf(
			"%w: got %q",
			ErrUnsupportedEmbeddingProvider,
			rawProvider,
		)
	}
}

func embeddingProviderDefaults(provider EmbeddingProvider) embeddingProviderConfig {
	switch provider {
	case EmbeddingProviderOpenAI:
		return embeddingProviderConfig{
			apiKeyEnvironment:   "OPENAI_API_KEY",
			endpointEnvironment: "OPENAI_EMBEDDING_ENDPOINT",
			defaultEndpoint:     defaultOpenAIEmbeddingEndpoint,
			defaultModel:        defaultOpenAIEmbeddingModel,
			defaultBatchSize:    defaultOpenAIEmbeddingBatchSize,
			maximumBatchSize:    maximumOpenAIEmbeddingBatchSize,
		}
	default:
		return embeddingProviderConfig{
			apiKeyEnvironment:   "DASHSCOPE_API_KEY",
			endpointEnvironment: "DASHSCOPE_EMBEDDING_ENDPOINT",
			defaultEndpoint:     defaultDashScopeEmbeddingEndpoint,
			defaultModel:        defaultDashScopeEmbeddingModel,
			defaultBatchSize:    defaultDashScopeEmbeddingBatchSize,
			maximumBatchSize:    maximumDashScopeEmbeddingBatchSize,
		}
	}
}

func loadOptionalBool(name string, defaultValue bool) (bool, error) {
	rawValue := strings.TrimSpace(os.Getenv(name))
	if rawValue == "" {
		return defaultValue, nil
	}

	value, err := strconv.ParseBool(rawValue)
	if err != nil {
		return false, fmt.Errorf("%s must be a boolean: %w", name, err)
	}

	return value, nil
}
