package answer

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	embeddingapplication "rag-reasoning-platform/backend/internal/application/embedding"
	accessdomain "rag-reasoning-platform/backend/internal/domain/access"
)

const (
	answerCacheSchemaVersion  = "v1"
	AnswerPromptVersion       = "v1"
	AnswerRetrievalVersion    = "v1"
	answerCacheWaitPollPeriod = 100 * time.Millisecond
)

var (
	// ErrAnswerCacheDependencies 表示缓存装饰器缺少下游服务、版本仓储或 Redis。
	ErrAnswerCacheDependencies = errors.New(
		"answer cache dependencies must be provided",
	)
	// ErrAnswerCacheConfiguration 表示缓存 Key 版本、模型或 TTL 配置无效。
	ErrAnswerCacheConfiguration = errors.New(
		"answer cache configuration is invalid",
	)
	// ErrInvalidAnswerCachePayload 表示 Redis 中的问答值无法通过当前协议校验。
	ErrInvalidAnswerCachePayload = errors.New("answer cache payload is invalid")
)

// CorpusRevisionReader 从 PostgreSQL 读取当前 Owner 的可检索语料版本。
// Redis 不能实现这个接口，因为版本是权限和失效判断的正式事实。
type CorpusRevisionReader interface {
	GetCorpusRevision(context.Context, accessdomain.OwnerScope) (int64, error)
}

// AnswerCacheConfig 固定问答缓存 Key、生成参数和填充租约策略。
type AnswerCacheConfig struct {
	Namespace           string
	GenerationProvider  string
	GenerationModel     string
	PromptVersion       string
	RetrievalVersion    string
	EmbeddingModel      string
	EmbeddingDimensions int
	MaxOutputTokens     int
	Temperature         float64
	TTL                 time.Duration
	LockTTL             time.Duration
	WaitTimeout         time.Duration
}

// AnswerCacheEventType 是不包含问题、答案和 Owner 的稳定观测分类。
type AnswerCacheEventType string

const (
	AnswerCacheHit             AnswerCacheEventType = "answer_cache_hit"
	AnswerCacheMiss            AnswerCacheEventType = "answer_cache_miss"
	AnswerCacheReadFailed      AnswerCacheEventType = "answer_cache_read_failed"
	AnswerCacheWriteFailed     AnswerCacheEventType = "answer_cache_write_failed"
	AnswerCacheRevisionFailed  AnswerCacheEventType = "answer_cache_revision_failed"
	AnswerCacheRevisionChanged AnswerCacheEventType = "answer_cache_revision_changed"
	AnswerCacheWaited          AnswerCacheEventType = "answer_cache_waited"
	AnswerCacheSkipped         AnswerCacheEventType = "answer_cache_skipped"
)

// AnswerCacheEvent 只记录缓存结果和版本，不泄露用户输入与生成内容。
type AnswerCacheEvent struct {
	Type           AnswerCacheEventType
	CorpusRevision int64
	WaitDuration   time.Duration
	Err            error
}

// AnswerCacheEventObserver 是问答缓存事件交给 Observability 的端口。
type AnswerCacheEventObserver interface {
	ObserveAnswerCacheEvent(context.Context, AnswerCacheEvent)
}

// CachedService 是问答结果 Cache-Aside 装饰器。
//
// 生产组装顺序应为 CachedService → ConcurrentService → Service。缓存命中不占用
// 远程模型槽位；未命中才进入已有问答并发闸门。
type CachedService struct {
	next       Answerer
	revisions  CorpusRevisionReader
	cache      embeddingapplication.BinaryCacheStore
	digester   embeddingapplication.CacheKeyDigester
	events     AnswerCacheEventObserver
	config     AnswerCacheConfig
	staticHash string
}

var _ Answerer = (*CachedService)(nil)

type answerCachePayload struct {
	SchemaVersion  string `json:"schema_version"`
	CorpusRevision int64  `json:"corpus_revision"`
	Output         Output `json:"output"`
}

// NewCachedService 创建带版本化 Redis 结果缓存的问答服务。
func NewCachedService(
	next Answerer,
	revisions CorpusRevisionReader,
	cache embeddingapplication.BinaryCacheStore,
	digester embeddingapplication.CacheKeyDigester,
	events AnswerCacheEventObserver,
	cacheConfig AnswerCacheConfig,
) (*CachedService, error) {
	if next == nil || revisions == nil || cache == nil || digester == nil || events == nil {
		return nil, ErrAnswerCacheDependencies
	}
	cacheConfig.Namespace = strings.TrimSpace(cacheConfig.Namespace)
	cacheConfig.GenerationProvider = strings.TrimSpace(cacheConfig.GenerationProvider)
	cacheConfig.GenerationModel = strings.TrimSpace(cacheConfig.GenerationModel)
	cacheConfig.PromptVersion = strings.TrimSpace(cacheConfig.PromptVersion)
	cacheConfig.RetrievalVersion = strings.TrimSpace(cacheConfig.RetrievalVersion)
	cacheConfig.EmbeddingModel = strings.TrimSpace(cacheConfig.EmbeddingModel)
	if cacheConfig.Namespace == "" || cacheConfig.GenerationProvider == "" ||
		cacheConfig.GenerationModel == "" || cacheConfig.PromptVersion == "" ||
		cacheConfig.RetrievalVersion == "" ||
		cacheConfig.EmbeddingModel == "" || cacheConfig.EmbeddingDimensions <= 0 ||
		cacheConfig.MaxOutputTokens <= 0 || cacheConfig.Temperature < 0 ||
		cacheConfig.Temperature > 2 || cacheConfig.TTL <= 0 ||
		cacheConfig.LockTTL <= 0 || cacheConfig.WaitTimeout <= 0 ||
		cacheConfig.WaitTimeout >= cacheConfig.LockTTL {
		return nil, ErrAnswerCacheConfiguration
	}

	staticConfig := strings.Join([]string{
		cacheConfig.GenerationProvider,
		cacheConfig.GenerationModel,
		cacheConfig.PromptVersion,
		cacheConfig.RetrievalVersion,
		cacheConfig.EmbeddingModel,
		strconv.Itoa(cacheConfig.EmbeddingDimensions),
		strconv.Itoa(cacheConfig.MaxOutputTokens),
		strconv.FormatFloat(cacheConfig.Temperature, 'g', -1, 64),
	}, "|")
	staticDigest := sha256.Sum256([]byte(staticConfig))

	return &CachedService{
		next:       next,
		revisions:  revisions,
		cache:      cache,
		digester:   digester,
		events:     events,
		config:     cacheConfig,
		staticHash: hex.EncodeToString(staticDigest[:]),
	}, nil
}

// Answer 先按 Owner 和 corpus revision 查缓存，未命中才执行完整问答链路。
func (s *CachedService) Answer(
	ctx context.Context,
	scope accessdomain.OwnerScope,
	input Input,
) (Output, error) {
	if !scope.IsValid() {
		return Output{}, accessdomain.ErrInvalidOwnerScope
	}

	normalizedInput, language, err := normalizeAnswerCacheInput(input)
	if err != nil {
		return Output{}, err
	}
	revision, err := s.revisions.GetCorpusRevision(ctx, scope)
	if err != nil {
		s.observe(ctx, AnswerCacheRevisionFailed, 0, 0, err)
		return s.next.Answer(ctx, scope, normalizedInput)
	}
	key := s.cacheKey(scope, revision, normalizedInput, language)
	if output, found := s.read(ctx, key, revision); found {
		latestRevision, revisionErr := s.revisions.GetCorpusRevision(ctx, scope)
		if revisionErr != nil {
			s.observe(ctx, AnswerCacheRevisionFailed, revision, 0, revisionErr)
			return s.next.Answer(ctx, scope, normalizedInput)
		}
		if latestRevision == revision {
			return cacheHitOutput(output), nil
		}
		s.observe(ctx, AnswerCacheRevisionChanged, latestRevision, 0, nil)
		return s.next.Answer(ctx, scope, normalizedInput)
	}

	return s.fill(ctx, scope, key, revision, normalizedInput)
}

func (s *CachedService) fill(
	ctx context.Context,
	scope accessdomain.OwnerScope,
	key string,
	revision int64,
	input Input,
) (Output, error) {
	leaseKey := key + ":fill"
	token, err := newAnswerCacheLeaseToken()
	if err != nil {
		return s.next.Answer(ctx, scope, input)
	}
	acquired, err := s.cache.AcquireLease(ctx, leaseKey, token, s.config.LockTTL)
	if err != nil {
		s.observe(ctx, AnswerCacheReadFailed, revision, 0, err)
		return s.next.Answer(ctx, scope, input)
	}
	if !acquired {
		waitStartedAt := time.Now()
		if output, found, waitErr := s.waitForFill(ctx, key, revision); waitErr != nil {
			return Output{}, waitErr
		} else if found {
			s.observe(ctx, AnswerCacheWaited, revision, time.Since(waitStartedAt), nil)
			return cacheHitOutput(output), nil
		}
		s.observe(ctx, AnswerCacheWaited, revision, time.Since(waitStartedAt), nil)
		return s.next.Answer(ctx, scope, input)
	}
	defer func() {
		_ = s.cache.ReleaseLease(context.Background(), leaseKey, token)
	}()

	output, err := s.next.Answer(ctx, scope, input)
	if err != nil {
		return Output{}, err
	}
	// 没有证据的稳定降级回答不会产生远程生成费用，且随着语料变化价值很低，
	// 第一版不缓存，避免“刚上传文档仍看见没有证据”的体验。
	if len(output.Sources) == 0 {
		s.observe(ctx, AnswerCacheSkipped, revision, 0, nil)
		return output, nil
	}

	latestRevision, revisionErr := s.revisions.GetCorpusRevision(ctx, scope)
	if revisionErr != nil {
		s.observe(ctx, AnswerCacheRevisionFailed, revision, 0, revisionErr)
		return output, nil
	}
	if latestRevision != revision {
		s.observe(ctx, AnswerCacheRevisionChanged, latestRevision, 0, nil)
		return output, nil
	}
	payload, marshalErr := json.Marshal(answerCachePayload{
		SchemaVersion:  answerCacheSchemaVersion,
		CorpusRevision: revision,
		Output:         output,
	})
	if marshalErr != nil {
		s.observe(ctx, AnswerCacheWriteFailed, revision, 0, marshalErr)
		return output, nil
	}
	if err := s.cache.Set(ctx, key, payload, s.config.TTL); err != nil {
		s.observe(ctx, AnswerCacheWriteFailed, revision, 0, err)
	}
	return output, nil
}

func (s *CachedService) read(
	ctx context.Context,
	key string,
	revision int64,
) (Output, bool) {
	payload, found, err := s.cache.Get(ctx, key)
	if err != nil {
		s.observe(ctx, AnswerCacheReadFailed, revision, 0, err)
		return Output{}, false
	}
	if !found {
		s.observe(ctx, AnswerCacheMiss, revision, 0, nil)
		return Output{}, false
	}
	var cached answerCachePayload
	if err := json.Unmarshal(payload, &cached); err != nil ||
		cached.SchemaVersion != answerCacheSchemaVersion ||
		cached.CorpusRevision != revision || len(cached.Output.Sources) == 0 {
		if err == nil {
			err = ErrInvalidAnswerCachePayload
		}
		s.observe(ctx, AnswerCacheReadFailed, revision, 0, err)
		return Output{}, false
	}
	s.observe(ctx, AnswerCacheHit, revision, 0, nil)
	return cached.Output, true
}

func (s *CachedService) waitForFill(
	ctx context.Context,
	key string,
	revision int64,
) (Output, bool, error) {
	timer := time.NewTimer(s.config.WaitTimeout)
	defer timer.Stop()
	ticker := time.NewTicker(answerCacheWaitPollPeriod)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return Output{}, false, ctx.Err()
		case <-timer.C:
			return Output{}, false, nil
		case <-ticker.C:
			if output, found := s.read(ctx, key, revision); found {
				return output, true, nil
			}
		}
	}
}

func (s *CachedService) cacheKey(
	scope accessdomain.OwnerScope,
	revision int64,
	input Input,
	language ResponseLanguage,
) string {
	documentScope := "all"
	if input.DocumentID != nil {
		documentScope = strconv.FormatInt(*input.DocumentID, 10)
	}
	dynamicConfig := strings.Join([]string{
		documentScope,
		strconv.Itoa(input.TopK),
		string(language),
		s.staticHash,
	}, "|")
	dynamicDigest := sha256.Sum256([]byte(dynamicConfig))

	return fmt.Sprintf(
		"%s:answer:%s:%d:%d:%s:%s",
		s.config.Namespace,
		answerCacheSchemaVersion,
		scope.OwnerUserID(),
		revision,
		hex.EncodeToString(dynamicDigest[:]),
		s.digester.Digest(input.Query),
	)
}

func (s *CachedService) observe(
	ctx context.Context,
	eventType AnswerCacheEventType,
	revision int64,
	waitDuration time.Duration,
	err error,
) {
	s.events.ObserveAnswerCacheEvent(ctx, AnswerCacheEvent{
		Type:           eventType,
		CorpusRevision: revision,
		WaitDuration:   waitDuration,
		Err:            err,
	})
}

func normalizeAnswerCacheInput(
	input Input,
) (Input, ResponseLanguage, error) {
	language, err := resolveResponseLanguage(input.ResponseLanguage, input.Query)
	if err != nil {
		return Input{}, "", err
	}
	candidateTopK := input.TopK
	if input.DocumentID == nil && input.TopK > 0 &&
		input.TopK <= embeddingapplication.MaxSemanticSearchTopK {
		candidateTopK = embeddingapplication.MaxSemanticSearchTopK
	}
	normalizedQuery, err := embeddingapplication.ValidateSemanticSearchInput(
		embeddingapplication.SemanticSearchInput{
			Query:      input.Query,
			DocumentID: input.DocumentID,
			TopK:       candidateTopK,
		},
	)
	if err != nil {
		return Input{}, "", err
	}
	input.Query = embeddingapplication.NormalizeCacheQuestion(normalizedQuery)
	input.ResponseLanguage = language
	return input, language, nil
}

func newAnswerCacheLeaseToken() (string, error) {
	var token [16]byte
	if _, err := rand.Read(token[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(token[:]), nil
}

// cacheHitOutput 表示本次请求没有调用生成模型，因此本次 Token 用量为 0。
// 缓存值仍保留首次生成用量，便于协议校验，但不会重复计入后续请求或异步任务。
func cacheHitOutput(output Output) Output {
	output.PromptTokens = 0
	output.CompletionTokens = 0
	output.TotalTokens = 0
	return output
}
