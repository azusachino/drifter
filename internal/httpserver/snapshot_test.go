package httpserver

import (
	"context"
	"database/sql"
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
	// Bound identities let the versioned API report who it is actually reading
	// instead of echoing a local account label.
	boundAt := time.Date(2026, 11, 11, 9, 0, 0, 0, time.UTC)
	for _, binding := range []struct{ provider, account, fingerprint string }{
		{"weread", "default", "weread:23849102"},
		{"hardcover", "default", "hardcover:4821"},
	} {
		if _, err := store.VerifyOrBindAccountFingerprint(context.Background(), binding.provider, binding.account, binding.fingerprint, boundAt); err != nil {
			t.Fatal(err)
		}
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	return path
}

// snapshotSetMappingUpdatedAt rewrites only the stored link instant so delta
// selection can be asserted without waiting on the wall clock.
func snapshotSetMappingUpdatedAt(t *testing.T, path, bookID string, updatedAt time.Time) {
	t.Helper()
	db, err := sql.Open("sqlite3", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(`UPDATE mappings SET updated_at=? WHERE book_id=?`, updatedAt.UTC().Format(time.RFC3339), bookID); err != nil {
		t.Fatal(err)
	}
}

// snapshotInsertMapping adds a confirmed link without taking the server's
// ownership lease, so a mapping-only delta is observable over HTTP.
func snapshotInsertMapping(t *testing.T, path, bookID string, workID int64, updatedAt time.Time) {
	t.Helper()
	db, err := sql.Open("sqlite3", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	stamp := updatedAt.UTC().Format(time.RFC3339)
	if _, err := db.Exec(`INSERT INTO mappings (provider, source_account, book_id, target_account,
		work_id, edition_id, source_title, source_isbn, created_at, updated_at)
		VALUES ('weread', 'default', ?, 'default', ?, 0, '', '', ?, ?)
		ON CONFLICT(provider, source_account, book_id, target_account) DO UPDATE SET updated_at=excluded.updated_at`,
		bookID, workID, stamp, stamp); err != nil {
		t.Fatal(err)
	}
}

// changesSource serves a shelf with one book read inside the delta window and
// one read long before it, so delta filtering is observable over HTTP.
type changesSource struct {
	now        time.Time
	shelfCalls atomic.Int32
}

func (s *changesSource) Shelf(context.Context) (companion.ShelfResult, error) {
	s.shelfCalls.Add(1)
	secret := 0
	return companion.ShelfResult{Complete: true, Books: []companion.ShelfBook{
		{ID: "123", Title: "刚读过", LastOpened: epoch(s.now.Add(-time.Minute).Unix()), Secret: &secret},
		{ID: "456", Title: "很久以前读过", LastOpened: epoch(s.now.Add(-48 * time.Hour).Unix()), Secret: &secret},
	}}, nil
}

func (*changesSource) Search(context.Context, string) (companion.SearchResult, error) {
	panic("unused")
}

func (*changesSource) Book(_ context.Context, id string) (companion.Book, error) {
	return companion.Book{ID: id, Title: "Enriched " + id, Author: "作者"}, nil
}

func (*changesSource) Progress(_ context.Context, id string) (companion.Progress, error) {
	return companion.Progress{Percent: 42, Started: true, ReadingTimeSeconds: 600, LastReadAt: 1700000000, Status: "reading"}, nil
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
	if snapshot.Version != companion.ReadingSnapshotVersion || !snapshot.GeneratedAt.Equal(now) {
		t.Fatalf("unexpected envelope: %+v", snapshot)
	}
	if snapshot.SourceIdentity != "weread:23849102" || snapshot.TargetIdentity != "hardcover:4821" {
		t.Fatalf("identities=%q/%q", snapshot.SourceIdentity, snapshot.TargetIdentity)
	}
	if snapshot.Watermark != now.Format(time.RFC3339) {
		t.Fatalf("watermark=%q", snapshot.Watermark)
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

func TestReadingChangesRequiresBearerToken(t *testing.T) {
	now := time.Date(2026, 11, 11, 10, 0, 0, 0, time.UTC)
	source := &changesSource{now: now}
	base := newSnapshotServer(t, source, snapshotDatabase(t), now, "synthetic-api-token")
	for _, header := range []map[string]string{
		nil,
		{"Authorization": "Bearer wrong-token"},
		{"Authorization": "Basic synthetic-api-token"},
	} {
		response := request(t, base, http.MethodGet, "/api/v1/reading/changes?since="+now.Add(-time.Hour).Format(time.RFC3339), header, nil)
		if response.StatusCode != http.StatusUnauthorized {
			t.Fatalf("status=%d headers=%v", response.StatusCode, header)
		}
	}
	if source.shelfCalls.Load() != 0 {
		t.Fatal("an unauthorized request reached the provider")
	}
}

func TestReadingChangesReturnsDeltaAndWatermark(t *testing.T) {
	now := time.Date(2026, 11, 11, 10, 0, 0, 0, time.UTC)
	source := &changesSource{now: now}
	db := snapshotDatabase(t)
	snapshotSetMappingUpdatedAt(t, db, "123", now.Add(-30*time.Minute))
	base := newSnapshotServer(t, source, db, now, "synthetic-api-token")
	authorization := map[string]string{"Authorization": "Bearer synthetic-api-token"}
	since := now.Add(-time.Hour)

	response := request(t, base, http.MethodGet, "/api/v1/reading/changes?since="+since.Format(time.RFC3339), authorization, nil)
	if response.StatusCode != http.StatusOK {
		t.Fatalf("status=%d", response.StatusCode)
	}
	var changes companion.ReadingSnapshot
	if err := json.NewDecoder(response.Body).Decode(&changes); err != nil {
		t.Fatal(err)
	}
	if len(changes.Books) != 1 || changes.Books[0].SourceBookID != "123" || changes.Books[0].Status != "reading" {
		t.Fatalf("delta books=%+v", changes.Books)
	}
	if changes.SourceIdentity != "weread:23849102" || changes.TargetIdentity != "hardcover:4821" {
		t.Fatalf("identities=%q/%q", changes.SourceIdentity, changes.TargetIdentity)
	}
	if changes.Watermark != now.Format(time.RFC3339) || !changes.GeneratedAt.Equal(now) || changes.HasMore || changes.Cursor != "" {
		t.Fatalf("changes envelope=%+v", changes)
	}

	// A re-link is a change even when the source read nothing new, so the mapping
	// instant alone must select a book.
	snapshotSetMappingUpdatedAt(t, db, "123", since.Add(-time.Minute))
	snapshotInsertMapping(t, db, "456", 654321, since.Add(time.Minute))
	relinked := request(t, base, http.MethodGet, "/api/v1/reading/changes?since="+since.Format(time.RFC3339), authorization, nil)
	if relinked.StatusCode != http.StatusOK {
		t.Fatalf("status=%d", relinked.StatusCode)
	}
	var relinkedChanges companion.ReadingSnapshot
	if err := json.NewDecoder(relinked.Body).Decode(&relinkedChanges); err != nil {
		t.Fatal(err)
	}
	// "456" was last read 48h before the watermark, so only its re-link can
	// select it; "123" stays selected by its own read instant.
	if ids := snapshotIDs(relinkedChanges.Books); len(ids) != 2 || ids[0] != "123" || ids[1] != "456" {
		t.Fatalf("mapping-only delta=%v", ids)
	}
}

func TestReadingChangesRefusesOldWatermarkWithGone(t *testing.T) {
	now := time.Date(2026, 11, 11, 10, 0, 0, 0, time.UTC)
	source := &changesSource{now: now}
	base := newSnapshotServer(t, source, snapshotDatabase(t), now, "synthetic-api-token")
	stale := now.Add(-15 * 24 * time.Hour).Format(time.RFC3339)
	response := request(t, base, http.MethodGet, "/api/v1/reading/changes?since="+stale,
		map[string]string{"Authorization": "Bearer synthetic-api-token"}, nil)
	if response.StatusCode != http.StatusGone {
		t.Fatalf("status=%d", response.StatusCode)
	}
	var body struct {
		Error            string `json:"error"`
		Message          string `json:"message"`
		SnapshotRequired bool   `json:"snapshot_required"`
	}
	if err := json.NewDecoder(response.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if body.Error != "horizon_exceeded" || !body.SnapshotRequired || body.Message != "watermark is older than 14 days; full snapshot required" {
		t.Fatalf("gone body=%+v", body)
	}
	if source.shelfCalls.Load() != 0 {
		t.Fatal("a refused horizon reached the provider")
	}
}

func TestReadingChangesQueryParsing(t *testing.T) {
	now := time.Date(2026, 11, 11, 10, 0, 0, 0, time.UTC)
	source := &changesSource{now: now}
	base := newSnapshotServer(t, source, snapshotDatabase(t), now, "synthetic-api-token")
	authorization := map[string]string{"Authorization": "Bearer synthetic-api-token"}
	since := now.Add(-time.Hour)

	// A unix-second watermark is accepted alongside RFC3339.
	unix := request(t, base, http.MethodGet, fmt.Sprintf("/api/v1/reading/changes?since=%d", since.Unix()), authorization, nil)
	if unix.StatusCode != http.StatusOK {
		t.Fatalf("unix watermark status=%d", unix.StatusCode)
	}

	for _, tc := range []struct{ name, path string }{
		{"missing since", "/api/v1/reading/changes"},
		{"invalid since", "/api/v1/reading/changes?since=yesterday"},
		{"unknown parameter", "/api/v1/reading/changes?account=default&since=" + since.Format(time.RFC3339) + "&watermark=1"},
		{"duplicate parameter", "/api/v1/reading/changes?since=" + since.Format(time.RFC3339) + "&since=" + since.Format(time.RFC3339)},
		{"invalid cursor", "/api/v1/reading/changes?since=" + since.Format(time.RFC3339) + "&cursor=not-a-cursor"},
		{"invalid account", "/api/v1/reading/changes?since=" + since.Format(time.RFC3339) + "&account=%20space"},
		{"invalid limit", "/api/v1/reading/changes?since=" + since.Format(time.RFC3339) + "&limit=lots"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			response := request(t, base, http.MethodGet, tc.path, authorization, nil)
			if response.StatusCode != http.StatusBadRequest {
				t.Fatalf("path %q status=%d", tc.path, response.StatusCode)
			}
			var body map[string]string
			if err := json.NewDecoder(response.Body).Decode(&body); err != nil || body["error"] == "" {
				t.Fatalf("path %q body=%+v err=%v", tc.path, body, err)
			}
		})
	}

	post := request(t, base, http.MethodPost, "/api/v1/reading/changes?since="+since.Format(time.RFC3339), authorization, http.NoBody)
	if post.StatusCode != http.StatusMethodNotAllowed {
		t.Fatalf("post status=%d", post.StatusCode)
	}
}

// snapshotIDs lists exported source book IDs for order assertions.
func snapshotIDs(books []companion.ReadingSnapshotBook) []string {
	ids := make([]string, 0, len(books))
	for _, book := range books {
		ids = append(ids, book.SourceBookID)
	}
	return ids
}
