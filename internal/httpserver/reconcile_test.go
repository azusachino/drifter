package httpserver

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/azusachino/drifter/internal/advisor/opencode"
	"github.com/azusachino/drifter/internal/companion"
)

type reconcileMockSource struct {
	fingerprint string
}

func (s *reconcileMockSource) Shelf(context.Context) (companion.ShelfResult, error) {
	return companion.ShelfResult{Complete: true, Books: []companion.ShelfBook{}}, nil
}

func (s *reconcileMockSource) Search(context.Context, string) (companion.SearchResult, error) {
	return companion.SearchResult{}, nil
}

func (s *reconcileMockSource) Book(_ context.Context, id string) (companion.Book, error) {
	return companion.Book{ID: id, Title: "Test Book"}, nil
}

func (s *reconcileMockSource) Progress(_ context.Context, id string) (companion.Progress, error) {
	return companion.Progress{Status: "read", Percent: 100}, nil
}

func (s *reconcileMockSource) ResolveAccountFingerprint(_ context.Context, _ string) (string, error) {
	if s.fingerprint != "" {
		return s.fingerprint, nil
	}
	return "weread:synthetic-fp", nil
}

type reconcileMockTarget struct {
	fingerprint string
}

func (t *reconcileMockTarget) Match(context.Context, companion.Book, string) (companion.MatchReport, error) {
	return companion.MatchReport{}, nil
}

func (t *reconcileMockTarget) ValidateLink(context.Context, int64, int64) error {
	return nil
}

func (t *reconcileMockTarget) GetUserBookStatus(_ context.Context, _ int64) (*companion.UserBookStatus, error) {
	return &companion.UserBookStatus{StatusID: companion.HardcoverStatusRead}, nil
}

func (t *reconcileMockTarget) ResolveAccountFingerprint(context.Context) (string, error) {
	if t.fingerprint != "" {
		return t.fingerprint, nil
	}
	return "hardcover:synthetic-fp", nil
}

func (t *reconcileMockTarget) ApplyStatusMutation(_ context.Context, req companion.StatusMutationRequest) (companion.StatusMutationOutcome, error) {
	return companion.StatusMutationOutcome{
		CurrentStatus: req.StatusID,
		UserBookID:    12345,
	}, nil
}

func newReconcileTestServer(t *testing.T, source companion.Source, target companion.Target, db string, now time.Time, apiToken string) string {
	t.Helper()
	server, err := New(Options{
		Address:  "127.0.0.1:0",
		Database: db,
		Source:   source,
		Target:   target,
		Logger:   slog.New(slog.NewTextHandler(io.Discard, nil)),
		Now:      func() time.Time { return now },
		APIToken: apiToken,
	})
	if err != nil {
		t.Fatal(err)
	}
	listener, err := server.Listen()
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	stopped := make(chan struct{})
	go func() {
		_ = server.Serve(ctx, listener)
		close(stopped)
	}()
	t.Cleanup(func() {
		http.DefaultClient.CloseIdleConnections()
		cancel()
		select {
		case <-stopped:
		case <-time.After(time.Second):
			t.Error("server did not stop")
		}
	})
	return "http://" + listener.Addr().String()
}

func reconcileTestDB(t *testing.T) string {
	t.Helper()
	path := filepath.Join(snapshotTempDir(t), "reconcile.sqlite")
	store, err := companion.OpenStore(path)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	return path
}

func TestReconcileRequiresBearerAuth(t *testing.T) {
	now := time.Date(2026, 11, 16, 12, 0, 0, 0, time.UTC)
	db := reconcileTestDB(t)
	base := newReconcileTestServer(t, &reconcileMockSource{}, &reconcileMockTarget{}, db, now, "secret-token")

	// Missing auth header
	res := request(t, base, http.MethodPost, "/api/v1/reconcile", nil, http.NoBody)
	if res.StatusCode != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", res.StatusCode)
	}
	if res.Header.Get("WWW-Authenticate") != "Bearer" {
		t.Fatalf("expected WWW-Authenticate: Bearer header, got %q", res.Header.Get("WWW-Authenticate"))
	}

	// Wrong token
	res = request(t, base, http.MethodPost, "/api/v1/reconcile", map[string]string{"Authorization": "Bearer wrong-token"}, http.NoBody)
	if res.StatusCode != http.StatusUnauthorized {
		t.Fatalf("expected 401 for wrong token, got %d", res.StatusCode)
	}
}

func TestReconcileMethodAndValidation(t *testing.T) {
	now := time.Date(2026, 11, 16, 12, 0, 0, 0, time.UTC)
	db := reconcileTestDB(t)
	base := newReconcileTestServer(t, &reconcileMockSource{}, &reconcileMockTarget{}, db, now, "secret-token")
	auth := map[string]string{"Authorization": "Bearer secret-token"}

	// GET method rejected
	res := request(t, base, http.MethodGet, "/api/v1/reconcile", auth, nil)
	if res.StatusCode != http.StatusMethodNotAllowed {
		t.Fatalf("expected 405 for GET, got %d", res.StatusCode)
	}

	// Conflicting adopt flags
	res = request(t, base, http.MethodPost, "/api/v1/reconcile?adopt_source=true&adopt_target=true", auth, http.NoBody)
	if res.StatusCode != http.StatusBadRequest {
		t.Fatalf("expected 400 for conflicting adopt flags, got %d", res.StatusCode)
	}

	// Invalid label (leading space)
	res = request(t, base, http.MethodPost, "/api/v1/reconcile?account=%20bad", auth, http.NoBody)
	if res.StatusCode != http.StatusBadRequest {
		t.Fatalf("expected 400 for invalid label, got %d", res.StatusCode)
	}
}

func TestReconcileSuccessEmptyMappings(t *testing.T) {
	now := time.Date(2026, 11, 16, 12, 0, 0, 0, time.UTC)
	db := reconcileTestDB(t)
	base := newReconcileTestServer(t, &reconcileMockSource{}, &reconcileMockTarget{}, db, now, "secret-token")
	auth := map[string]string{"Authorization": "Bearer secret-token"}

	// Test both /api/v1/reconcile and /api/reconcile
	for _, route := range []string{"/api/v1/reconcile", "/api/reconcile"} {
		res := request(t, base, http.MethodPost, route, auth, http.NoBody)
		if res.StatusCode != http.StatusOK {
			t.Fatalf("%s expected 200, got %d", route, res.StatusCode)
		}
		var summary companion.ReconcileSummary
		if err := json.NewDecoder(res.Body).Decode(&summary); err != nil {
			t.Fatalf("decode summary err: %v", err)
		}
		if summary.ErrorCount != 0 {
			t.Fatalf("expected 0 errors, got %d", summary.ErrorCount)
		}
	}
}

func TestAutoMatchEndpoint(t *testing.T) {
	now := time.Date(2026, 11, 16, 12, 0, 0, 0, time.UTC)
	db := reconcileTestDB(t)
	base := newReconcileTestServer(t, &reconcileMockSource{}, &reconcileMockTarget{}, db, now, "secret-token")
	auth := map[string]string{"Authorization": "Bearer secret-token"}

	// Unauthorized without bearer
	res := request(t, base, http.MethodPost, "/api/v1/auto-match", nil, http.NoBody)
	if res.StatusCode != http.StatusUnauthorized {
		t.Fatalf("expected 401 without bearer, got %d", res.StatusCode)
	}

	// Method not allowed
	res = request(t, base, http.MethodGet, "/api/v1/auto-match", auth, http.NoBody)
	if res.StatusCode != http.StatusMethodNotAllowed {
		t.Fatalf("expected 405 for GET, got %d", res.StatusCode)
	}

	// Invalid min_confidence
	res = request(t, base, http.MethodPost, "/api/v1/auto-match?min_confidence=1.5", auth, http.NoBody)
	if res.StatusCode != http.StatusBadRequest {
		t.Fatalf("expected 400 for min_confidence > 1, got %d", res.StatusCode)
	}

	// Successful run with dry_run=true
	res = request(t, base, http.MethodPost, "/api/v1/auto-match?dry_run=true", auth, http.NoBody)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", res.StatusCode)
	}
	var report companion.AutoMatchReport
	if err := json.NewDecoder(res.Body).Decode(&report); err != nil {
		t.Fatalf("decode report error: %v", err)
	}
}

func TestBearerAuthorizedMappingConfirm(t *testing.T) {
	now := time.Date(2026, 11, 16, 12, 0, 0, 0, time.UTC)
	db := reconcileTestDB(t)
	base := newReconcileTestServer(t, &reconcileMockSource{}, &reconcileMockTarget{}, db, now, "secret-token")
	auth := map[string]string{
		"Authorization": "Bearer secret-token",
		"Content-Type":  "application/json",
	}

	body := []byte(`{"source_account":"default","book_id":"3300189649","target_account":"default","work_id":1011275,"edition_id":0}`)
	res := request(t, base, http.MethodPost, "/api/v1/mappings/confirm", auth, bytes.NewReader(body))
	if res.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 for bearer authorized mapping confirm, got %d", res.StatusCode)
	}
}

type advisorTestSource struct {
	book companion.ShelfBook
}

func (s *advisorTestSource) Shelf(context.Context) (companion.ShelfResult, error) {
	return companion.ShelfResult{Complete: true, Books: []companion.ShelfBook{s.book}}, nil
}

func (s *advisorTestSource) Search(context.Context, string) (companion.SearchResult, error) {
	return companion.SearchResult{}, nil
}

func (s *advisorTestSource) Book(_ context.Context, id string) (companion.Book, error) {
	return companion.Book{ID: id, Title: s.book.Title, Author: s.book.Author, TranslatorKnown: true}, nil
}

func (s *advisorTestSource) Progress(context.Context, string) (companion.Progress, error) {
	return companion.Progress{Status: "reading", Percent: 20}, nil
}

func (s *advisorTestSource) ResolveAccountFingerprint(context.Context, string) (string, error) {
	return "weread:synthetic-fp", nil
}

type advisorTestTarget struct {
	reconcileMockTarget
}

func (t *advisorTestTarget) Match(_ context.Context, _ companion.Book, query string) (companion.MatchReport, error) {
	if query == "The Three-Body Problem" {
		return companion.MatchReport{
			Candidates: []companion.Candidate{{
				ID:    777001,
				Title: "The Three-Body Problem",
			}},
		}, nil
	}
	return companion.MatchReport{}, nil
}

func TestServerAdvisorOptionsHonoredByAutoMatchAndReconcile(t *testing.T) {
	for _, key := range []string{
		"DRIFTER_ADVISOR_URL",
		"MAGPIE_BASE_URL",
		"MAGPIE_URL",
		"OPENCODE_BASE_URL",
		"DRIFTER_ADVISOR_MODELS",
		"DRIFTER_ADVISOR_MODEL",
		"DRIFTER_ADVISOR_TIMEOUT",
		"MAGPIE_GATEWAY_KEY",
		"OPENCODE_API_KEY",
	} {
		t.Setenv(key, "")
	}
	t.Setenv("DRIFTER_ADVISOR_KEY", "test-advisor-key")

	var mu sync.Mutex
	var seenModels []string
	advSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Model string `json:"model"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		mu.Lock()
		seenModels = append(seenModels, body.Model)
		mu.Unlock()

		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"id":      "chatcmpl-test",
			"object":  "chat.completion",
			"created": 1700000000,
			"model":   body.Model,
			"choices": []map[string]any{{
				"index":         0,
				"finish_reason": "stop",
				"message": map[string]any{
					"role":    "assistant",
					"content": `{"original_title":"The Three-Body Problem","original_author":"Cixin Liu","confidence":0.95,"abstained":false,"rationale":"exact"}`,
				},
			}},
		})
	}))
	defer advSrv.Close()

	now := time.Date(2026, 11, 16, 12, 0, 0, 0, time.UTC)
	lastOpened := now.Add(-24 * time.Hour).Unix()
	secret := 0
	src := &advisorTestSource{
		book: companion.ShelfBook{
			ID:         "3300999001",
			Title:      "三体",
			Author:     "刘慈欣",
			LastOpened: &lastOpened,
			Secret:     &secret,
		},
	}

	startServer := func(db string) string {
		server, err := New(Options{
			Address:  "127.0.0.1:0",
			Database: db,
			Source:   src,
			Target:   &advisorTestTarget{},
			Logger:   slog.New(slog.NewTextHandler(io.Discard, nil)),
			Now:      func() time.Time { return now },
			APIToken: "secret-token",
			Advisor: &AdvisorOptions{
				Settings: opencode.Settings{
					BaseURL: advSrv.URL,
					Model:   "custom-settings-model",
				},
				CacheTTL: time.Hour,
			},
		})
		if err != nil {
			t.Fatal(err)
		}
		listener, err := server.Listen()
		if err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithCancel(context.Background())
		stopped := make(chan struct{})
		go func() {
			_ = server.Serve(ctx, listener)
			close(stopped)
		}()
		t.Cleanup(func() {
			http.DefaultClient.CloseIdleConnections()
			cancel()
			select {
			case <-stopped:
			case <-time.After(time.Second):
				t.Error("server did not stop")
			}
		})
		return "http://" + listener.Addr().String()
	}

	auth := map[string]string{"Authorization": "Bearer secret-token"}

	base1 := startServer(reconcileTestDB(t))
	res := request(t, base1, http.MethodPost, "/api/v1/auto-match?dry_run=true", auth, http.NoBody)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("auto-match expected 200, got %d", res.StatusCode)
	}
	var report companion.AutoMatchReport
	if err := json.NewDecoder(res.Body).Decode(&report); err != nil {
		t.Fatalf("decode auto-match report: %v", err)
	}
	if len(report.Matches) != 1 || report.Matches[0].MatchedWorkID != 777001 {
		t.Fatalf("expected advisor-matched work 777001, got %+v", report.Matches)
	}

	base2 := startServer(reconcileTestDB(t))
	res2 := request(t, base2, http.MethodPost, "/api/v1/reconcile?auto_match=true&dry_run=true", auth, http.NoBody)
	if res2.StatusCode != http.StatusOK {
		t.Fatalf("reconcile expected 200, got %d", res2.StatusCode)
	}

	mu.Lock()
	defer mu.Unlock()
	if len(seenModels) != 3 {
		t.Fatalf("expected 3 advisor HTTP calls (auto-match + reconcile auto-match + reconcile conflict), got %d (%v)", len(seenModels), seenModels)
	}
	for i, m := range seenModels {
		if m != "custom-settings-model" {
			t.Fatalf("call %d expected model custom-settings-model, got %q", i, m)
		}
	}
}
