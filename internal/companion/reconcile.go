package companion

import (
	"context"
	"encoding/json"
	"fmt"
	"time"
)

// ReconcileOptions specifies parameters for multi-book reconciliation.
type ReconcileOptions struct {
	Provider          string
	SourceAccount     string
	TargetAccount     string
	PolicyVersion     string
	AllowPrivateSync  bool
	DeclaredSourceVID string
	AdoptSource       bool
	DryRun            bool
	Now               time.Time
}

// ReconcileSummary captures the full results and statistics of a reconciliation run.
type ReconcileSummary struct {
	RunID         int64         `json:"run_id,omitempty"`
	SourceAccount string        `json:"source_account"`
	TargetAccount string        `json:"target_account"`
	PolicyVersion string        `json:"policy_version"`
	Status        string        `json:"status"` // "completed", "partial", "failed"
	TotalMappings int           `json:"total_mappings"`
	AppliedCount  int           `json:"applied_count"`
	NoOpCount     int           `json:"noop_count"`
	ConflictCount int           `json:"conflict_count"`
	HoldCount     int           `json:"hold_count"`
	ErrorCount    int           `json:"error_count"`
	Applied       []ApplyResult `json:"applied,omitempty"`
	Holds         []SyncHold    `json:"holds,omitempty"`
	StartedAt     time.Time     `json:"started_at"`
	FinishedAt    time.Time     `json:"finished_at"`
	DryRun        bool          `json:"dry_run,omitempty"`
}

// ReconcileAccount executes three-way continuous reconciliation across all confirmed mappings
// for an account pair, recording monotonic run history and preserving holds without stopping on partial errors.
func ReconcileAccount(
	ctx context.Context,
	store *Store,
	source PreviewSource,
	target ApplyTarget,
	opts ReconcileOptions,
) (ReconcileSummary, error) {
	if opts.Provider == "" {
		opts.Provider = "weread"
	}
	if opts.PolicyVersion == "" {
		opts.PolicyVersion = DefaultPolicyVersion
	}
	if !ValidLabel(opts.Provider) || !ValidLabel(opts.SourceAccount) || !ValidLabel(opts.TargetAccount) {
		return ReconcileSummary{}, ErrInvalidMappingKey
	}

	startedAt := opts.Now
	if startedAt.IsZero() {
		startedAt = time.Now().UTC()
	}

	summary := ReconcileSummary{
		SourceAccount: opts.SourceAccount,
		TargetAccount: opts.TargetAccount,
		PolicyVersion: opts.PolicyVersion,
		StartedAt:     startedAt,
		DryRun:        opts.DryRun,
		Applied:       []ApplyResult{},
		Holds:         []SyncHold{},
	}

	// 1. Upfront fingerprint verification
	sourceFP, err := source.ResolveAccountFingerprint(ctx, opts.DeclaredSourceVID)
	if err != nil {
		return summary, fmt.Errorf("source account identity resolution failed: %w", err)
	}
	if _, err := store.VerifyOrBindAccountFingerprint(ctx, opts.Provider, opts.SourceAccount, sourceFP, startedAt); err != nil {
		return summary, fmt.Errorf("source account identity mismatch: %w", err)
	}

	targetFP, err := target.ResolveAccountFingerprint(ctx)
	if err != nil {
		return summary, fmt.Errorf("target account identity resolution failed: %w", err)
	}
	if _, err := store.VerifyOrBindAccountFingerprint(ctx, "hardcover", opts.TargetAccount, targetFP, startedAt); err != nil {
		return summary, fmt.Errorf("target account identity mismatch: %w", err)
	}

	// 2. Query all confirmed mappings for this account pair
	mappings, err := store.ListMappingsForAccount(ctx, opts.Provider, opts.SourceAccount, opts.TargetAccount)
	if err != nil {
		return summary, fmt.Errorf("failed to list mappings for account: %w", err)
	}
	summary.TotalMappings = len(mappings)

	// 3. Create monotonic sync run record if not dry-run
	var runID int64
	if !opts.DryRun {
		runID, err = store.CreateSyncRun(ctx, opts.SourceAccount, opts.TargetAccount, opts.PolicyVersion, startedAt)
		if err != nil {
			return summary, fmt.Errorf("failed to create sync run: %w", err)
		}
		summary.RunID = runID
	}

	// 4. Process each mapping with partial failure isolation (US10)
	for _, m := range mappings {
		if opts.DryRun {
			proposal, err := PreviewSync(ctx, store, source, target, m.Provider, m.SourceAccount, m.BookID, m.TargetAccount, PreviewSyncOptions{
				PolicyVersion:    opts.PolicyVersion,
				AllowPrivateSync: opts.AllowPrivateSync,
				AdoptSource:      opts.AdoptSource,
				Now:              startedAt,
			})
			if err != nil {
				summary.ErrorCount++
				continue
			}
			switch proposal.Action {
			case ActionCreate, ActionUpdate:
				summary.AppliedCount++
				summary.Applied = append(summary.Applied, ApplyResult{
					Action:          string(proposal.Action),
					Message:         proposal.Message,
					Provider:        m.Provider,
					SourceAccount:   m.SourceAccount,
					BookID:          m.BookID,
					TargetAccount:   m.TargetAccount,
					WorkID:          m.WorkID,
					EditionID:       m.EditionID,
					PriorStatusID:   proposal.CurrentStatusID,
					PriorStatus:     proposal.CurrentStatus,
					AppliedStatusID: proposal.ProposedStatusID,
					AppliedStatus:   proposal.ProposedStatus,
				})
			case ActionNoOp:
				summary.NoOpCount++
			case ActionConflict:
				summary.ConflictCount++
				var evStr string
				if proposal.ConflictEvidence != nil {
					b, _ := json.Marshal(proposal.ConflictEvidence)
					evStr = string(b)
				}
				summary.Holds = append(summary.Holds, SyncHold{
					Provider:      m.Provider,
					SourceAccount: m.SourceAccount,
					BookID:        m.BookID,
					TargetAccount: m.TargetAccount,
					WorkID:        m.WorkID,
					Reason:        proposal.Reason,
					Message:       proposal.Message,
					Evidence:      evStr,
					CreatedAt:     startedAt,
				})
			case ActionHold:
				summary.HoldCount++
				summary.Holds = append(summary.Holds, SyncHold{
					Provider:      m.Provider,
					SourceAccount: m.SourceAccount,
					BookID:        m.BookID,
					TargetAccount: m.TargetAccount,
					WorkID:        m.WorkID,
					Reason:        proposal.Reason,
					Message:       proposal.Message,
					CreatedAt:     startedAt,
				})
			}
			continue
		}

		// Live apply with write intent and receipt
		res, err := ApplySync(ctx, store, source, target, m.Provider, m.SourceAccount, m.BookID, m.TargetAccount, ApplySyncOptions{
			PolicyVersion:     opts.PolicyVersion,
			AllowPrivateSync:  opts.AllowPrivateSync,
			DeclaredSourceVID: opts.DeclaredSourceVID,
			AdoptSource:       opts.AdoptSource,
			Now:               startedAt,
		})
		if err != nil {
			summary.ErrorCount++
			if res.Action == "uncertain" {
				summary.HoldCount++
				hold := SyncHold{
					RunID:         runID,
					Provider:      m.Provider,
					SourceAccount: m.SourceAccount,
					BookID:        m.BookID,
					TargetAccount: m.TargetAccount,
					WorkID:        m.WorkID,
					Reason:        "uncertain_timeout",
					Message:       err.Error(),
					CreatedAt:     startedAt,
				}
				if hID, hErr := store.RecordSyncHold(ctx, hold); hErr == nil {
					hold.ID = hID
				}
				summary.Holds = append(summary.Holds, hold)
			}
			continue
		}

		switch res.Action {
		case "applied":
			summary.AppliedCount++
			summary.Applied = append(summary.Applied, res)
		case "no-op":
			summary.NoOpCount++
		case "conflict":
			summary.ConflictCount++
			var evStr string
			if res.ConflictEvidence != nil {
				b, _ := json.Marshal(res.ConflictEvidence)
				evStr = string(b)
			}
			hold := SyncHold{
				RunID:         runID,
				Provider:      m.Provider,
				SourceAccount: m.SourceAccount,
				BookID:        m.BookID,
				TargetAccount: m.TargetAccount,
				WorkID:        m.WorkID,
				Reason:        res.Reason,
				Message:       res.Message,
				Evidence:      evStr,
				CreatedAt:     startedAt,
			}
			if hID, hErr := store.RecordSyncHold(ctx, hold); hErr == nil {
				hold.ID = hID
			}
			summary.Holds = append(summary.Holds, hold)
		case "hold":
			summary.HoldCount++
			hold := SyncHold{
				RunID:         runID,
				Provider:      m.Provider,
				SourceAccount: m.SourceAccount,
				BookID:        m.BookID,
				TargetAccount: m.TargetAccount,
				WorkID:        m.WorkID,
				Reason:        res.Reason,
				Message:       res.Message,
				CreatedAt:     startedAt,
			}
			if hID, hErr := store.RecordSyncHold(ctx, hold); hErr == nil {
				hold.ID = hID
			}
			summary.Holds = append(summary.Holds, hold)
		}
	}

	finishedAt := time.Now().UTC()
	summary.FinishedAt = finishedAt

	// 5. Compute run status
	switch {
	case summary.ErrorCount == 0 && summary.ConflictCount == 0 && summary.HoldCount == 0:
		summary.Status = "completed"
	case summary.AppliedCount > 0 || summary.NoOpCount > 0:
		summary.Status = "partial"
	default:
		summary.Status = "failed"
	}

	if !opts.DryRun {
		_ = store.UpdateSyncRun(
			ctx,
			runID,
			summary.Status,
			summary.TotalMappings,
			summary.AppliedCount,
			summary.NoOpCount,
			summary.ConflictCount,
			summary.HoldCount,
			summary.ErrorCount,
			finishedAt,
		)
	}

	return summary, nil
}
