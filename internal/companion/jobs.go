package companion

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

const (
	SyncJobScopeRecent   = "recent"
	SyncJobScopeLifetime = "lifetime"
	SyncJobScopeCustom   = "custom"

	SyncJobModeMatchAndReconcile = "match_and_reconcile"
	SyncJobModeMatchOnly         = "match_only"
	SyncJobModeReconcileOnly     = "reconcile_only"

	SyncJobStatusQueued              = "queued"
	SyncJobStatusRunning             = "running"
	SyncJobStatusCompleted           = "completed"
	SyncJobStatusCompletedWithErrors = "completed_with_errors"
	SyncJobStatusCancelled           = "cancelled"
	SyncJobStatusFailed              = "failed"

	AutoMatchAttemptCooldown = 30 * 24 * time.Hour
)

// SyncJob represents a background auto-match and/or reconciliation job.
type SyncJob struct {
	ID              int64      `json:"id"`
	Provider        string     `json:"provider"`
	SourceAccount   string     `json:"source_account"`
	TargetAccount   string     `json:"target_account"`
	Scope           string     `json:"scope"`
	Mode            string     `json:"mode"`
	Days            int        `json:"days"`
	FinishedOnly    bool       `json:"finished_only"`
	MinConfidence   float64    `json:"min_confidence"`
	DryRun          bool       `json:"dry_run"`
	Force           bool       `json:"force"`
	Status          string     `json:"status"`
	CancelRequested bool       `json:"cancel_requested"`
	TotalEstimated  int        `json:"total_estimated"`
	ProcessedCount  int        `json:"processed_count"`
	ConfirmedCount  int        `json:"confirmed_count"`
	SkippedCount    int        `json:"skipped_count"`
	ErrorCount      int        `json:"error_count"`
	SyncRunID       *int64     `json:"sync_run_id,omitempty"`
	ErrorMessage    string     `json:"error_message,omitempty"`
	CreatedAt       time.Time  `json:"created_at"`
	StartedAt       *time.Time `json:"started_at,omitempty"`
	FinishedAt      *time.Time `json:"finished_at,omitempty"`
}

// AutoMatchAttempt records a deterministic non-match for abstention cooldown.
type AutoMatchAttempt struct {
	Provider       string    `json:"provider"`
	SourceAccount  string    `json:"source_account"`
	BookID         string    `json:"book_id"`
	TargetAccount  string    `json:"target_account"`
	LastStatus     string    `json:"last_status"`
	LastConfidence float64   `json:"last_confidence"`
	LastReason     string    `json:"last_reason,omitempty"`
	AttemptedAt    time.Time `json:"attempted_at"`
}

func validSyncJobScope(scope string) bool {
	return scope == SyncJobScopeRecent || scope == SyncJobScopeLifetime || scope == SyncJobScopeCustom
}

func validSyncJobMode(mode string) bool {
	return mode == SyncJobModeMatchAndReconcile || mode == SyncJobModeMatchOnly || mode == SyncJobModeReconcileOnly
}

func validSyncJobTerminalStatus(status string) bool {
	return status == SyncJobStatusCompleted ||
		status == SyncJobStatusCompletedWithErrors ||
		status == SyncJobStatusCancelled ||
		status == SyncJobStatusFailed
}

func validDeterministicNonMatchStatus(status string) bool {
	return status == "abstained" || status == "low_confidence" || status == "no_candidate" || status == "ambiguous"
}

func normalizeSyncJobInput(job *SyncJob) error {
	if job.Provider == "" {
		job.Provider = DefaultSourceProvider
	}
	if job.SourceAccount == "" {
		job.SourceAccount = DefaultSourceAccount
	}
	if job.TargetAccount == "" {
		job.TargetAccount = DefaultTargetAccount
	}
	if job.Scope == "" {
		job.Scope = SyncJobScopeRecent
	}
	if job.Mode == "" {
		job.Mode = SyncJobModeMatchAndReconcile
	}
	if job.Days <= 0 {
		if job.Scope == SyncJobScopeLifetime {
			job.Days = 36500
		} else {
			job.Days = 7
		}
	}
	if job.MinConfidence <= 0 {
		job.MinConfidence = 0.80
	}
	if job.CreatedAt.IsZero() {
		job.CreatedAt = time.Now().UTC()
	} else {
		job.CreatedAt = job.CreatedAt.UTC()
	}
	if !ValidLabel(job.Provider) || !ValidLabel(job.SourceAccount) || !ValidLabel(job.TargetAccount) {
		return ErrInvalidMappingKey
	}
	if !validSyncJobScope(job.Scope) || !validSyncJobMode(job.Mode) || job.Days < 1 || job.Days > 36500 || job.MinConfidence <= 0 || job.MinConfidence > 1.0 {
		return fmt.Errorf("invalid sync job parameters")
	}
	job.Status = SyncJobStatusQueued
	return nil
}

func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

type rowScanner interface {
	Scan(dest ...any) error
}

func scanSyncJob(s rowScanner) (SyncJob, error) {
	var j SyncJob
	var finishedOnly, dryRun, force, cancelReq int
	var syncRunID sql.NullInt64
	var createdStr, startedStr, finishedStr string
	err := s.Scan(
		&j.ID,
		&j.Provider,
		&j.SourceAccount,
		&j.TargetAccount,
		&j.Scope,
		&j.Mode,
		&j.Days,
		&finishedOnly,
		&j.MinConfidence,
		&dryRun,
		&force,
		&j.Status,
		&cancelReq,
		&j.TotalEstimated,
		&j.ProcessedCount,
		&j.ConfirmedCount,
		&j.SkippedCount,
		&j.ErrorCount,
		&syncRunID,
		&j.ErrorMessage,
		&createdStr,
		&startedStr,
		&finishedStr,
	)
	if err != nil {
		return SyncJob{}, err
	}
	j.FinishedOnly = finishedOnly != 0
	j.DryRun = dryRun != 0
	j.Force = force != 0
	j.CancelRequested = cancelReq != 0
	if syncRunID.Valid && syncRunID.Int64 > 0 {
		id := syncRunID.Int64
		j.SyncRunID = &id
	}
	ct, err := time.Parse(time.RFC3339, createdStr)
	if err != nil {
		return SyncJob{}, fmt.Errorf("corrupt created_at in sync_jobs: %w", err)
	}
	j.CreatedAt = ct
	if startedStr != "" {
		st, err := time.Parse(time.RFC3339, startedStr)
		if err != nil {
			return SyncJob{}, fmt.Errorf("corrupt started_at in sync_jobs: %w", err)
		}
		j.StartedAt = &st
	}
	if finishedStr != "" {
		ft, err := time.Parse(time.RFC3339, finishedStr)
		if err != nil {
			return SyncJob{}, fmt.Errorf("corrupt finished_at in sync_jobs: %w", err)
		}
		j.FinishedAt = &ft
	}
	return j, nil
}

const syncJobColumns = `id, provider, source_account, target_account, scope, mode, days,
	finished_only, min_confidence, dry_run, force, status, cancel_requested,
	total_estimated, processed_count, confirmed_count, skipped_count, error_count,
	sync_run_id, error_message, created_at, started_at, finished_at`

// EnqueueSyncJob inserts a new queued sync job and returns the persisted record.
func (s *Store) EnqueueSyncJob(ctx context.Context, job SyncJob) (*SyncJob, error) {
	if err := normalizeSyncJobInput(&job); err != nil {
		return nil, err
	}
	release, err := s.writeGuard()
	if err != nil {
		return nil, err
	}
	defer release()
	createdStr := job.CreatedAt.Format(time.RFC3339)
	res, err := s.db.ExecContext(ctx, `INSERT INTO sync_jobs (
		provider, source_account, target_account, scope, mode, days,
		finished_only, min_confidence, dry_run, force, status, created_at
	) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		job.Provider, job.SourceAccount, job.TargetAccount, job.Scope, job.Mode, job.Days,
		boolToInt(job.FinishedOnly), job.MinConfidence, boolToInt(job.DryRun), boolToInt(job.Force),
		job.Status, createdStr,
	)
	if err != nil {
		return nil, err
	}
	id, err := res.LastInsertId()
	if err != nil {
		return nil, err
	}
	job.ID = id
	return &job, nil
}

// CoalesceOrEnqueueSyncJob returns an existing queued or running job with matching
// (provider, source_account, target_account, scope, mode, finished_only) if one exists
// (with coalesced=true), or enqueues a new job (with coalesced=false).
func (s *Store) CoalesceOrEnqueueSyncJob(ctx context.Context, job SyncJob) (*SyncJob, bool, error) {
	if err := normalizeSyncJobInput(&job); err != nil {
		return nil, false, err
	}
	release, err := s.writeGuard()
	if err != nil {
		return nil, false, err
	}
	defer release()

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, false, err
	}
	defer tx.Rollback()

	row := tx.QueryRowContext(ctx, `SELECT `+syncJobColumns+` FROM sync_jobs
		WHERE provider = ? AND source_account = ? AND target_account = ?
		  AND scope = ? AND mode = ? AND finished_only = ?
		  AND status IN ('queued', 'running')
		ORDER BY id DESC LIMIT 1`,
		job.Provider, job.SourceAccount, job.TargetAccount,
		job.Scope, job.Mode, boolToInt(job.FinishedOnly),
	)
	existing, err := scanSyncJob(row)
	if err == nil {
		if err := tx.Commit(); err != nil {
			return nil, false, err
		}
		return &existing, true, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return nil, false, err
	}

	createdStr := job.CreatedAt.Format(time.RFC3339)
	res, err := tx.ExecContext(ctx, `INSERT INTO sync_jobs (
		provider, source_account, target_account, scope, mode, days,
		finished_only, min_confidence, dry_run, force, status, created_at
	) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		job.Provider, job.SourceAccount, job.TargetAccount, job.Scope, job.Mode, job.Days,
		boolToInt(job.FinishedOnly), job.MinConfidence, boolToInt(job.DryRun), boolToInt(job.Force),
		job.Status, createdStr,
	)
	if err != nil {
		return nil, false, err
	}
	id, err := res.LastInsertId()
	if err != nil {
		return nil, false, err
	}
	if err := tx.Commit(); err != nil {
		return nil, false, err
	}
	job.ID = id
	return &job, false, nil
}

// GetSyncJob retrieves a sync job by ID, returning nil, nil if not found.
func (s *Store) GetSyncJob(ctx context.Context, id int64) (*SyncJob, error) {
	if id <= 0 {
		return nil, nil
	}
	row := s.db.QueryRowContext(ctx, `SELECT `+syncJobColumns+` FROM sync_jobs WHERE id = ?`, id)
	j, err := scanSyncJob(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &j, nil
}

// GetLatestSyncJob returns the most recently created sync job, or nil, nil if none exist.
func (s *Store) GetLatestSyncJob(ctx context.Context) (*SyncJob, error) {
	row := s.db.QueryRowContext(ctx, `SELECT `+syncJobColumns+` FROM sync_jobs ORDER BY id DESC LIMIT 1`)
	j, err := scanSyncJob(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &j, nil
}

// ListSyncJobs returns the most recent sync jobs ordered by id DESC.
func (s *Store) ListSyncJobs(ctx context.Context, limit int) ([]SyncJob, error) {
	if limit <= 0 || limit > 100 {
		limit = 20
	}
	rows, err := s.db.QueryContext(ctx, `SELECT `+syncJobColumns+` FROM sync_jobs ORDER BY id DESC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	jobs := []SyncJob{}
	for rows.Next() {
		j, err := scanSyncJob(rows)
		if err != nil {
			return nil, err
		}
		jobs = append(jobs, j)
	}
	return jobs, rows.Err()
}

// ClaimNextSyncJob atomically claims the oldest queued sync job in FIFO order,
// transitioning it to 'running' and setting started_at. If a queued job already had
// cancel_requested = 1, it is transitioned directly to 'cancelled' and the next
// queued job is inspected. Returns nil, nil when no queued job is available.
func (s *Store) ClaimNextSyncJob(ctx context.Context, startedAt time.Time) (*SyncJob, error) {
	release, err := s.writeGuard()
	if err != nil {
		return nil, err
	}
	defer release()
	if startedAt.IsZero() {
		startedAt = time.Now().UTC()
	} else {
		startedAt = startedAt.UTC()
	}
	nowStr := startedAt.Format(time.RFC3339)

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()

	for {
		row := tx.QueryRowContext(ctx, `SELECT `+syncJobColumns+` FROM sync_jobs WHERE status = 'queued' ORDER BY id ASC LIMIT 1`)
		job, err := scanSyncJob(row)
		if errors.Is(err, sql.ErrNoRows) {
			if err := tx.Commit(); err != nil {
				return nil, err
			}
			return nil, nil
		}
		if err != nil {
			return nil, err
		}
		if job.CancelRequested {
			if _, err := tx.ExecContext(ctx, `UPDATE sync_jobs SET status = 'cancelled', finished_at = ? WHERE id = ?`, nowStr, job.ID); err != nil {
				return nil, err
			}
			continue
		}
		if _, err := tx.ExecContext(ctx, `UPDATE sync_jobs SET status = 'running', started_at = ? WHERE id = ?`, nowStr, job.ID); err != nil {
			return nil, err
		}
		if err := tx.Commit(); err != nil {
			return nil, err
		}
		job.Status = SyncJobStatusRunning
		st := startedAt
		job.StartedAt = &st
		return &job, nil
	}
}

// CheckpointSyncJob updates progress counters on a running sync job.
func (s *Store) CheckpointSyncJob(ctx context.Context, id int64, totalEstimated, processed, confirmed, skipped, errCount int) error {
	if id <= 0 {
		return fmt.Errorf("invalid sync job ID")
	}
	release, err := s.writeGuard()
	if err != nil {
		return err
	}
	defer release()
	_, err = s.db.ExecContext(ctx, `UPDATE sync_jobs SET
		total_estimated = ?, processed_count = ?, confirmed_count = ?, skipped_count = ?, error_count = ?
		WHERE id = ?`,
		totalEstimated, processed, confirmed, skipped, errCount, id,
	)
	return err
}

// FinishSyncJob records the terminal status, optional sync_run_id (as sql.NullInt64),
// error message, and finished_at timestamp for a sync job.
func (s *Store) FinishSyncJob(ctx context.Context, id int64, status string, syncRunID sql.NullInt64, errorMessage string, finishedAt time.Time) error {
	if id <= 0 || !validSyncJobTerminalStatus(status) {
		return fmt.Errorf("invalid sync job finish parameters")
	}
	release, err := s.writeGuard()
	if err != nil {
		return err
	}
	defer release()
	if finishedAt.IsZero() {
		finishedAt = time.Now().UTC()
	} else {
		finishedAt = finishedAt.UTC()
	}
	if syncRunID.Int64 <= 0 {
		syncRunID = sql.NullInt64{}
	}
	_, err = s.db.ExecContext(ctx, `UPDATE sync_jobs SET
		status = ?, sync_run_id = ?, error_message = ?, finished_at = ?
		WHERE id = ?`,
		status, syncRunID, errorMessage, finishedAt.Format(time.RFC3339), id,
	)
	return err
}

// RequestCancelSyncJob marks a queued or running job for cancellation.
// If the job is still 'queued', it immediately transitions to 'cancelled' and sets finished_at.
// If the job is 'running', it sets cancel_requested = 1 so the worker can stop and finalize it.
// Returns the updated job and a boolean indicating whether the job was active (queued or running).
func (s *Store) RequestCancelSyncJob(ctx context.Context, id int64, now time.Time) (*SyncJob, bool, error) {
	if id <= 0 {
		return nil, false, nil
	}
	release, err := s.writeGuard()
	if err != nil {
		return nil, false, err
	}
	defer release()
	if now.IsZero() {
		now = time.Now().UTC()
	} else {
		now = now.UTC()
	}
	nowStr := now.Format(time.RFC3339)

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, false, err
	}
	defer tx.Rollback()

	row := tx.QueryRowContext(ctx, `SELECT `+syncJobColumns+` FROM sync_jobs WHERE id = ?`, id)
	job, err := scanSyncJob(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}

	switch job.Status {
	case SyncJobStatusQueued:
		if _, err := tx.ExecContext(ctx, `UPDATE sync_jobs SET cancel_requested = 1, status = 'cancelled', finished_at = ? WHERE id = ?`, nowStr, id); err != nil {
			return nil, false, err
		}
		job.CancelRequested = true
		job.Status = SyncJobStatusCancelled
		ft := now
		job.FinishedAt = &ft
		if err := tx.Commit(); err != nil {
			return nil, false, err
		}
		return &job, true, nil
	case SyncJobStatusRunning:
		if _, err := tx.ExecContext(ctx, `UPDATE sync_jobs SET cancel_requested = 1 WHERE id = ?`, id); err != nil {
			return nil, false, err
		}
		job.CancelRequested = true
		if err := tx.Commit(); err != nil {
			return nil, false, err
		}
		return &job, true, nil
	default:
		if err := tx.Commit(); err != nil {
			return nil, false, err
		}
		return &job, false, nil
	}
}

// RecoverStaleRunningJobs marks any sync_jobs left in status = 'running' as 'failed'
// (for startup recovery or ungraceful shutdown cleanup).
func (s *Store) RecoverStaleRunningJobs(ctx context.Context, reason string, now time.Time) (int64, error) {
	release, err := s.writeGuard()
	if err != nil {
		return 0, err
	}
	defer release()
	if reason == "" {
		reason = "interrupted by server restart"
	}
	if now.IsZero() {
		now = time.Now().UTC()
	} else {
		now = now.UTC()
	}
	res, err := s.db.ExecContext(ctx, `UPDATE sync_jobs SET
		status = 'failed', error_message = ?, finished_at = ?
		WHERE status = 'running'`,
		reason, now.Format(time.RFC3339),
	)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// RecoverStaleInProgressSyncRuns marks any sync_runs left in status = 'in_progress' as 'failed'
// (for startup recovery after an interrupted reconcile).
func (s *Store) RecoverStaleInProgressSyncRuns(ctx context.Context, now time.Time) (int64, error) {
	release, err := s.writeGuard()
	if err != nil {
		return 0, err
	}
	defer release()
	if now.IsZero() {
		now = time.Now().UTC()
	} else {
		now = now.UTC()
	}
	res, err := s.db.ExecContext(ctx, `UPDATE sync_runs SET
		status = 'failed', finished_at = ?
		WHERE status = 'in_progress'`,
		now.Format(time.RFC3339),
	)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// RecordAutoMatchAttempt persists a deterministic non-match ('abstained', 'low_confidence',
// 'no_candidate', 'ambiguous') into auto_match_attempts. Non-deterministic statuses (such as
// 'error', 'confirmed', 'dry_run') are ignored without error if passed, or rejected if invalid keys.
func (s *Store) RecordAutoMatchAttempt(ctx context.Context, attempt AutoMatchAttempt) error {
	if !ValidLabel(attempt.Provider) || !ValidLabel(attempt.SourceAccount) || !ValidLabel(attempt.TargetAccount) || !ValidBookID(attempt.BookID) {
		return ErrInvalidMappingKey
	}
	if !validDeterministicNonMatchStatus(attempt.LastStatus) {
		return fmt.Errorf("invalid auto match attempt status %q: only deterministic non-matches are recorded", attempt.LastStatus)
	}
	release, err := s.writeGuard()
	if err != nil {
		return err
	}
	defer release()
	if attempt.AttemptedAt.IsZero() {
		attempt.AttemptedAt = time.Now().UTC()
	} else {
		attempt.AttemptedAt = attempt.AttemptedAt.UTC()
	}
	_, err = s.db.ExecContext(ctx, `INSERT INTO auto_match_attempts (
		provider, source_account, book_id, target_account,
		last_status, last_confidence, last_reason, attempted_at
	) VALUES (?, ?, ?, ?, ?, ?, ?, ?)
	ON CONFLICT(provider, source_account, book_id, target_account) DO UPDATE SET
		last_status = excluded.last_status,
		last_confidence = excluded.last_confidence,
		last_reason = excluded.last_reason,
		attempted_at = excluded.attempted_at`,
		attempt.Provider, attempt.SourceAccount, attempt.BookID, attempt.TargetAccount,
		attempt.LastStatus, attempt.LastConfidence, attempt.LastReason, attempt.AttemptedAt.Format(time.RFC3339),
	)
	return err
}

func isAttemptInCooldown(lastStatus string, lastConfidence float64, attemptedAt time.Time, minConf float64, force bool, now time.Time) bool {
	if force {
		return false
	}
	if now.IsZero() {
		now = time.Now().UTC()
	} else {
		now = now.UTC()
	}
	if attemptedAt.Before(now.Add(-AutoMatchAttemptCooldown)) {
		return false
	}
	if lastStatus == "low_confidence" && minConf < lastConfidence {
		return false
	}
	return true
}

// IsAutoMatchAttemptInCooldown returns true if the book has a recorded deterministic
// non-match within the last 30 days, unless bypassed by force=true or a lower
// minConf threshold on a previous 'low_confidence' attempt.
func (s *Store) IsAutoMatchAttemptInCooldown(ctx context.Context, provider, sourceAccount, bookID, targetAccount string, minConf float64, force bool, now time.Time) (bool, error) {
	if !ValidLabel(provider) || !ValidLabel(sourceAccount) || !ValidLabel(targetAccount) || !ValidBookID(bookID) {
		return false, ErrInvalidMappingKey
	}
	if force {
		return false, nil
	}
	var lastStatus string
	var lastConfidence float64
	var attemptedStr string
	err := s.db.QueryRowContext(ctx, `SELECT last_status, last_confidence, attempted_at
		FROM auto_match_attempts
		WHERE provider = ? AND source_account = ? AND book_id = ? AND target_account = ?`,
		provider, sourceAccount, bookID, targetAccount,
	).Scan(&lastStatus, &lastConfidence, &attemptedStr)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	attemptedAt, err := time.Parse(time.RFC3339, attemptedStr)
	if err != nil {
		return false, fmt.Errorf("corrupt attempted_at in auto_match_attempts: %w", err)
	}
	return isAttemptInCooldown(lastStatus, lastConfidence, attemptedAt, minConf, force, now), nil
}

// ListCooldownBookIDs returns the set of book IDs currently in abstention cooldown
// for the given account pair and job parameters, so shelf filtering can skip them
// before calling source.Book(ctx, id).
func (s *Store) ListCooldownBookIDs(ctx context.Context, provider, sourceAccount, targetAccount string, minConf float64, force bool, now time.Time) (map[string]bool, error) {
	if !ValidLabel(provider) || !ValidLabel(sourceAccount) || !ValidLabel(targetAccount) {
		return nil, ErrInvalidMappingKey
	}
	result := make(map[string]bool)
	if force {
		return result, nil
	}
	if now.IsZero() {
		now = time.Now().UTC()
	} else {
		now = now.UTC()
	}
	cutoffStr := now.Add(-AutoMatchAttemptCooldown).Format(time.RFC3339)
	rows, err := s.db.QueryContext(ctx, `SELECT book_id, last_status, last_confidence, attempted_at
		FROM auto_match_attempts
		WHERE provider = ? AND source_account = ? AND target_account = ? AND attempted_at >= ?`,
		provider, sourceAccount, targetAccount, cutoffStr,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var bookID, lastStatus, attemptedStr string
		var lastConfidence float64
		if err := rows.Scan(&bookID, &lastStatus, &lastConfidence, &attemptedStr); err != nil {
			return nil, err
		}
		attemptedAt, err := time.Parse(time.RFC3339, attemptedStr)
		if err != nil {
			return nil, fmt.Errorf("corrupt attempted_at in auto_match_attempts: %w", err)
		}
		if isAttemptInCooldown(lastStatus, lastConfidence, attemptedAt, minConf, force, now) {
			result[bookID] = true
		}
	}
	return result, rows.Err()
}
