package companion

import (
	"context"
	"errors"
	"fmt"
	"time"
)

// ApplyTarget abstracts target provider capabilities for status mutation and verification.
type ApplyTarget interface {
	PreviewTarget
	ApplyStatusMutation(ctx context.Context, req StatusMutationRequest) (StatusMutationOutcome, error)
}

var _ ApplyTarget = (*Hardcover)(nil)

// ApplySyncOptions controls execution parameters for applying status sync.
type ApplySyncOptions struct {
	PolicyVersion     string
	AllowPrivateSync  bool
	DeclaredSourceVID string
	Now               time.Time
}

// ApplyResult describes the outcome of applying status synchronization.
type ApplyResult struct {
	Action           string            `json:"action"` // "applied", "no-op", "conflict", "hold", "uncertain", "failed"
	Message          string            `json:"message"`
	IntentID         int64             `json:"intent_id,omitempty"`
	ReceiptID        int64             `json:"receipt_id,omitempty"`
	Provider         string            `json:"provider"`
	SourceAccount    string            `json:"source_account"`
	BookID           string            `json:"book_id"`
	TargetAccount    string            `json:"target_account"`
	WorkID           int64             `json:"work_id"`
	EditionID        int64             `json:"edition_id,omitempty"`
	PriorStatusID    int               `json:"prior_status_id,omitempty"`
	PriorStatus      string            `json:"prior_status,omitempty"`
	AppliedStatusID  int               `json:"applied_status_id,omitempty"`
	AppliedStatus    string            `json:"applied_status,omitempty"`
	ConflictEvidence *ConflictEvidence `json:"conflict_evidence,omitempty"`
	Reason           string            `json:"reason,omitempty"`
	Reconciled       bool              `json:"reconciled,omitempty"`
}

// ApplySync safely applies an evaluated sync proposal to the target provider.
// It persists durable write intent before send, applies only approved fields,
// reads back target status, writes immutable audit receipts and updates the accepted baseline.
// Any interrupted or uncertain intent from a prior run is reconciled first before new mutations.
func ApplySync(
	ctx context.Context,
	store *Store,
	source PreviewSource,
	target ApplyTarget,
	provider, sourceAccount, bookID, targetAccount string,
	opts ApplySyncOptions,
) (ApplyResult, error) {
	if !ValidLabel(provider) || !ValidLabel(sourceAccount) || !ValidLabel(targetAccount) || !ValidBookID(bookID) {
		return ApplyResult{}, ErrInvalidMappingKey
	}
	now := opts.Now
	if now.IsZero() {
		now = time.Now().UTC()
	}

	// 1. Reconcile active/uncertain intent from prior run if any (US7 restart recovery)
	activeIntent, err := store.GetActiveIntent(ctx, provider, sourceAccount, bookID, targetAccount)
	if err != nil {
		return ApplyResult{}, fmt.Errorf("failed to check active intent: %w", err)
	}
	if activeIntent != nil {
		tgtStatus, err := target.GetUserBookStatus(ctx, activeIntent.WorkID)
		if err != nil {
			return ApplyResult{
				Action:   "uncertain",
				Message:  fmt.Sprintf("cannot reconcile prior %s intent #%d: target query failed: %v", activeIntent.State, activeIntent.ID, err),
				IntentID: activeIntent.ID,
			}, fmt.Errorf("prior intent #%d is %s; cannot query target to reconcile: %w", activeIntent.ID, activeIntent.State, err)
		}
		if tgtStatus != nil && tgtStatus.StatusID == activeIntent.ProposedStatusID {
			// Remote mutation landed during prior attempt!
			_ = store.UpdateIntentState(ctx, activeIntent.ID, "applied", "", now)
			receiptID, _ := store.RecordReceipt(ctx, SyncReceipt{
				IntentID:       activeIntent.ID,
				Provider:       provider,
				SourceAccount:  sourceAccount,
				BookID:         bookID,
				TargetAccount:  targetAccount,
				WorkID:         activeIntent.WorkID,
				EditionID:      activeIntent.EditionID,
				TargetStatusID: tgtStatus.StatusID,
				AppliedAt:      now,
			})
			_ = store.SaveStatusBaseline(ctx, StatusBaseline{
				Provider:          provider,
				SourceAccount:     sourceAccount,
				BookID:            bookID,
				TargetAccount:     targetAccount,
				WorkID:            activeIntent.WorkID,
				EditionID:         activeIntent.EditionID,
				SourceStatusID:    activeIntent.ProposedStatusID,
				TargetStatusID:    tgtStatus.StatusID,
				PolicyVersion:     activeIntent.PolicyVersion,
				SourceFingerprint: activeIntent.SourceFingerprint,
				TargetFingerprint: activeIntent.TargetFingerprint,
				LastSyncedAt:      now,
			})
			return ApplyResult{
				Action:          "applied",
				Message:         fmt.Sprintf("reconciled prior %s intent #%d: remote mutation already applied on target", activeIntent.State, activeIntent.ID),
				IntentID:        activeIntent.ID,
				ReceiptID:       receiptID,
				Provider:        provider,
				SourceAccount:   sourceAccount,
				BookID:          bookID,
				TargetAccount:   targetAccount,
				WorkID:          activeIntent.WorkID,
				EditionID:       activeIntent.EditionID,
				AppliedStatusID: tgtStatus.StatusID,
				AppliedStatus:   HardcoverStatusName(tgtStatus.StatusID),
				Reconciled:      true,
			}, nil
		}
		// Did not land; mark failed so fresh evaluation proceeds cleanly
		_ = store.UpdateIntentState(ctx, activeIntent.ID, "failed", "reconciled after restart: target status did not match proposed mutation", now)
	}

	// 2. Perform three-way preview evaluation
	proposal, err := PreviewSync(ctx, store, source, target, provider, sourceAccount, bookID, targetAccount, PreviewSyncOptions{
		PolicyVersion:     opts.PolicyVersion,
		AllowPrivateSync:  opts.AllowPrivateSync,
		DeclaredSourceVID: opts.DeclaredSourceVID,
		Now:               now,
	})
	if err != nil {
		return ApplyResult{}, err
	}

	baseResult := ApplyResult{
		Provider:      provider,
		SourceAccount: sourceAccount,
		BookID:        bookID,
		TargetAccount: targetAccount,
		WorkID:        proposal.WorkID,
		EditionID:     proposal.EditionID,
		PriorStatusID: proposal.CurrentStatusID,
		PriorStatus:   proposal.CurrentStatus,
		Message:       proposal.Message,
		Reason:        proposal.Reason,
	}

	// 3. Handle non-mutating proposals
	switch proposal.Action {
	case ActionNoOp:
		baseResult.Action = "no-op"
		baseResult.AppliedStatusID = proposal.ProposedStatusID
		baseResult.AppliedStatus = proposal.ProposedStatus
		// If bootstrap with matching target and no prior baseline, record initial baseline
		existingBaseline, _ := store.GetStatusBaseline(ctx, provider, sourceAccount, bookID, targetAccount)
		if existingBaseline == nil {
			_ = store.SaveStatusBaseline(ctx, StatusBaseline{
				Provider:          provider,
				SourceAccount:     sourceAccount,
				BookID:            bookID,
				TargetAccount:     targetAccount,
				WorkID:            proposal.WorkID,
				EditionID:         proposal.EditionID,
				SourceStatusID:    proposal.ProposedStatusID,
				TargetStatusID:    proposal.ProposedStatusID,
				PolicyVersion:     proposal.PolicyVersion,
				SourceFingerprint: proposal.SourceFingerprint,
				TargetFingerprint: proposal.TargetFingerprint,
				LastSyncedAt:      now,
			})
		}
		return baseResult, nil

	case ActionConflict, ActionHold:
		baseResult.Action = string(proposal.Action)
		baseResult.ConflictEvidence = proposal.ConflictEvidence
		return baseResult, nil

	case ActionCreate, ActionUpdate:
		// Proceed to controlled mutation below
	default:
		baseResult.Action = "hold"
		baseResult.Message = "unrecognized proposal action"
		return baseResult, nil
	}

	// 4. Persist durable intent before send (US7)
	intent := SyncIntent{
		Provider:          provider,
		SourceAccount:     sourceAccount,
		BookID:            bookID,
		TargetAccount:     targetAccount,
		WorkID:            proposal.WorkID,
		EditionID:         proposal.EditionID,
		ProposedStatusID:  proposal.ProposedStatusID,
		PolicyVersion:     proposal.PolicyVersion,
		SourceFingerprint: proposal.SourceFingerprint,
		TargetFingerprint: proposal.TargetFingerprint,
		State:             "pending",
		CreatedAt:         now,
		UpdatedAt:         now,
	}
	if proposal.PrivacySettingID != 0 {
		intent.PrivacySettingID = &proposal.PrivacySettingID
	}
	intentID, err := store.RecordIntent(ctx, intent)
	if err != nil {
		return ApplyResult{}, fmt.Errorf("failed to persist sync intent: %w", err)
	}
	baseResult.IntentID = intentID

	// Mark in-flight
	if err := store.UpdateIntentState(ctx, intentID, "in_flight", "", now); err != nil {
		return ApplyResult{}, fmt.Errorf("failed to mark intent in_flight: %w", err)
	}

	// 5. Apply only approved status fields (US1)
	mutationReq := StatusMutationRequest{
		WorkID:    proposal.WorkID,
		EditionID: proposal.EditionID,
		StatusID:  proposal.ProposedStatusID,
	}
	_, err = target.ApplyStatusMutation(ctx, mutationReq)
	if err != nil {
		if errors.Is(err, ErrUncertainStatusMutation) {
			_ = store.UpdateIntentState(ctx, intentID, "uncertain", err.Error(), now)
			baseResult.Action = "uncertain"
			baseResult.Message = "status mutation timed out or uncertain; preserved as uncertain intent for restart reconciliation"
			baseResult.AppliedStatusID = proposal.ProposedStatusID
			baseResult.AppliedStatus = proposal.ProposedStatus
			return baseResult, err
		}
		_ = store.UpdateIntentState(ctx, intentID, "failed", err.Error(), now)
		baseResult.Action = "failed"
		baseResult.Message = fmt.Sprintf("status mutation failed: %v", err)
		return baseResult, err
	}

	// 6. Read back from target to verify applied state
	verifiedStatus, err := target.GetUserBookStatus(ctx, proposal.WorkID)
	if err != nil {
		_ = store.UpdateIntentState(ctx, intentID, "uncertain", "read-back verification failed: "+err.Error(), now)
		baseResult.Action = "uncertain"
		baseResult.Message = "status mutation sent but read-back verification failed; held as uncertain"
		baseResult.AppliedStatusID = proposal.ProposedStatusID
		baseResult.AppliedStatus = proposal.ProposedStatus
		return baseResult, err
	}
	if verifiedStatus == nil || verifiedStatus.StatusID != proposal.ProposedStatusID {
		_ = store.UpdateIntentState(ctx, intentID, "uncertain", "read-back status did not reflect applied change", now)
		baseResult.Action = "uncertain"
		baseResult.Message = "read-back status did not match proposed status"
		return baseResult, fmt.Errorf("read-back status verification failed")
	}

	// 7. Record immutable receipt
	receiptID, err := store.RecordReceipt(ctx, SyncReceipt{
		IntentID:       intentID,
		Provider:       provider,
		SourceAccount:  sourceAccount,
		BookID:         bookID,
		TargetAccount:  targetAccount,
		WorkID:         proposal.WorkID,
		EditionID:      proposal.EditionID,
		TargetStatusID: verifiedStatus.StatusID,
		AppliedAt:      now,
	})
	if err != nil {
		return ApplyResult{}, fmt.Errorf("failed to record sync receipt: %w", err)
	}
	baseResult.ReceiptID = receiptID

	// 8. Update accepted baseline
	newBaseline := StatusBaseline{
		Provider:          provider,
		SourceAccount:     sourceAccount,
		BookID:            bookID,
		TargetAccount:     targetAccount,
		WorkID:            proposal.WorkID,
		EditionID:         proposal.EditionID,
		SourceStatusID:    proposal.ProposedStatusID,
		TargetStatusID:    verifiedStatus.StatusID,
		PolicyVersion:     proposal.PolicyVersion,
		SourceFingerprint: proposal.SourceFingerprint,
		TargetFingerprint: proposal.TargetFingerprint,
		LastSyncedAt:      now,
	}
	if err := store.SaveStatusBaseline(ctx, newBaseline); err != nil {
		return ApplyResult{}, fmt.Errorf("failed to update status baseline: %w", err)
	}

	// 9. Mark intent applied
	_ = store.UpdateIntentState(ctx, intentID, "applied", "", now)

	baseResult.Action = "applied"
	baseResult.AppliedStatusID = verifiedStatus.StatusID
	baseResult.AppliedStatus = HardcoverStatusName(verifiedStatus.StatusID)
	baseResult.Message = fmt.Sprintf("status successfully synchronized to %s (%d)", baseResult.AppliedStatus, verifiedStatus.StatusID)
	return baseResult, nil
}
