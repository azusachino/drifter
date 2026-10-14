package httpserver

import (
	"context"
	"encoding/json"
	"fmt"
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

type sourceStub struct {
	calls     int
	bookCalls atomic.Int32
	now       time.Time
}

func (s *sourceStub) Shelf(context.Context) (companion.ShelfResult, error) {
	s.calls++
	opened := s.now.Unix()
	secret := 0
	return companion.ShelfResult{Complete: true, Books: []companion.ShelfBook{{ID: "123", Title: "Shelf title", LastOpened: &opened, Secret: &secret}}}, nil
}
func (*sourceStub) Search(context.Context, string) (companion.SearchResult, error) { panic("unused") }
func (source *sourceStub) Book(_ context.Context, id string) (companion.Book, error) {
	source.bookCalls.Add(1)
	return companion.Book{ID: id, Title: "Core book", TranslatorKnown: true}, nil
}
func (*sourceStub) Progress(context.Context, string) (companion.Progress, error) { panic("unused") }

type targetStub struct {
	calls         atomic.Int32
	rejectEdition int64
}

func (*targetStub) Match(context.Context, companion.Book, string) (companion.MatchReport, error) {
	panic("unused")
}
func (target *targetStub) ValidateLink(_ context.Context, _, editionID int64) error {
	target.calls.Add(1)
	if editionID == target.rejectEdition && target.rejectEdition != 0 {
		return fmt.Errorf("synthetic target validation failure")
	}
	return nil
}

type parallelSource struct {
	now        time.Time
	shelfCalls atomic.Int32
}

func (s *parallelSource) Shelf(context.Context) (companion.ShelfResult, error) {
	s.shelfCalls.Add(1)
	opened, secret := s.now.Unix(), 0
	return companion.ShelfResult{Complete: true, Books: []companion.ShelfBook{{ID: "123", LastOpened: &opened, Secret: &secret}}}, nil
}
func (*parallelSource) Search(context.Context, string) (companion.SearchResult, error) {
	panic("unused")
}
func (*parallelSource) Book(_ context.Context, id string) (companion.Book, error) {
	return companion.Book{ID: id, Title: "Concurrent synthetic", TranslatorKnown: true}, nil
}
func (*parallelSource) Progress(context.Context, string) (companion.Progress, error) { panic("unused") }

type blockingSource struct {
	sourceStub
	started chan struct{}
	stopped chan struct{}
}

func (s *blockingSource) Book(ctx context.Context, _ string) (companion.Book, error) {
	close(s.started)
	<-ctx.Done()
	close(s.stopped)
	return companion.Book{}, ctx.Err()
}

func newTestServer(t *testing.T, source companion.Source, db string, now time.Time, targets ...companion.Target) (*Server, string, context.CancelFunc, <-chan error) {
	t.Helper()
	var target companion.Target = &targetStub{}
	if len(targets) != 0 {
		target = targets[0]
	}
	server, err := New(Options{Address: "127.0.0.1:0", Database: db, Source: source, Target: target,
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil)), Now: func() time.Time { return now }})
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
		cancel()
		select {
		case <-stopped:
		case <-time.After(time.Second):
			t.Error("server did not stop")
		}
	})
	return server, "http://" + listener.Addr().String(), cancel, done
}

func request(t *testing.T, base, method, path string, headers map[string]string, body io.Reader) *http.Response {
	t.Helper()
	req, err := http.NewRequest(method, base+path, body)
	if err != nil {
		t.Fatal(err)
	}
	for key, value := range headers {
		if strings.EqualFold(key, "Host") {
			req.Host = value
		} else {
			req.Header.Set(key, value)
		}
	}
	response, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { response.Body.Close() })
	return response
}

func TestRootRendersMappingInterface(t *testing.T) {
	now := time.Now()
	source := &sourceStub{now: now}
	_, base, _, _ := newTestServer(t, source, filepath.Join(t.TempDir(), "db.sqlite"), now)
	response := request(t, base, http.MethodGet, "/", nil, nil)
	body, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != http.StatusOK || !strings.Contains(string(body), "Drifter mapping interface") || !strings.Contains(string(body), "Confirm a mapping") {
		t.Fatalf("root did not render the mapping interface: status=%d", response.StatusCode)
	}
	if source.calls != 0 || source.bookCalls.Load() != 0 {
		t.Fatal("rendering the mapping interface contacted a provider")
	}
	legacy := request(t, base, http.MethodGet, "/admin", nil, nil)
	if legacy.StatusCode != http.StatusNotFound {
		t.Fatalf("legacy /admin path remained available: status=%d", legacy.StatusCode)
	}
}

func TestHostBoundaryWithHTTPTestServer(t *testing.T) {
	now := time.Now()
	source := &sourceStub{now: now}
	server, err := New(Options{Address: "127.0.0.1:8081", Database: filepath.Join(t.TempDir(), "db.sqlite"),
		Source: source, Target: &targetStub{}, Logger: slog.New(slog.NewTextHandler(io.Discard, nil)), Now: func() time.Time { return now }})
	if err != nil {
		t.Fatal(err)
	}
	owner, err := companion.AcquireDatabaseOwnership(server.database)
	if err != nil {
		t.Fatal(err)
	}
	defer owner.Close()
	server.ownership = owner
	httpServer := httptest.NewServer(hostAndBrowserBoundary("drifter.test:8081", server.http.Handler))
	defer httpServer.Close()
	request := func(host string) *http.Response {
		req, err := http.NewRequest(http.MethodGet, httpServer.URL+"/", nil)
		if err != nil {
			t.Fatal(err)
		}
		req.Host = host
		response, err := httpServer.Client().Do(req)
		if err != nil {
			t.Fatal(err)
		}
		return response
	}
	if response := request("attacker.test:8081"); response.StatusCode != http.StatusMisdirectedRequest {
		t.Fatal(response.StatusCode)
	} else {
		response.Body.Close()
	}
	if response := request("drifter.test:8081"); response.StatusCode != http.StatusOK {
		t.Fatal(response.StatusCode)
	} else {
		response.Body.Close()
	}
	if source.calls != 0 {
		t.Fatal("page render contacted provider")
	}
}

func TestMappingInterfaceReadsWithoutProviderCalls(t *testing.T) {
	now := time.Now().UTC()
	source := &sourceStub{now: now}
	db := filepath.Join(t.TempDir(), "state.sqlite")
	store, err := companion.OpenStore(db)
	if err != nil {
		t.Fatal(err)
	}
	mapping := companion.Mapping{Provider: "weread", SourceAccount: "src", BookID: "456", TargetAccount: "dst", WorkID: 7, SourceTitle: "Private title"}
	if err := store.Confirm(context.Background(), mapping, false); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	target := &targetStub{}
	_, base, _, _ := newTestServer(t, source, db, now, target)
	pageResponse := request(t, base, http.MethodGet, "/", nil, nil)
	page, _ := io.ReadAll(pageResponse.Body)
	if pageResponse.StatusCode != http.StatusOK || !strings.Contains(string(page), "Request pending books") || source.calls != 0 || source.bookCalls.Load() != 0 || target.calls.Load() != 0 {
		t.Fatalf("mapping interface status=%d provider calls=%d", pageResponse.StatusCode, source.calls)
	}
	if pageResponse.Header.Get("Cache-Control") != "no-store" || pageResponse.Header.Get("X-Frame-Options") != "DENY" || pageResponse.Header.Get("Referrer-Policy") != "strict-origin-when-cross-origin" || !strings.Contains(pageResponse.Header.Get("Content-Security-Policy"), "frame-ancestors 'none'") {
		t.Fatalf("mapping interface missing browser security headers: %v", pageResponse.Header)
	}
	mappings := request(t, base, http.MethodGet, "/api/mappings", nil, nil)
	var rows []companion.Mapping
	if err := json.NewDecoder(mappings.Body).Decode(&rows); err != nil {
		t.Fatal(err)
	}
	if mappings.StatusCode != http.StatusOK || len(rows) != 1 || rows[0].SourceTitle != "Private title" || source.bookCalls.Load() != 0 || target.calls.Load() != 0 {
		t.Fatalf("mapping endpoint: status=%d rows=%+v", mappings.StatusCode, rows)
	}
	query := url.Values{"account": {"src"}, "target_account": {"dst"}}
	pending := request(t, base, http.MethodGet, "/api/pending?"+query.Encode(), nil, nil)
	var report companion.PendingReport
	if err := json.NewDecoder(pending.Body).Decode(&report); err != nil {
		t.Fatal(err)
	}
	if pending.StatusCode != http.StatusOK || source.calls != 1 || source.bookCalls.Load() != 1 || target.calls.Load() != 0 || len(report.Books) != 1 || report.Books[0].Title != "Core book" {
		t.Fatalf("pending endpoint status=%d calls=%d report=%+v", pending.StatusCode, source.calls, report)
	}
}

func TestHTTPServerHostOriginFetchMetadataMethodsAndBodies(t *testing.T) {
	now := time.Now()
	source := &sourceStub{now: now}
	_, base, _, _ := newTestServer(t, source, filepath.Join(t.TempDir(), "db.sqlite"), now)
	for _, tc := range []struct {
		name, host, origin, site string
		method                   string
		body                     string
		status                   int
	}{
		{"wrong-host", "attacker.test", "", "", http.MethodGet, "", http.StatusMisdirectedRequest},
		{"cross-origin", "", "http://attacker.test", "", http.MethodGet, "", http.StatusForbidden},
		{"cross-site", "", "", "cross-site", http.MethodGet, "", http.StatusForbidden},
		{"wrong-method", "", "", "", http.MethodPost, "", http.StatusMethodNotAllowed},
		{"body", "", "", "", http.MethodGet, "unexpected", http.StatusBadRequest},
	} {
		t.Run(tc.name, func(t *testing.T) {
			headers := map[string]string{}
			if tc.host != "" {
				headers["Host"] = tc.host
			}
			if tc.origin != "" {
				headers["Origin"] = tc.origin
			}
			if tc.site != "" {
				headers["Sec-Fetch-Site"] = tc.site
			}
			response := request(t, base, tc.method, "/api/mappings", headers, strings.NewReader(tc.body))
			if response.StatusCode != tc.status {
				t.Fatalf("status %d; want %d", response.StatusCode, tc.status)
			}
		})
	}
	if source.calls != 0 {
		t.Fatal("security rejection caused provider calls")
	}
}

func TestPendingRouteSharesLazySourceClient(t *testing.T) {
	now := time.Now()
	source := &sourceStub{now: now}
	factoryCalls := 0
	server, err := New(Options{
		Address: "127.0.0.1:0", Database: filepath.Join(t.TempDir(), "db.sqlite"),
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil)), Now: func() time.Time { return now },
		SourceFactory: func() (companion.Source, error) { factoryCalls++; return source, nil },
		TargetFactory: func() (companion.Target, error) { return &targetStub{}, nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	listener, err := server.Listen()
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- server.Serve(ctx, listener) }()
	base := "http://" + listener.Addr().String()
	if response := request(t, base, http.MethodGet, "/", nil, nil); response.StatusCode != http.StatusOK {
		t.Fatal(response.StatusCode)
	}
	if response := request(t, base, http.MethodGet, "/api/mappings", nil, nil); response.StatusCode != http.StatusOK {
		t.Fatal(response.StatusCode)
	}
	if factoryCalls != 0 {
		t.Fatalf("provider factory called for read-local routes: %d", factoryCalls)
	}
	query := url.Values{"account": {"src"}, "target_account": {"dst"}}
	if response := request(t, base, http.MethodGet, "/api/pending?"+query.Encode(), nil, nil); response.StatusCode != http.StatusOK {
		t.Fatal(response.StatusCode)
	}
	if factoryCalls != 1 || source.calls != 1 {
		t.Fatalf("provider factory=%d shelf calls=%d", factoryCalls, source.calls)
	}
	if response := request(t, base, http.MethodGet, "/api/pending?"+query.Encode(), nil, nil); response.StatusCode != http.StatusOK {
		t.Fatal(response.StatusCode)
	}
	if factoryCalls != 1 || source.calls != 2 {
		t.Fatalf("source factory was not shared: factory=%d shelf calls=%d", factoryCalls, source.calls)
	}
	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func TestConcurrentPendingRequestsShareOneProviderClient(t *testing.T) {
	now := time.Now()
	db := filepath.Join(t.TempDir(), "db.sqlite")
	store, err := companion.OpenStore(db)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	source := &parallelSource{now: now}
	var factoryCalls atomic.Int32
	server, err := New(Options{Address: "127.0.0.1:0", Database: db, Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
		Now: func() time.Time { return now }, TargetFactory: func() (companion.Target, error) { return &targetStub{}, nil }, SourceFactory: func() (companion.Source, error) {
			factoryCalls.Add(1)
			return source, nil
		}})
	if err != nil {
		t.Fatal(err)
	}
	listener, err := server.Listen()
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- server.Serve(ctx, listener) }()
	base := "http://" + listener.Addr().String()
	query := url.Values{"account": {"src"}, "target_account": {"dst"}}
	const requestCount = 4
	results := make(chan error, requestCount)
	for i := 0; i < requestCount; i++ {
		go func() {
			response, err := http.Get(base + "/api/pending?" + query.Encode())
			if err != nil {
				results <- err
				return
			}
			response.Body.Close()
			if response.StatusCode != http.StatusOK {
				results <- fmt.Errorf("pending status %d", response.StatusCode)
				return
			}
			results <- nil
		}()
	}
	for i := 0; i < requestCount; i++ {
		if err := <-results; err != nil {
			t.Fatal(err)
		}
	}
	if factoryCalls.Load() != 1 || source.shelfCalls.Load() != requestCount {
		t.Fatalf("factory calls=%d shelf calls=%d", factoryCalls.Load(), source.shelfCalls.Load())
	}
	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func TestPendingRejectsInvalidParametersWithoutProviderCalls(t *testing.T) {
	now := time.Now()
	source := &sourceStub{now: now}
	_, base, _, _ := newTestServer(t, source, filepath.Join(t.TempDir(), "db.sqlite"), now)
	for _, path := range []string{"/api/pending", "/api/pending?account=a&target_account=b&days=0", "/api/pending?account=a&account=b&target_account=c", "/api/pending?account=a&target_account=b&include_private=true"} {
		response := request(t, base, http.MethodGet, path, nil, nil)
		if response.StatusCode != http.StatusBadRequest {
			t.Errorf("%s status=%d", path, response.StatusCode)
		}
	}
	if source.calls != 0 {
		t.Fatal("invalid query contacted provider")
	}
}

func TestHTTPServerStopsOnContextCancel(t *testing.T) {
	now := time.Now()
	_, _, cancel, done := newTestServer(t, &sourceStub{now: now}, filepath.Join(t.TempDir(), "db.sqlite"), now)
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("server did not stop after cancellation")
	}
}

func TestServeCancellationPropagatesIntoInFlightPendingLookup(t *testing.T) {
	now := time.Now()
	source := &blockingSource{sourceStub: sourceStub{now: now}, started: make(chan struct{}), stopped: make(chan struct{})}
	_, base, cancel, done := newTestServer(t, source, filepath.Join(t.TempDir(), "db.sqlite"), now)
	query := url.Values{"account": {"src"}, "target_account": {"dst"}}
	result := make(chan error, 1)
	go func() {
		response, err := http.Get(base + "/api/pending?" + query.Encode())
		if response != nil {
			response.Body.Close()
		}
		result <- err
	}()
	select {
	case <-source.started:
	case <-time.After(time.Second):
		t.Fatal("pending request did not reach blocking lookup")
	}
	cancel()
	select {
	case <-source.stopped:
	case <-time.After(time.Second):
		t.Fatal("server context cancellation did not cancel provider lookup")
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("server did not drain after request cancellation")
	}
	select {
	case <-result:
	case <-time.After(time.Second):
		t.Fatal("pending HTTP request did not finish")
	}
}

func TestPendingHeadIsRejectedWithoutProviderCalls(t *testing.T) {
	now := time.Now()
	source := &sourceStub{now: now}
	_, base, _, _ := newTestServer(t, source, filepath.Join(t.TempDir(), "db.sqlite"), now)
	query := url.Values{"account": {"src"}, "target_account": {"dst"}}
	response := request(t, base, http.MethodHead, "/api/pending?"+query.Encode(), nil, nil)
	if response.StatusCode != http.StatusMethodNotAllowed || source.calls != 0 {
		t.Fatalf("HEAD pending status=%d provider calls=%d", response.StatusCode, source.calls)
	}
}

func TestHTTPServerReadinessIsLocal(t *testing.T) {
	now := time.Now()
	source := &sourceStub{now: now}
	_, base, _, _ := newTestServer(t, source, filepath.Join(t.TempDir(), "db.sqlite"), now)
	response := request(t, base, http.MethodGet, "/readyz", nil, nil)
	if response.StatusCode != http.StatusOK || source.calls != 0 {
		t.Fatalf("ready=%d calls=%d", response.StatusCode, source.calls)
	}
}

func csrfToken(t *testing.T, base string) string {
	t.Helper()
	response := request(t, base, http.MethodGet, "/api/csrf", nil, nil)
	if response.StatusCode != http.StatusOK {
		t.Fatalf("CSRF token endpoint status=%d", response.StatusCode)
	}
	if response.Header.Get("Cache-Control") != "no-store" || response.Header.Get("X-Content-Type-Options") != "nosniff" {
		t.Fatalf("CSRF response missing security headers: %v", response.Header)
	}
	var result struct {
		Token string `json:"csrf_token"`
	}
	if err := json.NewDecoder(response.Body).Decode(&result); err != nil || result.Token == "" {
		t.Fatalf("decode CSRF token response: %v", err)
	}
	return result.Token
}

func mutationHeaders(base, token string) map[string]string {
	return map[string]string{"Origin": base, "Sec-Fetch-Site": "same-origin", "Content-Type": "application/json", "X-Drifter-CSRF": token}
}

func mappingBody(sourceAccount, bookID, targetAccount string, workID, editionID int64) string {
	return fmt.Sprintf(`{"source_account":%q,"book_id":%q,"target_account":%q,"work_id":%d,"edition_id":%d}`, sourceAccount, bookID, targetAccount, workID, editionID)
}

func listHTTPMappings(t *testing.T, base string) []companion.Mapping {
	t.Helper()
	response := request(t, base, http.MethodGet, "/api/mappings", nil, nil)
	if response.StatusCode != http.StatusOK {
		t.Fatalf("mapping list status=%d", response.StatusCode)
	}
	var mappings []companion.Mapping
	if err := json.NewDecoder(response.Body).Decode(&mappings); err != nil {
		t.Fatal(err)
	}
	return mappings
}

func TestMappingMutationHTTPUsesCoreValidationAndOwnedStore(t *testing.T) {
	now := time.Now()
	source := &sourceStub{now: now}
	target := &targetStub{rejectEdition: 999}
	server, base, _, _ := newTestServer(t, source, filepath.Join(t.TempDir(), "db.sqlite"), now, target)
	targetFactoryCalls := 0
	server.target = nil
	server.targetFactory = func() (companion.Target, error) { targetFactoryCalls++; return target, nil }
	token := csrfToken(t, base)

	post := func(path, body string) int {
		headers := mutationHeaders(base, token)
		return request(t, base, http.MethodPost, path, headers, strings.NewReader(body)).StatusCode
	}
	wrongEdition := request(t, base, http.MethodPost, "/api/mappings/confirm", mutationHeaders(base, token), strings.NewReader(mappingBody("src", "123", "dst", 7, 999)))
	wrongEditionBody, err := io.ReadAll(wrongEdition.Body)
	if err != nil || wrongEdition.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("wrong edition status=%d body read error=%v", wrongEdition.StatusCode, err)
	}
	if strings.Contains(string(wrongEditionBody), "Core book") || strings.Contains(string(wrongEditionBody), "synthetic target validation failure") {
		t.Fatalf("mutation error disclosed provider details: %s", wrongEditionBody)
	}
	if got := listHTTPMappings(t, base); len(got) != 0 {
		t.Fatalf("wrong edition persisted mapping: %+v", got)
	}
	if status := post("/api/mappings/confirm", mappingBody("src", "123", "dst", 7, 0)); status != http.StatusOK {
		t.Fatalf("work-only confirmation status=%d", status)
	}
	if status := post("/api/mappings/confirm", mappingBody("src", "123", "dst", 7, 0)); status != http.StatusOK {
		t.Fatalf("idempotent confirmation status=%d", status)
	}
	if status := post("/api/mappings/confirm", mappingBody("src", "123", "dst", 8, 0)); status != http.StatusConflict {
		t.Fatalf("conflicting confirmation status=%d", status)
	}
	if status := post("/api/mappings/replace", mappingBody("src", "123", "dst", 8, 11)); status != http.StatusOK {
		t.Fatalf("explicit replacement status=%d", status)
	}
	if status := post("/api/mappings/confirm", mappingBody("src", "456", "other", 9, 0)); status != http.StatusOK {
		t.Fatalf("second scoped confirmation status=%d", status)
	}
	unlink := `{"source_account":"src","book_id":"123","target_account":"dst"}`
	if response := request(t, base, http.MethodDelete, "/api/mappings/unlink", mutationHeaders(base, token), strings.NewReader(unlink)); response.StatusCode != http.StatusOK {
		t.Fatalf("unlink status=%d", response.StatusCode)
	}
	if response := request(t, base, http.MethodDelete, "/api/mappings/unlink", mutationHeaders(base, token), strings.NewReader(unlink)); response.StatusCode != http.StatusOK {
		t.Fatalf("idempotent unlink status=%d", response.StatusCode)
	}
	remaining := listHTTPMappings(t, base)
	if len(remaining) != 1 || remaining[0].TargetAccount != "other" || remaining[0].BookID != "456" {
		t.Fatalf("unlink escaped its mapping scope: %+v", remaining)
	}
	if source.bookCalls.Load() != 6 || target.calls.Load() != 6 || targetFactoryCalls != 1 {
		t.Fatalf("provider calls/factory: source=%d target=%d target factory=%d", source.bookCalls.Load(), target.calls.Load(), targetFactoryCalls)
	}
}

func TestNativeMappingFormsUseExplicitCoreActions(t *testing.T) {
	now := time.Now()
	source := &sourceStub{now: now}
	target := &targetStub{}
	_, base, _, _ := newTestServer(t, source, filepath.Join(t.TempDir(), "db.sqlite"), now, target)
	token := csrfToken(t, base)
	postForm := func(path string, fields url.Values) *http.Response {
		headers := map[string]string{"Origin": base, "Sec-Fetch-Site": "same-origin", "Content-Type": "application/x-www-form-urlencoded"}
		return request(t, base, http.MethodPost, path, headers, strings.NewReader(fields.Encode()))
	}
	selection := url.Values{"csrf_token": {token}, "source_account": {"src"}, "book_id": {"123"},
		"target_account": {"dst"}, "work_id": {"7"}, "edition_mode": {"edition"}, "edition_id": {"8"}}
	response := postForm("/mappings/confirm", selection)
	if response.StatusCode != http.StatusOK || !strings.Contains(response.Header.Get("Content-Type"), "text/html") {
		t.Fatalf("confirm form status=%d content type=%q", response.StatusCode, response.Header.Get("Content-Type"))
	}
	if mappings := listHTTPMappings(t, base); len(mappings) != 1 || mappings[0].WorkID != 7 || mappings[0].EditionID != 8 {
		t.Fatalf("form confirmation did not persist explicit edition: %+v", mappings)
	}
	selection.Set("work_id", "9")
	selection.Set("edition_id", "10")
	response = postForm("/mappings/replace", selection)
	if response.StatusCode != http.StatusOK || !strings.Contains(response.Header.Get("Content-Type"), "text/html") {
		t.Fatalf("replace form status=%d", response.StatusCode)
	}
	if mappings := listHTTPMappings(t, base); len(mappings) != 1 || mappings[0].WorkID != 9 || mappings[0].EditionID != 10 {
		t.Fatalf("form replacement did not persist: %+v", mappings)
	}
	unlink := url.Values{"csrf_token": {token}, "source_account": {"src"}, "book_id": {"123"}, "target_account": {"dst"}}
	response = postForm("/mappings/unlink", unlink)
	if response.StatusCode != http.StatusOK || len(listHTTPMappings(t, base)) != 0 {
		t.Fatalf("form unlink status=%d", response.StatusCode)
	}
	if source.bookCalls.Load() != 2 || target.calls.Load() != 2 {
		t.Fatalf("form provider calls source=%d target=%d", source.bookCalls.Load(), target.calls.Load())
	}
}

func TestMutationRejectionsPrecedeProviderCallsAndStateWrites(t *testing.T) {
	now := time.Now()
	source := &sourceStub{now: now}
	target := &targetStub{}
	db := filepath.Join(t.TempDir(), "db.sqlite")
	store, err := companion.OpenStore(db)
	if err != nil {
		t.Fatal(err)
	}
	seed := companion.Mapping{Provider: "weread", SourceAccount: "src", BookID: "321", TargetAccount: "dst", WorkID: 4}
	if err := store.Confirm(context.Background(), seed, false); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	_, base, _, _ := newTestServer(t, source, db, now, target)
	token := csrfToken(t, base)
	valid := mappingBody("src", "123", "dst", 7, 0)
	baseHeaders := mutationHeaders(base, token)
	cases := []struct {
		name, path, body string
		headers          map[string]string
		want             int
	}{
		{"missing origin", "/api/mappings/confirm", valid, map[string]string{"Content-Type": "application/json", "X-Drifter-CSRF": token}, http.StatusForbidden},
		{"cross origin", "/api/mappings/confirm", valid, map[string]string{"Origin": "http://attacker.invalid", "Content-Type": "application/json", "X-Drifter-CSRF": token}, http.StatusForbidden},
		{"cross-site fetch metadata", "/api/mappings/confirm", valid, map[string]string{"Origin": base, "Sec-Fetch-Site": "cross-site", "Content-Type": "application/json", "X-Drifter-CSRF": token}, http.StatusForbidden},
		{"bad token", "/api/mappings/confirm", valid, map[string]string{"Origin": base, "Content-Type": "application/json", "X-Drifter-CSRF": "wrong"}, http.StatusForbidden},
		{"wrong host", "/api/mappings/confirm", valid, map[string]string{"Host": "attacker.invalid", "Origin": base, "Content-Type": "application/json", "X-Drifter-CSRF": token}, http.StatusMisdirectedRequest},
		{"simple content type", "/api/mappings/confirm", valid, map[string]string{"Origin": base, "Content-Type": "text/plain", "X-Drifter-CSRF": token}, http.StatusUnsupportedMediaType},
		{"unknown field", "/api/mappings/confirm", strings.TrimSuffix(valid, "}") + `,"unexpected":true}`, baseHeaders, http.StatusBadRequest},
		{"duplicate field", "/api/mappings/confirm", `{"source_account":"src","source_account":"evil","book_id":"123","target_account":"dst","work_id":7,"edition_id":0}`, baseHeaders, http.StatusBadRequest},
		{"case-ambiguous duplicate", "/api/mappings/confirm", `{"source_account":"src","SOURCE_ACCOUNT":"evil","book_id":"123","target_account":"dst","work_id":7,"edition_id":0}`, baseHeaders, http.StatusBadRequest},
		{"trailing value", "/api/mappings/confirm", valid + ` {}`, baseHeaders, http.StatusBadRequest},
		{"invalid UTF-8", "/api/mappings/confirm", strings.Replace(valid, "src", "\xff", 1), baseHeaders, http.StatusBadRequest},
		{"missing edition choice", "/api/mappings/confirm", `{"source_account":"src","book_id":"123","target_account":"dst","work_id":7}`, baseHeaders, http.StatusBadRequest},
		{"GET cannot mutate", "/api/mappings/confirm", "", nil, http.StatusMethodNotAllowed},
		{"HEAD cannot mutate", "/api/mappings/confirm", "", nil, http.StatusMethodNotAllowed},
		{"oversized body", "/api/mappings/confirm", strings.Repeat("x", maxRequestBody+1), baseHeaders, http.StatusRequestEntityTooLarge},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			method := http.MethodPost
			if tc.name == "GET cannot mutate" {
				method = http.MethodGet
			} else if tc.name == "HEAD cannot mutate" {
				method = http.MethodHead
			}
			response := request(t, base, method, tc.path, tc.headers, strings.NewReader(tc.body))
			if response.StatusCode != tc.want {
				t.Fatalf("status=%d want=%d", response.StatusCode, tc.want)
			}
		})
	}
	if source.bookCalls.Load() != 0 || target.calls.Load() != 0 {
		t.Fatalf("rejected request called providers: source=%d target=%d", source.bookCalls.Load(), target.calls.Load())
	}
	mappings := listHTTPMappings(t, base)
	if len(mappings) != 1 || mappings[0].BookID != "321" {
		t.Fatalf("rejected requests changed state: %+v", mappings)
	}

	form := url.Values{"csrf_token": {token}, "source_account": {"src"}, "book_id": {"123"},
		"target_account": {"dst"}, "work_id": {"7"}, "edition_mode": {"work_only"}, "edition_id": {""}}
	formHeaders := map[string]string{"Origin": base, "Sec-Fetch-Site": "same-origin", "Content-Type": "application/x-www-form-urlencoded"}
	badFormCases := []struct {
		name    string
		headers map[string]string
		body    string
		want    int
	}{
		{"bad origin", map[string]string{"Origin": "http://evil.invalid", "Content-Type": "application/x-www-form-urlencoded"}, form.Encode(), http.StatusForbidden},
		{"bad token", formHeaders, strings.Replace(form.Encode(), url.QueryEscape(token), "wrong", 1), http.StatusForbidden},
		{"wrong form media type", map[string]string{"Origin": base, "Content-Type": "text/plain"}, form.Encode(), http.StatusUnsupportedMediaType},
		{"wrong form host", map[string]string{"Host": "attacker.invalid", "Origin": base, "Content-Type": "application/x-www-form-urlencoded"}, form.Encode(), http.StatusMisdirectedRequest},
		{"duplicate form key", formHeaders, form.Encode() + "&book_id=999", http.StatusBadRequest},
	}
	for _, tc := range badFormCases {
		t.Run("form "+tc.name, func(t *testing.T) {
			response := request(t, base, http.MethodPost, "/mappings/confirm", tc.headers, strings.NewReader(tc.body))
			if response.StatusCode != tc.want {
				t.Fatalf("status=%d want=%d", response.StatusCode, tc.want)
			}
		})
	}
	if source.bookCalls.Load() != 0 || target.calls.Load() != 0 || len(listHTTPMappings(t, base)) != 1 {
		t.Fatal("rejected form request contacted providers or changed mappings")
	}
}

var _ companion.Source = (*sourceStub)(nil)
