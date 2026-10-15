package httpserver

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/azusachino/drifter/internal/companion"
)

type budgetSource struct {
	now     time.Time
	started chan string
	release chan struct{}
	books   atomic.Int32
	calls   atomic.Int32
}

func (s *budgetSource) Shelf(context.Context) (companion.ShelfResult, error) {
	s.calls.Add(1)
	opened, secret := s.now.Unix(), 0
	return companion.ShelfResult{Complete: true, Books: []companion.ShelfBook{{ID: "one", LastOpened: &opened, Secret: &secret}, {ID: "two", LastOpened: &opened, Secret: &secret}}}, nil
}
func (s *budgetSource) Book(ctx context.Context, id string) (companion.Book, error) {
	s.calls.Add(1)
	n := s.books.Add(1)
	if n == 2 {
		s.started <- id
		select {
		case <-s.release:
		case <-ctx.Done():
		}
		return companion.Book{}, ctx.Err()
	}
	return companion.Book{ID: id, Title: "known", TranslatorKnown: true}, nil
}
func (*budgetSource) Search(context.Context, string) (companion.SearchResult, error) { panic("unused") }
func (*budgetSource) Progress(context.Context, string) (companion.Progress, error)   { panic("unused") }

type heldSource struct {
	*sourceStub
	started chan struct{}
	release chan struct{}
	calls   atomic.Int32
}

func (s *heldSource) Book(ctx context.Context, id string) (companion.Book, error) {
	if s.calls.Add(1) == 1 {
		close(s.started)
		select {
		case <-s.release:
		case <-ctx.Done():
			return companion.Book{}, ctx.Err()
		}
	}
	return companion.Book{ID: id, Title: "held", TranslatorKnown: true}, nil
}

func newBudgetServer(t *testing.T, source companion.Source, timeout time.Duration) (*Server, string) {
	t.Helper()
	s, base, _, _ := newTestServer(t, source, filepath.Join(t.TempDir(), "db.sqlite"), time.Now())
	s.operationTimeout = timeout
	return s, base
}

func TestPendingTimeoutPreservesCompletedRowsAndReturns504(t *testing.T) {
	now := time.Now()
	src := &budgetSource{now: now, started: make(chan string, 1), release: make(chan struct{})}
	_, base := newBudgetServer(t, src, 35*time.Millisecond)
	res := request(t, base, http.MethodGet, "/api/pending?account=src&target_account=dst", nil, nil)
	defer res.Body.Close()
	var report companion.PendingReport
	if err := json.NewDecoder(res.Body).Decode(&report); err != nil {
		t.Fatal(err)
	}
	if res.StatusCode != http.StatusGatewayTimeout || report.Complete || len(report.Books) != 1 || report.Books[0].ID != "one" || len(report.Issues) != 2 || report.Issues[0].Code != "metadata_lookup_failed" || report.Issues[0].BookID != "two" || report.Issues[1].Code != "operation_timeout" || report.NextCursor == "" {
		t.Fatalf("timeout status=%d report=%+v", res.StatusCode, report)
	}
}

func TestBusySlotLeavesReadRoutesAndValidationAvailableThenReuses(t *testing.T) {
	now := time.Now()
	src := &heldSource{sourceStub: &sourceStub{now: now}, started: make(chan struct{}), release: make(chan struct{})}
	s, base := newBudgetServer(t, src, time.Second)
	var sourceFactories, targetFactories atomic.Int32
	s.source = nil
	s.sourceFactory = func() (companion.Source, error) { sourceFactories.Add(1); return src, nil }
	s.target = nil
	s.targetFactory = func() (companion.Target, error) { targetFactories.Add(1); return &targetStub{}, nil }
	query := "/api/pending?account=src&target_account=dst"
	first := make(chan *http.Response, 1)
	go func() { first <- request(t, base, http.MethodGet, query, nil, nil) }()
	select {
	case <-src.started:
	case <-time.After(time.Second):
		t.Fatal("provider did not start")
	}
	busy := request(t, base, http.MethodGet, query, nil, nil)
	if busy.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("busy status=%d", busy.StatusCode)
	}
	body, _ := io.ReadAll(busy.Body)
	busy.Body.Close()
	if strings.Contains(string(body), "private") || src.calls.Load() != 1 || sourceFactories.Load() != 1 || targetFactories.Load() != 0 {
		t.Fatalf("busy response=%q provider=%d factories=%d/%d", body, src.calls.Load(), sourceFactories.Load(), targetFactories.Load())
	}
	for _, path := range []string{"/", "/api/mappings", "/healthz", "/readyz"} {
		res := request(t, base, http.MethodGet, path, nil, nil)
		if res.StatusCode != http.StatusOK {
			t.Fatalf("local route %s=%d", path, res.StatusCode)
		}
		res.Body.Close()
	}
	bad := request(t, base, http.MethodPost, "/api/mappings/confirm", map[string]string{"Origin": "http://attacker.invalid", "Content-Type": "application/json"}, strings.NewReader(mappingBody("src", "123", "dst", 7, 0)))
	if bad.StatusCode != http.StatusForbidden {
		t.Fatalf("cross-origin mutation while busy=%d", bad.StatusCode)
	}
	token := csrfToken(t, base)
	good := mutationHeaders(base, token)
	bodyJSON := mappingBody("src", "123", "dst", 7, 0)
	busyMutation := request(t, base, http.MethodPost, "/api/mappings/confirm", good, strings.NewReader(bodyJSON))
	if busyMutation.StatusCode != http.StatusServiceUnavailable || src.calls.Load() != 1 || sourceFactories.Load() != 1 || targetFactories.Load() != 0 {
		t.Fatalf("valid busy mutation status=%d provider=%d factories=%d/%d", busyMutation.StatusCode, src.calls.Load(), sourceFactories.Load(), targetFactories.Load())
	}
	invalidToken := mutationHeaders(base, "wrong")
	invalid := request(t, base, http.MethodPost, "/api/mappings/confirm", invalidToken, strings.NewReader(bodyJSON))
	if invalid.StatusCode != http.StatusForbidden || src.calls.Load() != 1 {
		t.Fatalf("invalid-token mutation status=%d provider calls=%d", invalid.StatusCode, src.calls.Load())
	}
	malformed := request(t, base, http.MethodPost, "/api/mappings/confirm", good, strings.NewReader(`{"unexpected":true}`))
	if malformed.StatusCode != http.StatusBadRequest || src.calls.Load() != 1 {
		t.Fatalf("malformed mutation status=%d provider calls=%d", malformed.StatusCode, src.calls.Load())
	}
	close(src.release)
	response := (<-first)
	if response.StatusCode != http.StatusOK {
		t.Fatalf("first=%d", response.StatusCode)
	}
	response.Body.Close()
	response = request(t, base, http.MethodGet, query, nil, nil)
	if response.StatusCode != http.StatusOK || sourceFactories.Load() != 1 || src.calls.Load() != 2 {
		t.Fatalf("reused request status=%d factories=%d provider calls=%d", response.StatusCode, sourceFactories.Load(), src.calls.Load())
	}
	response.Body.Close()
}

func TestCanceledPendingReturnsSafeEmptyArrays(t *testing.T) {
	now := time.Now()
	src := &sourceStub{now: now}
	// This test invokes the handler directly, bypassing Serve's initialization
	// before Accept. Initialize ownership synchronously instead of racing Serve.
	db := filepath.Join(t.TempDir(), "db.sqlite")
	s, err := New(Options{Address: "127.0.0.1:8081", Database: db, Source: src, Target: &targetStub{}, operationTimeout: time.Second, Logger: slog.New(slog.NewTextHandler(io.Discard, nil)), Now: func() time.Time { return now }})
	if err != nil {
		t.Fatal(err)
	}
	owner, err := companion.AcquireDatabaseOwnership(db)
	if err != nil {
		t.Fatal(err)
	}
	defer owner.Close()
	s.ownership = owner
	store, err := companion.OpenOwnedStore(db, owner)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	req := httptest.NewRequest(http.MethodGet, "/api/pending?account=src&target_account=dst", nil).WithContext(ctx)
	req.Host = "127.0.0.1:8081"
	response := httptest.NewRecorder()
	s.handlePending(response, req)
	var report companion.PendingReport
	if err := json.Unmarshal(response.Body.Bytes(), &report); err != nil {
		t.Fatal(err)
	}
	if response.Code != http.StatusGatewayTimeout || report.Complete || report.Books == nil || report.Issues == nil {
		t.Fatalf("canceled response status=%d body=%s report=%+v", response.Code, response.Body.String(), report)
	}
}

func TestMutationDeadlineIsConsistentForJSONAndNativeForm(t *testing.T) {
	for _, native := range []bool{false, true} {
		t.Run(map[bool]string{false: "json", true: "form"}[native], func(t *testing.T) {
			now := time.Now()
			src := &heldSource{sourceStub: &sourceStub{now: now}, started: make(chan struct{}), release: make(chan struct{})}
			s, base := newBudgetServer(t, src, 25*time.Millisecond)
			_ = s
			path := "/api/mappings/confirm"
			body := mappingBody("src", "123", "dst", 7, 0)
			headers := mutationHeaders(base, csrfToken(t, base))
			if native {
				path = "/mappings/confirm"
				values := url.Values{"csrf_token": {csrfToken(t, base)}, "source_account": {"src"}, "book_id": {"123"}, "target_account": {"dst"}, "work_id": {"7"}, "edition_mode": {"work_only"}, "edition_id": {""}}
				body = values.Encode()
				headers = map[string]string{"Origin": base, "Sec-Fetch-Site": "same-origin", "Content-Type": "application/x-www-form-urlencoded"}
			}
			res := request(t, base, http.MethodPost, path, headers, strings.NewReader(body))
			defer res.Body.Close()
			if native {
				b, _ := io.ReadAll(res.Body)
				if res.StatusCode != http.StatusGatewayTimeout || strings.Contains(string(b), "confirmed") {
					t.Fatalf("form timeout status=%d body=%s", res.StatusCode, b)
				}
			} else {
				var e map[string]string
				_ = json.NewDecoder(res.Body).Decode(&e)
				if res.StatusCode != http.StatusGatewayTimeout || e["error"] != "provider_operation_timeout" {
					t.Fatalf("JSON timeout %d %+v", res.StatusCode, e)
				}
			}
			close(src.release)
			if rows := listHTTPMappings(t, base); len(rows) != 0 {
				t.Fatalf("timeout created mapping: %+v", rows)
			}
		})
	}
}
