package integration_test

import (
	"context"
	"fmt"
	"os"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"

	answerapplication "rag-reasoning-platform/backend/internal/application/answer"
	embeddingapplication "rag-reasoning-platform/backend/internal/application/embedding"
	accessdomain "rag-reasoning-platform/backend/internal/domain/access"
	documentdomain "rag-reasoning-platform/backend/internal/domain/document"
	embeddingdomain "rag-reasoning-platform/backend/internal/domain/embedding"
	"rag-reasoning-platform/backend/internal/infrastructure"
)

const (
	cacheProfileUniqueQuestions = 100
	cacheProfileRepeats         = 10
	cacheProfileConcurrentCalls = 50
	cacheProfileDimensions      = 1536
)

// TestRedisRAGCacheProfileWithFakeProviders 使用真实 Redis 和零费用 Fake Provider，
// 同时测量重复请求复用、并发击穿保护和 Redis 实际 MEMORY USAGE。
//
// 该测试默认跳过；设置 RUN_REDIS_TESTS=1 后才会连接本地测试 Redis。
func TestRedisRAGCacheProfileWithFakeProviders(t *testing.T) {
	if os.Getenv("RUN_REDIS_TESTS") != "1" {
		t.Skip("set RUN_REDIS_TESTS=1 to run the Redis RAG cache profile")
	}

	ctx := t.Context()
	cache, redisClient, namespace := newProfileRedis(t, ctx)
	observer := profileCacheObserver{}
	digester, err := infrastructure.NewHMACSHA256Digester(
		[]byte("zero-cost-cache-profile-secret-32-bytes"),
	)
	if err != nil {
		t.Fatalf("NewHMACSHA256Digester() error = %v", err)
	}
	scope, err := accessdomain.NewOwnerScope(900001)
	if err != nil {
		t.Fatalf("NewOwnerScope() error = %v", err)
	}

	queryResult := profileQueryVectorCache(
		t,
		ctx,
		cache,
		redisClient,
		namespace+":query",
		digester,
		observer,
		scope,
	)
	answerResult := profileAnswerResultCache(
		t,
		ctx,
		cache,
		redisClient,
		namespace+":answer",
		digester,
		observer,
		scope,
	)

	t.Logf(
		"CACHE_PROFILE query_requests=%d query_provider_calls=%d query_provider_saved=%d query_saving_rate=%.2f%% query_stampede_requests=%d query_stampede_provider_calls=%d query_keys=%d query_memory_bytes=%d query_average_bytes=%d",
		queryResult.requests,
		queryResult.providerCalls,
		queryResult.providerSaved,
		queryResult.providerSavingRate(),
		queryResult.stampedeRequests,
		queryResult.stampedeProviderCalls,
		queryResult.keys,
		queryResult.memoryBytes,
		queryResult.averageBytes(),
	)
	t.Logf(
		"CACHE_PROFILE answer_requests=%d answer_provider_calls=%d answer_provider_saved=%d answer_saving_rate=%.2f%% answer_stampede_requests=%d answer_stampede_provider_calls=%d answer_keys=%d answer_memory_bytes=%d answer_average_bytes=%d",
		answerResult.requests,
		answerResult.providerCalls,
		answerResult.providerSaved,
		answerResult.providerSavingRate(),
		answerResult.stampedeRequests,
		answerResult.stampedeProviderCalls,
		answerResult.keys,
		answerResult.memoryBytes,
		answerResult.averageBytes(),
	)
}

type cacheProfileResult struct {
	requests              int64
	providerCalls         int64
	providerSaved         int64
	stampedeRequests      int64
	stampedeProviderCalls int64
	keys                  int64
	memoryBytes           int64
}

func (r cacheProfileResult) providerSavingRate() float64 {
	if r.requests == 0 {
		return 0
	}
	return float64(r.providerSaved) / float64(r.requests) * 100
}

func (r cacheProfileResult) averageBytes() int64 {
	if r.keys == 0 {
		return 0
	}
	return r.memoryBytes / r.keys
}

func profileQueryVectorCache(
	t *testing.T,
	ctx context.Context,
	cache *infrastructure.RedisCache,
	redisClient *redis.Client,
	namespace string,
	digester embeddingapplication.CacheKeyDigester,
	observer embeddingapplication.QueryVectorCacheEventObserver,
	scope accessdomain.OwnerScope,
) cacheProfileResult {
	t.Helper()
	embedder := &profileEmbedder{}
	repository := &profileSemanticRepository{}
	service, err := embeddingapplication.NewSemanticSearchServiceWithQueryCache(
		embedder,
		repository,
		"fake-embedding-1536",
		cacheProfileDimensions,
		cache,
		digester,
		observer,
		embeddingapplication.QueryVectorCacheConfig{
			Namespace:   namespace,
			Provider:    "fake",
			TTL:         time.Hour,
			LockTTL:     30 * time.Second,
			WaitTimeout: 2 * time.Second,
		},
	)
	if err != nil {
		t.Fatalf("NewSemanticSearchServiceWithQueryCache() error = %v", err)
	}

	for question := 0; question < cacheProfileUniqueQuestions; question++ {
		query := fmt.Sprintf("查询向量缓存画像问题 %03d", question)
		for repeat := 0; repeat < cacheProfileRepeats; repeat++ {
			if _, err := service.Search(
				ctx,
				scope,
				embeddingapplication.SemanticSearchInput{Query: query, TopK: 5},
			); err != nil {
				t.Fatalf("warm query cache Search() error = %v", err)
			}
		}
	}

	providerCallsBeforeStampede := embedder.calls.Load()
	runConcurrentProfile(t, func() error {
		_, err := service.Search(
			context.Background(),
			scope,
			embeddingapplication.SemanticSearchInput{
				Query: "查询向量并发击穿问题",
				TopK:  5,
			},
		)
		return err
	})
	stampedeProviderCalls := embedder.calls.Load() - providerCallsBeforeStampede
	if stampedeProviderCalls != 1 {
		t.Fatalf(
			"concurrent query provider calls = %d, want 1",
			stampedeProviderCalls,
		)
	}

	requests := int64(
		cacheProfileUniqueQuestions*cacheProfileRepeats +
			cacheProfileConcurrentCalls,
	)
	providerCalls := embedder.calls.Load()
	if repository.calls.Load() != requests {
		t.Fatalf(
			"PostgreSQL search calls = %d, want %d; cache must not bypass Owner-scoped search",
			repository.calls.Load(),
			requests,
		)
	}
	keys, memoryBytes := profileRedisMemory(t, ctx, redisClient, namespace+":*")
	if keys != cacheProfileUniqueQuestions+1 {
		t.Fatalf("query cache keys = %d, want %d", keys, cacheProfileUniqueQuestions+1)
	}

	return cacheProfileResult{
		requests:              requests,
		providerCalls:         providerCalls,
		providerSaved:         requests - providerCalls,
		stampedeRequests:      cacheProfileConcurrentCalls,
		stampedeProviderCalls: stampedeProviderCalls,
		keys:                  keys,
		memoryBytes:           memoryBytes,
	}
}

func profileAnswerResultCache(
	t *testing.T,
	ctx context.Context,
	cache *infrastructure.RedisCache,
	redisClient *redis.Client,
	namespace string,
	digester embeddingapplication.CacheKeyDigester,
	observer answerapplication.AnswerCacheEventObserver,
	scope accessdomain.OwnerScope,
) cacheProfileResult {
	t.Helper()
	answerer := &profileAnswerer{}
	service, err := answerapplication.NewCachedService(
		answerer,
		profileCorpusRevisionReader{},
		cache,
		digester,
		observer,
		answerapplication.AnswerCacheConfig{
			Namespace:           namespace,
			GenerationProvider:  "fake",
			GenerationModel:     "fake-generation",
			PromptVersion:       answerapplication.AnswerPromptVersion,
			RetrievalVersion:    answerapplication.AnswerRetrievalVersion,
			EmbeddingModel:      "fake-embedding-1536",
			EmbeddingDimensions: cacheProfileDimensions,
			MaxOutputTokens:     1024,
			Temperature:         0.1,
			TTL:                 time.Hour,
			LockTTL:             90 * time.Second,
			WaitTimeout:         10 * time.Second,
		},
	)
	if err != nil {
		t.Fatalf("NewCachedService() error = %v", err)
	}

	for question := 0; question < cacheProfileUniqueQuestions; question++ {
		query := fmt.Sprintf("问答结果缓存画像问题 %03d", question)
		for repeat := 0; repeat < cacheProfileRepeats; repeat++ {
			if _, err := service.Answer(
				ctx,
				scope,
				answerapplication.Input{
					Query:            query,
					TopK:             5,
					ResponseLanguage: answerapplication.ResponseLanguageChinese,
				},
			); err != nil {
				t.Fatalf("warm answer cache Answer() error = %v", err)
			}
		}
	}

	providerCallsBeforeStampede := answerer.calls.Load()
	runConcurrentProfile(t, func() error {
		_, err := service.Answer(
			context.Background(),
			scope,
			answerapplication.Input{
				Query:            "问答结果并发击穿问题",
				TopK:             5,
				ResponseLanguage: answerapplication.ResponseLanguageChinese,
			},
		)
		return err
	})
	stampedeProviderCalls := answerer.calls.Load() - providerCallsBeforeStampede
	if stampedeProviderCalls != 1 {
		t.Fatalf(
			"concurrent answer provider calls = %d, want 1",
			stampedeProviderCalls,
		)
	}

	requests := int64(
		cacheProfileUniqueQuestions*cacheProfileRepeats +
			cacheProfileConcurrentCalls,
	)
	providerCalls := answerer.calls.Load()
	keys, memoryBytes := profileRedisMemory(t, ctx, redisClient, namespace+":*")
	if keys != cacheProfileUniqueQuestions+1 {
		t.Fatalf("answer cache keys = %d, want %d", keys, cacheProfileUniqueQuestions+1)
	}

	return cacheProfileResult{
		requests:              requests,
		providerCalls:         providerCalls,
		providerSaved:         requests - providerCalls,
		stampedeRequests:      cacheProfileConcurrentCalls,
		stampedeProviderCalls: stampedeProviderCalls,
		keys:                  keys,
		memoryBytes:           memoryBytes,
	}
}

func runConcurrentProfile(t *testing.T, operation func() error) {
	t.Helper()
	start := make(chan struct{})
	errorsChannel := make(chan error, cacheProfileConcurrentCalls)
	var waitGroup sync.WaitGroup
	for worker := 0; worker < cacheProfileConcurrentCalls; worker++ {
		waitGroup.Add(1)
		go func() {
			defer waitGroup.Done()
			<-start
			errorsChannel <- operation()
		}()
	}
	close(start)
	waitGroup.Wait()
	close(errorsChannel)
	for err := range errorsChannel {
		if err != nil {
			t.Fatalf("concurrent cache profile operation error = %v", err)
		}
	}
}

type profileEmbedder struct {
	calls atomic.Int64
}

func (e *profileEmbedder) Embed(
	_ context.Context,
	request embeddingdomain.EmbedRequest,
) (embeddingdomain.EmbedResult, error) {
	e.calls.Add(1)
	if strings.Contains(request.Inputs[0], "并发击穿") {
		time.Sleep(100 * time.Millisecond)
	}
	vector := make([]float32, request.Dimensions)
	for index := range vector {
		vector[index] = float32(index%97) / 97
	}
	return embeddingdomain.EmbedResult{Vectors: [][]float32{vector}}, nil
}

type profileSemanticRepository struct {
	calls atomic.Int64
}

func (*profileSemanticRepository) HasCompleteSemanticEmbeddings(
	context.Context,
	accessdomain.OwnerScope,
	documentdomain.SemanticEmbeddingReadinessOptions,
) (bool, error) {
	return true, nil
}

func (r *profileSemanticRepository) SearchSimilar(
	context.Context,
	accessdomain.OwnerScope,
	documentdomain.SemanticSearchOptions,
) ([]documentdomain.SemanticSearchHit, error) {
	r.calls.Add(1)
	return []documentdomain.SemanticSearchHit{}, nil
}

type profileAnswerer struct {
	calls atomic.Int64
}

func (a *profileAnswerer) Answer(
	_ context.Context,
	_ accessdomain.OwnerScope,
	input answerapplication.Input,
) (answerapplication.Output, error) {
	a.calls.Add(1)
	if strings.Contains(input.Query, "并发击穿") {
		time.Sleep(250 * time.Millisecond)
	}
	title := strings.Repeat("代表性文献标题", 8)
	sources := make([]answerapplication.Source, 5)
	for index := range sources {
		page := index + 1
		sources[index] = answerapplication.Source{
			Citation:     index + 1,
			ChunkID:      int64(1000 + index),
			DocumentID:   int64(2000 + index),
			ChunkIndex:   index,
			Title:        &title,
			OriginalName: strings.Repeat("representative-paper-", 4) + ".pdf",
			PageStart:    &page,
			PageEnd:      &page,
			Similarity:   0.95 - float64(index)*0.01,
		}
	}
	return answerapplication.Output{
		Query:            input.Query,
		Answer:           strings.Repeat("这是基于文献证据生成的代表性缓存答案。", 60),
		ResponseLanguage: answerapplication.ResponseLanguageChinese,
		Sources:          sources,
		PromptTokens:     1800,
		CompletionTokens: 400,
		TotalTokens:      2200,
	}, nil
}

type profileCorpusRevisionReader struct{}

func (profileCorpusRevisionReader) GetCorpusRevision(
	context.Context,
	accessdomain.OwnerScope,
) (int64, error) {
	return 1, nil
}

type profileCacheObserver struct{}

func (profileCacheObserver) ObserveQueryVectorCacheEvent(
	context.Context,
	embeddingapplication.QueryVectorCacheEvent,
) {
}

func (profileCacheObserver) ObserveAnswerCacheEvent(
	context.Context,
	answerapplication.AnswerCacheEvent,
) {
}

func newProfileRedis(
	t *testing.T,
	ctx context.Context,
) (*infrastructure.RedisCache, *redis.Client, string) {
	t.Helper()
	address := os.Getenv("REDIS_ADDRESS")
	if address == "" {
		address = "127.0.0.1:6380"
	}
	database := 0
	if rawDatabase := os.Getenv("REDIS_DATABASE"); rawDatabase != "" {
		parsedDatabase, err := strconv.Atoi(rawDatabase)
		if err != nil {
			t.Fatalf("parse REDIS_DATABASE: %v", err)
		}
		database = parsedDatabase
	}
	password := os.Getenv("REDIS_PASSWORD")
	cache, err := infrastructure.NewRedisCache(infrastructure.RedisCacheOptions{
		Address:          address,
		Password:         password,
		Database:         database,
		OperationTimeout: 2 * time.Second,
	})
	if err != nil {
		t.Fatalf("NewRedisCache() error = %v", err)
	}
	redisClient := redis.NewClient(&redis.Options{
		Addr:     address,
		Password: password,
		DB:       database,
	})
	if err := redisClient.Ping(ctx).Err(); err != nil {
		_ = cache.Close()
		_ = redisClient.Close()
		t.Fatalf("Ping Redis for cache profile: %v", err)
	}

	namespace := fmt.Sprintf("rag-profile-%d", time.Now().UnixNano())
	t.Cleanup(func() {
		keys, scanErr := scanRedisKeys(context.Background(), redisClient, namespace+":*")
		if scanErr != nil {
			t.Errorf("scan Redis profile keys during cleanup: %v", scanErr)
		} else if len(keys) > 0 {
			if err := redisClient.Del(context.Background(), keys...).Err(); err != nil {
				t.Errorf("delete Redis profile keys: %v", err)
			}
		}
		if err := cache.Close(); err != nil {
			t.Errorf("close Redis cache: %v", err)
		}
		if err := redisClient.Close(); err != nil {
			t.Errorf("close Redis profile client: %v", err)
		}
	})
	return cache, redisClient, namespace
}

func profileRedisMemory(
	t *testing.T,
	ctx context.Context,
	client *redis.Client,
	pattern string,
) (int64, int64) {
	t.Helper()
	keys, err := scanRedisKeys(ctx, client, pattern)
	if err != nil {
		t.Fatalf("scan Redis profile keys: %v", err)
	}
	var memoryBytes int64
	for _, key := range keys {
		usage, err := client.MemoryUsage(ctx, key).Result()
		if err != nil {
			t.Fatalf("read Redis MEMORY USAGE: %v", err)
		}
		memoryBytes += usage
	}
	return int64(len(keys)), memoryBytes
}

func scanRedisKeys(
	ctx context.Context,
	client *redis.Client,
	pattern string,
) ([]string, error) {
	var cursor uint64
	keys := make([]string, 0)
	for {
		batch, nextCursor, err := client.Scan(ctx, cursor, pattern, 100).Result()
		if err != nil {
			return nil, err
		}
		keys = append(keys, batch...)
		cursor = nextCursor
		if cursor == 0 {
			return keys, nil
		}
	}
}
