package embedding

import (
	"context"
	"errors"
	"reflect"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	accessdomain "rag-reasoning-platform/backend/internal/domain/access"
	documentdomain "rag-reasoning-platform/backend/internal/domain/document"
	embeddingdomain "rag-reasoning-platform/backend/internal/domain/embedding"
)

type fakeBinaryCacheStore struct {
	mutex        sync.Mutex
	values       map[string][]byte
	leases       map[string]string
	getErr       error
	setErr       error
	acquireErr   error
	setCalls     int
	acquireCalls int
}

func newFakeBinaryCacheStore() *fakeBinaryCacheStore {
	return &fakeBinaryCacheStore{
		values: make(map[string][]byte),
		leases: make(map[string]string),
	}
}

func (f *fakeBinaryCacheStore) Get(
	_ context.Context,
	key string,
) ([]byte, bool, error) {
	f.mutex.Lock()
	defer f.mutex.Unlock()
	if f.getErr != nil {
		return nil, false, f.getErr
	}
	value, found := f.values[key]
	return append([]byte(nil), value...), found, nil
}

func (f *fakeBinaryCacheStore) Set(
	_ context.Context,
	key string,
	value []byte,
	_ time.Duration,
) error {
	f.mutex.Lock()
	defer f.mutex.Unlock()
	f.setCalls++
	if f.setErr != nil {
		return f.setErr
	}
	f.values[key] = append([]byte(nil), value...)
	return nil
}

func (f *fakeBinaryCacheStore) AcquireLease(
	_ context.Context,
	key string,
	token string,
	_ time.Duration,
) (bool, error) {
	f.mutex.Lock()
	defer f.mutex.Unlock()
	f.acquireCalls++
	if f.acquireErr != nil {
		return false, f.acquireErr
	}
	if _, exists := f.leases[key]; exists {
		return false, nil
	}
	f.leases[key] = token
	return true, nil
}

func (f *fakeBinaryCacheStore) ReleaseLease(
	_ context.Context,
	key string,
	token string,
) error {
	f.mutex.Lock()
	defer f.mutex.Unlock()
	if f.leases[key] == token {
		delete(f.leases, key)
	}
	return nil
}

type fakeCacheKeyDigester struct{}

func (fakeCacheKeyDigester) Digest(value string) string { return "digest:" + value }

type recordingQueryVectorCacheObserver struct {
	mutex  sync.Mutex
	events []QueryVectorCacheEvent
}

func (o *recordingQueryVectorCacheObserver) ObserveQueryVectorCacheEvent(
	_ context.Context,
	event QueryVectorCacheEvent,
) {
	o.mutex.Lock()
	defer o.mutex.Unlock()
	o.events = append(o.events, event)
}

func TestNormalizeCacheQuestion(t *testing.T) {
	decomposed := "Cafe\u0301"
	if actual := NormalizeCacheQuestion(" \t" + decomposed + "\n  PDF  "); actual != "Café PDF" {
		t.Fatalf("NormalizeCacheQuestion() = %q, want %q", actual, "Café PDF")
	}
	if actual := NormalizeCacheQuestion("RAG rag"); actual != "RAG rag" {
		t.Fatalf("NormalizeCacheQuestion() changed case: %q", actual)
	}
}

func TestQueryVectorBinaryPayloadRoundTrip(t *testing.T) {
	wanted := []float32{-1.5, 0, 0.25, 3.75}
	actual, err := decodeQueryVector(encodeQueryVector(wanted), len(wanted))
	if err != nil {
		t.Fatalf("decodeQueryVector() error = %v, want nil", err)
	}
	if !reflect.DeepEqual(actual, wanted) {
		t.Fatalf("decoded vector = %v, want %v", actual, wanted)
	}
	if _, err := decodeQueryVector(encodeQueryVector(wanted), 3); !errors.Is(err, ErrInvalidQueryVectorCachePayload) {
		t.Fatalf("wrong dimensions error = %v, want ErrInvalidQueryVectorCachePayload", err)
	}
}

func TestSemanticSearchQueryVectorCacheReusesProviderVectorButStillSearches(t *testing.T) {
	cache := newFakeBinaryCacheStore()
	observer := &recordingQueryVectorCacheObserver{}
	var embedCalls int
	embedder := &fakeEmbedder{embedFunc: func(
		_ context.Context,
		request embeddingdomain.EmbedRequest,
	) (embeddingdomain.EmbedResult, error) {
		embedCalls++
		if request.Inputs[0] != "同一个 问题" {
			t.Fatalf("Embed() input = %q, want normalized query", request.Inputs[0])
		}
		return embeddingdomain.EmbedResult{Vectors: [][]float32{{1, 2, 3}}}, nil
	}}
	searcher := &fakeSemanticSearcher{searchFunc: func(
		context.Context,
		accessdomain.OwnerScope,
		documentdomain.SemanticSearchOptions,
	) ([]documentdomain.SemanticSearchHit, error) {
		return []documentdomain.SemanticSearchHit{}, nil
	}}
	service := newCachedSemanticSearchServiceForTest(t, embedder, searcher, cache, observer)

	for _, query := range []string{"同一个   问题", " 同一个\n问题 "} {
		if _, err := service.Search(
			context.Background(),
			testEmbeddingOwnerScope(t),
			SemanticSearchInput{Query: query, TopK: 5},
		); err != nil {
			t.Fatalf("Search(%q) error = %v", query, err)
		}
	}

	if embedCalls != 1 {
		t.Fatalf("Embed() calls = %d, want 1", embedCalls)
	}
	if searcher.calls != 2 {
		t.Fatalf("SearchSimilar() calls = %d, want 2", searcher.calls)
	}
	if cache.setCalls != 1 {
		t.Fatalf("cache Set() calls = %d, want 1", cache.setCalls)
	}
}

func TestSemanticSearchQueryVectorCacheSeparatesOwners(t *testing.T) {
	cache := newFakeBinaryCacheStore()
	observer := &recordingQueryVectorCacheObserver{}
	var embedCalls int
	embedder := &fakeEmbedder{embedFunc: func(
		context.Context,
		embeddingdomain.EmbedRequest,
	) (embeddingdomain.EmbedResult, error) {
		embedCalls++
		return embeddingdomain.EmbedResult{Vectors: [][]float32{{1, 2, 3}}}, nil
	}}
	searcher := &fakeSemanticSearcher{searchFunc: func(
		context.Context,
		accessdomain.OwnerScope,
		documentdomain.SemanticSearchOptions,
	) ([]documentdomain.SemanticSearchHit, error) {
		return nil, nil
	}}
	service := newCachedSemanticSearchServiceForTest(t, embedder, searcher, cache, observer)
	ownerA := testEmbeddingOwnerScope(t)
	ownerB, err := accessdomain.NewOwnerScope(testEmbeddingOwnerUserID + 1)
	if err != nil {
		t.Fatalf("NewOwnerScope() error = %v", err)
	}

	for _, owner := range []accessdomain.OwnerScope{ownerA, ownerB} {
		if _, err := service.Search(
			context.Background(),
			owner,
			SemanticSearchInput{Query: "相同问题", TopK: 5},
		); err != nil {
			t.Fatalf("Search() error = %v", err)
		}
	}
	if embedCalls != 2 {
		t.Fatalf("Embed() calls = %d, want 2 for two owners", embedCalls)
	}
}

func TestSemanticSearchQueryVectorCacheFailureFallsBackToProvider(t *testing.T) {
	cache := newFakeBinaryCacheStore()
	cache.getErr = errors.New("Redis unavailable")
	cache.acquireErr = errors.New("Redis unavailable")
	observer := &recordingQueryVectorCacheObserver{}
	var embedCalls int
	embedder := &fakeEmbedder{embedFunc: func(
		context.Context,
		embeddingdomain.EmbedRequest,
	) (embeddingdomain.EmbedResult, error) {
		embedCalls++
		return embeddingdomain.EmbedResult{Vectors: [][]float32{{1, 2, 3}}}, nil
	}}
	searcher := &fakeSemanticSearcher{searchFunc: func(
		context.Context,
		accessdomain.OwnerScope,
		documentdomain.SemanticSearchOptions,
	) ([]documentdomain.SemanticSearchHit, error) {
		return nil, nil
	}}
	service := newCachedSemanticSearchServiceForTest(t, embedder, searcher, cache, observer)

	if _, err := service.Search(
		context.Background(),
		testEmbeddingOwnerScope(t),
		SemanticSearchInput{Query: "缓存故障", TopK: 5},
	); err != nil {
		t.Fatalf("Search() error = %v, want provider fallback", err)
	}
	if embedCalls != 1 {
		t.Fatalf("Embed() calls = %d, want 1", embedCalls)
	}
}

func TestSemanticSearchQueryVectorCacheCollapsesConcurrentMisses(t *testing.T) {
	cache := newFakeBinaryCacheStore()
	observer := &recordingQueryVectorCacheObserver{}
	var embedCalls atomic.Int32
	embedder := &fakeEmbedder{embedFunc: func(
		context.Context,
		embeddingdomain.EmbedRequest,
	) (embeddingdomain.EmbedResult, error) {
		embedCalls.Add(1)
		time.Sleep(30 * time.Millisecond)
		return embeddingdomain.EmbedResult{Vectors: [][]float32{{1, 2, 3}}}, nil
	}}
	searcher := &concurrentSemanticSearcher{}
	service := newCachedSemanticSearchServiceForTest(t, embedder, searcher, cache, observer)

	const workers = 12
	errorsChannel := make(chan error, workers)
	var waitGroup sync.WaitGroup
	for index := 0; index < workers; index++ {
		waitGroup.Add(1)
		go func() {
			defer waitGroup.Done()
			_, err := service.Search(
				context.Background(),
				testEmbeddingOwnerScope(t),
				SemanticSearchInput{Query: "并发相同问题", TopK: 5},
			)
			errorsChannel <- err
		}()
	}
	waitGroup.Wait()
	close(errorsChannel)
	for err := range errorsChannel {
		if err != nil {
			t.Fatalf("concurrent Search() error = %v", err)
		}
	}
	if embedCalls.Load() != 1 {
		t.Fatalf("Embed() calls = %d, want 1", embedCalls.Load())
	}
	if searcher.calls.Load() != workers {
		t.Fatalf("SearchSimilar() calls = %d, want %d", searcher.calls.Load(), workers)
	}
}

type concurrentSemanticSearcher struct {
	calls atomic.Int32
}

func (*concurrentSemanticSearcher) HasCompleteSemanticEmbeddings(
	context.Context,
	accessdomain.OwnerScope,
	documentdomain.SemanticEmbeddingReadinessOptions,
) (bool, error) {
	return true, nil
}

func (s *concurrentSemanticSearcher) SearchSimilar(
	context.Context,
	accessdomain.OwnerScope,
	documentdomain.SemanticSearchOptions,
) ([]documentdomain.SemanticSearchHit, error) {
	s.calls.Add(1)
	return nil, nil
}

func newCachedSemanticSearchServiceForTest(
	t *testing.T,
	embedder embeddingdomain.Embedder,
	searcher semanticSearchRepository,
	cache BinaryCacheStore,
	observer QueryVectorCacheEventObserver,
) *SemanticSearchService {
	t.Helper()
	service, err := NewSemanticSearchServiceWithQueryCache(
		embedder,
		searcher,
		"test-model",
		3,
		cache,
		fakeCacheKeyDigester{},
		observer,
		QueryVectorCacheConfig{
			Namespace:   "test",
			Provider:    "fake",
			TTL:         time.Hour,
			LockTTL:     time.Minute,
			WaitTimeout: time.Second,
		},
	)
	if err != nil {
		t.Fatalf("NewSemanticSearchServiceWithQueryCache() error = %v", err)
	}
	return service
}
