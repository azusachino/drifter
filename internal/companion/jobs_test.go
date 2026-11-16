package companion

import (
	"context"
	"database/sql"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestSyncJobLifecycleAndNullableSyncRunID(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "jobs.sqlite")
	s, err := OpenStore(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	t0 := time.Date(2026, 11, 16, 10, 0, 0, 0, time.UTC)

	// 1. Enqueue a job with defaults
	job, err := s.EnqueueSyncJob(ctx, SyncJob{
		Provider:      "weread",
		SourceAccount: "default",
		TargetAccount: "default",
		Scope:         SyncJobScopeRecent,
		CreatedAt:     t0,
	})
	if err != nil {
		t.Fatalf("EnqueueSyncJob failed: %v", err)
	}
	if job.ID <= 0 || job.Status != SyncJobStatusQueued || job.Days != 7 || job.Mode != SyncJobModeMatchAndReconcile {
		t.Fatalf("unexpected enqueued job: %+v", job)
	}

	// 2. ClaimNextSyncJob transitions queued -> running
	tStart := t0.Add(time.Second)
	claimed, err := s.ClaimNextSyncJob(ctx, tStart)
	if err != nil || claimed == nil {
		t.Fatalf("ClaimNextSyncJob failed: %+v, err=%v", claimed, err)
	}
	if claimed.ID != job.ID || claimed.Status != SyncJobStatusRunning || claimed.StartedAt == nil || !claimed.StartedAt.Equal(tStart) {
		t.Fatalf("unexpected claimed job: %+v", claimed)
	}

	// 3. CheckpointSyncJob updates progress counters
	if err := s.CheckpointSyncJob(ctx, job.ID, 25, 10, 4, 5, 1); err != nil {
		t.Fatalf("CheckpointSyncJob failed: %v", err)
	}
	got, err := s.GetSyncJob(ctx, job.ID)
	if err != nil || got == nil {
		t.Fatalf("GetSyncJob failed: %+v, err=%v", got, err)
	}
	if got.TotalEstimated != 25 || got.ProcessedCount != 10 || got.ConfirmedCount != 4 || got.SkippedCount != 5 || got.ErrorCount != 1 {
		t.Fatalf("unexpected checkpoint counts: %+v", got)
	}

	// 4. FinishSyncJob with NULL sync_run_id (e.g. DryRun or RunID == 0) must succeed with _foreign_keys=on
	tFin := tStart.Add(5 * time.Second)
	if err := s.FinishSyncJob(ctx, job.ID, SyncJobStatusCompletedWithErrors, sql.NullInt64{}, "1 book failed", tFin); err != nil {
		t.Fatalf("FinishSyncJob with null sync_run_id failed: %v", err)
	}
	got, err = s.GetSyncJob(ctx, job.ID)
	if err != nil || got == nil {
		t.Fatalf("GetSyncJob after finish failed: %+v, err=%v", got, err)
	}
	if got.Status != SyncJobStatusCompletedWithErrors || got.SyncRunID != nil || got.ErrorMessage != "1 book failed" || got.FinishedAt == nil || !got.FinishedAt.Equal(tFin) {
		t.Fatalf("unexpected finished job: %+v", got)
	}

	// 5. FinishSyncJob with valid sync_run_id FK
	runID, err := s.CreateSyncRun(ctx, "default", "default", "v1", tFin)
	if err != nil {
		t.Fatalf("CreateSyncRun failed: %v", err)
	}
	job2, err := s.EnqueueSyncJob(ctx, SyncJob{
		Provider:      "weread",
		SourceAccount: "default",
		TargetAccount: "default",
		Scope:         SyncJobScopeLifetime,
		FinishedOnly:  true,
		CreatedAt:     tFin,
	})
	if err != nil {
		t.Fatalf("EnqueueSyncJob 2 failed: %v", err)
	}
	if job2.Days != 36500 || !job2.FinishedOnly {
		t.Fatalf("expected lifetime days=36500 and finished_only=true, got %+v", job2)
	}
	if _, err := s.ClaimNextSyncJob(ctx, tFin.Add(time.Second)); err != nil {
		t.Fatalf("ClaimNextSyncJob 2 failed: %v", err)
	}
	if err := s.FinishSyncJob(ctx, job2.ID, SyncJobStatusCompleted, sql.NullInt64{Int64: runID, Valid: true}, "", tFin.Add(2*time.Second)); err != nil {
		t.Fatalf("FinishSyncJob with valid sync_run_id failed: %v", err)
	}
	got2, err := s.GetSyncJob(ctx, job2.ID)
	if err != nil || got2 == nil || got2.SyncRunID == nil || *got2.SyncRunID != runID {
		t.Fatalf("expected SyncRunID=%d, got %+v (err=%v)", runID, got2, err)
	}

	// 6. ListSyncJobs and GetLatestSyncJob
	list, err := s.ListSyncJobs(ctx, 10)
	if err != nil || len(list) != 2 || list[0].ID != job2.ID || list[1].ID != job.ID {
		t.Fatalf("unexpected ListSyncJobs: %+v, err=%v", list, err)
	}
	latest, err := s.GetLatestSyncJob(ctx)
	if err != nil || latest == nil || latest.ID != job2.ID {
		t.Fatalf("unexpected GetLatestSyncJob: %+v, err=%v", latest, err)
	}
}

func TestSyncJobCoalescingCancellationAndRecovery(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "coalesce.sqlite")
	s, err := OpenStore(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	t0 := time.Date(2026, 11, 16, 11, 0, 0, 0, time.UTC)

	// 1. First CoalesceOrEnqueueSyncJob creates a queued job
	first, coalesced, err := s.CoalesceOrEnqueueSyncJob(ctx, SyncJob{
		Provider:      "weread",
		SourceAccount: "default",
		TargetAccount: "default",
		Scope:         SyncJobScopeRecent,
		Mode:          SyncJobModeMatchAndReconcile,
		CreatedAt:     t0,
	})
	if err != nil || coalesced || first == nil {
		t.Fatalf("expected new job, got %+v, coalesced=%v, err=%v", first, coalesced, err)
	}

	// 2. Second identical trigger coalesces onto queued job
	second, coalesced, err := s.CoalesceOrEnqueueSyncJob(ctx, SyncJob{
		Provider:      "weread",
		SourceAccount: "default",
		TargetAccount: "default",
		Scope:         SyncJobScopeRecent,
		Mode:          SyncJobModeMatchAndReconcile,
		CreatedAt:     t0.Add(time.Second),
	})
	if err != nil || !coalesced || second == nil || second.ID != first.ID {
		t.Fatalf("expected coalesced queued job %d, got %+v, coalesced=%v, err=%v", first.ID, second, coalesced, err)
	}

	// 3. Claim first job -> status='running'; third trigger still coalesces onto running job
	if _, err := s.ClaimNextSyncJob(ctx, t0.Add(2*time.Second)); err != nil {
		t.Fatalf("ClaimNextSyncJob failed: %v", err)
	}
	third, coalesced, err := s.CoalesceOrEnqueueSyncJob(ctx, SyncJob{
		Provider:      "weread",
		SourceAccount: "default",
		TargetAccount: "default",
		Scope:         SyncJobScopeRecent,
		Mode:          SyncJobModeMatchAndReconcile,
		CreatedAt:     t0.Add(3 * time.Second),
	})
	if err != nil || !coalesced || third == nil || third.ID != first.ID || third.Status != SyncJobStatusRunning {
		t.Fatalf("expected coalesced running job %d, got %+v, coalesced=%v, err=%v", first.ID, third, coalesced, err)
	}

	// 4. Different scope ('lifetime') does NOT coalesce with 'recent'
	lifetimeJob, coalesced, err := s.CoalesceOrEnqueueSyncJob(ctx, SyncJob{
		Provider:      "weread",
		SourceAccount: "default",
		TargetAccount: "default",
		Scope:         SyncJobScopeLifetime,
		Mode:          SyncJobModeMatchAndReconcile,
		CreatedAt:     t0.Add(4 * time.Second),
	})
	if err != nil || coalesced || lifetimeJob == nil || lifetimeJob.ID == first.ID {
		t.Fatalf("expected separate lifetime job, got %+v, coalesced=%v, err=%v", lifetimeJob, coalesced, err)
	}

	// 5. RequestCancelSyncJob on queued job transitions immediately to 'cancelled'
	cancelledQueued, active, err := s.RequestCancelSyncJob(ctx, lifetimeJob.ID, t0.Add(5*time.Second))
	if err != nil || !active || cancelledQueued == nil || cancelledQueued.Status != SyncJobStatusCancelled || !cancelledQueued.CancelRequested || cancelledQueued.FinishedAt == nil {
		t.Fatalf("expected queued job to cancel immediately: %+v, active=%v, err=%v", cancelledQueued, active, err)
	}

	// 6. RequestCancelSyncJob on running job sets cancel_requested=1 while keeping status='running'
	cancelRunning, active, err := s.RequestCancelSyncJob(ctx, first.ID, t0.Add(6*time.Second))
	if err != nil || !active || cancelRunning == nil || cancelRunning.Status != SyncJobStatusRunning || !cancelRunning.CancelRequested {
		t.Fatalf("expected running job to set cancel_requested: %+v, active=%v, err=%v", cancelRunning, active, err)
	}

	// 7. RecoverStaleRunningJobs marks running jobs as failed ("interrupted by server restart")
	recovered, err := s.RecoverStaleRunningJobs(ctx, "interrupted by server restart", t0.Add(10*time.Second))
	if err != nil || recovered != 1 {
		t.Fatalf("expected 1 recovered running job, got %d, err=%v", recovered, err)
	}
	afterRecover, err := s.GetSyncJob(ctx, first.ID)
	if err != nil || afterRecover == nil || afterRecover.Status != SyncJobStatusFailed || afterRecover.ErrorMessage != "interrupted by server restart" {
		t.Fatalf("unexpected job state after recovery: %+v, err=%v", afterRecover, err)
	}
}

func TestAutoMatchAttemptCooldownAndBypass(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "cooldown.sqlite")
	s, err := OpenStore(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	now := time.Date(2026, 11, 16, 12, 0, 0, 0, time.UTC)

	// Non-deterministic status ('error') must be rejected
	if err := s.RecordAutoMatchAttempt(ctx, AutoMatchAttempt{
		Provider: "weread", SourceAccount: "default", BookID: "100", TargetAccount: "default",
		LastStatus: "error", AttemptedAt: now,
	}); err == nil {
		t.Fatal("expected error when recording transient 'error' status in auto_match_attempts")
	}

	// Record deterministic non-matches
	attempts := []AutoMatchAttempt{
		{
			Provider: "weread", SourceAccount: "default", BookID: "101", TargetAccount: "default",
			LastStatus: "abstained", LastConfidence: 0, LastReason: "insufficient info",
			AttemptedAt: now.Add(-5 * 24 * time.Hour),
		},
		{
			Provider: "weread", SourceAccount: "default", BookID: "102", TargetAccount: "default",
			LastStatus: "low_confidence", LastConfidence: 0.75, LastReason: "below 0.80",
			AttemptedAt: now.Add(-10 * 24 * time.Hour),
		},
		{
			Provider: "weread", SourceAccount: "default", BookID: "103", TargetAccount: "default",
			LastStatus: "no_candidate", LastConfidence: 0.90, LastReason: "not on hardcover",
			AttemptedAt: now.Add(-31 * 24 * time.Hour), // expired (> 30 days)
		},
		{
			Provider: "weread", SourceAccount: "default", BookID: "104", TargetAccount: "default",
			LastStatus: "ambiguous", LastConfidence: 0.85, LastReason: "multiple candidates",
			AttemptedAt: now.Add(-1 * time.Hour),
		},
	}
	for _, a := range attempts {
		if err := s.RecordAutoMatchAttempt(ctx, a); err != nil {
			t.Fatalf("RecordAutoMatchAttempt(%s) failed: %v", a.BookID, err)
		}
	}

	// At minConf = 0.80, force = false:
	// - 101 (abstained, 5d ago) -> in cooldown
	// - 102 (low_confidence 0.75, 10d ago, minConf 0.80 >= 0.75) -> in cooldown
	// - 103 (no_candidate, 31d ago) -> expired, NOT in cooldown
	// - 104 (ambiguous, 1h ago) -> in cooldown
	cooldownMap, err := s.ListCooldownBookIDs(ctx, "weread", "default", "default", 0.80, false, now)
	if err != nil {
		t.Fatalf("ListCooldownBookIDs failed: %v", err)
	}
	if !cooldownMap["101"] || !cooldownMap["102"] || cooldownMap["103"] || !cooldownMap["104"] {
		t.Fatalf("unexpected cooldown map at minConf=0.80: %+v", cooldownMap)
	}

	// Lowering minConf to 0.70 (< 0.75) bypasses cooldown for 102 ('low_confidence')
	inCD, err := s.IsAutoMatchAttemptInCooldown(ctx, "weread", "default", "102", "default", 0.70, false, now)
	if err != nil || inCD {
		t.Fatalf("expected low_confidence 0.75 to bypass cooldown when minConf=0.70, got inCD=%v, err=%v", inCD, err)
	}
	cooldownMapLow, err := s.ListCooldownBookIDs(ctx, "weread", "default", "default", 0.70, false, now)
	if err != nil {
		t.Fatalf("ListCooldownBookIDs(0.70) failed: %v", err)
	}
	if !cooldownMapLow["101"] || cooldownMapLow["102"] || !cooldownMapLow["104"] {
		t.Fatalf("unexpected cooldown map at minConf=0.70: %+v", cooldownMapLow)
	}

	// force = true bypasses cooldown for all books
	inCDForce, err := s.IsAutoMatchAttemptInCooldown(ctx, "weread", "default", "101", "default", 0.80, true, now)
	if err != nil || inCDForce {
		t.Fatalf("expected force=true to bypass cooldown, got inCD=%v, err=%v", inCDForce, err)
	}
	cooldownMapForce, err := s.ListCooldownBookIDs(ctx, "weread", "default", "default", 0.80, true, now)
	if err != nil || len(cooldownMapForce) != 0 {
		t.Fatalf("expected empty cooldown map when force=true, got %+v, err=%v", cooldownMapForce, err)
	}
}

func TestOwnedStoresSerializeConcurrentInProcessWrites(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "concurrent.sqlite")
	owner, err := AcquireDatabaseOwnership(path)
	if err != nil {
		t.Fatal(err)
	}
	defer owner.Close()

	storeA, err := OpenOwnedStore(path, owner)
	if err != nil {
		t.Fatal(err)
	}
	defer storeA.Close()

	storeB, err := OpenOwnedStore(path, owner)
	if err != nil {
		t.Fatal(err)
	}
	defer storeB.Close()

	var activeWriters atomic.Int32
	var maxConcurrent atomic.Int32
	var wg sync.WaitGroup

	for i := range 20 {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			s := storeA
			if idx%2 == 1 {
				s = storeB
			}
			release, err := s.writeGuard()
			if err != nil {
				t.Errorf("writeGuard failed: %v", err)
				return
			}
			cur := activeWriters.Add(1)
			for {
				prev := maxConcurrent.Load()
				if cur <= prev || maxConcurrent.CompareAndSwap(prev, cur) {
					break
				}
			}
			time.Sleep(2 * time.Millisecond)
			activeWriters.Add(-1)
			release()

			if _, err := s.EnqueueSyncJob(ctx, SyncJob{
				Provider:      "weread",
				SourceAccount: "default",
				TargetAccount: "default",
				Scope:         SyncJobScopeRecent,
			}); err != nil {
				t.Errorf("concurrent EnqueueSyncJob failed: %v", err)
			}
		}(i)
	}
	wg.Wait()

	if max := maxConcurrent.Load(); max > 1 {
		t.Fatalf("expected DatabaseOwnership writeGuard to serialize writes (max 1), got %d concurrent writers", max)
	}
	jobs, err := storeA.ListSyncJobs(ctx, 50)
	if err != nil || len(jobs) != 20 {
		t.Fatalf("expected 20 persisted jobs, got %d (err=%v)", len(jobs), err)
	}
}
