package httpserver

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/azusachino/drifter/internal/companion"
)

// snapshotSource serves a deterministic two-book shelf so snapshot HTTP
// behaviour can be asserted without any network provider.
type snapshotSource struct {
	now        time.Time
	shelfCalls atomic.Int32
}

func (s *snapshotSource) Shelf(context.Context) (companion.ShelfResult, error) {
	s.shelfCalls.Add(1)
	secret := 0
	return companion.ShelfResult{Complete: true, Books: []companion.ShelfBook{
		{ID: "123", Title: "可读的书", LastOpened: epoch(s.now.Unix()), Secret: &secret},
		{ID: "456", Title: "未读的书", LastOpened: epoch(s.now.Unix() - 10), Secret: &secret},
	}}, nil
}

func (*snapshotSource) Search(context.Context, string) (companion.SearchResult, error) {
	panic("unused")
}

func (*snapshotSource) Book(_ context.Context, id string) (companion.Book, error) {
	if id == "789" {
		return companion.Book{}, errors.New("synthetic metadata failure")
	}
	return companion.Book{ID: id, Title: "Enriched " + id, Author: "作者", Translator: "译者",
		ISBN: "9787532786831", Publisher: "上海译文出版社", ReaderURL: "https://weread.qq.com/web/bookDetail/" + id, TranslatorKnown: true}, nil
}

func (*snapshotSource) Progress(_ context.Context, id string) (companion.Progress, error) {
	if id == "123" {
		return companion.Progress{Percent: 100, Started: true, FinishedAt: 1700000000, ReadingTimeSeconds: 18240, LastReadAt: 1700000000, Status: "read"}, nil
	}
	return companion.Progress{Status: "unknown"}, nil
}

func epoch(value int64) *int64 { return &value }

func newSnapshotServer(t *testing.T, source companion.Source, db string, now time.Time, apiToken string) string {
	t.Helper()
	server, err := New(Options{Address: "127.0.0.1:0", Database: db, Source: source, Target: &targetStub{},
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil)), Now: func() time.Time { return now }, APIToken: apiToken})
	if err != nil {
		t.Fatal(err)
	}
	listener, err := server.Listen()
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	stopped := make(chan struct{})
	go func() {
		done <- server.Serve(ctx, listener)
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

// snapshotTempDir returns a private state directory regardless of the caller's
// umask, because the store rejects group- or world-writable parents.
func snapshotTempDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.Chmod(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	return dir
}

func snapshotDatabase(t *testing.T) string {
	t.Helper()
	path := filepath.Join(snapshotTempDir(t), "snapshot.sqlite")
	store, err := companion.OpenStore(path)
	if err != nil {
		t.Fatal(err)
	}
	mapping := companion.Mapping{Provider: "weread", SourceAccount: "default", BookID: "123",
		TargetAccount: "default", WorkID: 142089, EditionID: 312044, SourceTitle: "可读的书"}
	if err := store.Confirm(context.Background(), mapping, false); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestReadingSnapshotRequiresBearerToken(t *testing.T) {
	now := time.Date(2026, 11, 11, 10, 0, 0, 0, time.UTC)
	source := &snapshotSource{now: now}
	base := newSnapshotServer(t, source, filepath.Join(snapshotTempDir(t), "auth.sqlite"), now, "synthetic-api-token")
	for _, tc := range []struct {
		name   string
		header []string
	}{
		{"missing", nil},
		{"wrong", []string{"Bearer synthetic-api-token-wrong"}},
		{"wrong scheme", []string{"Basic synthetic-api-token"}},
		{"empty credential", []string{"Bearer "}},
		{"scheme only", []string{"Bearer"}},
		{"duplicated", []string{"Bearer synthetic-api-token", "Bearer synthetic-api-token"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req, err := http.NewRequest(http.MethodGet, base+"/api/v1/reading/snapshot", nil)
			if err != nil {
				t.Fatal(err)
			}
			for _, value := range tc.header {
				req.Header.Add("Authorization", value)
			}
			response, err := http.DefaultClient.Do(req)
			if err != nil {
				t.Fatal(err)
			}
			defer response.Body.Close()
			body, err := io.ReadAll(response.Body)
			if err != nil {
				t.Fatal(err)
			}
			if response.StatusCode != http.StatusUnauthorized || string(body) != "{\"error\":\"unauthorized\"}\n" {
				t.Fatalf("status=%d body=%q", response.StatusCode, body)
			}
		})
	}
	if source.shelfCalls.Load() != 0 {
		t.Fatal("an unauthorized request reached the provider")
	}

	unconfigured := &snapshotSource{now: now}
	unconfiguredBase := newSnapshotServer(t, unconfigured, filepath.Join(snapshotTempDir(t), "auth-none.sqlite"), now, "")
	response := request(t, unconfiguredBase, http.MethodGet, "/api/v1/reading/snapshot", map[string]string{"Authorization": "Bearer anything"}, nil)
	if response.StatusCode != http.StatusUnauthorized || unconfigured.shelfCalls.Load() != 0 {
		t.Fatalf("unconfigured token did not fail closed: status=%d", response.StatusCode)
	}
}

func TestReadingSnapshotBearerTokenFromEnvironment(t *testing.T) {
	t.Setenv("DRIFTER_API_TOKEN", "environment-api-token")
	now := time.Date(2026, 11, 11, 10, 0, 0, 0, time.UTC)
	source := &snapshotSource{now: now}
	base := newSnapshotServer(t, source, filepath.Join(snapshotTempDir(t), "env-token.sqlite"), now, "")
	response := request(t, base, http.MethodGet, "/api/v1/reading/snapshot", map[string]string{"Authorization": "Bearer environment-api-token"}, nil)
	if response.StatusCode != http.StatusOK {
		t.Fatalf("environment token rejected: status=%d", response.StatusCode)
	}
}

func TestReadingSnapshotReturnsPagedBooks(t *testing.T) {
	now := time.Date(2026, 11, 11, 10, 0, 0, 0, time.UTC)
	source := &snapshotSource{now: now}
	base := newSnapshotServer(t, source, snapshotDatabase(t), now, "synthetic-api-token")
	authorization := map[string]string{"Authorization": "Bearer synthetic-api-token"}

	response := request(t, base, http.MethodGet, "/api/v1/reading/snapshot", authorization, nil)
	if response.StatusCode != http.StatusOK {
		t.Fatalf("status=%d", response.StatusCode)
	}
	if contentType := response.Header.Get("Content-Type"); contentType != "application/json; charset=utf-8" {
		t.Fatalf("content type=%q", contentType)
	}
	var snapshot companion.ReadingSnapshot
	if err := json.NewDecoder(response.Body).Decode(&snapshot); err != nil {
		t.Fatal(err)
	}
	if snapshot.Version != companion.ReadingSnapshotVersion || !snapshot.GeneratedAt.Equal(now) || snapshot.SourceAccount != "default" {
		t.Fatalf("unexpected envelope: %+v", snapshot)
	}
	if len(snapshot.Books) != 2 || snapshot.HasMore || snapshot.Cursor != "" {
		t.Fatalf("unexpected page: %+v", snapshot)
	}
	completed := snapshot.Books[0]
	if completed.SourceBookID != "123" || completed.Status != "completed" || completed.ProgressPercent != 100 ||
		completed.ReadingTimeSeconds != 18240 || completed.FinishedAt == nil || completed.Translator != "译者" ||
		completed.Publisher != "上海译文出版社" || completed.ISBN != "9787532786831" || completed.StartedOn != nil {
		t.Fatalf("completed book=%+v", completed)
	}
	if completed.MatchedTarget == nil || completed.MatchedTarget.TargetProvider != "hardcover" ||
		completed.MatchedTarget.WorkID != 142089 || completed.MatchedTarget.EditionID != 312044 {
		t.Fatalf("matched target=%+v", completed.MatchedTarget)
	}
	planned := snapshot.Books[1]
	if planned.SourceBookID != "456" || planned.Status != "planned" || planned.MatchedTarget != nil || planned.LastReadAt != nil {
		t.Fatalf("planned book=%+v", planned)
	}
}

func TestReadingSnapshotQueryParameters(t *testing.T) {
	now := time.Date(2026, 11, 11, 10, 0, 0, 0, time.UTC)
	source := &snapshotSource{now: now}
	base := newSnapshotServer(t, source, snapshotDatabase(t), now, "synthetic-api-token")
	authorization := map[string]string{"Authorization": "Bearer synthetic-api-token"}

	first := request(t, base, http.MethodGet, "/api/v1/reading/snapshot?limit=1", authorization, nil)
	if first.StatusCode != http.StatusOK {
		t.Fatalf("status=%d", first.StatusCode)
	}
	var page companion.ReadingSnapshot
	if err := json.NewDecoder(first.Body).Decode(&page); err != nil {
		t.Fatal(err)
	}
	if len(page.Books) != 1 || page.Books[0].SourceBookID != "123" || !page.HasMore || page.Cursor == "" {
		t.Fatalf("first page=%+v", page)
	}

	second := request(t, base, http.MethodGet, "/api/v1/reading/snapshot?limit=1&cursor="+page.Cursor, authorization, nil)
	if second.StatusCode != http.StatusOK {
		t.Fatalf("status=%d", second.StatusCode)
	}
	var next companion.ReadingSnapshot
	if err := json.NewDecoder(second.Body).Decode(&next); err != nil {
		t.Fatal(err)
	}
	if len(next.Books) != 1 || next.Books[0].SourceBookID != "456" || next.HasMore || next.Cursor != "" {
		t.Fatalf("second page=%+v", next)
	}

	for _, tc := range []struct{ name, query string }{
		{"non-numeric limit", "?limit=lots"},
		{"unknown parameter", "?since=yesterday"},
		{"duplicate parameter", "?limit=1&limit=2"},
		{"invalid cursor", "?cursor=not-a-cursor"},
		{"invalid account", "?account=" + "%20space"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			response := request(t, base, http.MethodGet, "/api/v1/reading/snapshot"+tc.query, authorization, nil)
			if response.StatusCode != http.StatusBadRequest {
				t.Fatalf("query %q status=%d", tc.query, response.StatusCode)
			}
			var body map[string]string
			if err := json.NewDecoder(response.Body).Decode(&body); err != nil || body["error"] == "" {
				t.Fatalf("query %q body=%+v err=%v", tc.query, body, err)
			}
		})
	}

	// A page-size request outside the contract is clamped instead of failing.
	for _, tc := range []struct {
		name, query string
		want        int
	}{
		{"smallest", "?limit=0", 1},
		{"largest", fmt.Sprintf("?limit=%d", companion.MaxReadingSnapshotLimit+50), 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			response := request(t, base, http.MethodGet, "/api/v1/reading/snapshot"+tc.query, authorization, nil)
			if response.StatusCode != http.StatusOK {
				t.Fatalf("query %q status=%d", tc.query, response.StatusCode)
			}
			var page companion.ReadingSnapshot
			if err := json.NewDecoder(response.Body).Decode(&page); err != nil {
				t.Fatal(err)
			}
			if len(page.Books) != tc.want {
				t.Fatalf("query %q books=%d want=%d", tc.query, len(page.Books), tc.want)
			}
		})
	}
}

func TestReadingSnapshotRejectsUnsupportedMethods(t *testing.T) {
	now := time.Now()
	source := &snapshotSource{now: now}
	base := newSnapshotServer(t, source, filepath.Join(snapshotTempDir(t), "methods.sqlite"), now, "synthetic-api-token")
	response := request(t, base, http.MethodPost, "/api/v1/reading/snapshot", map[string]string{"Authorization": "Bearer synthetic-api-token"},
		http.NoBody)
	if response.StatusCode != http.StatusMethodNotAllowed || source.shelfCalls.Load() != 0 {
		t.Fatalf("status=%d", response.StatusCode)
	}
}
