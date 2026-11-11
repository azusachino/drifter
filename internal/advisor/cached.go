package advisor

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"
	"time"
)

// CacheStore abstracts the persistence required for caching advisor responses.
type CacheStore interface {
	GetAdvisorCache(ctx context.Context, key string) (string, error)
	PutAdvisorCache(ctx context.Context, key, promptType, model, responseJSON string, createdAt time.Time) error
}

// CachedAdvisor wraps an Advisor with transparent cache lookups.
type CachedAdvisor struct {
	inner Advisor
	store CacheStore
	model string
}

var _ Advisor = (*CachedAdvisor)(nil)

// NewCachedAdvisor creates a CachedAdvisor wrapping inner with the given CacheStore.
func NewCachedAdvisor(inner Advisor, store CacheStore, model string) *CachedAdvisor {
	return &CachedAdvisor{
		inner: inner,
		store: store,
		model: strings.TrimSpace(model),
	}
}

// ExplainConflict checks the cache before delegating to the inner advisor.
func (c *CachedAdvisor) ExplainConflict(ctx context.Context, req ConflictExplanationRequest) (ConflictExplanationResponse, error) {
	if c.store != nil {
		key := hashKey("conflict", req.SourceStatus, req.TargetStatus, req.BaselineStatus, req.Reason, req.SourceTitle)
		if cached, err := c.store.GetAdvisorCache(ctx, key); err == nil && cached != "" {
			var resp ConflictExplanationResponse
			if err := json.Unmarshal([]byte(cached), &resp); err == nil {
				return resp, nil
			}
		}
		resp, err := c.inner.ExplainConflict(ctx, req)
		if err != nil {
			return ConflictExplanationResponse{}, err
		}
		if raw, err := json.Marshal(resp); err == nil {
			_ = c.store.PutAdvisorCache(ctx, key, "conflict", c.model, string(raw), time.Now().UTC())
		}
		return resp, nil
	}
	return c.inner.ExplainConflict(ctx, req)
}

// ResolveOriginalWork checks the cache before delegating to the inner advisor.
func (c *CachedAdvisor) ResolveOriginalWork(ctx context.Context, req OriginalWorkRequest) (OriginalWorkResponse, error) {
	if c.store != nil {
		key := hashKey("original_work", req.Title, req.Author, req.Translator, req.Publisher, req.ISBN, req.DoubanOriginalTitle)
		if cached, err := c.store.GetAdvisorCache(ctx, key); err == nil && cached != "" {
			var resp OriginalWorkResponse
			if err := json.Unmarshal([]byte(cached), &resp); err == nil {
				return resp, nil
			}
		}
		resp, err := c.inner.ResolveOriginalWork(ctx, req)
		if err != nil {
			return OriginalWorkResponse{}, err
		}
		if raw, err := json.Marshal(resp); err == nil {
			_ = c.store.PutAdvisorCache(ctx, key, "original_work", c.model, string(raw), time.Now().UTC())
		}
		return resp, nil
	}
	return c.inner.ResolveOriginalWork(ctx, req)
}

func hashKey(parts ...string) string {
	h := sha256.New()
	for i, part := range parts {
		if i > 0 {
			h.Write([]byte{0})
		}
		h.Write([]byte(strings.TrimSpace(part)))
	}
	return hex.EncodeToString(h.Sum(nil))
}
