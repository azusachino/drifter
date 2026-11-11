package advisor

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"
)

type memoryStore struct {
	data map[string]string
}

func (m *memoryStore) GetAdvisorCache(ctx context.Context, key string) (string, error) {
	return m.data[key], nil
}

func (m *memoryStore) PutAdvisorCache(ctx context.Context, key, promptType, model, responseJSON string, createdAt time.Time) error {
	m.data[key] = responseJSON
	return nil
}

type countingAdvisor struct {
	conflictCalls     atomic.Int32
	originalWorkCalls atomic.Int32
}

func (c *countingAdvisor) ExplainConflict(ctx context.Context, req ConflictExplanationRequest) (ConflictExplanationResponse, error) {
	c.conflictCalls.Add(1)
	return ConflictExplanationResponse{
		Summary:           "diverged status",
		RecommendedAction: "adopt_source",
		Rationale:         "source is newer",
	}, nil
}

func (c *countingAdvisor) ResolveOriginalWork(ctx context.Context, req OriginalWorkRequest) (OriginalWorkResponse, error) {
	c.originalWorkCalls.Add(1)
	return OriginalWorkResponse{
		OriginalTitle: "Test Original",
		Confidence:    0.9,
	}, nil
}

func TestCachedAdvisorConflict(t *testing.T) {
	mock := &countingAdvisor{}
	store := &memoryStore{data: make(map[string]string)}
	cached := NewCachedAdvisor(mock, store, "test-model")

	req := ConflictExplanationRequest{
		SourceTitle:  "Book 1",
		SourceStatus: "read",
		TargetStatus: "reading",
	}

	// First call -> calls inner advisor
	res1, err := cached.ExplainConflict(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	if mock.conflictCalls.Load() != 1 {
		t.Fatalf("expected 1 call, got %d", mock.conflictCalls.Load())
	}
	if res1.RecommendedAction != "adopt_source" {
		t.Fatalf("unexpected res1: %+v", res1)
	}

	// Second call with same request -> returns cached response, inner not called
	res2, err := cached.ExplainConflict(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	if mock.conflictCalls.Load() != 1 {
		t.Fatalf("expected 1 call still, got %d", mock.conflictCalls.Load())
	}
	if res2.RecommendedAction != "adopt_source" {
		t.Fatalf("unexpected res2: %+v", res2)
	}
}

func TestCachedAdvisorOriginalWork(t *testing.T) {
	mock := &countingAdvisor{}
	store := &memoryStore{data: make(map[string]string)}
	cached := NewCachedAdvisor(mock, store, "test-model")

	req := OriginalWorkRequest{
		Title:  "Book 2",
		Author: "Author 2",
	}

	// First call
	res1, err := cached.ResolveOriginalWork(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	if res1.OriginalTitle != "Test Original" {
		t.Fatalf("unexpected res1: %+v", res1)
	}
	if mock.originalWorkCalls.Load() != 1 {
		t.Fatalf("expected 1 call, got %d", mock.originalWorkCalls.Load())
	}

	// Second call
	res2, err := cached.ResolveOriginalWork(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	if mock.originalWorkCalls.Load() != 1 {
		t.Fatalf("expected 1 call still, got %d", mock.originalWorkCalls.Load())
	}
	if res2.OriginalTitle != "Test Original" {
		t.Fatalf("unexpected res2: %+v", res2)
	}
}

type errAdvisor struct{}

func (e *errAdvisor) ExplainConflict(ctx context.Context, req ConflictExplanationRequest) (ConflictExplanationResponse, error) {
	return ConflictExplanationResponse{}, errors.New("upstream failure")
}

func (e *errAdvisor) ResolveOriginalWork(ctx context.Context, req OriginalWorkRequest) (OriginalWorkResponse, error) {
	return OriginalWorkResponse{}, errors.New("upstream failure")
}

func TestCachedAdvisorErrorsNotCached(t *testing.T) {
	mock := &errAdvisor{}
	store := &memoryStore{data: make(map[string]string)}
	cached := NewCachedAdvisor(mock, store, "test-model")

	_, err := cached.ExplainConflict(context.Background(), ConflictExplanationRequest{SourceTitle: "X"})
	if err == nil {
		t.Fatal("expected error")
	}
	if len(store.data) != 0 {
		t.Fatalf("expected nothing cached on error, got %v", store.data)
	}
}
