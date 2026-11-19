package companion

import (
	"context"
	"fmt"
	"sync"
	"time"
)

// Default TTLs and capacity for cached companion operations.
const (
	DefaultCacheMaxEntries = 10000
	DefaultShelfCacheTTL   = 5 * time.Minute
	DefaultBookCacheTTL    = 6 * time.Hour
	DefaultSearchCacheTTL  = 1 * time.Hour
	DefaultTargetCacheTTL  = 1 * time.Hour
)

// Cache defines the minimal thread-safe cache operations.
type Cache interface {
	Get(key string) (any, bool)
	Set(key string, value any, ttl time.Duration)
	Delete(key string)
}

// memoryTTLCacheEntry holds a cached value and its absolute expiration time.
type memoryTTLCacheEntry struct {
	value     any
	expiresAt time.Time
}

// MemoryTTLCache is a thread-safe in-memory cache with TTL expiration and bounded capacity.
type MemoryTTLCache struct {
	mu         sync.RWMutex
	entries    map[string]memoryTTLCacheEntry
	maxEntries int
	now        func() time.Time
}

var _ Cache = (*MemoryTTLCache)(nil)

// NewMemoryTTLCache creates an in-memory TTL cache.
// If maxEntries <= 0, a default capacity of 10,000 is used.
// If now is nil, time.Now().UTC is used.
func NewMemoryTTLCache(maxEntries int, now func() time.Time) *MemoryTTLCache {
	if maxEntries <= 0 {
		maxEntries = DefaultCacheMaxEntries
	}
	if now == nil {
		now = func() time.Time { return time.Now().UTC() }
	}
	return &MemoryTTLCache{
		entries:    make(map[string]memoryTTLCacheEntry),
		maxEntries: maxEntries,
		now:        now,
	}
}

// Get returns the cached value for key if present and not expired.
func (c *MemoryTTLCache) Get(key string) (any, bool) {
	c.mu.RLock()
	entry, ok := c.entries[key]
	if !ok {
		c.mu.RUnlock()
		return nil, false
	}
	now := c.now()
	if !entry.expiresAt.IsZero() && now.After(entry.expiresAt) {
		c.mu.RUnlock()
		// Evict expired entry lazily under write lock
		c.mu.Lock()
		if cur, exists := c.entries[key]; exists && !cur.expiresAt.IsZero() && c.now().After(cur.expiresAt) {
			delete(c.entries, key)
		}
		c.mu.Unlock()
		return nil, false
	}
	val := entry.value
	c.mu.RUnlock()
	return val, true
}

// Set stores value with the given ttl. If ttl <= 0, the item does not expire.
func (c *MemoryTTLCache) Set(key string, value any, ttl time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()

	var expiresAt time.Time
	if ttl > 0 {
		expiresAt = c.now().Add(ttl)
	}

	// If at max capacity and key is new, evict expired entries first.
	if _, exists := c.entries[key]; !exists && len(c.entries) >= c.maxEntries {
		now := c.now()
		for k, e := range c.entries {
			if !e.expiresAt.IsZero() && now.After(e.expiresAt) {
				delete(c.entries, k)
			}
		}
		// If still at capacity, evict any single entry to enforce hard bound.
		if len(c.entries) >= c.maxEntries {
			for k := range c.entries {
				delete(c.entries, k)
				break
			}
		}
	}

	c.entries[key] = memoryTTLCacheEntry{
		value:     value,
		expiresAt: expiresAt,
	}
}

// Delete removes key from cache.
func (c *MemoryTTLCache) Delete(key string) {
	c.mu.Lock()
	delete(c.entries, key)
	c.mu.Unlock()
}

// DeletePrefix deletes all keys starting with prefix.
func (c *MemoryTTLCache) DeletePrefix(prefix string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for k := range c.entries {
		if len(k) >= len(prefix) && k[:len(prefix)] == prefix {
			delete(c.entries, k)
		}
	}
}

// Len returns current number of entries.
func (c *MemoryTTLCache) Len() int {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return len(c.entries)
}

// CachedSource wraps a Source (and optionally PreviewSource / SourceProvider) with in-memory caching.
// - Shelf is cached with 5-minute TTL.
// - Book is cached with 6-hour TTL.
// - Progress is NOT cached (live reading state).
// - Search is cached with 1-hour TTL.
// - ResolveAccountFingerprint is passed through without caching.
type CachedSource struct {
	inner     Source
	cache     *MemoryTTLCache
	shelfTTL  time.Duration
	bookTTL   time.Duration
	searchTTL time.Duration
	now       func() time.Time
}

var _ Source = (*CachedSource)(nil)
var _ PreviewSource = (*CachedSource)(nil)
var _ SourceProvider = (*CachedSource)(nil)

// NewCachedSource wraps inner Source with MemoryTTLCache using default TTLs.
// If cache is nil, a default cache is instantiated.
// If now is nil, time.Now().UTC is used.
func NewCachedSource(inner Source, cache *MemoryTTLCache, now func() time.Time) *CachedSource {
	return NewCachedSourceWithTTLs(inner, cache, now, DefaultShelfCacheTTL, DefaultBookCacheTTL, DefaultSearchCacheTTL)
}

// NewCachedSourceWithTTLs wraps inner Source with MemoryTTLCache and custom TTLs.
// Non-positive TTLs fall back to their respective package defaults.
func NewCachedSourceWithTTLs(inner Source, cache *MemoryTTLCache, now func() time.Time, shelfTTL, bookTTL, searchTTL time.Duration) *CachedSource {
	if cache == nil {
		cache = NewMemoryTTLCache(DefaultCacheMaxEntries, now)
	}
	if now == nil {
		now = func() time.Time { return time.Now().UTC() }
	}
	if shelfTTL <= 0 {
		shelfTTL = DefaultShelfCacheTTL
	}
	if bookTTL <= 0 {
		bookTTL = DefaultBookCacheTTL
	}
	if searchTTL <= 0 {
		searchTTL = DefaultSearchCacheTTL
	}
	return &CachedSource{
		inner:     inner,
		cache:     cache,
		shelfTTL:  shelfTTL,
		bookTTL:   bookTTL,
		searchTTL: searchTTL,
		now:       now,
	}
}

// InvalidateShelf removes the cached shelf entry.
func (c *CachedSource) InvalidateShelf() {
	c.cache.Delete("source:shelf")
}

// InvalidateBook removes a cached book by ID.
func (c *CachedSource) InvalidateBook(id string) {
	c.cache.Delete("source:book:" + id)
}

// Shelf returns the cached shelf result if available, or fetches from inner Source.
func (c *CachedSource) Shelf(ctx context.Context) (ShelfResult, error) {
	return c.ShelfWithForce(ctx, false)
}

// ShelfWithForce returns the shelf result, bypassing and updating the cache if force is true.
func (c *CachedSource) ShelfWithForce(ctx context.Context, force bool) (ShelfResult, error) {
	key := "source:shelf"
	if !force {
		if val, found := c.cache.Get(key); found {
			if res, ok := val.(ShelfResult); ok {
				return res, nil
			}
		}
	}
	res, err := c.inner.Shelf(ctx)
	if err != nil {
		return ShelfResult{}, err
	}
	c.cache.Set(key, res, c.shelfTTL)
	return res, nil
}

// Search searches for books. Caches successful non-empty results with DefaultSearchCacheTTL.
func (c *CachedSource) Search(ctx context.Context, query string) (SearchResult, error) {
	key := "source:search:" + query
	if val, found := c.cache.Get(key); found {
		if res, ok := val.(SearchResult); ok {
			return res, nil
		}
	}
	res, err := c.inner.Search(ctx, query)
	if err != nil {
		return SearchResult{}, err
	}
	c.cache.Set(key, res, c.searchTTL)
	return res, nil
}

// Book returns book metadata. Cached with 6-hour TTL.
func (c *CachedSource) Book(ctx context.Context, id string) (Book, error) {
	key := "source:book:" + id
	if val, found := c.cache.Get(key); found {
		if b, ok := val.(Book); ok {
			return b, nil
		}
	}
	b, err := c.inner.Book(ctx, id)
	if err != nil {
		return Book{}, err
	}
	c.cache.Set(key, b, c.bookTTL)
	return b, nil
}

// Progress returns live reading progress. Never cached.
func (c *CachedSource) Progress(ctx context.Context, id string) (Progress, error) {
	return c.inner.Progress(ctx, id)
}

// ResolveAccountFingerprint implements PreviewSource and SourceProvider. Never cached.
func (c *CachedSource) ResolveAccountFingerprint(ctx context.Context, declaredVID string) (string, error) {
	if p, ok := c.inner.(PreviewSource); ok {
		return p.ResolveAccountFingerprint(ctx, declaredVID)
	}
	if sp, ok := c.inner.(SourceProvider); ok {
		return sp.ResolveAccountFingerprint(ctx, declaredVID)
	}
	return "", fmt.Errorf("inner source does not implement ResolveAccountFingerprint")
}

// CachedTarget wraps a Target (and optionally TargetProvider / PreviewTarget / ApplyTarget) with caching.
// - Match/Search is cached with 1-hour TTL.
// - ValidateLink is cached with 1-hour TTL.
// - Mutating methods (ApplyStatusMutation) pass through directly.
type CachedTarget struct {
	inner     Target
	cache     *MemoryTTLCache
	targetTTL time.Duration
	now       func() time.Time
}

var _ Target = (*CachedTarget)(nil)
var _ PreviewTarget = (*CachedTarget)(nil)
var _ TargetProvider = (*CachedTarget)(nil)
var _ ApplyTarget = (*CachedTarget)(nil)

// NewCachedTarget wraps inner Target with MemoryTTLCache using DefaultTargetCacheTTL.
func NewCachedTarget(inner Target, cache *MemoryTTLCache, now func() time.Time) *CachedTarget {
	return NewCachedTargetWithTTL(inner, cache, now, DefaultTargetCacheTTL)
}

// NewCachedTargetWithTTL wraps inner Target with MemoryTTLCache and a custom TTL.
// A non-positive targetTTL falls back to DefaultTargetCacheTTL.
func NewCachedTargetWithTTL(inner Target, cache *MemoryTTLCache, now func() time.Time, targetTTL time.Duration) *CachedTarget {
	if cache == nil {
		cache = NewMemoryTTLCache(DefaultCacheMaxEntries, now)
	}
	if now == nil {
		now = func() time.Time { return time.Now().UTC() }
	}
	if targetTTL <= 0 {
		targetTTL = DefaultTargetCacheTTL
	}
	return &CachedTarget{
		inner:     inner,
		cache:     cache,
		targetTTL: targetTTL,
		now:       now,
	}
}

// SearchWorks provides backward-compatible or helper search method caching work candidates.
func (c *CachedTarget) SearchWorks(ctx context.Context, query string) (MatchReport, error) {
	return c.Match(ctx, Book{Title: query}, query)
}

// Match performs candidate matching. Cached with 1-hour TTL.
func (c *CachedTarget) Match(ctx context.Context, book Book, query string) (MatchReport, error) {
	key := fmt.Sprintf("target:match:%s:%s:%s", book.ID, book.ISBN, query)
	if val, found := c.cache.Get(key); found {
		if report, ok := val.(MatchReport); ok {
			return report, nil
		}
	}
	report, err := c.inner.Match(ctx, book, query)
	if err != nil {
		return MatchReport{}, err
	}
	c.cache.Set(key, report, c.targetTTL)
	return report, nil
}

// ValidateLink verifies link identities. Cached with 1-hour TTL.
func (c *CachedTarget) ValidateLink(ctx context.Context, workID, editionID int64) error {
	key := fmt.Sprintf("target:validatelink:%d:%d", workID, editionID)
	if val, found := c.cache.Get(key); found {
		if valid, ok := val.(bool); ok && valid {
			return nil
		}
	}
	err := c.inner.ValidateLink(ctx, workID, editionID)
	if err != nil {
		return err
	}
	c.cache.Set(key, true, c.targetTTL)
	return nil
}

// GetUserBookStatus implements TargetProvider and PreviewTarget.
func (c *CachedTarget) GetUserBookStatus(ctx context.Context, workID int64) (*UserBookStatus, error) {
	if tp, ok := c.inner.(TargetProvider); ok {
		return tp.GetUserBookStatus(ctx, workID)
	}
	if pt, ok := c.inner.(PreviewTarget); ok {
		return pt.GetUserBookStatus(ctx, workID)
	}
	return nil, fmt.Errorf("inner target does not implement GetUserBookStatus")
}

// ApplyStatusMutation implements TargetProvider and ApplyTarget. Mutating operation; never cached.
func (c *CachedTarget) ApplyStatusMutation(ctx context.Context, req StatusMutationRequest) (StatusMutationOutcome, error) {
	if tp, ok := c.inner.(TargetProvider); ok {
		return tp.ApplyStatusMutation(ctx, req)
	}
	if at, ok := c.inner.(ApplyTarget); ok {
		return at.ApplyStatusMutation(ctx, req)
	}
	return StatusMutationOutcome{}, fmt.Errorf("inner target does not implement ApplyStatusMutation")
}

// ResolveAccountFingerprint implements TargetProvider and PreviewTarget. Never cached.
func (c *CachedTarget) ResolveAccountFingerprint(ctx context.Context) (string, error) {
	if tp, ok := c.inner.(TargetProvider); ok {
		return tp.ResolveAccountFingerprint(ctx)
	}
	if pt, ok := c.inner.(PreviewTarget); ok {
		return pt.ResolveAccountFingerprint(ctx)
	}
	return "", fmt.Errorf("inner target does not implement ResolveAccountFingerprint")
}
