package answer

import (
	"context"
	"errors"
	"reflect"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	embeddingapplication "rag-reasoning-platform/backend/internal/application/embedding"
	accessdomain "rag-reasoning-platform/backend/internal/domain/access"
)

type fakeAnswerCacheStore struct {
	mutex    sync.Mutex
	values   map[string][]byte
	leases   map[string]string
	getErr   error
	setErr   error
	setCalls int
}

func newFakeAnswerCacheStore() *fakeAnswerCacheStore {
	return &fakeAnswerCacheStore{
		values: make(map[string][]byte),
		leases: make(map[string]string),
	}
}

func (f *fakeAnswerCacheStore) Get(
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

func (f *fakeAnswerCacheStore) Set(
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

func (f *fakeAnswerCacheStore) AcquireLease(
	_ context.Context,
	key string,
	token string,
	_ time.Duration,
) (bool, error) {
	f.mutex.Lock()
	defer f.mutex.Unlock()
	if _, exists := f.leases[key]; exists {
		return false, nil
	}
	f.leases[key] = token
	return true, nil
}

func (f *fakeAnswerCacheStore) ReleaseLease(
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

type fakeAnswerCacheDigester struct{}

func (fakeAnswerCacheDigester) Digest(value string) string { return "digest:" + value }

type mutableCorpusRevisionReader struct {
	mutex    sync.Mutex
	revision int64
	err      error
	calls    int
}

func (r *mutableCorpusRevisionReader) GetCorpusRevision(
	context.Context,
	accessdomain.OwnerScope,
) (int64, error) {
	r.mutex.Lock()
	defer r.mutex.Unlock()
	r.calls++
	return r.revision, r.err
}

func (r *mutableCorpusRevisionReader) setRevision(revision int64) {
	r.mutex.Lock()
	defer r.mutex.Unlock()
	r.revision = revision
}

type recordingAnswerCacheObserver struct {
	mutex  sync.Mutex
	events []AnswerCacheEvent
}

func (o *recordingAnswerCacheObserver) ObserveAnswerCacheEvent(
	_ context.Context,
	event AnswerCacheEvent,
) {
	o.mutex.Lock()
	defer o.mutex.Unlock()
	o.events = append(o.events, event)
}

type countingAnswerer struct {
	calls  atomic.Int32
	delay  time.Duration
	output Output
	err    error
}

func (a *countingAnswerer) Answer(
	_ context.Context,
	_ accessdomain.OwnerScope,
	input Input,
) (Output, error) {
	a.calls.Add(1)
	if a.delay > 0 {
		time.Sleep(a.delay)
	}
	output := a.output
	output.Query = input.Query
	return output, a.err
}

func TestCachedServiceReusesAnswerWithinSameCorpusRevision(t *testing.T) {
	next := &countingAnswerer{output: cacheableAnswerOutput()}
	revisions := &mutableCorpusRevisionReader{revision: 7}
	cache := newFakeAnswerCacheStore()
	service := newCachedAnswerServiceForTest(t, next, revisions, cache)
	input := Input{Query: "  如何   提高稳定性？ ", TopK: 5}

	first, err := service.Answer(t.Context(), testAnswerOwnerScope(t), input)
	if err != nil {
		t.Fatalf("first Answer() error = %v", err)
	}
	second, err := service.Answer(t.Context(), testAnswerOwnerScope(t), Input{
		Query: "如何\n提高稳定性？",
		TopK:  5,
	})
	if err != nil {
		t.Fatalf("second Answer() error = %v", err)
	}
	if next.calls.Load() != 1 {
		t.Fatalf("downstream Answer() calls = %d, want 1", next.calls.Load())
	}
	if cache.setCalls != 1 {
		t.Fatalf("cache Set() calls = %d, want 1", cache.setCalls)
	}
	wantedCached := cacheHitOutput(first)
	if !reflect.DeepEqual(wantedCached, second) {
		t.Fatalf("cached output = %+v, want %+v", second, wantedCached)
	}
}

func TestCachedServiceCorpusRevisionChangeInvalidatesOldAnswer(t *testing.T) {
	next := &countingAnswerer{output: cacheableAnswerOutput()}
	revisions := &mutableCorpusRevisionReader{revision: 3}
	service := newCachedAnswerServiceForTest(
		t,
		next,
		revisions,
		newFakeAnswerCacheStore(),
	)
	input := Input{Query: "版本化问题", TopK: 5}

	if _, err := service.Answer(t.Context(), testAnswerOwnerScope(t), input); err != nil {
		t.Fatalf("first Answer() error = %v", err)
	}
	revisions.setRevision(4)
	if _, err := service.Answer(t.Context(), testAnswerOwnerScope(t), input); err != nil {
		t.Fatalf("second Answer() error = %v", err)
	}
	if next.calls.Load() != 2 {
		t.Fatalf("downstream Answer() calls = %d, want 2 after revision change", next.calls.Load())
	}
}

func TestCachedServiceDoesNotCacheInsufficientEvidence(t *testing.T) {
	next := &countingAnswerer{output: Output{Answer: InsufficientEvidenceAnswer}}
	cache := newFakeAnswerCacheStore()
	service := newCachedAnswerServiceForTest(
		t,
		next,
		&mutableCorpusRevisionReader{revision: 1},
		cache,
	)
	input := Input{Query: "无证据问题", TopK: 5}

	for call := 0; call < 2; call++ {
		if _, err := service.Answer(t.Context(), testAnswerOwnerScope(t), input); err != nil {
			t.Fatalf("Answer() error = %v", err)
		}
	}
	if next.calls.Load() != 2 || cache.setCalls != 0 {
		t.Fatalf(
			"downstream/cache calls = %d/%d, want 2/0",
			next.calls.Load(),
			cache.setCalls,
		)
	}
}

func TestCachedServiceCacheOrRevisionFailureFallsBack(t *testing.T) {
	testCases := []struct {
		name        string
		cacheErr    error
		revisionErr error
	}{
		{name: "Redis unavailable", cacheErr: errors.New("Redis unavailable")},
		{name: "revision unavailable", revisionErr: errors.New("PostgreSQL unavailable")},
	}

	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			next := &countingAnswerer{output: cacheableAnswerOutput()}
			cache := newFakeAnswerCacheStore()
			cache.getErr = testCase.cacheErr
			service := newCachedAnswerServiceForTest(
				t,
				next,
				&mutableCorpusRevisionReader{revision: 1, err: testCase.revisionErr},
				cache,
			)

			if _, err := service.Answer(
				t.Context(),
				testAnswerOwnerScope(t),
				Input{Query: "降级问题", TopK: 5},
			); err != nil {
				t.Fatalf("Answer() error = %v, want downstream fallback", err)
			}
			if next.calls.Load() != 1 {
				t.Fatalf("downstream Answer() calls = %d, want 1", next.calls.Load())
			}
		})
	}
}

func TestCachedServiceRejectsInvalidInputBeforeCache(t *testing.T) {
	next := &countingAnswerer{output: cacheableAnswerOutput()}
	revisions := &mutableCorpusRevisionReader{revision: 1}
	service := newCachedAnswerServiceForTest(t, next, revisions, newFakeAnswerCacheStore())

	_, err := service.Answer(
		t.Context(),
		testAnswerOwnerScope(t),
		Input{Query: " ", TopK: 5},
	)
	if !errors.Is(err, embeddingapplication.ErrSemanticSearchQueryRequired) {
		t.Fatalf("Answer() error = %v, want query required", err)
	}
	if next.calls.Load() != 0 || revisions.calls != 0 {
		t.Fatalf("dependency calls = downstream:%d revision:%d, want 0/0", next.calls.Load(), revisions.calls)
	}
}

func TestCachedServiceCollapsesConcurrentMisses(t *testing.T) {
	next := &countingAnswerer{
		delay:  30 * time.Millisecond,
		output: cacheableAnswerOutput(),
	}
	service := newCachedAnswerServiceForTest(
		t,
		next,
		&mutableCorpusRevisionReader{revision: 1},
		newFakeAnswerCacheStore(),
	)

	const workers = 12
	errorsChannel := make(chan error, workers)
	var waitGroup sync.WaitGroup
	for index := 0; index < workers; index++ {
		waitGroup.Add(1)
		go func() {
			defer waitGroup.Done()
			_, err := service.Answer(
				context.Background(),
				testAnswerOwnerScope(t),
				Input{Query: "并发问答", TopK: 5},
			)
			errorsChannel <- err
		}()
	}
	waitGroup.Wait()
	close(errorsChannel)
	for err := range errorsChannel {
		if err != nil {
			t.Fatalf("concurrent Answer() error = %v", err)
		}
	}
	if next.calls.Load() != 1 {
		t.Fatalf("downstream Answer() calls = %d, want 1", next.calls.Load())
	}
}

func cacheableAnswerOutput() Output {
	return Output{
		Answer: "根据证据，系统稳定性得到提高。[1]",
		Sources: []Source{
			{Citation: 1, ChunkID: 11, DocumentID: 22, OriginalName: "paper.pdf"},
		},
		PromptTokens:     100,
		CompletionTokens: 20,
		TotalTokens:      120,
	}
}

func newCachedAnswerServiceForTest(
	t *testing.T,
	next Answerer,
	revisions CorpusRevisionReader,
	cache embeddingapplication.BinaryCacheStore,
) *CachedService {
	t.Helper()
	service, err := NewCachedService(
		next,
		revisions,
		cache,
		fakeAnswerCacheDigester{},
		&recordingAnswerCacheObserver{},
		AnswerCacheConfig{
			Namespace:           "test",
			GenerationProvider:  "fake",
			GenerationModel:     "test-generation",
			PromptVersion:       AnswerPromptVersion,
			RetrievalVersion:    AnswerRetrievalVersion,
			EmbeddingModel:      "test-embedding",
			EmbeddingDimensions: 3,
			MaxOutputTokens:     100,
			Temperature:         0.1,
			TTL:                 time.Minute,
			LockTTL:             time.Minute,
			WaitTimeout:         time.Second,
		},
	)
	if err != nil {
		t.Fatalf("NewCachedService() error = %v", err)
	}
	return service
}
