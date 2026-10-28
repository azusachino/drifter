package companion

import (
	"context"
	"fmt"
	"time"
)

// ProposalAction represents the evaluated dry-run action for a mapping.
type ProposalAction string

const (
	ActionCreate   ProposalAction = "create"
	ActionUpdate   ProposalAction = "update"
	ActionNoOp     ProposalAction = "no-op"
	ActionConflict ProposalAction = "conflict"
	ActionHold     ProposalAction = "hold"
)

const DefaultPolicyVersion = "v1"

// Machine-readable reason codes for sync proposals.
const (
	ReasonTargetMissing              = "target_record_missing"
	ReasonAlreadyMatching            = "already_matching"
	ReasonDifferingTargetOnBootstrap = "differing_target_on_bootstrap"
	ReasonSourceAdvanced             = "source_progress_advanced"
	ReasonUnchangedSinceBaseline     = "unchanged_since_baseline"
	ReasonManualTargetEdit           = "manual_target_edit"
	ReasonConvergentAgreement        = "convergent_agreement"
	ReasonDivergentChanges           = "divergent_status_changes"
	ReasonTargetDisappeared          = "target_record_disappeared"
	ReasonCompetingSourceAuthority   = "competing_source_authority"
	ReasonUnmappedSourceStatus       = "unmapped_source_status"
	ReasonPrivateBookExcluded        = "private_book_excluded"
	ReasonPublicTargetPrivacy        = "public_target_privacy_conflict"
	ReasonPolicyVersionMismatch      = "policy_version_mismatch"
	ReasonInvalidTargetStatus        = "invalid_target_status"
)

// HardcoverStatusName returns the canonical Hardcover status label for an ID.
func HardcoverStatusName(statusID int) string {
	switch statusID {
	case HardcoverStatusWantToRead:
		return "Want to Read"
	case HardcoverStatusCurrentlyReading:
		return "Currently Reading"
	case HardcoverStatusRead:
		return "Read"
	case HardcoverStatusDidNotFinish:
		return "Did Not Finish"
	case HardcoverStatusOnHold:
		return "On Hold"
	default:
		return "Unknown"
	}
}

// SourceStatusToHardcover maps WeRead progress status to Hardcover status ID.
func SourceStatusToHardcover(sourceStatus string) int {
	switch sourceStatus {
	case "read":
		return HardcoverStatusRead
	case "reading":
		return HardcoverStatusCurrentlyReading
	default:
		return 0
	}
}

// StatusBaseline records the last accepted sync state between source and target for a book.
type StatusBaseline struct {
	Provider          string    `json:"provider"`
	SourceAccount     string    `json:"source_account"`
	BookID            string    `json:"book_id"`
	TargetAccount     string    `json:"target_account"`
	WorkID            int64     `json:"work_id"`
	EditionID         int64     `json:"edition_id,omitempty"`
	SourceStatusID    int       `json:"source_status_id"`
	TargetStatusID    int       `json:"target_status_id"`
	PolicyVersion     string    `json:"policy_version"`
	SourceFingerprint string    `json:"source_fingerprint"`
	TargetFingerprint string    `json:"target_fingerprint"`
	LastSyncedAt      time.Time `json:"last_synced_at"`
}

func (b StatusBaseline) valid() bool {
	return ValidLabel(b.Provider) && ValidLabel(b.SourceAccount) && ValidLabel(b.TargetAccount) &&
		ValidBookID(b.BookID) && b.WorkID > 0 && b.EditionID >= 0 &&
		ValidHardcoverStatus(b.SourceStatusID) && ValidHardcoverStatus(b.TargetStatusID) &&
		b.PolicyVersion != "" && ValidFingerprint(b.SourceFingerprint) && ValidFingerprint(b.TargetFingerprint) &&
		!b.LastSyncedAt.IsZero()
}

// TargetAuthority defines approved status write authority for a target work.
type TargetAuthority struct {
	TargetAccount      string    `json:"target_account"`
	WorkID             int64     `json:"work_id"`
	AuthorizedProvider string    `json:"authorized_provider"`
	AuthorizedAccount  string    `json:"authorized_account"`
	AuthorizedBookID   string    `json:"authorized_book_id"`
	PolicyVersion      string    `json:"policy_version"`
	PrivacyMode        string    `json:"privacy_mode"` // "default", "private-target", "exclude"
	CreatedAt          time.Time `json:"created_at"`
	UpdatedAt          time.Time `json:"updated_at"`
}

func (a TargetAuthority) valid() bool {
	return ValidLabel(a.TargetAccount) && a.WorkID > 0 &&
		ValidLabel(a.AuthorizedProvider) && ValidLabel(a.AuthorizedAccount) && ValidBookID(a.AuthorizedBookID) &&
		a.PolicyVersion != "" && (a.PrivacyMode == "default" || a.PrivacyMode == "private-target" || a.PrivacyMode == "exclude") &&
		!a.CreatedAt.IsZero() && !a.UpdatedAt.IsZero()
}

// SourceEvidence freezes the observed source state for conflict inspection.
type SourceEvidence struct {
	Provider     string `json:"provider"`
	Account      string `json:"account"`
	Fingerprint  string `json:"fingerprint"`
	BookID       string `json:"book_id"`
	Title        string `json:"title,omitempty"`
	Percent      int    `json:"percent"`
	Started      bool   `json:"started"`
	FinishedAt   int64  `json:"finished_at,omitempty"`
	SourceStatus string `json:"source_status"`
	Private      bool   `json:"private"`
}

// TargetEvidence freezes the observed target state for conflict inspection.
type TargetEvidence struct {
	Provider         string              `json:"provider"`
	Account          string              `json:"account"`
	Fingerprint      string              `json:"fingerprint"`
	WorkID           int64               `json:"work_id"`
	EditionID        *int64              `json:"edition_id,omitempty"`
	Exists           bool                `json:"exists"`
	StatusID         int                 `json:"status_id,omitempty"`
	StatusName       string              `json:"status_name,omitempty"`
	PrivacySettingID *int                `json:"privacy_setting_id,omitempty"`
	HasRating        bool                `json:"has_rating"`
	HasReview        bool                `json:"has_review"`
	ReadCount        int                 `json:"read_count"`
	UserBookReads    []UserBookReadEntry `json:"user_book_reads,omitempty"`
}

// BaselineEvidence freezes the baseline facts for conflict inspection.
type BaselineEvidence struct {
	SourceStatusID int       `json:"source_status_id"`
	TargetStatusID int       `json:"target_status_id"`
	PolicyVersion  string    `json:"policy_version"`
	LastSyncedAt   time.Time `json:"last_synced_at"`
}

// ConflictEvidence records immutable evidence of why a proposed sync cannot proceed automatically.
type ConflictEvidence struct {
	ConflictType string            `json:"conflict_type"`
	Message      string            `json:"message"`
	ObservedAt   time.Time         `json:"observed_at"`
	Source       SourceEvidence    `json:"source"`
	Target       TargetEvidence    `json:"target"`
	Baseline     *BaselineEvidence `json:"baseline,omitempty"`
}

// SyncProposal is the read-only output of status evaluation.
type SyncProposal struct {
	Action            ProposalAction    `json:"action"`
	Reason            string            `json:"reason"`
	Message           string            `json:"message"`
	PolicyVersion     string            `json:"policy_version"`
	Provider          string            `json:"provider"`
	SourceAccount     string            `json:"source_account"`
	SourceFingerprint string            `json:"source_fingerprint"`
	BookID            string            `json:"book_id"`
	SourceTitle       string            `json:"source_title,omitempty"`
	SourceStatus      string            `json:"source_status"`
	TargetAccount     string            `json:"target_account"`
	TargetFingerprint string            `json:"target_fingerprint"`
	WorkID            int64             `json:"work_id"`
	EditionID         int64             `json:"edition_id,omitempty"`
	CurrentStatusID   int               `json:"current_status_id,omitempty"`
	CurrentStatus     string            `json:"current_status,omitempty"`
	ProposedStatusID  int               `json:"proposed_status_id,omitempty"`
	ProposedStatus    string            `json:"proposed_status,omitempty"`
	PrivacySettingID  int               `json:"privacy_setting_id,omitempty"`
	LastReadDate      *string           `json:"last_read_date,omitempty"`
	AuthorityStatus   string            `json:"authority_status"`
	ConflictEvidence  *ConflictEvidence `json:"conflict_evidence,omitempty"`
}

// StatusEvaluationParams bundles the complete observation and state inputs for three-way evaluation.
type StatusEvaluationParams struct {
	Provider          string
	SourceAccount     string
	SourceFingerprint string
	BookID            string
	SourceBook        Book
	SourceProgress    Progress
	IsPrivate         bool

	TargetAccount     string
	TargetFingerprint string
	WorkID            int64
	EditionID         int64
	TargetStatus      *UserBookStatus

	Baseline             *StatusBaseline
	Authority            *TargetAuthority
	HasCompetingMappings bool

	PolicyVersion    string
	AllowPrivateSync bool
	AdoptSource      bool
	Now              time.Time
}

// EvaluateStatus performs pure, three-way status evaluation without remote writes or mutations.
func EvaluateStatus(p StatusEvaluationParams) SyncProposal {
	if p.PolicyVersion == "" {
		p.PolicyVersion = DefaultPolicyVersion
	}
	if p.Now.IsZero() {
		p.Now = time.Now().UTC()
	}

	proposedStatusID := SourceStatusToHardcover(p.SourceProgress.Status)

	var lastReadDate *string
	if proposedStatusID == HardcoverStatusRead && p.SourceProgress.FinishedAt > 0 {
		dateStr := time.Unix(p.SourceProgress.FinishedAt, 0).UTC().Format("2006-01-02")
		lastReadDate = &dateStr
	}

	prop := SyncProposal{
		PolicyVersion:     p.PolicyVersion,
		Provider:          p.Provider,
		SourceAccount:     p.SourceAccount,
		SourceFingerprint: p.SourceFingerprint,
		BookID:            p.BookID,
		SourceTitle:       p.SourceBook.Title,
		SourceStatus:      p.SourceProgress.Status,
		TargetAccount:     p.TargetAccount,
		TargetFingerprint: p.TargetFingerprint,
		WorkID:            p.WorkID,
		EditionID:         p.EditionID,
		ProposedStatusID:  proposedStatusID,
		ProposedStatus:    HardcoverStatusName(proposedStatusID),
		LastReadDate:      lastReadDate,
		AuthorityStatus:   "authorized",
	}

	if p.TargetStatus != nil {
		prop.CurrentStatusID = p.TargetStatus.StatusID
		prop.CurrentStatus = HardcoverStatusName(p.TargetStatus.StatusID)
	}

	makeConflict := func(conflictType, reason, message string) SyncProposal {
		targetEv := TargetEvidence{
			Provider:    "hardcover",
			Account:     p.TargetAccount,
			Fingerprint: p.TargetFingerprint,
			WorkID:      p.WorkID,
			Exists:      p.TargetStatus != nil,
		}
		if p.TargetStatus != nil {
			targetEv.EditionID = p.TargetStatus.EditionID
			targetEv.StatusID = p.TargetStatus.StatusID
			targetEv.StatusName = HardcoverStatusName(p.TargetStatus.StatusID)
			targetEv.PrivacySettingID = p.TargetStatus.PrivacySettingID
			targetEv.HasRating = p.TargetStatus.Rating != nil
			targetEv.HasReview = p.TargetStatus.Review != nil && *p.TargetStatus.Review != ""
			targetEv.ReadCount = len(p.TargetStatus.UserBookReads)
			targetEv.UserBookReads = p.TargetStatus.UserBookReads
		}
		sourceEv := SourceEvidence{
			Provider:     p.Provider,
			Account:      p.SourceAccount,
			Fingerprint:  p.SourceFingerprint,
			BookID:       p.BookID,
			Title:        p.SourceBook.Title,
			Percent:      p.SourceProgress.Percent,
			Started:      p.SourceProgress.Started,
			FinishedAt:   p.SourceProgress.FinishedAt,
			SourceStatus: p.SourceProgress.Status,
			Private:      p.IsPrivate,
		}
		var baseEv *BaselineEvidence
		if p.Baseline != nil {
			baseEv = &BaselineEvidence{
				SourceStatusID: p.Baseline.SourceStatusID,
				TargetStatusID: p.Baseline.TargetStatusID,
				PolicyVersion:  p.Baseline.PolicyVersion,
				LastSyncedAt:   p.Baseline.LastSyncedAt,
			}
		}
		prop.Action = ActionConflict
		prop.Reason = reason
		prop.Message = message
		prop.ConflictEvidence = &ConflictEvidence{
			ConflictType: conflictType,
			Message:      message,
			ObservedAt:   p.Now,
			Source:       sourceEv,
			Target:       targetEv,
			Baseline:     baseEv,
		}
		return prop
	}

	// 1. Source progress validation
	if proposedStatusID == 0 {
		prop.Action = ActionHold
		prop.Reason = ReasonUnmappedSourceStatus
		prop.Message = fmt.Sprintf("source progress status %q cannot be mapped to a target reading status", p.SourceProgress.Status)
		return prop
	}

	// 2. Privacy policy checks (US8)
	if p.IsPrivate {
		if !p.AllowPrivateSync {
			prop.Action = ActionHold
			prop.Reason = ReasonPrivateBookExcluded
			prop.Message = "book is marked private in WeRead; excluded from status synchronization by policy"
			return prop
		}
		prop.PrivacySettingID = 3 // 3 = private in Hardcover
		if p.TargetStatus != nil && (p.TargetStatus.PrivacySettingID == nil || *p.TargetStatus.PrivacySettingID != 3) {
			return makeConflict(
				ReasonPublicTargetPrivacy,
				ReasonPublicTargetPrivacy,
				"private source book mapped to an existing public or unstated-privacy target record; refuse silent public exposure",
			)
		}
	}

	// 3. Source authority check (US6)
	if p.HasCompetingMappings {
		isAuthorized := p.Authority != nil &&
			p.Authority.AuthorizedProvider == p.Provider &&
			p.Authority.AuthorizedAccount == p.SourceAccount &&
			p.Authority.AuthorizedBookID == p.BookID
		if !isAuthorized {
			prop.AuthorityStatus = "unassigned"
			if p.Authority != nil {
				prop.AuthorityStatus = "competing"
			}
			return makeConflict(
				ReasonCompetingSourceAuthority,
				ReasonCompetingSourceAuthority,
				"multiple source books map to this target work, but this source lacks exclusive status write authority",
			)
		}
	}

	// 4. Baseline policy version check
	if p.Baseline != nil && p.Baseline.PolicyVersion != p.PolicyVersion {
		prop.Action = ActionHold
		prop.Reason = ReasonPolicyVersionMismatch
		prop.Message = fmt.Sprintf("baseline policy version %q does not match current %q; hold for re-evaluation", p.Baseline.PolicyVersion, p.PolicyVersion)
		return prop
	}

	// 5. Bootstrap evaluation (Baseline is nil)
	if p.Baseline == nil {
		if p.TargetStatus == nil {
			prop.Action = ActionCreate
			prop.Reason = ReasonTargetMissing
			prop.Message = fmt.Sprintf("target user_book does not exist; propose creating record with status %s (%d)", prop.ProposedStatus, proposedStatusID)
			return prop
		}
		if p.TargetStatus.StatusID == proposedStatusID {
			if proposedStatusID == HardcoverStatusRead && lastReadDate != nil && *lastReadDate != "" {
				targetDate := ""
				if len(p.TargetStatus.UserBookReads) > 0 {
					latest := p.TargetStatus.UserBookReads[len(p.TargetStatus.UserBookReads)-1]
					if latest.FinishedAt != nil {
						targetDate = *latest.FinishedAt
					}
				}
				if targetDate != *lastReadDate && (targetDate == "" || p.AdoptSource) {
					prop.Action = ActionUpdate
					prop.Reason = ReasonSourceAdvanced
					prop.Message = fmt.Sprintf("target status matches Read (%d) but reading date %q differs from source %q; updating target read date",
						proposedStatusID, targetDate, *lastReadDate)
					return prop
				}
			}
			prop.Action = ActionNoOp
			prop.Reason = ReasonAlreadyMatching
			prop.Message = fmt.Sprintf("target record already has status %s (%d); zero mutations required", prop.CurrentStatus, proposedStatusID)
			return prop
		}
		if p.AdoptSource {
			prop.Action = ActionUpdate
			prop.Reason = ReasonSourceAdvanced
			prop.Message = fmt.Sprintf("bootstrap adopt-source approved: updating target status from %s (%d) to source status %s (%d)",
				prop.CurrentStatus, p.TargetStatus.StatusID, prop.ProposedStatus, proposedStatusID)
			return prop
		}
		// Target exists with differing status on first sync: do not overwrite without explicit approval.
		return makeConflict(
			ReasonDifferingTargetOnBootstrap,
			ReasonDifferingTargetOnBootstrap,
			fmt.Sprintf("first sync conflict: target record exists with status %s (%d) differing from source status %s (%d)",
				prop.CurrentStatus, p.TargetStatus.StatusID, prop.ProposedStatus, proposedStatusID),
		)
	}

	// 6. Three-way reconciliation (Baseline exists)
	if p.TargetStatus == nil {
		return makeConflict(
			ReasonTargetDisappeared,
			ReasonTargetDisappeared,
			"target record existed in baseline but has disappeared on Hardcover",
		)
	}

	sourceChanged := proposedStatusID != p.Baseline.SourceStatusID
	targetChanged := p.TargetStatus.StatusID != p.Baseline.TargetStatusID

	switch {
	case !sourceChanged && !targetChanged:
		if proposedStatusID == HardcoverStatusRead && lastReadDate != nil && *lastReadDate != "" && p.AdoptSource {
			targetDate := ""
			if len(p.TargetStatus.UserBookReads) > 0 {
				latest := p.TargetStatus.UserBookReads[len(p.TargetStatus.UserBookReads)-1]
				if latest.FinishedAt != nil {
					targetDate = *latest.FinishedAt
				}
			}
			if targetDate != *lastReadDate {
				prop.Action = ActionUpdate
				prop.Reason = ReasonSourceAdvanced
				prop.Message = fmt.Sprintf("adopt-source approved: updating target read date from %q to source %q", targetDate, *lastReadDate)
				return prop
			}
		}
		prop.Action = ActionNoOp
		prop.Reason = ReasonUnchangedSinceBaseline
		prop.Message = fmt.Sprintf("both source and target are unchanged from accepted baseline status %s (%d)", prop.CurrentStatus, proposedStatusID)
		return prop

	case sourceChanged && !targetChanged:
		prop.Action = ActionUpdate
		prop.Reason = ReasonSourceAdvanced
		prop.Message = fmt.Sprintf("source advanced from %s (%d) to %s (%d); propose updating target",
			HardcoverStatusName(p.Baseline.SourceStatusID), p.Baseline.SourceStatusID, prop.ProposedStatus, proposedStatusID)
		return prop

	case !sourceChanged && targetChanged:
		// US5: Protect manual target edit
		return makeConflict(
			ReasonManualTargetEdit,
			ReasonManualTargetEdit,
			fmt.Sprintf("manual target edit detected on Hardcover: status changed from %s (%d) to %s (%d) while source was unchanged",
				HardcoverStatusName(p.Baseline.TargetStatusID), p.Baseline.TargetStatusID, prop.CurrentStatus, p.TargetStatus.StatusID),
		)

	case sourceChanged && targetChanged:
		if proposedStatusID == p.TargetStatus.StatusID {
			prop.Action = ActionNoOp
			prop.Reason = ReasonConvergentAgreement
			prop.Message = fmt.Sprintf("source and target both independently converged to status %s (%d)", prop.ProposedStatus, proposedStatusID)
			return prop
		}
		return makeConflict(
			ReasonDivergentChanges,
			ReasonDivergentChanges,
			fmt.Sprintf("conflicting changes: source changed to %s (%d) while target changed to %s (%d)",
				prop.ProposedStatus, proposedStatusID, prop.CurrentStatus, p.TargetStatus.StatusID),
		)
	}

	prop.Action = ActionHold
	prop.Reason = "unexpected_state"
	prop.Message = "unexpected evaluation state"
	return prop
}

// PreviewSource abstracts the source provider dependencies for previewing sync.
type PreviewSource interface {
	Book(ctx context.Context, id string) (Book, error)
	Progress(ctx context.Context, id string) (Progress, error)
	ResolveAccountFingerprint(ctx context.Context, declaredVID string) (string, error)
}

// PreviewTarget abstracts the target provider dependencies for previewing sync.
type PreviewTarget interface {
	GetUserBookStatus(ctx context.Context, workID int64) (*UserBookStatus, error)
	ResolveAccountFingerprint(ctx context.Context) (string, error)
}

// PreviewSyncOptions controls optional evaluation policies.
type PreviewSyncOptions struct {
	PolicyVersion     string
	AllowPrivateSync  bool
	DeclaredSourceVID string
	AdoptSource       bool
	Now               time.Time
}

// PreviewSync orchestrates read-only three-way status evaluation for a mapped book.
// It verifies account identity fingerprints first and performs zero provider mutations.
func PreviewSync(
	ctx context.Context,
	store *Store,
	source PreviewSource,
	target PreviewTarget,
	provider, sourceAccount, bookID, targetAccount string,
	opts PreviewSyncOptions,
) (SyncProposal, error) {
	if !ValidLabel(provider) || !ValidLabel(sourceAccount) || !ValidLabel(targetAccount) || !ValidBookID(bookID) {
		return SyncProposal{}, ErrInvalidMappingKey
	}
	now := opts.Now
	if now.IsZero() {
		now = time.Now().UTC()
	}

	// 1. Verify account fingerprints first to prevent cross-account pollution.
	sourceFP, err := source.ResolveAccountFingerprint(ctx, opts.DeclaredSourceVID)
	if err != nil {
		return SyncProposal{}, fmt.Errorf("source account identity resolution failed: %w", err)
	}
	if _, err := store.VerifyOrBindAccountFingerprint(ctx, provider, sourceAccount, sourceFP, now); err != nil {
		return SyncProposal{}, fmt.Errorf("source account identity verification failed: %w", err)
	}

	targetFP, err := target.ResolveAccountFingerprint(ctx)
	if err != nil {
		return SyncProposal{}, fmt.Errorf("target account identity resolution failed: %w", err)
	}
	if _, err := store.VerifyOrBindAccountFingerprint(ctx, "hardcover", targetAccount, targetFP, now); err != nil {
		return SyncProposal{}, fmt.Errorf("target account identity verification failed: %w", err)
	}

	// 2. Fetch mapping
	mapping, err := store.GetMapping(ctx, provider, sourceAccount, bookID, targetAccount)
	if err != nil {
		return SyncProposal{}, err
	}
	if mapping == nil {
		return SyncProposal{}, fmt.Errorf("no confirmed mapping found for %s:%s:%s -> %s; confirm mapping first",
			provider, sourceAccount, bookID, targetAccount)
	}

	// 3. Fetch source book and progress (read-only)
	book, err := source.Book(ctx, bookID)
	if err != nil {
		return SyncProposal{}, fmt.Errorf("source book lookup failed: %w", err)
	}
	progress, err := source.Progress(ctx, bookID)
	if err != nil {
		return SyncProposal{}, fmt.Errorf("source progress lookup failed: %w", err)
	}

	// 4. Fetch target status (read-only)
	targetStatus, err := target.GetUserBookStatus(ctx, mapping.WorkID)
	if err != nil {
		return SyncProposal{}, fmt.Errorf("target user_book status lookup failed: %w", err)
	}

	// 5. Fetch baseline and authority from store
	baseline, err := store.GetStatusBaseline(ctx, provider, sourceAccount, bookID, targetAccount)
	if err != nil {
		return SyncProposal{}, err
	}
	authority, err := store.GetTargetAuthority(ctx, targetAccount, mapping.WorkID)
	if err != nil {
		return SyncProposal{}, err
	}
	competing, err := store.ListMappingsForTarget(ctx, targetAccount, mapping.WorkID)
	if err != nil {
		return SyncProposal{}, err
	}

	params := StatusEvaluationParams{
		Provider:             provider,
		SourceAccount:        sourceAccount,
		SourceFingerprint:    sourceFP,
		BookID:               bookID,
		SourceBook:           book,
		SourceProgress:       progress,
		IsPrivate:            false,
		TargetAccount:        targetAccount,
		TargetFingerprint:    targetFP,
		WorkID:               mapping.WorkID,
		EditionID:            mapping.EditionID,
		TargetStatus:         targetStatus,
		Baseline:             baseline,
		Authority:            authority,
		HasCompetingMappings: len(competing) > 1,
		PolicyVersion:        opts.PolicyVersion,
		AllowPrivateSync:     opts.AllowPrivateSync,
		AdoptSource:          opts.AdoptSource,
		Now:                  now,
	}

	return EvaluateStatus(params), nil
}
