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
	defaultDashScopeGenerationEndpoint = "https://dashscope.aliyuncs.com/compatible-mode/v1/chat/completions"
	defaultGenerationModel             = "qwen3.6-flash"
	defaultGenerationHTTPTimeout       = 60 * time.Second
	defaultGenerationMaxOutputTokens   = 1024
	maximumGenerationMaxOutputTokens   = 8192
	defaultGenerationTemperature       = 0.1
)

var (
	// ErrGenerationAPIKeyRequired 表示启用问答时缺少百炼 API Key。
	ErrGenerationAPIKeyRequired = errors.New(
		"generation API key must be provided when answer generation is enabled",
	)

	// ErrInvalidGenerationTemperature 表示温度不在兼容协议允许的范围内。
	ErrInvalidGenerationTemperature = errors.New(
		"generation temperature must be between 0 and 2",
	)
)

// GenerationConfig 保存第一版带来源问答所需的远程生成配置。
//
// APIKey 只允许交给 Infrastructure HTTP 适配器，禁止写入日志、数据库和响应。
type GenerationConfig struct {
	Enabled         bool
	APIKey          string
	Endpoint        string
	ModelName       string
	HTTPTimeout     time.Duration
	MaxOutputTokens int
	Temperature     float64
	ThinkingEnabled bool
}

// LoadGeneration 从环境变量加载文本生成配置。
func LoadGeneration() (GenerationConfig, error) {
	enabled, err := loadOptionalBool("ANSWER_ENABLED", false)
	if err != nil {
		return GenerationConfig{}, err
	}

	apiKey := strings.TrimSpace(os.Getenv("DASHSCOPE_API_KEY"))
	if enabled && apiKey == "" {
		return GenerationConfig{}, ErrGenerationAPIKeyRequired
	}

	endpoint := strings.TrimSpace(os.Getenv("DASHSCOPE_GENERATION_ENDPOINT"))
	if endpoint == "" {
		endpoint = defaultDashScopeGenerationEndpoint
	}

	modelName := strings.TrimSpace(os.Getenv("GENERATION_MODEL"))
	if modelName == "" {
		modelName = defaultGenerationModel
	}

	httpTimeout, err := loadPositiveDuration(
		"GENERATION_HTTP_TIMEOUT",
		defaultGenerationHTTPTimeout,
	)
	if err != nil {
		return GenerationConfig{}, fmt.Errorf(
			"load generation HTTP timeout: %w",
			err,
		)
	}

	maxOutputTokens, err := loadPositiveBoundedInt(
		"GENERATION_MAX_OUTPUT_TOKENS",
		defaultGenerationMaxOutputTokens,
		maximumGenerationMaxOutputTokens,
	)
	if err != nil {
		return GenerationConfig{}, fmt.Errorf(
			"load generation max output tokens: %w",
			err,
		)
	}

	temperature, err := loadGenerationTemperature()
	if err != nil {
		return GenerationConfig{}, err
	}

	thinkingEnabled, err := loadOptionalBool(
		"GENERATION_THINKING_ENABLED",
		false,
	)
	if err != nil {
		return GenerationConfig{}, err
	}

	return GenerationConfig{
		Enabled:         enabled,
		APIKey:          apiKey,
		Endpoint:        endpoint,
		ModelName:       modelName,
		HTTPTimeout:     httpTimeout,
		MaxOutputTokens: maxOutputTokens,
		Temperature:     temperature,
		ThinkingEnabled: thinkingEnabled,
	}, nil
}

func loadGenerationTemperature() (float64, error) {
	rawValue := strings.TrimSpace(os.Getenv("GENERATION_TEMPERATURE"))
	if rawValue == "" {
		return defaultGenerationTemperature, nil
	}

	temperature, err := strconv.ParseFloat(rawValue, 64)
	if err != nil {
		return 0, fmt.Errorf(
			"GENERATION_TEMPERATURE must be a number: %w",
			err,
		)
	}
	if temperature < 0 || temperature > 2 {
		return 0, ErrInvalidGenerationTemperature
	}

	return temperature, nil
}
