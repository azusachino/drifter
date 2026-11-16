package httpserver

import (
	"context"
	"database/sql"
	"errors"
	"sync"
	"time"

	"github.com/azusachino/drifter/internal/advisor"
	"github.com/azusachino/drifter/internal/advisor/opencode"
	"github.com/azusachino/drifter/internal/companion"
)

const (
	jobWorkerFallbackPollInterval = 5 * time.Second
	jobAutoMatchBatchSize         = 5
	jobBookStepTimeout            = 75 * time.Second
	jobReconcileTimeout           = 90 * time.Second
	jobRecoveryReason             = "interrupted by server restart"

	defaultJobCooldownBase      = 15 * time.Second
	defaultJobCooldownMax       = 90 * time.Second
	defaultJobMaxCooldownWait   = 2 * time.Minute
	defaultJobMaxRateLimitRetry = 5
	defaultJobAdvisorPaceDelay  = 1 * time.Second
)

type jobWorkerConfig struct {
	cooldownBase      time.Duration
	cooldownMax       time.Duration
	maxCooldownWait   time.Duration
	maxRateLimitRetry int
	advisorPaceDelay  time.Duration // < 0 disables pacing delay
	advisor           advisor.Advisor
}

var (
	serverJobCachesMu sync.Mutex
	serverJobCaches   = make(map[*Server]*companion.MemoryTTLCache)
	serverJobConfigs  = make(map[*Server]jobWorkerConfig)
)

func (s *Server) setJobWorkerConfig(cfg jobWorkerConfig) {
	serverJobCachesMu.Lock()
	serverJobConfigs[s] = cfg
	serverJobCachesMu.Unlock()
}

func (s *Server) getJobWorkerConfig() jobWorkerConfig {
	serverJobCachesMu.Lock()
	cfg := serverJobConfigs[s]
	serverJobCachesMu.Unlock()
	if cfg.cooldownBase <= 0 {
		cfg.cooldownBase = defaultJobCooldownBase
	}
	if cfg.cooldownMax <= 0 {
		cfg.cooldownMax = defaultJobCooldownMax
	}
	if cfg.cooldownMax < cfg.cooldownBase {
		cfg.cooldownMax = cfg.cooldownBase
	}
	if cfg.maxCooldownWait <= 0 {
		cfg.maxCooldownWait = defaultJobMaxCooldownWait
	}
	if cfg.maxRateLimitRetry <= 0 {
		cfg.maxRateLimitRetry = defaultJobMaxRateLimitRetry
	}
	if cfg.advisorPaceDelay == 0 {
		cfg.advisorPaceDelay = defaultJobAdvisorPaceDelay
	} else if cfg.advisorPaceDelay < 0 {
		cfg.advisorPaceDelay = 0
	}
	return cfg
}

type advisorCallTracker struct {
	inner  advisor.Advisor
	called bool
}

func (t *advisorCallTracker) ExplainConflict(ctx context.Context, req advisor.ConflictExplanationRequest) (advisor.ConflictExplanationResponse, error) {
	t.called = true
	return t.inner.ExplainConflict(ctx, req)
}

func (t *advisorCallTracker) ResolveOriginalWork(ctx context.Context, req advisor.OriginalWorkRequest) (advisor.OriginalWorkResponse, error) {
	t.called = true
	return t.inner.ResolveOriginalWork(ctx, req)
}

func (s *Server) getJobCache() *companion.MemoryTTLCache {
	serverJobCachesMu.Lock()
	defer serverJobCachesMu.Unlock()
	c, ok := serverJobCaches[s]
	if !ok {
		c = companion.NewMemoryTTLCache(10000, s.now)
		serverJobCaches[s] = c
	}
	return c
}

func (s *Server) clearJobCache() {
	serverJobCachesMu.Lock()
	delete(serverJobCaches, s)
	delete(serverJobConfigs, s)
	serverJobCachesMu.Unlock()
}

func (s *Server) ensureJobNotifyCh() chan struct{} {
	s.activeJobMu.Lock()
	defer s.activeJobMu.Unlock()
	if s.jobNotifyCh == nil {
		s.jobNotifyCh = make(chan struct{}, 1)
	}
	return s.jobNotifyCh
}

// notifyJobWorker performs a non-blocking send to jobNotifyCh to wake the FIFO job worker.
func (s *Server) notifyJobWorker() {
	ch := s.ensureJobNotifyCh()
	select {
	case ch <- struct{}{}:
	default:
	}
}

// cancelActiveJob cancels the per-job context if jobID is currently executing.
func (s *Server) cancelActiveJob(jobID int64) bool {
	if jobID <= 0 {
		return false
	}
	s.activeJobMu.Lock()
	defer s.activeJobMu.Unlock()
	if s.activeJobID == jobID && s.activeJobCancel != nil {
		s.activeJobCancel()
		return true
	}
	return false
}

// startJobWorker recovers any stale running jobs from a previous run and starts the
// background FIFO worker loop. It returns a stop() function that cancels the worker
// context and waits for the worker goroutine to exit before returning (so Serve can
// call stop() before closing DatabaseOwnership).
func (s *Server) startJobWorker(serverCtx context.Context) func() {
	notifyCh := s.ensureJobNotifyCh()

	if store, err := companion.OpenOwnedStore(s.database, s.ownership); err == nil {
		_, _ = store.RecoverStaleRunningJobs(serverCtx, jobRecoveryReason, s.now().UTC())
		_ = store.Close()
	}

	workerCtx, cancel := context.WithCancel(serverCtx)
	var wg sync.WaitGroup
	wg.Add(1)

	go func() {
		defer wg.Done()
		s.jobWorkerLoop(workerCtx, notifyCh)
	}()

	var stopOnce sync.Once
	return func() {
		stopOnce.Do(func() {
			cancel()
			wg.Wait()
			s.clearJobCache()
		})
	}
}

func (s *Server) jobWorkerLoop(workerCtx context.Context, notifyCh <-chan struct{}) {
	ticker := time.NewTicker(jobWorkerFallbackPollInterval)
	defer ticker.Stop()

	s.drainQueuedJobs(workerCtx)

	for {
		select {
		case <-workerCtx.Done():
			return
		case <-notifyCh:
			s.drainQueuedJobs(workerCtx)
		case <-ticker.C:
			s.drainQueuedJobs(workerCtx)
		}
	}
}

func (s *Server) drainQueuedJobs(workerCtx context.Context) {
	for {
		if workerCtx.Err() != nil {
			return
		}
		store, err := companion.OpenOwnedStore(s.database, s.ownership)
		if err != nil {
			return
		}
		job, err := store.ClaimNextSyncJob(workerCtx, s.now().UTC())
		_ = store.Close()
		if err != nil || job == nil {
			return
		}
		s.runSyncJob(workerCtx, *job)
	}
}

func (s *Server) acquireBatchSlot(jobCtx, serverCtx context.Context) (func(), error) {
	select {
	case <-jobCtx.Done():
		return nil, jobCtx.Err()
	case <-serverCtx.Done():
		return nil, serverCtx.Err()
	default:
	}

	select {
	case s.operationSlot <- struct{}{}:
		return func() { <-s.operationSlot }, nil
	case <-jobCtx.Done():
		return nil, jobCtx.Err()
	case <-serverCtx.Done():
		return nil, serverCtx.Err()
	}
}

func (s *Server) waitCooldown(jobCtx, serverCtx context.Context, store *companion.Store, jobID int64, d time.Duration) bool {
	if d <= 0 {
		return jobCtx.Err() == nil && serverCtx.Err() == nil && !s.isJobCancelRequested(store, jobID)
	}
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-timer.C:
		return jobCtx.Err() == nil && serverCtx.Err() == nil && !s.isJobCancelRequested(store, jobID)
	case <-jobCtx.Done():
		return false
	case <-serverCtx.Done():
		return false
	}
}

func (s *Server) isJobCancelRequested(store *companion.Store, jobID int64) bool {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	current, err := store.GetSyncJob(ctx, jobID)
	if err != nil || current == nil {
		return false
	}
	return current.CancelRequested || current.Status == companion.SyncJobStatusCancelled
}

func (s *Server) finalizeInterruptedOrCancelledJob(store *companion.Store, jobID int64, serverCtx context.Context) {
	finCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	now := s.now().UTC()
	if s.isJobCancelRequested(store, jobID) {
		_ = store.FinishSyncJob(finCtx, jobID, companion.SyncJobStatusCancelled, sql.NullInt64{}, "", now)
		return
	}
	if serverCtx.Err() != nil {
		_ = store.FinishSyncJob(finCtx, jobID, companion.SyncJobStatusFailed, sql.NullInt64{}, jobRecoveryReason, now)
		return
	}
	_ = store.FinishSyncJob(finCtx, jobID, companion.SyncJobStatusCancelled, sql.NullInt64{}, "", now)
}

// runSyncJob executes a claimed SyncJob through Phase 1 (single-shelf candidate discovery
// and batched auto-matching with per-batch operationSlot yielding) and Phase 2 (account
// reconciliation), persisting progress checkpoints and final status to SQLite.
func (s *Server) runSyncJob(serverCtx context.Context, job companion.SyncJob) {
	jobCtx, jobCancel := context.WithCancel(serverCtx)
	s.activeJobMu.Lock()
	s.activeJobID = job.ID
	s.activeJobCancel = jobCancel
	s.activeJobMu.Unlock()

	defer func() {
		jobCancel()
		s.activeJobMu.Lock()
		if s.activeJobID == job.ID {
			s.activeJobID = 0
			s.activeJobCancel = nil
		}
		s.activeJobMu.Unlock()
	}()

	store, err := companion.OpenOwnedStore(s.database, s.ownership)
	if err != nil {
		return
	}
	defer store.Close()

	finishFail := func(msg string) {
		finCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = store.FinishSyncJob(finCtx, job.ID, companion.SyncJobStatusFailed, sql.NullInt64{}, msg, s.now().UTC())
	}

	if s.isJobCancelRequested(store, job.ID) {
		s.finalizeInterruptedOrCancelledJob(store, job.ID, serverCtx)
		return
	}
	if jobCtx.Err() != nil || serverCtx.Err() != nil {
		s.finalizeInterruptedOrCancelledJob(store, job.ID, serverCtx)
		return
	}

	rawSrc, err := s.pendingSource()
	if err != nil {
		finishFail("source provider unavailable")
		return
	}
	rawTgt, err := s.confirmationTarget()
	if err != nil {
		finishFail("target provider unavailable")
		return
	}

	cache := s.getJobCache()
	cachedSrc := companion.NewCachedSource(rawSrc, cache, s.now)
	if job.Force {
		cachedSrc.InvalidateShelf()
	}
	cachedTgt := companion.NewCachedTarget(rawTgt, cache, s.now)

	resolveCtx, resolveCancel := context.WithTimeout(jobCtx, 10*time.Second)
	sourceAccount, targetAccount, err := store.ResolveAccounts(resolveCtx, job.SourceAccount, job.TargetAccount)
	resolveCancel()
	if err != nil {
		if jobCtx.Err() != nil || serverCtx.Err() != nil {
			s.finalizeInterruptedOrCancelledJob(store, job.ID, serverCtx)
			return
		}
		finishFail("invalid account configuration")
		return
	}

	workerCfg := s.getJobWorkerConfig()
	var adv advisor.Advisor
	var advTracker *advisorCallTracker
	if workerCfg.advisor != nil {
		advTracker = &advisorCallTracker{inner: workerCfg.advisor}
		adv = advTracker
	} else if cfg, cfgErr := opencode.LoadConfig(); cfgErr == nil {
		if client, cliErr := opencode.NewWithConfig(cfg); cliErr == nil {
			advTracker = &advisorCallTracker{inner: client}
			adv = advisor.NewCachedAdvisor(advTracker, store, cfg.Model, opencode.PromptHash())
		}
	}

	totalEstimated := job.TotalEstimated
	processedCount := job.ProcessedCount
	confirmedCount := job.ConfirmedCount
	skippedCount := job.SkippedCount
	errorCount := job.ErrorCount
	phase1ErrorMessage := ""

	// Phase 1: In-Memory Shelf Snapshot & Batched Auto-Match
	if job.Mode != companion.SyncJobModeReconcileOnly {
		releaseSlot, slotErr := s.acquireBatchSlot(jobCtx, serverCtx)
		if slotErr != nil {
			s.finalizeInterruptedOrCancelledJob(store, job.ID, serverCtx)
			return
		}

		discoverCtx, discoverCancel := context.WithTimeout(jobCtx, jobBookStepTimeout)
		autoOpts := companion.AutoMatchOptions{
			Provider:      job.Provider,
			SourceAccount: sourceAccount,
			TargetAccount: targetAccount,
			Days:          job.Days,
			MinConfidence: job.MinConfidence,
			FinishedOnly:  job.FinishedOnly,
			DryRun:        job.DryRun,
			Force:         job.Force,
			Now:           s.now().UTC(),
		}
		candidates, discoverErr := companion.DiscoverAutoMatchCandidates(discoverCtx, store, cachedSrc, autoOpts)
		discoverCancel()
		releaseSlot()

		if discoverErr != nil {
			if jobCtx.Err() != nil || serverCtx.Err() != nil || errors.Is(discoverErr, context.Canceled) {
				s.finalizeInterruptedOrCancelledJob(store, job.ID, serverCtx)
				return
			}
			finishFail("candidate discovery failed: " + discoverErr.Error())
			return
		}

		totalEstimated = len(candidates)
		_ = store.CheckpointSyncJob(jobCtx, job.ID, totalEstimated, processedCount, confirmedCount, skippedCount, errorCount)

		backoff := workerCfg.cooldownBase

		for start := 0; start < len(candidates); start += jobAutoMatchBatchSize {
			if jobCtx.Err() != nil || serverCtx.Err() != nil || s.isJobCancelRequested(store, job.ID) {
				s.finalizeInterruptedOrCancelledJob(store, job.ID, serverCtx)
				return
			}

			end := start + jobAutoMatchBatchSize
			if end > len(candidates) {
				end = len(candidates)
			}
			batch := candidates[start:end]

			releaseBatch, batchSlotErr := s.acquireBatchSlot(jobCtx, serverCtx)
			if batchSlotErr != nil {
				s.finalizeInterruptedOrCancelledJob(store, job.ID, serverCtx)
				return
			}

			interrupted := false
			rateLimitAborted := false
			batchHitAdvisor := false
			rateLimitRetries := 0

			for i := 0; i < len(batch); {
				row := batch[i]
				if jobCtx.Err() != nil || serverCtx.Err() != nil || s.isJobCancelRequested(store, job.ID) {
					interrupted = true
					break
				}
				if advTracker != nil {
					advTracker.called = false
				}
				bookCtx, bookCancel := context.WithTimeout(jobCtx, jobBookStepTimeout)
				autoOpts.Now = s.now().UTC()
				item := companion.AutoMatchBookWithStore(bookCtx, store, cachedSrc, cachedTgt, adv, row, autoOpts)
				bookCancel()
				if advTracker != nil && advTracker.called {
					batchHitAdvisor = true
				}

				if item.Status == "error" && (jobCtx.Err() != nil || serverCtx.Err() != nil) {
					interrupted = true
					break
				}

				if item.RateLimited {
					wait := max(item.RetryAfter, backoff)
					if wait > workerCfg.maxCooldownWait || rateLimitRetries >= workerCfg.maxRateLimitRetry {
						errorCount++
						phase1ErrorMessage = "all advisor models rate-limited; retry after " + wait.Round(time.Second).String()
						rateLimitAborted = true
						break
					}

					rateLimitRetries++
					waitDur := wait
					if waitDur > workerCfg.cooldownMax {
						waitDur = workerCfg.cooldownMax
					}
					backoff *= 2
					if backoff > workerCfg.cooldownMax {
						backoff = workerCfg.cooldownMax
					}

					// Release s.operationSlot before waiting so interactive HTTP requests
					// (/api/pending, /api/mappings/*) are never blocked during cooldown.
					if releaseBatch != nil {
						releaseBatch()
						releaseBatch = nil
					}

					if !s.waitCooldown(jobCtx, serverCtx, store, job.ID, waitDur) {
						interrupted = true
						break
					}

					newRelease, slotErr := s.acquireBatchSlot(jobCtx, serverCtx)
					if slotErr != nil {
						interrupted = true
						break
					}
					releaseBatch = newRelease
					continue
				}

				rateLimitRetries = 0
				backoff = workerCfg.cooldownBase

				processedCount++
				switch item.Status {
				case "confirmed", "dry_run":
					confirmedCount++
				case "error":
					errorCount++
					skippedCount++
				default:
					skippedCount++
				}
				i++
			}

			if releaseBatch != nil {
				releaseBatch()
				releaseBatch = nil
			}

			chkCtx, chkCancel := context.WithTimeout(context.Background(), 5*time.Second)
			_ = store.CheckpointSyncJob(chkCtx, job.ID, totalEstimated, processedCount, confirmedCount, skippedCount, errorCount)
			chkCancel()

			if interrupted {
				s.finalizeInterruptedOrCancelledJob(store, job.ID, serverCtx)
				return
			}
			if rateLimitAborted {
				break
			}

			if batchHitAdvisor && start+jobAutoMatchBatchSize < len(candidates) && workerCfg.advisorPaceDelay > 0 {
				if !s.waitCooldown(jobCtx, serverCtx, store, job.ID, workerCfg.advisorPaceDelay) {
					s.finalizeInterruptedOrCancelledJob(store, job.ID, serverCtx)
					return
				}
			}
		}
	}

	if jobCtx.Err() != nil || serverCtx.Err() != nil || s.isJobCancelRequested(store, job.ID) {
		s.finalizeInterruptedOrCancelledJob(store, job.ID, serverCtx)
		return
	}

	// Phase 2: Reconcile
	var syncRunID sql.NullInt64
	reconcileErrors := 0

	if job.Mode != companion.SyncJobModeMatchOnly {
		releaseReconcile, slotErr := s.acquireBatchSlot(jobCtx, serverCtx)
		if slotErr != nil {
			s.finalizeInterruptedOrCancelledJob(store, job.ID, serverCtx)
			return
		}

		// Verify rawSrc and rawTgt support PreviewSource and ApplyTarget
		if _, ok := rawSrc.(companion.PreviewSource); !ok {
			releaseReconcile()
			finishFail("source provider does not support reconciliation")
			return
		}
		if _, ok := rawTgt.(companion.ApplyTarget); !ok {
			releaseReconcile()
			finishFail("target provider does not support reconciliation")
			return
		}

		recTimeout := s.operationTimeout
		if recTimeout < jobReconcileTimeout {
			recTimeout = jobReconcileTimeout
		}
		recCtx, recCancel := context.WithTimeout(jobCtx, recTimeout)
		summary, recErr := companion.ReconcileAccount(
			recCtx,
			store,
			cachedSrc,
			cachedTgt,
			companion.ReconcileOptions{
				Provider:      job.Provider,
				SourceAccount: sourceAccount,
				TargetAccount: targetAccount,
				PolicyVersion: companion.DefaultPolicyVersion,
				DryRun:        job.DryRun,
				Now:           s.now().UTC(),
				Advisor:       adv,
				WithAdvisor:   adv != nil,
			},
		)
		recCancel()
		releaseReconcile()

		if summary.RunID > 0 {
			syncRunID = sql.NullInt64{Int64: summary.RunID, Valid: true}
		}

		if recErr != nil {
			if jobCtx.Err() != nil || serverCtx.Err() != nil || errors.Is(recErr, context.Canceled) {
				s.finalizeInterruptedOrCancelledJob(store, job.ID, serverCtx)
				return
			}
			finCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			_ = store.FinishSyncJob(finCtx, job.ID, companion.SyncJobStatusFailed, syncRunID, recErr.Error(), s.now().UTC())
			return
		}

		reconcileErrors = summary.ErrorCount
		if reconcileErrors > 0 {
			errorCount += reconcileErrors
			chkCtx, chkCancel := context.WithTimeout(context.Background(), 5*time.Second)
			_ = store.CheckpointSyncJob(chkCtx, job.ID, totalEstimated, processedCount, confirmedCount, skippedCount, errorCount)
			chkCancel()
		}
	}

	if jobCtx.Err() != nil || serverCtx.Err() != nil || s.isJobCancelRequested(store, job.ID) {
		s.finalizeInterruptedOrCancelledJob(store, job.ID, serverCtx)
		return
	}

	finalStatus := companion.SyncJobStatusCompleted
	if errorCount > 0 || reconcileErrors > 0 || phase1ErrorMessage != "" {
		finalStatus = companion.SyncJobStatusCompletedWithErrors
	}
	finCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = store.FinishSyncJob(finCtx, job.ID, finalStatus, syncRunID, phase1ErrorMessage, s.now().UTC())
}
