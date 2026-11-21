package embedding

import (
	"context"
	"crypto/rand"
	"encoding/binary"
	"errors"
	"fmt"
	"math"
	"strings"
	"time"

	"golang.org/x/sync/singleflight"
	"golang.org/x/text/unicode/norm"

	accessdomain "rag-reasoning-platform/backend/internal/domain/access"
	embeddingdomain "rag-reasoning-platform/backend/internal/domain/embedding"
)

const (
	queryVectorCacheSchemaVersion = "v1"
	queryNormalizationVersion     = "n1"
	queryVectorPayloadHeaderSize  = 8
	queryVectorWaitPollInterval   = 50 * time.Millisecond
)

var (
	// ErrQueryVectorCacheDependencies 表示缓存装饰器缺少 Redis、摘要器或观察器。
	ErrQueryVectorCacheDependencies = errors.New(
		"query vector cache dependencies must be provided",
	)
	// ErrQueryVectorCacheConfiguration 表示命名空间、提供方或 TTL 配置无效。
	ErrQueryVectorCacheConfiguration = errors.New(
		"query vector cache configuration is invalid",
	)
	// ErrInvalidQueryVectorCachePayload 表示 Redis 中的数据不是当前二进制协议。
	ErrInvalidQueryVectorCachePayload = errors.New(
		"query vector cache payload is invalid",
	)
)

// BinaryCacheStore 是应用层使用 Redis 所需的最小二进制缓存插口。
// 具体客户端位于 Infrastructure；测试可使用内存 Fake。
type BinaryCacheStore interface {
	Get(context.Context, string) ([]byte, bool, error)
	Set(context.Context, string, []byte, time.Duration) error
	AcquireLease(context.Context, string, string, time.Duration) (bool, error)
	ReleaseLease(context.Context, string, string) error
}

// CacheKeyDigester 把规范化问题转换为不可逆摘要。
type CacheKeyDigester interface {
	Digest(string) string
}

// QueryVectorCacheConfig 固定查询向量 Key、TTL 和防击穿等待策略。
type QueryVectorCacheConfig struct {
	Namespace   string
	Provider    string
	TTL         time.Duration
	LockTTL     time.Duration
	WaitTimeout time.Duration
}

// QueryVectorCacheEventType 是不包含用户问题的缓存观测事件类型。
type QueryVectorCacheEventType string

const (
	QueryVectorCacheHit         QueryVectorCacheEventType = "query_vector_cache_hit"
	QueryVectorCacheMiss        QueryVectorCacheEventType = "query_vector_cache_miss"
	QueryVectorCacheReadFailed  QueryVectorCacheEventType = "query_vector_cache_read_failed"
	QueryVectorCacheWriteFailed QueryVectorCacheEventType = "query_vector_cache_write_failed"
	QueryVectorCacheWaited      QueryVectorCacheEventType = "query_vector_cache_waited"
)

// QueryVectorCacheEvent 只记录模型和结果分类，不记录 Owner 或问题明文。
type QueryVectorCacheEvent struct {
	Type         QueryVectorCacheEventType
	Provider     string
	ModelName    string
	Dimensions   int
	WaitDuration time.Duration
	Err          error
}

// QueryVectorCacheEventObserver 是缓存事件交给 Observability 的端口。
type QueryVectorCacheEventObserver interface {
	ObserveQueryVectorCacheEvent(context.Context, QueryVectorCacheEvent)
}

type semanticQueryVectorProvider interface {
	EmbedQuery(context.Context, accessdomain.OwnerScope, string) ([]float32, error)
}

type directSemanticQueryVectorProvider struct {
	embedder   embeddingdomain.Embedder
	modelName  string
	dimensions int
}

func newDirectSemanticQueryVectorProvider(
	embedder embeddingdomain.Embedder,
	modelName string,
	dimensions int,
) *directSemanticQueryVectorProvider {
	return &directSemanticQueryVectorProvider{
		embedder:   embedder,
		modelName:  modelName,
		dimensions: dimensions,
	}
}

func (p *directSemanticQueryVectorProvider) EmbedQuery(
	ctx context.Context,
	_ accessdomain.OwnerScope,
	query string,
) ([]float32, error) {
	embeddedQuery, err := p.embedder.Embed(
		ctx,
		embeddingdomain.EmbedRequest{
			Inputs:     []string{query},
			Model:      p.modelName,
			Dimensions: p.dimensions,
		},
	)
	if err != nil {
		return nil, err
	}
	if len(embeddedQuery.Vectors) != 1 ||
		len(embeddedQuery.Vectors[0]) != p.dimensions {
		return nil, fmt.Errorf(
			"%w: semantic query returned %d vectors with expected dimensions %d",
			embeddingdomain.ErrInvalidEmbeddingResponse,
			len(embeddedQuery.Vectors),
			p.dimensions,
		)
	}
	return embeddedQuery.Vectors[0], nil
}

type cachedSemanticQueryVectorProvider struct {
	next         semanticQueryVectorProvider
	cache        BinaryCacheStore
	digester     CacheKeyDigester
	events       QueryVectorCacheEventObserver
	config       QueryVectorCacheConfig
	modelName    string
	dimensions   int
	requestGroup singleflight.Group
}

// NewSemanticSearchServiceWithQueryCache 创建带 Redis 查询向量缓存的语义检索服务。
// 缓存命中后仍会执行 Owner-scoped PostgreSQL 检索，Redis 不替代鉴权或数据事实。
func NewSemanticSearchServiceWithQueryCache(
	embedder embeddingdomain.Embedder,
	repository semanticSearchRepository,
	modelName string,
	dimensions int,
	cache BinaryCacheStore,
	digester CacheKeyDigester,
	events QueryVectorCacheEventObserver,
	cacheConfig QueryVectorCacheConfig,
) (*SemanticSearchService, error) {
	if embedder == nil || repository == nil || cache == nil || digester == nil || events == nil {
		return nil, ErrQueryVectorCacheDependencies
	}
	cacheConfig.Namespace = strings.TrimSpace(cacheConfig.Namespace)
	cacheConfig.Provider = strings.TrimSpace(cacheConfig.Provider)
	if cacheConfig.Namespace == "" || cacheConfig.Provider == "" ||
		cacheConfig.TTL <= 0 || cacheConfig.LockTTL <= 0 ||
		cacheConfig.WaitTimeout <= 0 || cacheConfig.WaitTimeout >= cacheConfig.LockTTL {
		return nil, ErrQueryVectorCacheConfiguration
	}

	direct := newDirectSemanticQueryVectorProvider(embedder, modelName, dimensions)
	cached := &cachedSemanticQueryVectorProvider{
		next:       direct,
		cache:      cache,
		digester:   digester,
		events:     events,
		config:     cacheConfig,
		modelName:  strings.TrimSpace(modelName),
		dimensions: dimensions,
	}
	return newSemanticSearchService(cached, repository, modelName, dimensions)
}

func (p *cachedSemanticQueryVectorProvider) EmbedQuery(
	ctx context.Context,
	scope accessdomain.OwnerScope,
	query string,
) ([]float32, error) {
	normalizedQuery := NormalizeCacheQuestion(query)
	key := p.cacheKey(scope, normalizedQuery)

	if vector, found := p.read(ctx, key); found {
		return vector, nil
	}

	result, err, _ := p.requestGroup.Do(key, func() (any, error) {
		if vector, found := p.read(ctx, key); found {
			return vector, nil
		}
		return p.fill(ctx, scope, key, normalizedQuery)
	})
	if err != nil {
		return nil, err
	}
	return append([]float32(nil), result.([]float32)...), nil
}

func (p *cachedSemanticQueryVectorProvider) fill(
	ctx context.Context,
	scope accessdomain.OwnerScope,
	key string,
	normalizedQuery string,
) ([]float32, error) {
	leaseKey := key + ":fill"
	token, err := newCacheLeaseToken()
	if err != nil {
		return p.next.EmbedQuery(ctx, scope, normalizedQuery)
	}
	acquired, err := p.cache.AcquireLease(ctx, leaseKey, token, p.config.LockTTL)
	if err != nil {
		p.observe(ctx, QueryVectorCacheReadFailed, 0, err)
		return p.next.EmbedQuery(ctx, scope, normalizedQuery)
	}
	if !acquired {
		waitStartedAt := time.Now()
		if vector, found, waitErr := p.waitForFill(ctx, key); waitErr != nil {
			return nil, waitErr
		} else if found {
			p.observe(ctx, QueryVectorCacheWaited, time.Since(waitStartedAt), nil)
			return vector, nil
		}
		p.observe(ctx, QueryVectorCacheWaited, time.Since(waitStartedAt), nil)
		return p.next.EmbedQuery(ctx, scope, normalizedQuery)
	}
	defer func() {
		_ = p.cache.ReleaseLease(context.Background(), leaseKey, token)
	}()

	vector, err := p.next.EmbedQuery(ctx, scope, normalizedQuery)
	if err != nil {
		return nil, err
	}
	payload := encodeQueryVector(vector)
	if err := p.cache.Set(ctx, key, payload, p.config.TTL); err != nil {
		p.observe(ctx, QueryVectorCacheWriteFailed, 0, err)
	}
	return vector, nil
}

func (p *cachedSemanticQueryVectorProvider) read(
	ctx context.Context,
	key string,
) ([]float32, bool) {
	payload, found, err := p.cache.Get(ctx, key)
	if err != nil {
		p.observe(ctx, QueryVectorCacheReadFailed, 0, err)
		return nil, false
	}
	if !found {
		p.observe(ctx, QueryVectorCacheMiss, 0, nil)
		return nil, false
	}
	vector, err := decodeQueryVector(payload, p.dimensions)
	if err != nil {
		p.observe(ctx, QueryVectorCacheReadFailed, 0, err)
		return nil, false
	}
	p.observe(ctx, QueryVectorCacheHit, 0, nil)
	return vector, true
}

func (p *cachedSemanticQueryVectorProvider) waitForFill(
	ctx context.Context,
	key string,
) ([]float32, bool, error) {
	timer := time.NewTimer(p.config.WaitTimeout)
	defer timer.Stop()
	ticker := time.NewTicker(queryVectorWaitPollInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return nil, false, ctx.Err()
		case <-timer.C:
			return nil, false, nil
		case <-ticker.C:
			if vector, found := p.read(ctx, key); found {
				return vector, true, nil
			}
		}
	}
}

func (p *cachedSemanticQueryVectorProvider) cacheKey(
	scope accessdomain.OwnerScope,
	normalizedQuery string,
) string {
	return fmt.Sprintf(
		"%s:qvec:%s:%d:%s:%s:%d:%s:%s",
		p.config.Namespace,
		queryVectorCacheSchemaVersion,
		scope.OwnerUserID(),
		p.config.Provider,
		p.modelName,
		p.dimensions,
		queryNormalizationVersion,
		p.digester.Digest(normalizedQuery),
	)
}

func (p *cachedSemanticQueryVectorProvider) observe(
	ctx context.Context,
	eventType QueryVectorCacheEventType,
	waitDuration time.Duration,
	err error,
) {
	p.events.ObserveQueryVectorCacheEvent(ctx, QueryVectorCacheEvent{
		Type:         eventType,
		Provider:     p.config.Provider,
		ModelName:    p.modelName,
		Dimensions:   p.dimensions,
		WaitDuration: waitDuration,
		Err:          err,
	})
}

// NormalizeCacheQuestion 固定缓存问题规范化规则 n1：Unicode NFC、首尾去空白，
// 并把连续 Unicode 空白折叠成一个 ASCII 空格。大小写保留，避免改变缩写语义。
func NormalizeCacheQuestion(query string) string {
	return norm.NFC.String(strings.Join(strings.Fields(query), " "))
}

func encodeQueryVector(vector []float32) []byte {
	payload := make([]byte, queryVectorPayloadHeaderSize+len(vector)*4)
	copy(payload[:4], []byte{'Q', 'V', '1', 0})
	binary.LittleEndian.PutUint32(payload[4:8], uint32(len(vector)))
	for index, value := range vector {
		binary.LittleEndian.PutUint32(
			payload[queryVectorPayloadHeaderSize+index*4:],
			math.Float32bits(value),
		)
	}
	return payload
}

func decodeQueryVector(payload []byte, dimensions int) ([]float32, error) {
	if dimensions <= 0 || len(payload) < queryVectorPayloadHeaderSize ||
		string(payload[:4]) != "QV1\x00" {
		return nil, ErrInvalidQueryVectorCachePayload
	}
	storedDimensions := int(binary.LittleEndian.Uint32(payload[4:8]))
	if storedDimensions != dimensions ||
		len(payload) != queryVectorPayloadHeaderSize+storedDimensions*4 {
		return nil, ErrInvalidQueryVectorCachePayload
	}
	vector := make([]float32, storedDimensions)
	for index := range vector {
		bits := binary.LittleEndian.Uint32(
			payload[queryVectorPayloadHeaderSize+index*4:],
		)
		vector[index] = math.Float32frombits(bits)
	}
	return vector, nil
}

func newCacheLeaseToken() (string, error) {
	var token [16]byte
	if _, err := rand.Read(token[:]); err != nil {
		return "", err
	}
	return fmt.Sprintf("%x", token[:]), nil
}
