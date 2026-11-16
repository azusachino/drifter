package companion

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// mockSource implements Source and PreviewSource
type mockSource struct {
	shelfCalls       atomic.Int64
	searchCalls      atomic.Int64
	bookCalls        atomic.Int64
	progressCalls    atomic.Int64
	fingerprintCalls atomic.Int64

	shelfFunc       func(context.Context) (ShelfResult, error)
	searchFunc      func(context.Context, string) (SearchResult, error)
	bookFunc        func(context.Context, string) (Book, error)
	progressFunc    func(context.Context, string) (Progress, error)
	fingerprintFunc func(context.Context, string) (string, error)
}

func (m *mockSource) Shelf(ctx context.Context) (ShelfResult, error) {
	m.shelfCalls.Add(1)
	if m.shelfFunc != nil {
		return m.shelfFunc(ctx)
	}
	return ShelfResult{Books: []ShelfBook{{ID: "b1", Title: "Book One"}}, Complete: true}, nil
}

func (m *mockSource) Search(ctx context.Context, query string) (SearchResult, error) {
	m.searchCalls.Add(1)
	if m.searchFunc != nil {
		return m.searchFunc(ctx, query)
	}
	return SearchResult{Books: []Book{{ID: "b1", Title: query}}}, nil
}

func (m *mockSource) Book(ctx context.Context, id string) (Book, error) {
	m.bookCalls.Add(1)
	if m.bookFunc != nil {
		return m.bookFunc(ctx, id)
	}
	return Book{ID: id, Title: "Title " + id}, nil
}

func (m *mockSource) Progress(ctx context.Context, id string) (Progress, error) {
	m.progressCalls.Add(1)
	if m.progressFunc != nil {
		return m.progressFunc(ctx, id)
	}
	return Progress{Status: "reading", Percent: 50}, nil
}

func (m *mockSource) ResolveAccountFingerprint(ctx context.Context, declaredVID string) (string, error) {
	m.fingerprintCalls.Add(1)
	if m.fingerprintFunc != nil {
		return m.fingerprintFunc(ctx, declaredVID)
	}
	return "fp-source-123", nil
}

// mockTarget implements Target, TargetProvider, PreviewTarget, ApplyTarget
type mockTarget struct {
	matchCalls       atomic.Int64
	validateCalls    atomic.Int64
	statusCalls      atomic.Int64
	mutationCalls    atomic.Int64
	fingerprintCalls atomic.Int64

	matchFunc       func(context.Context, Book, string) (MatchReport, error)
	validateFunc    func(context.Context, int64, int64) error
	statusFunc      func(context.Context, int64) (*UserBookStatus, error)
	mutationFunc    func(context.Context, StatusMutationRequest) (StatusMutationOutcome, error)
	fingerprintFunc func(context.Context) (string, error)
}

func (m *mockTarget) Match(ctx context.Context, book Book, query string) (MatchReport, error) {
	m.matchCalls.Add(1)
	if m.matchFunc != nil {
		return m.matchFunc(ctx, book, query)
	}
	return MatchReport{Book: book, SearchQuery: query, Candidates: []Candidate{{ID: 42, Title: query}}}, nil
}

func (m *mockTarget) ValidateLink(ctx context.Context, workID, editionID int64) error {
	m.validateCalls.Add(1)
	if m.validateFunc != nil {
		return m.validateFunc(ctx, workID, editionID)
	}
	return nil
}

func (m *mockTarget) GetUserBookStatus(ctx context.Context, workID int64) (*UserBookStatus, error) {
	m.statusCalls.Add(1)
	if m.statusFunc != nil {
		return m.statusFunc(ctx, workID)
	}
	return &UserBookStatus{ID: 10, BookID: workID, StatusID: 2}, nil
}

func (m *mockTarget) ApplyStatusMutation(ctx context.Context, req StatusMutationRequest) (StatusMutationOutcome, error) {
	m.mutationCalls.Add(1)
	if m.mutationFunc != nil {
		return m.mutationFunc(ctx, req)
	}
	return StatusMutationOutcome{UserBookID: 10, CurrentStatus: req.StatusID}, nil
}

func (m *mockTarget) ResolveAccountFingerprint(ctx context.Context) (string, error) {
	m.fingerprintCalls.Add(1)
	if m.fingerprintFunc != nil {
		return m.fingerprintFunc(ctx)
	}
	return "fp-target-456", nil
}

func TestMemoryTTLCache_BasicOperations(t *testing.T) {
	now := time.Date(2026, 11, 16, 12, 0, 0, 0, time.UTC)
	currentTime := now
	nowFunc := func() time.Time { return currentTime }

	cache := NewMemoryTTLCache(10, nowFunc)

	// Missing key
	if val, ok := cache.Get("missing"); ok || val != nil {
		t.Fatalf("expected missing key, got %v, %v", val, ok)
	}

	// Set and Get
	cache.Set("k1", "v1", 10*time.Minute)
	if val, ok := cache.Get("k1"); !ok || val != "v1" {
		t.Fatalf("expected v1, got %v, %v", val, ok)
	}

	// Advance time before expiry
	currentTime = currentTime.Add(5 * time.Minute)
	if val, ok := cache.Get("k1"); !ok || val != "v1" {
		t.Fatalf("expected v1 before expiry, got %v, %v", val, ok)
	}

	// Advance time past expiry
	currentTime = currentTime.Add(6 * time.Minute)
	if val, ok := cache.Get("k1"); ok || val != nil {
		t.Fatalf("expected expired entry, got %v, %v", val, ok)
	}

	// Delete
	cache.Set("k2", "v2", 10*time.Minute)
	cache.Delete("k2")
	if _, ok := cache.Get("k2"); ok {
		t.Fatalf("expected k2 to be deleted")
	}

	// DeletePrefix
	cache.Set("p:1", "1", 10*time.Minute)
	cache.Set("p:2", "2", 10*time.Minute)
	cache.Set("other", "3", 10*time.Minute)
	cache.DeletePrefix("p:")
	if _, ok := cache.Get("p:1"); ok {
		t.Fatalf("expected p:1 deleted")
	}
	if _, ok := cache.Get("p:2"); ok {
		t.Fatalf("expected p:2 deleted")
	}
	if val, ok := cache.Get("other"); !ok || val != "3" {
		t.Fatalf("expected other to remain, got %v, %v", val, ok)
	}
}

func TestMemoryTTLCache_CapacityEviction(t *testing.T) {
	now := time.Date(2026, 11, 16, 12, 0, 0, 0, time.UTC)
	currentTime := now
	nowFunc := func() time.Time { return currentTime }

	cache := NewMemoryTTLCache(3, nowFunc)
	cache.Set("k1", "v1", 10*time.Minute)
	cache.Set("k2", "v2", 10*time.Minute)
	cache.Set("k3", "v3", 10*time.Minute)

	if cache.Len() != 3 {
		t.Fatalf("expected 3 entries, got %d", cache.Len())
	}

	// Add 4th item when none expired: must evict one to preserve maxEntries
	cache.Set("k4", "v4", 10*time.Minute)
	if cache.Len() > 3 {
		t.Fatalf("expected bounded size <= 3, got %d", cache.Len())
	}

	// Let k2 expire, add k5: expired entries evicted first
	currentTime = currentTime.Add(15 * time.Minute)
	cache.Set("k5", "v5", 10*time.Minute)
	if cache.Len() > 3 {
		t.Fatalf("expected size <= 3 after eviction, got %d", cache.Len())
	}
}

func TestMemoryTTLCache_ConcurrentAccess(t *testing.T) {
	cache := NewMemoryTTLCache(100, nil)
	var wg sync.WaitGroup

	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func(workerID int) {
			defer wg.Done()
			for j := 0; j < 50; j++ {
				key := fmt.Sprintf("k-%d-%d", workerID, j%10)
				cache.Set(key, j, time.Minute)
				cache.Get(key)
				if j%5 == 0 {
					cache.Delete(key)
				}
			}
		}(i)
	}
	wg.Wait()
}

func TestCachedSource_ShelfCachingAndInvalidation(t *testing.T) {
	now := time.Date(2026, 11, 16, 12, 0, 0, 0, time.UTC)
	currentTime := now
	nowFunc := func() time.Time { return currentTime }

	inner := &mockSource{}
	cached := NewCachedSource(inner, nil, nowFunc)
	ctx := context.Background()

	// Initial fetch
	res1, err := cached.Shelf(ctx)
	if err != nil || len(res1.Books) != 1 {
		t.Fatalf("unexpected res1: %+v, %v", res1, err)
	}
	if inner.shelfCalls.Load() != 1 {
		t.Fatalf("expected 1 call to inner, got %d", inner.shelfCalls.Load())
	}

	// Second fetch hits cache
	res2, err := cached.Shelf(ctx)
	if err != nil || len(res2.Books) != 1 {
		t.Fatalf("unexpected res2: %+v, %v", res2, err)
	}
	if inner.shelfCalls.Load() != 1 {
		t.Fatalf("expected cache hit (still 1 call), got %d", inner.shelfCalls.Load())
	}

	// Expiration after 5 minutes
	currentTime = currentTime.Add(5*time.Minute + time.Second)
	_, err = cached.Shelf(ctx)
	if err != nil {
		t.Fatalf("unexpected error after TTL: %v", err)
	}
	if inner.shelfCalls.Load() != 2 {
		t.Fatalf("expected 2 calls after TTL expiration, got %d", inner.shelfCalls.Load())
	}

	// Explicit invalidation
	cached.InvalidateShelf()
	_, err = cached.Shelf(ctx)
	if err != nil {
		t.Fatalf("unexpected error after InvalidateShelf: %v", err)
	}
	if inner.shelfCalls.Load() != 3 {
		t.Fatalf("expected 3 calls after InvalidateShelf, got %d", inner.shelfCalls.Load())
	}

	// Force bypass
	_, err = cached.ShelfWithForce(ctx, true)
	if err != nil {
		t.Fatalf("unexpected error on force shelf: %v", err)
	}
	if inner.shelfCalls.Load() != 4 {
		t.Fatalf("expected 4 calls after ShelfWithForce(true), got %d", inner.shelfCalls.Load())
	}
}

func TestCachedSource_BookCachingAndTTL(t *testing.T) {
	now := time.Date(2026, 11, 16, 12, 0, 0, 0, time.UTC)
	currentTime := now
	nowFunc := func() time.Time { return currentTime }

	inner := &mockSource{}
	cached := NewCachedSource(inner, nil, nowFunc)
	ctx := context.Background()

	// First fetch
	b1, err := cached.Book(ctx, "book-1")
	if err != nil || b1.ID != "book-1" {
		t.Fatalf("unexpected book: %+v, %v", b1, err)
	}
	if inner.bookCalls.Load() != 1 {
		t.Fatalf("expected 1 call, got %d", inner.bookCalls.Load())
	}

	// Second fetch hits cache
	_, err = cached.Book(ctx, "book-1")
	if err != nil {
		t.Fatalf("unexpected error on hit: %v", err)
	}
	if inner.bookCalls.Load() != 1 {
		t.Fatalf("expected cache hit, got %d", inner.bookCalls.Load())
	}

	// Different book ID
	_, err = cached.Book(ctx, "book-2")
	if err != nil {
		t.Fatalf("unexpected error on book-2: %v", err)
	}
	if inner.bookCalls.Load() != 2 {
		t.Fatalf("expected 2 calls, got %d", inner.bookCalls.Load())
	}

	// Invalidate specific book
	cached.InvalidateBook("book-1")
	_, err = cached.Book(ctx, "book-1")
	if err != nil {
		t.Fatalf("unexpected error on book-1 after invalidate: %v", err)
	}
	if inner.bookCalls.Load() != 3 {
		t.Fatalf("expected 3 calls after invalidation, got %d", inner.bookCalls.Load())
	}

	// 6-hour TTL expiration
	currentTime = currentTime.Add(6*time.Hour + time.Minute)
	_, err = cached.Book(ctx, "book-1")
	if err != nil {
		t.Fatalf("unexpected error after 6h: %v", err)
	}
	if inner.bookCalls.Load() != 4 {
		t.Fatalf("expected 4 calls after 6-hour TTL, got %d", inner.bookCalls.Load())
	}
}

func TestCachedSource_ErrorsNotCached(t *testing.T) {
	inner := &mockSource{
		bookFunc: func(ctx context.Context, id string) (Book, error) {
			return Book{}, errors.New("provider failure")
		},
		shelfFunc: func(ctx context.Context) (ShelfResult, error) {
			return ShelfResult{}, errors.New("shelf network timeout")
		},
	}
	cached := NewCachedSource(inner, nil, nil)
	ctx := context.Background()

	// Shelf error
	_, err := cached.Shelf(ctx)
	if err == nil {
		t.Fatal("expected error")
	}
	_, err = cached.Shelf(ctx)
	if err == nil {
		t.Fatal("expected error")
	}
	if inner.shelfCalls.Load() != 2 {
		t.Fatalf("expected 2 calls (error not cached), got %d", inner.shelfCalls.Load())
	}

	// Book error
	_, err = cached.Book(ctx, "err-id")
	if err == nil {
		t.Fatal("expected error")
	}
	_, err = cached.Book(ctx, "err-id")
	if err == nil {
		t.Fatal("expected error")
	}
	if inner.bookCalls.Load() != 2 {
		t.Fatalf("expected 2 calls (error not cached), got %d", inner.bookCalls.Load())
	}
}

func TestCachedSource_ProgressAndFingerprintNeverCached(t *testing.T) {
	inner := &mockSource{}
	cached := NewCachedSource(inner, nil, nil)
	ctx := context.Background()

	// Progress must pass through each time
	_, _ = cached.Progress(ctx, "b1")
	_, _ = cached.Progress(ctx, "b1")
	if inner.progressCalls.Load() != 2 {
		t.Fatalf("expected Progress not to be cached, got %d calls", inner.progressCalls.Load())
	}

	// ResolveAccountFingerprint must pass through
	_, _ = cached.ResolveAccountFingerprint(ctx, "declared-vid")
	_, _ = cached.ResolveAccountFingerprint(ctx, "declared-vid")
	if inner.fingerprintCalls.Load() != 2 {
		t.Fatalf("expected ResolveAccountFingerprint not to be cached, got %d calls", inner.fingerprintCalls.Load())
	}
}

func TestCachedTarget_MatchAndSearchWorksCaching(t *testing.T) {
	now := time.Date(2026, 11, 16, 12, 0, 0, 0, time.UTC)
	currentTime := now
	nowFunc := func() time.Time { return currentTime }

	inner := &mockTarget{}
	cached := NewCachedTarget(inner, nil, nowFunc)
	ctx := context.Background()

	book := Book{ID: "101", Title: "Test Book", ISBN: "9781234567890"}

	// Match call 1
	rep1, err := cached.Match(ctx, book, "Test Book")
	if err != nil || rep1.Candidates[0].ID != 42 {
		t.Fatalf("unexpected rep1: %+v, %v", rep1, err)
	}
	if inner.matchCalls.Load() != 1 {
		t.Fatalf("expected 1 call, got %d", inner.matchCalls.Load())
	}

	// Match call 2 (hit)
	rep2, err := cached.Match(ctx, book, "Test Book")
	if err != nil || rep2.Candidates[0].ID != 42 {
		t.Fatalf("unexpected rep2: %+v, %v", rep2, err)
	}
	if inner.matchCalls.Load() != 1 {
		t.Fatalf("expected cache hit (1 call), got %d", inner.matchCalls.Load())
	}

	// SearchWorks helper
	sw1, err := cached.SearchWorks(ctx, "Work Title")
	if err != nil || sw1.Candidates[0].ID != 42 {
		t.Fatalf("unexpected sw1: %+v, %v", sw1, err)
	}
	if inner.matchCalls.Load() != 2 {
		t.Fatalf("expected 2 calls, got %d", inner.matchCalls.Load())
	}

	// SearchWorks helper hit
	_, err = cached.SearchWorks(ctx, "Work Title")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if inner.matchCalls.Load() != 2 {
		t.Fatalf("expected hit on SearchWorks, got %d calls", inner.matchCalls.Load())
	}

	// ValidateLink caching
	if err := cached.ValidateLink(ctx, 42, 100); err != nil {
		t.Fatalf("unexpected error on ValidateLink: %v", err)
	}
	if inner.validateCalls.Load() != 1 {
		t.Fatalf("expected 1 validate call, got %d", inner.validateCalls.Load())
	}
	if err := cached.ValidateLink(ctx, 42, 100); err != nil {
		t.Fatalf("unexpected error on ValidateLink hit: %v", err)
	}
	if inner.validateCalls.Load() != 1 {
		t.Fatalf("expected hit on ValidateLink, got %d calls", inner.validateCalls.Load())
	}

	// Mutating operations: ApplyStatusMutation must never be cached
	req := StatusMutationRequest{WorkID: 42, StatusID: 2}
	_, _ = cached.ApplyStatusMutation(ctx, req)
	_, _ = cached.ApplyStatusMutation(ctx, req)
	if inner.mutationCalls.Load() != 2 {
		t.Fatalf("expected ApplyStatusMutation not to be cached, got %d calls", inner.mutationCalls.Load())
	}
}

func TestCachedTarget_MatchErrorNotCached(t *testing.T) {
	inner := &mockTarget{
		matchFunc: func(ctx context.Context, b Book, q string) (MatchReport, error) {
			return MatchReport{}, errors.New("upstream timeout")
		},
	}
	cached := NewCachedTarget(inner, nil, nil)
	ctx := context.Background()

	_, err := cached.Match(ctx, Book{ID: "1"}, "q")
	if err == nil {
		t.Fatal("expected error")
	}
	_, err = cached.Match(ctx, Book{ID: "1"}, "q")
	if err == nil {
		t.Fatal("expected error")
	}
	if inner.matchCalls.Load() != 2 {
		t.Fatalf("expected 2 calls (error not cached), got %d", inner.matchCalls.Load())
	}
}

func TestCachedSourceAndTarget_ConcurrentAccess(t *testing.T) {
	source := &mockSource{}
	target := &mockTarget{}
	cachedSource := NewCachedSource(source, nil, nil)
	cachedTarget := NewCachedTarget(target, nil, nil)

	var wg sync.WaitGroup
	ctx := context.Background()

	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			for j := 0; j < 30; j++ {
				_, _ = cachedSource.Shelf(ctx)
				_, _ = cachedSource.Book(ctx, fmt.Sprintf("book-%d", j%5))
				_, _ = cachedSource.Progress(ctx, fmt.Sprintf("book-%d", j%5))
				_, _ = cachedTarget.SearchWorks(ctx, fmt.Sprintf("work-%d", j%5))
				_ = cachedTarget.ValidateLink(ctx, int64(j%5), int64(j%5))
			}
		}(i)
	}
	wg.Wait()
}
