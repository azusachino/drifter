package httpserver

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/azusachino/drifter/internal/companion"
)

type jobsTargetStub struct {
	targetStub
}

func (j *jobsTargetStub) Match(context.Context, companion.Book, string) (companion.MatchReport, error) {
	return companion.MatchReport{RequiresConfirmation: true}, nil
}

func (j *jobsTargetStub) ValidateLink(context.Context, int64, int64) error {
	return nil
}

func TestJSONJobsAPI_CRUDAndCancellation(t *testing.T) {
	now := time.Now().UTC()
	source := &sourceStub{now: now}
	dbPath := filepath.Join(t.TempDir(), "jobs_api.sqlite")
	apiToken := "test-secret-token"
	server, err := New(Options{
		Address:  "127.0.0.1:0",
		Database: dbPath,
		Source:   source,
		Target:   &jobsTargetStub{},
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
		<-stopped
	})
	base := "http://" + listener.Addr().String()
	token := server.token

	bearerHeaders := map[string]string{
		"Authorization": "Bearer " + apiToken,
	}

	// 1. Unauthorized when missing or bad Bearer token
	res := request(t, base, http.MethodGet, "/api/v1/jobs", nil, nil)
	if res.StatusCode != http.StatusUnauthorized {
		t.Fatalf("expected 401 without bearer, got %d", res.StatusCode)
	}
	res = request(t, base, http.MethodPost, "/api/v1/jobs/sync", nil, nil)
	if res.StatusCode != http.StatusUnauthorized {
		t.Fatalf("expected 401 without bearer, got %d", res.StatusCode)
	}

	// 2. Enqueue via query parameters (empty body and no Content-Type header)
	syncURL := "/api/v1/jobs/sync?scope=recent&mode=match_and_reconcile"
	res = request(t, base, http.MethodPost, syncURL, bearerHeaders, nil)
	if res.StatusCode != http.StatusAccepted {
		b, _ := io.ReadAll(res.Body)
		t.Fatalf("expected 202 Accepted, got %d: %s", res.StatusCode, string(b))
	}
	var job1 companion.SyncJob
	if err := json.NewDecoder(res.Body).Decode(&job1); err != nil {
		t.Fatalf("failed to decode job: %v", err)
	}
	if job1.ID <= 0 || job1.Scope != "recent" || job1.Mode != "match_and_reconcile" || job1.Status != companion.SyncJobStatusQueued {
		t.Fatalf("unexpected job1: %+v", job1)
	}

	// 3. Coalescing: enqueuing duplicate while queued returns 200 OK with same job
	res = request(t, base, http.MethodPost, syncURL, bearerHeaders, nil)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 OK for coalesced job, got %d", res.StatusCode)
	}
	var coalescedJob companion.SyncJob
	if err := json.NewDecoder(res.Body).Decode(&coalescedJob); err != nil {
		t.Fatalf("failed to decode coalesced job: %v", err)
	}
	if coalescedJob.ID != job1.ID {
		t.Fatalf("expected coalesced job ID %d, got %d", job1.ID, coalescedJob.ID)
	}

	// 4. Enqueue via JSON body
	jsonHeaders := map[string]string{
		"Authorization": "Bearer " + apiToken,
		"Content-Type":  "application/json",
	}
	jobBody := `{"scope":"lifetime","mode":"match_only","finished_only":true,"force":true}`
	res = request(t, base, http.MethodPost, "/api/v1/jobs/sync", jsonHeaders, strings.NewReader(jobBody))
	if res.StatusCode != http.StatusAccepted {
		b, _ := io.ReadAll(res.Body)
		t.Fatalf("expected 202 Accepted, got %d: %s", res.StatusCode, string(b))
	}
	var job2 companion.SyncJob
	if err := json.NewDecoder(res.Body).Decode(&job2); err != nil {
		t.Fatalf("failed to decode job2: %v", err)
	}
	if job2.ID <= 0 || job2.Scope != "lifetime" || !job2.FinishedOnly || !job2.Force {
		t.Fatalf("unexpected job2: %+v", job2)
	}

	// 5. List jobs (limit 20)
	res = request(t, base, http.MethodGet, "/api/v1/jobs", bearerHeaders, nil)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 OK from GET /api/v1/jobs, got %d", res.StatusCode)
	}
	var jobs []companion.SyncJob
	if err := json.NewDecoder(res.Body).Decode(&jobs); err != nil {
		t.Fatalf("failed to decode jobs list: %v", err)
	}
	if len(jobs) != 2 {
		t.Fatalf("expected 2 jobs, got %d", len(jobs))
	}
	if jobs[0].ID != job2.ID || jobs[1].ID != job1.ID {
		t.Fatalf("expected jobs in ID DESC order: %+v", jobs)
	}

	// 6. Get single job
	getURL := fmt.Sprintf("/api/v1/jobs/%d", job2.ID)
	res = request(t, base, http.MethodGet, getURL, bearerHeaders, nil)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 OK from GET %s, got %d", getURL, res.StatusCode)
	}
	var fetchedJob companion.SyncJob
	if err := json.NewDecoder(res.Body).Decode(&fetchedJob); err != nil {
		t.Fatalf("failed to decode fetched job: %v", err)
	}
	if fetchedJob.ID != job2.ID {
		t.Fatalf("expected job ID %d, got %d", job2.ID, fetchedJob.ID)
	}

	// 7. Cancel job
	cancelURL := fmt.Sprintf("/api/v1/jobs/%d/cancel", job2.ID)
	res = request(t, base, http.MethodPost, cancelURL, bearerHeaders, nil)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 OK from POST %s, got %d", cancelURL, res.StatusCode)
	}
	var cancelledJob companion.SyncJob
	if err := json.NewDecoder(res.Body).Decode(&cancelledJob); err != nil {
		t.Fatalf("failed to decode cancelled job: %v", err)
	}
	if cancelledJob.Status != companion.SyncJobStatusCancelled || !cancelledJob.CancelRequested {
		t.Fatalf("expected cancelled job, got: %+v", cancelledJob)
	}

	// 8. GET /api/status surfaces latest_job
	res = request(t, base, http.MethodGet, "/api/status", nil, nil)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 OK from /api/status, got %d", res.StatusCode)
	}
	var status daemonStatusResponse
	if err := json.NewDecoder(res.Body).Decode(&status); err != nil {
		t.Fatalf("failed to decode status: %v", err)
	}
	if status.LatestJob == nil || status.LatestJob.ID != job2.ID {
		t.Fatalf("expected latest_job ID %d, got %+v", job2.ID, status.LatestJob)
	}

	// 9. Browser Form Routes: POST /jobs/sync and POST /jobs/cancel
	formHeaders := map[string]string{
		"Origin":       base,
		"Content-Type": "application/x-www-form-urlencoded",
	}

	// Form Sync Recent
	formVals := url.Values{
		"csrf_token": {token},
		"scope":      {"custom"},
		"days":       {"14"},
		"mode":       {"match_only"},
	}
	client := &http.Client{
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			return http.ErrUseLastResponse // don't follow redirect
		},
	}
	req, _ := http.NewRequest(http.MethodPost, base+"/jobs/sync", strings.NewReader(formVals.Encode()))
	for k, v := range formHeaders {
		req.Header.Set(k, v)
	}
	formRes, err := client.Do(req)
	if err != nil {
		t.Fatalf("form POST /jobs/sync failed: %v", err)
	}
	formRes.Body.Close()
	if formRes.StatusCode != http.StatusSeeOther {
		t.Fatalf("expected 303 See Other, got %d", formRes.StatusCode)
	}
	loc := formRes.Header.Get("Location")
	if loc != "/?notice=job_started" {
		t.Fatalf("expected Location /?notice=job_started, got %s", loc)
	}

	// Form Cancel
	cancelFormVals := url.Values{
		"csrf_token": {token},
		"job_id":     {fmt.Sprintf("%d", job1.ID)},
	}
	cancelReq, _ := http.NewRequest(http.MethodPost, base+"/jobs/cancel", strings.NewReader(cancelFormVals.Encode()))
	for k, v := range formHeaders {
		cancelReq.Header.Set(k, v)
	}
	cancelRes, err := client.Do(cancelReq)
	if err != nil {
		t.Fatalf("form POST /jobs/cancel failed: %v", err)
	}
	cancelRes.Body.Close()
	if cancelRes.StatusCode != http.StatusSeeOther {
		t.Fatalf("expected 303 See Other, got %d", cancelRes.StatusCode)
	}
	loc = cancelRes.Header.Get("Location")
	if loc != "/?notice=job_cancelled" {
		t.Fatalf("expected Location /?notice=job_cancelled, got %s", loc)
	}

	// Verify root page renders with notices without 400
	for _, notice := range []string{"job_started", "job_coalesced", "job_cancelled"} {
		rootRes := request(t, base, http.MethodGet, "/?notice="+notice, nil, nil)
		if rootRes.StatusCode != http.StatusOK {
			t.Fatalf("expected 200 OK for /?notice=%s, got %d", notice, rootRes.StatusCode)
		}
	}
}
