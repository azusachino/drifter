package httpserver

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/azusachino/drifter/internal/advisor"
	"github.com/azusachino/drifter/internal/companion"
)

type workerTestSource struct {
	mu          sync.Mutex
	shelf       companion.ShelfResult
	books       map[string]companion.Book
	progress    map[string]companion.Progress
	shelfCalls  atomic.Int32
	bookCalls   atomic.Int32
	bookHook    func(ctx context.Context, id string) error
	fingerprint string
}

func (s *workerTestSource) Shelf(context.Context) (companion.ShelfResult, error) {
	s.shelfCalls.Add(1)
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.shelf, nil
}

func (s *workerTestSource) Search(context.Context, string) (companion.SearchResult, error) {
	return companion.SearchResult{}, nil
}

func (s *workerTestSource) Book(ctx context.Context, id string) (companion.Book, error) {
	s.bookCalls.Add(1)
	if s.bookHook != nil {
		if err := s.bookHook(ctx, id); err != nil {
			return companion.Book{}, err
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if b, ok := s.books[id]; ok {
		return b, nil
	}
	return companion.Book{ID: id, Title: "Book " + id, TranslatorKnown: true}, nil
}

func (s *workerTestSource) Progress(_ context.Context, id string) (companion.Progress, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if p, ok := s.progress[id]; ok {
		return p, nil
	}
	return companion.Progress{Status: "read", Percent: 100}, nil
}

func (s *workerTestSource) ResolveAccountFingerprint(_ context.Context, _ string) (string, error) {
	if s.fingerprint != "" {
		return s.fingerprint, nil
	}
	return "weread:worker-test-fp", nil
}

type workerTestTarget struct {
	mu           sync.Mutex
	matches      map[string]companion.MatchReport
	queryMatches map[string]companion.MatchReport
	statuses     map[int64]*companion.UserBookStatus
	matchCalls   atomic.Int32
	applyCalls   atomic.Int32
	fingerprint  string
}

func (t *workerTestTarget) Match(_ context.Context, book companion.Book, query string) (companion.MatchReport, error) {
	t.matchCalls.Add(1)
	t.mu.Lock()
	defer t.mu.Unlock()
	if query != "" && t.queryMatches != nil {
		if r, ok := t.queryMatches[query]; ok {
			return r, nil
		}
	}
	if r, ok := t.matches[book.ID]; ok {
		return r, nil
	}
	return companion.MatchReport{}, nil
}

func (t *workerTestTarget) ValidateLink(context.Context, int64, int64) error {
	return nil
}

func (t *workerTestTarget) GetUserBookStatus(_ context.Context, workID int64) (*companion.UserBookStatus, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.statuses != nil {
		return t.statuses[workID], nil
	}
	return nil, nil
}

func (t *workerTestTarget) ResolveAccountFingerprint(context.Context) (string, error) {
	if t.fingerprint != "" {
		return t.fingerprint, nil
	}
	return "hardcover:worker-test-fp", nil
}

func (t *workerTestTarget) ApplyStatusMutation(_ context.Context, req companion.StatusMutationRequest) (companion.StatusMutationOutcome, error) {
	t.applyCalls.Add(1)
	t.mu.Lock()
	if t.statuses == nil {
		t.statuses = make(map[int64]*companion.UserBookStatus)
	}
	t.statuses[req.WorkID] = &companion.UserBookStatus{
		ID:       9000 + req.WorkID,
		BookID:   req.WorkID,
		StatusID: req.StatusID,
	}
	t.mu.Unlock()
	return companion.StatusMutationOutcome{
		CurrentStatus: req.StatusID,
		UserBookID:    9000 + req.WorkID,
	}, nil
}

func isolateWorkerAdvisorEnv(t *testing.T) {
	t.Helper()
	t.Setenv("MAGPIE_GATEWAY_KEY", "")
	t.Setenv("OPENCODE_API_KEY", "")
	t.Setenv("DRIFTER_ADVISOR_KEY", "")
	t.Setenv("DRIFTER_ADVISOR_MODELS", "")
	t.Setenv("DRIFTER_ADVISOR_MODEL", "")
}

func waitForJobTerminal(t *testing.T, dbPath string, owner *companion.DatabaseOwnership, jobID int64, timeout time.Duration) *companion.SyncJob {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		store, err := companion.OpenOwnedStore(dbPath, owner)
		if err == nil {
			job, err := store.GetSyncJob(context.Background(), jobID)
			_ = store.Close()
			if err == nil && job != nil {
				switch job.Status {
				case companion.SyncJobStatusCompleted,
					companion.SyncJobStatusCompletedWithErrors,
					companion.SyncJobStatusCancelled,
					companion.SyncJobStatusFailed:
					return job
				}
			}
		}
		time.Sleep(15 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for job %d to reach terminal status", jobID)
	return nil
}

func TestJobWorkerStartupRecoversStaleRunningJobs(t *testing.T) {
	now := time.Date(2026, 11, 16, 12, 0, 0, 0, time.UTC)
	db := filepath.Join(t.TempDir(), "worker-recover.sqlite")

	store, err := companion.OpenStore(db)
	if err != nil {
		t.Fatal(err)
	}
	enqueued, err := store.EnqueueSyncJob(context.Background(), companion.SyncJob{
		Scope:     companion.SyncJobScopeRecent,
		Mode:      companion.SyncJobModeMatchOnly,
		CreatedAt: now.Add(-10 * time.Minute),
	})
	if err != nil {
		t.Fatal(err)
	}
	claimed, err := store.ClaimNextSyncJob(context.Background(), now.Add(-9*time.Minute))
	if err != nil || claimed == nil || claimed.ID != enqueued.ID {
		t.Fatalf("claim failed: %+v, err=%v", claimed, err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	src := &workerTestSource{}
	tgt := &workerTestTarget{}
	srv, base, cancel, done := newTestServer(t, src, db, now, tgt)
	if resp := request(t, base, http.MethodGet, "/readyz", nil, nil); resp.StatusCode != http.StatusOK {
		t.Fatalf("readyz status=%d", resp.StatusCode)
	}

	checkStore, err := companion.OpenOwnedStore(db, srv.ownership)
	if err != nil {
		t.Fatal(err)
	}
	recovered, err := checkStore.GetSyncJob(context.Background(), enqueued.ID)
	_ = checkStore.Close()
	if err != nil {
		t.Fatal(err)
	}
	if recovered == nil || recovered.Status != companion.SyncJobStatusFailed || recovered.ErrorMessage != "interrupted by server restart" {
		t.Fatalf("expected stale running job to be marked failed with restart reason, got %+v", recovered)
	}

	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func TestJobWorkerPhase1BatchesYieldsSlotAndPhase2Reconciles(t *testing.T) {
	isolateWorkerAdvisorEnv(t)
	now := time.Date(2026, 11, 16, 12, 0, 0, 0, time.UTC)
	db := filepath.Join(t.TempDir(), "worker-phases.sqlite")

	secret := 0
	finished := 1
	opened := now.Add(-24 * time.Hour).Unix()

	// Create 7 books (> 1 batch of 5) so batching & checkpointing run across 2 batches.
	var shelfBooks []companion.ShelfBook
	booksMap := make(map[string]companion.Book)
	matchesMap := make(map[string]companion.MatchReport)

	for i := 1; i <= 7; i++ {
		id := fmt.Sprintf("100%d", i)
		shelfBooks = append(shelfBooks, companion.ShelfBook{
			ID:            id,
			Title:         fmt.Sprintf("Title %d", i),
			Author:        "Author",
			LastOpened:    &opened,
			FinishReading: &finished,
			Secret:        &secret,
		})
		booksMap[id] = companion.Book{
			ID:              id,
			Title:           fmt.Sprintf("Title %d", i),
			Author:          "Author",
			ISBN:            fmt.Sprintf("978000000000%d", i),
			TranslatorKnown: true,
		}
		if i <= 6 {
			// Exact match for first 6 books
			matchesMap[id] = companion.MatchReport{
				ExactEditions: []companion.Edition{{
					ID:     int64(200 + i),
					WorkID: int64(300 + i),
					Title:  fmt.Sprintf("Title %d", i),
				}},
			}
		} else {
			// 7th book has no match -> abstained
			matchesMap[id] = companion.MatchReport{}
		}
	}

	// Pause after book 5 (end of batch 1) while batch 2 has not acquired slot yet,
	// or test that interactive admit() can interleave when we hold operationSlot between batches.
	batch1Done := make(chan struct{})
	allowBatch2 := make(chan struct{})

	src := &workerTestSource{
		shelf: companion.ShelfResult{Complete: true, Books: shelfBooks},
		books: booksMap,
	}
	tgt := &workerTestTarget{
		matches: matchesMap,
	}

	srv, err := New(Options{
		Address:  "127.0.0.1:0",
		Database: db,
		Source:   src,
		Target:   tgt,
		Logger:   slog.New(slog.NewTextHandler(io.Discard, nil)),
		Now:      func() time.Time { return now },
	})
	if err != nil {
		t.Fatal(err)
	}

	// Hook book "1005" (5th processed book in batch 1) to signal when batch 1 is finishing,
	// then acquire s.operationSlot from outside right as batch 1 releases it to prove yielding!
	src.bookHook = func(_ context.Context, id string) error {
		if id == "1005" {
			close(batch1Done)
			<-allowBatch2
		}
		return nil
	}

	listener, err := srv.Listen()
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- srv.Serve(ctx, listener) }()

	// Wait until srv.ownership is ready by polling /readyz
	base := "http://" + listener.Addr().String()
	for i := 0; i < 50; i++ {
		resp, err := http.Get(base + "/readyz")
		if err == nil {
			resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				break
			}
		}
		time.Sleep(10 * time.Millisecond)
	}

	store, err := companion.OpenOwnedStore(db, srv.ownership)
	if err != nil {
		t.Fatal(err)
	}
	job, err := store.EnqueueSyncJob(context.Background(), companion.SyncJob{
		Scope:         companion.SyncJobScopeRecent,
		Mode:          companion.SyncJobModeMatchAndReconcile,
		Days:          7,
		MinConfidence: 0.80,
		CreatedAt:     now,
	})
	_ = store.Close()
	if err != nil {
		t.Fatal(err)
	}

	srv.notifyJobWorker()

	select {
	case <-batch1Done:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for batch 1 to reach book 1005")
	}

	// Start a goroutine waiting to acquire operationSlot as soon as batch 1 releases it,
	// then unblock book 1005 so batch 1 finishes, checkpoints, and releases operationSlot.
	slotInterleaved := make(chan struct{})
	go func() {
		srv.operationSlot <- struct{}{}
		// Verify checkpoint after batch 1 while we hold the slot between batch 1 and batch 2!
		st, err := companion.OpenOwnedStore(db, srv.ownership)
		if err == nil {
			j, _ := st.GetSyncJob(context.Background(), job.ID)
			_ = st.Close()
			if j != nil && j.ProcessedCount == 5 && j.TotalEstimated == 7 {
				close(slotInterleaved)
			}
		}
		<-srv.operationSlot
	}()

	close(allowBatch2)

	select {
	case <-slotInterleaved:
	case <-time.After(2 * time.Second):
		t.Fatal("interactive slot acquisition did not interleave with 5-book checkpoint after batch 1")
	}

	finishedJob := waitForJobTerminal(t, db, srv.ownership, job.ID, 3*time.Second)
	if finishedJob.Status != companion.SyncJobStatusCompleted {
		t.Fatalf("expected job completed, got status=%s err=%q", finishedJob.Status, finishedJob.ErrorMessage)
	}
	if finishedJob.TotalEstimated != 7 || finishedJob.ProcessedCount != 7 || finishedJob.ConfirmedCount != 6 || finishedJob.SkippedCount != 1 || finishedJob.ErrorCount != 0 {
		t.Fatalf("unexpected job counters: %+v", finishedJob)
	}
	if finishedJob.SyncRunID == nil || *finishedJob.SyncRunID <= 0 {
		t.Fatalf("expected valid sync_run_id from Phase 2 reconcile, got %v", finishedJob.SyncRunID)
	}
	if src.shelfCalls.Load() != 1 {
		t.Fatalf("expected Shelf() to be called once (cached across phases), got %d", src.shelfCalls.Load())
	}
	if tgt.applyCalls.Load() != 6 {
		t.Fatalf("expected 6 status mutations applied in Phase 2, got %d", tgt.applyCalls.Load())
	}

	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func TestJobWorkerDistinguishesCancelRequestedFromServerShutdown(t *testing.T) {
	isolateWorkerAdvisorEnv(t)
	now := time.Date(2026, 11, 16, 12, 0, 0, 0, time.UTC)
	db := filepath.Join(t.TempDir(), "worker-cancel.sqlite")

	secret := 0
	opened := now.Add(-1 * time.Hour).Unix()
	shelf := companion.ShelfResult{
		Complete: true,
		Books: []companion.ShelfBook{
			{ID: "2001", Title: "Book 1", LastOpened: &opened, Secret: &secret},
			{ID: "2002", Title: "Book 2", LastOpened: &opened, Secret: &secret},
		},
	}

	bookStarted := make(chan struct{}, 2)
	src := &workerTestSource{
		shelf: shelf,
		bookHook: func(ctx context.Context, _ string) error {
			bookStarted <- struct{}{}
			<-ctx.Done()
			return ctx.Err()
		},
	}
	tgt := &workerTestTarget{}

	srv, err := New(Options{
		Address:  "127.0.0.1:0",
		Database: db,
		Source:   src,
		Target:   tgt,
		Logger:   slog.New(slog.NewTextHandler(io.Discard, nil)),
		Now:      func() time.Time { return now },
	})
	if err != nil {
		t.Fatal(err)
	}

	listener, err := srv.Listen()
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- srv.Serve(ctx, listener) }()

	base := "http://" + listener.Addr().String()
	for i := 0; i < 50; i++ {
		resp, err := http.Get(base + "/readyz")
		if err == nil {
			resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				break
			}
		}
		time.Sleep(10 * time.Millisecond)
	}

	// 1. Enqueue job 1 and cancel via RequestCancelSyncJob + cancelActiveJob
	store, err := companion.OpenOwnedStore(db, srv.ownership)
	if err != nil {
		t.Fatal(err)
	}
	job1, err := store.EnqueueSyncJob(context.Background(), companion.SyncJob{
		Scope:     companion.SyncJobScopeRecent,
		Mode:      companion.SyncJobModeMatchOnly,
		CreatedAt: now,
	})
	_ = store.Close()
	if err != nil {
		t.Fatal(err)
	}
	srv.notifyJobWorker()

	select {
	case <-bookStarted:
	case <-time.After(2 * time.Second):
		t.Fatal("job 1 did not start book lookup")
	}

	store, err = companion.OpenOwnedStore(db, srv.ownership)
	if err != nil {
		t.Fatal(err)
	}
	_, active, err := store.RequestCancelSyncJob(context.Background(), job1.ID, now)
	_ = store.Close()
	if err != nil || !active {
		t.Fatalf("RequestCancelSyncJob failed: active=%v err=%v", active, err)
	}
	if !srv.cancelActiveJob(job1.ID) {
		t.Fatal("expected cancelActiveJob to return true for running job 1")
	}

	finishedJob1 := waitForJobTerminal(t, db, srv.ownership, job1.ID, 2*time.Second)
	if finishedJob1.Status != companion.SyncJobStatusCancelled {
		t.Fatalf("expected job 1 status 'cancelled', got %q", finishedJob1.Status)
	}

	// 2. Enqueue job 2 and trigger server shutdown (cancel(ctx)) without RequestCancelSyncJob
	store, err = companion.OpenOwnedStore(db, srv.ownership)
	if err != nil {
		t.Fatal(err)
	}
	job2, err := store.EnqueueSyncJob(context.Background(), companion.SyncJob{
		Scope:     companion.SyncJobScopeRecent,
		Mode:      companion.SyncJobModeMatchOnly,
		Force:     true,
		CreatedAt: now,
	})
	_ = store.Close()
	if err != nil {
		t.Fatal(err)
	}
	srv.notifyJobWorker()

	select {
	case <-bookStarted:
	case <-time.After(2 * time.Second):
		t.Fatal("job 2 did not start book lookup")
	}

	// Cancel server context (graceful shutdown) -> stopWorker() runs before owner.Close()!
	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}

	// Inspect database after server has completely shut down and released ownership
	postShutdownStore, err := companion.OpenStore(db)
	if err != nil {
		t.Fatal(err)
	}
	defer postShutdownStore.Close()
	finishedJob2, err := postShutdownStore.GetSyncJob(context.Background(), job2.ID)
	if err != nil {
		t.Fatal(err)
	}
	if finishedJob2 == nil || finishedJob2.Status != companion.SyncJobStatusFailed || finishedJob2.ErrorMessage != "interrupted by server restart" {
		t.Fatalf("expected job 2 interrupted by server shutdown to be marked failed with restart reason before owner.Close(), got %+v", finishedJob2)
	}
}

func TestJobWorkerForceInvalidatesCachedShelfAndReconcileOnlyMode(t *testing.T) {
	isolateWorkerAdvisorEnv(t)
	now := time.Date(2026, 11, 16, 12, 0, 0, 0, time.UTC)
	db := filepath.Join(t.TempDir(), "worker-force-modes.sqlite")

	secret := 0
	opened := now.Add(-2 * time.Hour).Unix()
	src := &workerTestSource{
		shelf: companion.ShelfResult{
			Complete: true,
			Books: []companion.ShelfBook{
				{ID: "3001", Title: "Book 3001", LastOpened: &opened, Secret: &secret},
			},
		},
	}
	tgt := &workerTestTarget{}

	srv, base, cancel, done := newTestServer(t, src, db, now, tgt)
	if resp := request(t, base, http.MethodGet, "/readyz", nil, nil); resp.StatusCode != http.StatusOK {
		t.Fatalf("readyz status=%d", resp.StatusCode)
	}

	// Job 1: match_only, Force=false -> populates shelf cache (shelfCalls == 1)
	store, err := companion.OpenOwnedStore(db, srv.ownership)
	if err != nil {
		t.Fatal(err)
	}
	job1, err := store.EnqueueSyncJob(context.Background(), companion.SyncJob{
		Scope:     companion.SyncJobScopeRecent,
		Mode:      companion.SyncJobModeMatchOnly,
		DryRun:    true,
		Force:     false,
		CreatedAt: now,
	})
	_ = store.Close()
	if err != nil {
		t.Fatal(err)
	}
	srv.notifyJobWorker()
	res1 := waitForJobTerminal(t, db, srv.ownership, job1.ID, 2*time.Second)
	if res1.Status != companion.SyncJobStatusCompleted || src.shelfCalls.Load() != 1 {
		t.Fatalf("job 1 unexpected status=%s shelfCalls=%d", res1.Status, src.shelfCalls.Load())
	}

	// Job 2: match_only, Force=false -> reuses cached shelf (shelfCalls stays 1)
	store, err = companion.OpenOwnedStore(db, srv.ownership)
	if err != nil {
		t.Fatal(err)
	}
	job2, err := store.EnqueueSyncJob(context.Background(), companion.SyncJob{
		Scope:     companion.SyncJobScopeRecent,
		Mode:      companion.SyncJobModeMatchOnly,
		DryRun:    true,
		Force:     false,
		CreatedAt: now,
	})
	_ = store.Close()
	if err != nil {
		t.Fatal(err)
	}
	srv.notifyJobWorker()
	res2 := waitForJobTerminal(t, db, srv.ownership, job2.ID, 2*time.Second)
	if res2.Status != companion.SyncJobStatusCompleted || src.shelfCalls.Load() != 1 {
		t.Fatalf("expected Force=false to reuse shelf cache (shelfCalls=1), got status=%s shelfCalls=%d", res2.Status, src.shelfCalls.Load())
	}

	// Job 3: match_only, Force=true -> invalidates shelf cache (shelfCalls increments to 2)
	store, err = companion.OpenOwnedStore(db, srv.ownership)
	if err != nil {
		t.Fatal(err)
	}
	job3, err := store.EnqueueSyncJob(context.Background(), companion.SyncJob{
		Scope:     companion.SyncJobScopeRecent,
		Mode:      companion.SyncJobModeMatchOnly,
		DryRun:    true,
		Force:     true,
		CreatedAt: now,
	})
	_ = store.Close()
	if err != nil {
		t.Fatal(err)
	}
	srv.notifyJobWorker()
	res3 := waitForJobTerminal(t, db, srv.ownership, job3.ID, 2*time.Second)
	if res3.Status != companion.SyncJobStatusCompleted || src.shelfCalls.Load() != 2 {
		t.Fatalf("expected Force=true to invalidate shelf cache (shelfCalls=2), got status=%s shelfCalls=%d", res3.Status, src.shelfCalls.Load())
	}

	// Job 4: reconcile_only -> skips Phase 1 completely (shelfCalls stays 2)
	store, err = companion.OpenOwnedStore(db, srv.ownership)
	if err != nil {
		t.Fatal(err)
	}
	job4, err := store.EnqueueSyncJob(context.Background(), companion.SyncJob{
		Scope:     companion.SyncJobScopeRecent,
		Mode:      companion.SyncJobModeReconcileOnly,
		DryRun:    false,
		CreatedAt: now,
	})
	_ = store.Close()
	if err != nil {
		t.Fatal(err)
	}
	srv.notifyJobWorker()
	res4 := waitForJobTerminal(t, db, srv.ownership, job4.ID, 2*time.Second)
	if res4.Status != companion.SyncJobStatusCompleted || src.shelfCalls.Load() != 2 || res4.SyncRunID == nil {
		t.Fatalf("reconcile_only unexpected result: %+v shelfCalls=%d", res4, src.shelfCalls.Load())
	}

	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

type workerTestAdvisor struct {
	resolveFunc func(ctx context.Context, req advisor.OriginalWorkRequest) (advisor.OriginalWorkResponse, error)
}

func (a *workerTestAdvisor) ExplainConflict(context.Context, advisor.ConflictExplanationRequest) (advisor.ConflictExplanationResponse, error) {
	return advisor.ConflictExplanationResponse{RecommendedAction: advisor.ActionKeepSeparate}, nil
}

func (a *workerTestAdvisor) ResolveOriginalWork(ctx context.Context, req advisor.OriginalWorkRequest) (advisor.OriginalWorkResponse, error) {
	if a.resolveFunc != nil {
		return a.resolveFunc(ctx, req)
	}
	return advisor.OriginalWorkResponse{Abstained: true}, nil
}

func TestJobWorkerRateLimitCooldownReleasesSlotAndRetries(t *testing.T) {
	now := time.Date(2026, 11, 16, 12, 0, 0, 0, time.UTC)
	db := filepath.Join(t.TempDir(), "worker-ratelimit-retry.sqlite")

	secret := 0
	finished := 1
	opened := now.Add(-1 * time.Hour).Unix()

	src := &workerTestSource{
		shelf: companion.ShelfResult{
			Complete: true,
			Books: []companion.ShelfBook{
				{
					ID:            "4001",
					Title:         "克拉拉与太阳",
					Author:        "石黑一雄",
					LastOpened:    &opened,
					FinishReading: &finished,
					Secret:        &secret,
				},
			},
		},
		books: map[string]companion.Book{
			"4001": {
				ID:              "4001",
				Title:           "克拉拉与太阳",
				Author:          "石黑一雄",
				Translator:      "宋佥",
				TranslatorKnown: true,
			},
		},
	}

	tgt := &workerTestTarget{
		queryMatches: map[string]companion.MatchReport{
			"Klara and the Sun": {
				Candidates: []companion.Candidate{{
					ID:    5001,
					Title: "Klara and the Sun",
				}},
			},
		},
	}

	var advisorAttempts atomic.Int32
	rateLimitedCh := make(chan struct{})
	slotVerifiedDuringCooldown := make(chan struct{})

	adv := &workerTestAdvisor{
		resolveFunc: func(_ context.Context, _ advisor.OriginalWorkRequest) (advisor.OriginalWorkResponse, error) {
			attempt := advisorAttempts.Add(1)
			if attempt == 1 {
				close(rateLimitedCh)
				return advisor.OriginalWorkResponse{}, &advisor.RateLimitError{
					RetryAfter: 120 * time.Millisecond,
					Detail:     "HTTP 429 rate limited",
				}
			}
			<-slotVerifiedDuringCooldown
			return advisor.OriginalWorkResponse{
				OriginalTitle:  "Klara and the Sun",
				OriginalAuthor: "Kazuo Ishiguro",
				Confidence:     0.95,
				Abstained:      false,
			}, nil
		},
	}

	srv, base, cancel, done := newTestServer(t, src, db, now, tgt)
	srv.setJobWorkerConfig(jobWorkerConfig{
		cooldownBase:      20 * time.Millisecond,
		cooldownMax:       200 * time.Millisecond,
		maxRateLimitRetry: 3,
		advisorPaceDelay:  -1,
		advisor:           adv,
	})
	if resp := request(t, base, http.MethodGet, "/readyz", nil, nil); resp.StatusCode != http.StatusOK {
		t.Fatalf("readyz status=%d", resp.StatusCode)
	}

	store, err := companion.OpenOwnedStore(db, srv.ownership)
	if err != nil {
		t.Fatal(err)
	}
	job, err := store.EnqueueSyncJob(context.Background(), companion.SyncJob{
		Scope:         companion.SyncJobScopeRecent,
		Mode:          companion.SyncJobModeMatchOnly,
		MinConfidence: 0.80,
		CreatedAt:     now,
	})
	_ = store.Close()
	if err != nil {
		t.Fatal(err)
	}
	srv.notifyJobWorker()

	select {
	case <-rateLimitedCh:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for first advisor call to hit 429")
	}

	// Verify s.operationSlot is released while the worker is waiting in cooldown!
	select {
	case srv.operationSlot <- struct{}{}:
		<-srv.operationSlot
		close(slotVerifiedDuringCooldown)
	case <-time.After(2 * time.Second):
		t.Fatal("operationSlot was not released during rate-limit cooldown wait")
	}

	finishedJob := waitForJobTerminal(t, db, srv.ownership, job.ID, 3*time.Second)
	if finishedJob.Status != companion.SyncJobStatusCompleted {
		t.Fatalf("expected job completed after retry, got status=%s err=%q", finishedJob.Status, finishedJob.ErrorMessage)
	}
	if finishedJob.ProcessedCount != 1 || finishedJob.ConfirmedCount != 1 || finishedJob.ErrorCount != 0 {
		t.Fatalf("expected 1 processed and 1 confirmed after retry, got %+v", finishedJob)
	}
	if got := advisorAttempts.Load(); got != 2 {
		t.Fatalf("expected 2 advisor attempts (429 then 200), got %d", got)
	}

	// Verify mapping was persisted and book was NOT recorded in auto_match_attempts cooldown
	checkStore, err := companion.OpenOwnedStore(db, srv.ownership)
	if err != nil {
		t.Fatal(err)
	}
	m, err := checkStore.GetMapping(context.Background(), "weread", "default", "4001", "default")
	inCooldown, cdErr := checkStore.IsAutoMatchAttemptInCooldown(context.Background(), "weread", "default", "4001", "default", 0.80, false, now)
	_ = checkStore.Close()
	if err != nil || m == nil || m.WorkID != 5001 {
		t.Fatalf("expected confirmed mapping for 4001 -> 5001, got %+v, err=%v", m, err)
	}
	if cdErr != nil || inCooldown {
		t.Fatalf("expected 4001 not in auto_match_attempts cooldown, got inCooldown=%v err=%v", inCooldown, cdErr)
	}

	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func TestJobWorkerRateLimitCooldownCancelAbortsImmediately(t *testing.T) {
	now := time.Date(2026, 11, 16, 12, 0, 0, 0, time.UTC)
	db := filepath.Join(t.TempDir(), "worker-ratelimit-cancel.sqlite")

	secret := 0
	opened := now.Add(-1 * time.Hour).Unix()

	src := &workerTestSource{
		shelf: companion.ShelfResult{
			Complete: true,
			Books: []companion.ShelfBook{
				{ID: "4002", Title: "Book 4002", Author: "Author", LastOpened: &opened, Secret: &secret},
			},
		},
	}
	tgt := &workerTestTarget{}

	rateLimitedCh := make(chan struct{})
	adv := &workerTestAdvisor{
		resolveFunc: func(_ context.Context, _ advisor.OriginalWorkRequest) (advisor.OriginalWorkResponse, error) {
			select {
			case <-rateLimitedCh:
			default:
				close(rateLimitedCh)
			}
			return advisor.OriginalWorkResponse{}, &advisor.RateLimitError{
				RetryAfter: 10 * time.Second,
				Detail:     "long cooldown",
			}
		},
	}

	srv, base, cancel, done := newTestServer(t, src, db, now, tgt)
	srv.setJobWorkerConfig(jobWorkerConfig{
		cooldownBase:      10 * time.Second,
		cooldownMax:       30 * time.Second,
		maxRateLimitRetry: 3,
		advisorPaceDelay:  -1,
		advisor:           adv,
	})
	if resp := request(t, base, http.MethodGet, "/readyz", nil, nil); resp.StatusCode != http.StatusOK {
		t.Fatalf("readyz status=%d", resp.StatusCode)
	}

	store, err := companion.OpenOwnedStore(db, srv.ownership)
	if err != nil {
		t.Fatal(err)
	}
	job, err := store.EnqueueSyncJob(context.Background(), companion.SyncJob{
		Scope:     companion.SyncJobScopeRecent,
		Mode:      companion.SyncJobModeMatchOnly,
		CreatedAt: now,
	})
	_ = store.Close()
	if err != nil {
		t.Fatal(err)
	}
	srv.notifyJobWorker()

	select {
	case <-rateLimitedCh:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for advisor 429")
	}

	// Wait until the worker releases s.operationSlot to enter waitCooldown
	srv.operationSlot <- struct{}{}
	<-srv.operationSlot

	cancelStart := time.Now()
	store, err = companion.OpenOwnedStore(db, srv.ownership)
	if err != nil {
		t.Fatal(err)
	}
	_, active, err := store.RequestCancelSyncJob(context.Background(), job.ID, now)
	_ = store.Close()
	if err != nil || !active {
		t.Fatalf("RequestCancelSyncJob failed: active=%v err=%v", active, err)
	}
	if !srv.cancelActiveJob(job.ID) {
		t.Fatal("expected cancelActiveJob to return true during cooldown wait")
	}

	finishedJob := waitForJobTerminal(t, db, srv.ownership, job.ID, 1*time.Second)
	if elapsed := time.Since(cancelStart); elapsed > 500*time.Millisecond {
		t.Fatalf("expected cooldown wait to abort immediately on cancel, took %v", elapsed)
	}
	if finishedJob.Status != companion.SyncJobStatusCancelled {
		t.Fatalf("expected cancelled status, got %q", finishedJob.Status)
	}
	if finishedJob.ProcessedCount != 0 {
		t.Fatalf("expected ProcessedCount=0 when cancelled during cooldown before retry, got %d", finishedJob.ProcessedCount)
	}

	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func TestJobWorkerLongRateLimitCooldownAbortsPhase1AndRunsPhase2(t *testing.T) {
	now := time.Date(2026, 11, 16, 12, 0, 0, 0, time.UTC)
	db := filepath.Join(t.TempDir(), "worker-ratelimit-5h.sqlite")

	secret := 0
	finished := 1
	opened := now.Add(-1 * time.Hour).Unix()

	src := &workerTestSource{
		shelf: companion.ShelfResult{
			Complete: true,
			Books: []companion.ShelfBook{
				{
					ID:            "5001",
					Title:         "Exact Match Book",
					Author:        "Author 1",
					LastOpened:    &opened,
					FinishReading: &finished,
					Secret:        &secret,
				},
				{
					ID:            "5002",
					Title:         "需要顾问翻译的书",
					Author:        "Author 2",
					LastOpened:    &opened,
					FinishReading: &finished,
					Secret:        &secret,
				},
				{
					ID:            "5003",
					Title:         "Untouched Book After Abort",
					Author:        "Author 3",
					LastOpened:    &opened,
					FinishReading: &finished,
					Secret:        &secret,
				},
			},
		},
		books: map[string]companion.Book{
			"5001": {
				ID:              "5001",
				Title:           "Exact Match Book",
				Author:          "Author 1",
				ISBN:            "9780000005001",
				TranslatorKnown: true,
			},
			"5002": {
				ID:              "5002",
				Title:           "需要顾问翻译的书",
				Author:          "Author 2",
				Translator:      "Translator",
				TranslatorKnown: true,
			},
			"5003": {
				ID:              "5003",
				Title:           "Untouched Book After Abort",
				Author:          "Author 3",
				ISBN:            "9780000005003",
				TranslatorKnown: true,
			},
		},
	}

	tgt := &workerTestTarget{
		matches: map[string]companion.MatchReport{
			"5001": {
				ExactEditions: []companion.Edition{{
					ID:     6001,
					WorkID: 7001,
					Title:  "Exact Match Book",
				}},
			},
			"5003": {
				ExactEditions: []companion.Edition{{
					ID:     6003,
					WorkID: 7003,
					Title:  "Untouched Book After Abort",
				}},
			},
		},
	}

	var advisorCalls atomic.Int32
	adv := &workerTestAdvisor{
		resolveFunc: func(_ context.Context, _ advisor.OriginalWorkRequest) (advisor.OriginalWorkResponse, error) {
			advisorCalls.Add(1)
			return advisor.OriginalWorkResponse{}, &advisor.RateLimitError{
				RetryAfter: 5 * time.Hour,
				Detail:     "5-hour rolling quota exhausted across all fallback models",
			}
		},
	}

	srv, base, cancel, done := newTestServer(t, src, db, now, tgt)
	srv.setJobWorkerConfig(jobWorkerConfig{
		cooldownBase:      15 * time.Second,
		cooldownMax:       90 * time.Second,
		maxCooldownWait:   2 * time.Minute,
		maxRateLimitRetry: 5,
		advisorPaceDelay:  -1,
		advisor:           adv,
	})
	if resp := request(t, base, http.MethodGet, "/readyz", nil, nil); resp.StatusCode != http.StatusOK {
		t.Fatalf("readyz status=%d", resp.StatusCode)
	}

	start := time.Now()
	store, err := companion.OpenOwnedStore(db, srv.ownership)
	if err != nil {
		t.Fatal(err)
	}
	job, err := store.EnqueueSyncJob(context.Background(), companion.SyncJob{
		Scope:         companion.SyncJobScopeRecent,
		Mode:          companion.SyncJobModeMatchAndReconcile,
		MinConfidence: 0.80,
		CreatedAt:     now,
	})
	_ = store.Close()
	if err != nil {
		t.Fatal(err)
	}
	srv.notifyJobWorker()

	finishedJob := waitForJobTerminal(t, db, srv.ownership, job.ID, 2*time.Second)
	if elapsed := time.Since(start); elapsed > 1*time.Second {
		t.Fatalf("expected 5h rate-limit cooldown to abort Phase 1 immediately without sleeping, took %v", elapsed)
	}
	if finishedJob.Status != companion.SyncJobStatusCompletedWithErrors {
		t.Fatalf("expected status %q, got %q (err=%q)", companion.SyncJobStatusCompletedWithErrors, finishedJob.Status, finishedJob.ErrorMessage)
	}
	wantErr := "all advisor models rate-limited; retry after 5h0m0s"
	if finishedJob.ErrorMessage != wantErr {
		t.Fatalf("expected error_message %q, got %q", wantErr, finishedJob.ErrorMessage)
	}
	if finishedJob.TotalEstimated != 3 || finishedJob.ProcessedCount != 1 || finishedJob.ConfirmedCount != 1 || finishedJob.ErrorCount != 1 {
		t.Fatalf("unexpected job counters after 5h rate-limit abort: %+v", finishedJob)
	}
	if got := advisorCalls.Load(); got != 1 {
		t.Fatalf("expected advisor to be called once before aborting Phase 1, got %d", got)
	}
	if finishedJob.SyncRunID == nil || *finishedJob.SyncRunID <= 0 {
		t.Fatalf("expected Phase 2 ReconcileAccount to still run and record sync_run_id, got %v", finishedJob.SyncRunID)
	}
	if got := tgt.applyCalls.Load(); got != 1 {
		t.Fatalf("expected Phase 2 to apply status mutation for the 1 confirmed mapping (5001), got %d", got)
	}

	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}
