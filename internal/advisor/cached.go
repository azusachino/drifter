package advisor

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"
	"time"
)

// CacheTTL is how long a cached advisor answer stays valid.
const CacheTTL = 30 * 24 * time.Hour

// CacheStore abstracts the persistence required for caching advisor responses.
// GetAdvisorCache returns an empty string for a missing entry or one created
// before notBefore.
type CacheStore interface {
	GetAdvisorCache(ctx context.Context, key string, notBefore time.Time) (string, error)
	PutAdvisorCache(ctx context.Context, key, promptType, model, responseJSON string, createdAt time.Time) error
}

// CachedAdvisor wraps an Advisor with transparent cache lookups. The cache key
// covers the whole request, the model and the prompt template, so an answer is
// reused only for the same question put to the same model with the same prompt.
type CachedAdvisor struct {
	inner      Advisor
	store      CacheStore
	model      string
	promptHash string
	now        func() time.Time
}

var _ Advisor = (*CachedAdvisor)(nil)

// NewCachedAdvisor creates a CachedAdvisor wrapping inner with the given
// CacheStore. promptHash identifies the prompt templates in use.
func NewCachedAdvisor(inner Advisor, store CacheStore, model, promptHash string) *CachedAdvisor {
	return &CachedAdvisor{
		inner:      inner,
		store:      store,
		model:      strings.TrimSpace(model),
		promptHash: promptHash,
		now:        func() time.Time { return time.Now().UTC() },
	}
}

// ExplainConflict checks the cache before delegating to the inner advisor.
func (c *CachedAdvisor) ExplainConflict(ctx context.Context, req ConflictExplanationRequest) (ConflictExplanationResponse, error) {
	return cached(ctx, c, "conflict", req, c.inner.ExplainConflict)
}

// ResolveOriginalWork checks the cache before delegating to the inner advisor.
func (c *CachedAdvisor) ResolveOriginalWork(ctx context.Context, req OriginalWorkRequest) (OriginalWorkResponse, error) {
	return cached(ctx, c, "original_work", req, c.inner.ResolveOriginalWork)
}

func cached[Req, Resp any](ctx context.Context, c *CachedAdvisor, promptType string, req Req, call func(context.Context, Req) (Resp, error)) (Resp, error) {
	var zero Resp
	if c.store == nil {
		return call(ctx, req)
	}
	key, err := c.key(promptType, req)
	if err != nil {
		return call(ctx, req)
	}
	now := c.now()
	if hit, err := c.store.GetAdvisorCache(ctx, key, now.Add(-CacheTTL)); err == nil && hit != "" {
		var resp Resp
		if err := json.Unmarshal([]byte(hit), &resp); err == nil {
			return resp, nil
		}
	}
	resp, err := call(ctx, req)
	if err != nil {
		return zero, err
	}
	if raw, err := json.Marshal(resp); err == nil {
		_ = c.store.PutAdvisorCache(ctx, key, promptType, c.model, string(raw), now)
	}
	return resp, nil
}

// key hashes a canonical JSON encoding of the whole request together with the
// prompt type, model and prompt hash. Adding a request field changes the
// encoding, so it cannot be left out of the key.
func (c *CachedAdvisor) key(promptType string, req any) (string, error) {
	enc, err := json.Marshal(struct {
		Type   string `json:"type"`
		Model  string `json:"model"`
		Prompt string `json:"prompt"`
		Req    any    `json:"req"`
	}{promptType, c.model, c.promptHash, req})
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(enc)
	return hex.EncodeToString(sum[:]), nil
}
