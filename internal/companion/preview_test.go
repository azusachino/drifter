package companion

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"
)

var _ PreviewSource = (*WeRead)(nil)
var _ PreviewTarget = (*Hardcover)(nil)

type mockPreviewSource struct {
	book        Book
	progress    Progress
	fingerprint string
	bookErr     error
	progressErr error
	fpErr       error
}

func (m *mockPreviewSource) Book(_ context.Context, _ string) (Book, error) {
	return m.book, m.bookErr
}

func (m *mockPreviewSource) Progress(_ context.Context, _ string) (Progress, error) {
	return m.progress, m.progressErr
}

func (m *mockPreviewSource) ResolveAccountFingerprint(_ context.Context, _ string) (string, error) {
	return m.fingerprint, m.fpErr
}

type mockPreviewTarget struct {
	status         *UserBookStatus
	fingerprint    string
	statusErr      error
	fpErr          error
	mutationsCount int
}

func (m *mockPreviewTarget) GetUserBookStatus(_ context.Context, _ int64) (*UserBookStatus, error) {
	return m.status, m.statusErr
}

func (m *mockPreviewTarget) ResolveAccountFingerprint(_ context.Context) (string, error) {
	return m.fingerprint, m.fpErr
}

func intPtr(i int) *int {
	return &i
}

func int64Ptr(i int64) *int64 {
	return &i
}

func TestBootstrapEvaluation_US1(t *testing.T) {
	now := time.Date(2026, 10, 27, 12, 0, 0, 0, time.UTC)
	baseParams := StatusEvaluationParams{
		Provider:          "weread",
		SourceAccount:     "mine",
		SourceFingerprint: "weread:10001",
		BookID:            "10001",
		SourceBook:        Book{Title: "Test Book"},
		SourceProgress:    Progress{Status: "reading", Percent: 45, Started: true},
		TargetAccount:     "default",
		TargetFingerprint: "hardcover:20002",
		WorkID:            501,
		EditionID:         601,
		PolicyVersion:     "v1",
		Now:               now,
	}

	t.Run("target record missing evaluates to create", func(t *testing.T) {
		p := baseParams
		p.TargetStatus = nil

		prop := EvaluateStatus(p)
		if prop.Action != ActionCreate {
			t.Fatalf("expected action create, got %q", prop.Action)
		}
		if prop.Reason != ReasonTargetMissing {
			t.Fatalf("expected reason %s, got %s", ReasonTargetMissing, prop.Reason)
		}
		if prop.ProposedStatusID != HardcoverStatusCurrentlyReading {
			t.Fatalf("expected proposed status %d, got %d", HardcoverStatusCurrentlyReading, prop.ProposedStatusID)
		}
		if prop.ConflictEvidence != nil {
			t.Fatal("expected nil conflict evidence on create")
		}
	})

	t.Run("target record already matching evaluates to no-op", func(t *testing.T) {
		p := baseParams
		p.TargetStatus = &UserBookStatus{
			ID:       777,
			BookID:   501,
			StatusID: HardcoverStatusCurrentlyReading,
		}

		prop := EvaluateStatus(p)
		if prop.Action != ActionNoOp {
			t.Fatalf("expected action no-op, got %q", prop.Action)
		}
		if prop.Reason != ReasonAlreadyMatching {
			t.Fatalf("expected reason %s, got %s", ReasonAlreadyMatching, prop.Reason)
		}
		if prop.ConflictEvidence != nil {
			t.Fatal("expected nil conflict evidence on matching no-op")
		}
	})

	t.Run("target record differing on bootstrap evaluates to conflict with evidence", func(t *testing.T) {
		p := baseParams
		p.TargetStatus = &UserBookStatus{
			ID:               777,
			BookID:           501,
			StatusID:         HardcoverStatusWantToRead,
			PrivacySettingID: intPtr(1),
		}

		prop := EvaluateStatus(p)
		if prop.Action != ActionConflict {
			t.Fatalf("expected action conflict, got %q", prop.Action)
		}
		if prop.Reason != ReasonDifferingTargetOnBootstrap {
			t.Fatalf("expected reason %s, got %s", ReasonDifferingTargetOnBootstrap, prop.Reason)
		}
		if prop.ConflictEvidence == nil {
			t.Fatal("expected conflict evidence on bootstrap conflict")
		}
		ev := prop.ConflictEvidence
		if ev.ConflictType != ReasonDifferingTargetOnBootstrap {
			t.Fatalf("expected conflict type %s, got %s", ReasonDifferingTargetOnBootstrap, ev.ConflictType)
		}
		if ev.Baseline != nil {
			t.Fatal("expected nil baseline in bootstrap conflict evidence")
		}
		if !ev.Target.Exists || ev.Target.StatusID != HardcoverStatusWantToRead {
			t.Fatalf("unexpected target evidence: %+v", ev.Target)
		}
		if ev.Source.BookID != "10001" || ev.Source.SourceStatus != "reading" {
			t.Fatalf("unexpected source evidence: %+v", ev.Source)
		}
	})
}

func TestManualTargetEditProtection_US5(t *testing.T) {
	now := time.Date(2026, 10, 27, 12, 0, 0, 0, time.UTC)
	baseline := &StatusBaseline{
		Provider:          "weread",
		SourceAccount:     "mine",
		BookID:            "10001",
		TargetAccount:     "default",
		WorkID:            501,
		EditionID:         601,
		SourceStatusID:    HardcoverStatusCurrentlyReading,
		TargetStatusID:    HardcoverStatusCurrentlyReading,
		PolicyVersion:     "v1",
		SourceFingerprint: "weread:10001",
		TargetFingerprint: "hardcover:20002",
		LastSyncedAt:      now.Add(-24 * time.Hour),
	}

	baseParams := StatusEvaluationParams{
		Provider:          "weread",
		SourceAccount:     "mine",
		SourceFingerprint: "weread:10001",
		BookID:            "10001",
		SourceBook:        Book{Title: "Test Book"},
		SourceProgress:    Progress{Status: "reading", Percent: 50, Started: true},
		TargetAccount:     "default",
		TargetFingerprint: "hardcover:20002",
		WorkID:            501,
		EditionID:         601,
		Baseline:          baseline,
		PolicyVersion:     "v1",
		Now:               now,
	}

	t.Run("manual target edit detected when target changed and source unchanged", func(t *testing.T) {
		p := baseParams
		p.TargetStatus = &UserBookStatus{
			ID:       777,
			BookID:   501,
			StatusID: HardcoverStatusRead, // Changed manually on Hardcover
		}

		prop := EvaluateStatus(p)
		if prop.Action != ActionConflict {
			t.Fatalf("expected action conflict, got %q", prop.Action)
		}
		if prop.Reason != ReasonManualTargetEdit {
			t.Fatalf("expected reason %s, got %s", ReasonManualTargetEdit, prop.Reason)
		}
		if prop.ConflictEvidence == nil || prop.ConflictEvidence.Baseline == nil {
			t.Fatal("expected conflict evidence with baseline")
		}
		if prop.ConflictEvidence.Baseline.TargetStatusID != HardcoverStatusCurrentlyReading {
			t.Fatalf("expected baseline target status %d, got %d",
				HardcoverStatusCurrentlyReading, prop.ConflictEvidence.Baseline.TargetStatusID)
		}
	})

	t.Run("source advance updates target when target unchanged", func(t *testing.T) {
		p := baseParams
		p.SourceProgress = Progress{Status: "read", Percent: 100, Started: true} // Source finished
		p.TargetStatus = &UserBookStatus{
			ID:       777,
			BookID:   501,
			StatusID: HardcoverStatusCurrentlyReading, // Target unchanged from baseline
		}

		prop := EvaluateStatus(p)
		if prop.Action != ActionUpdate {
			t.Fatalf("expected action update, got %q", prop.Action)
		}
		if prop.Reason != ReasonSourceAdvanced {
			t.Fatalf("expected reason %s, got %s", ReasonSourceAdvanced, prop.Reason)
		}
		if prop.ProposedStatusID != HardcoverStatusRead {
			t.Fatalf("expected proposed status %d, got %d", HardcoverStatusRead, prop.ProposedStatusID)
		}
	})

	t.Run("source finished populates last_read_date", func(t *testing.T) {
		p := baseParams
		p.SourceProgress = Progress{Status: "read", Percent: 100, Started: true, FinishedAt: 1752627658}
		p.TargetStatus = nil

		prop := EvaluateStatus(p)
		if prop.ProposedStatusID != HardcoverStatusRead {
			t.Fatalf("expected proposed status %d, got %d", HardcoverStatusRead, prop.ProposedStatusID)
		}
		if prop.LastReadDate == nil || *prop.LastReadDate != "2025-07-16" {
			t.Fatalf("expected last_read_date 2025-07-16, got %v", prop.LastReadDate)
		}
	})

	t.Run("bootstrap read status updates read date when target read date differs with adoptSource", func(t *testing.T) {
		p := baseParams
		p.Baseline = nil
		p.SourceProgress = Progress{Status: "read", Percent: 100, Started: true, FinishedAt: 1752627658} // 2025-07-16
		oldDate := "2026-01-01"
		p.TargetStatus = &UserBookStatus{
			ID:       777,
			BookID:   501,
			StatusID: HardcoverStatusRead,
			UserBookReads: []UserBookReadEntry{
				{ID: 101, FinishedAt: &oldDate},
			},
		}

		propNoAdopt := EvaluateStatus(p)
		if propNoAdopt.Action != ActionNoOp {
			t.Fatalf("expected action no-op without adoptSource, got %q", propNoAdopt.Action)
		}

		p.AdoptSource = true
		propAdopt := EvaluateStatus(p)
		if propAdopt.Action != ActionUpdate {
			t.Fatalf("expected action update with adoptSource, got %q", propAdopt.Action)
		}
		if propAdopt.LastReadDate == nil || *propAdopt.LastReadDate != "2025-07-16" {
			t.Fatalf("expected last read date 2025-07-16, got %v", propAdopt.LastReadDate)
		}
	})

	t.Run("convergent agreement when both changed to same status", func(t *testing.T) {
		p := baseParams
		p.SourceProgress = Progress{Status: "read", Percent: 100, Started: true}
		p.TargetStatus = &UserBookStatus{
			ID:       777,
			BookID:   501,
			StatusID: HardcoverStatusRead,
		}

		prop := EvaluateStatus(p)
		if prop.Action != ActionNoOp {
			t.Fatalf("expected action no-op, got %q", prop.Action)
		}
		if prop.Reason != ReasonConvergentAgreement {
			t.Fatalf("expected reason %s, got %s", ReasonConvergentAgreement, prop.Reason)
		}
	})

	t.Run("divergent changes when both changed to different statuses", func(t *testing.T) {
		p := baseParams
		p.SourceProgress = Progress{Status: "read", Percent: 100, Started: true} // Source -> Read
		p.TargetStatus = &UserBookStatus{
			ID:       777,
			BookID:   501,
			StatusID: HardcoverStatusDidNotFinish, // Target -> DNF
		}

		prop := EvaluateStatus(p)
		if prop.Action != ActionConflict {
			t.Fatalf("expected action conflict, got %q", prop.Action)
		}
		if prop.Reason != ReasonDivergentChanges {
			t.Fatalf("expected reason %s, got %s", ReasonDivergentChanges, prop.Reason)
		}
	})

	t.Run("both unchanged since baseline evaluates to no-op", func(t *testing.T) {
		p := baseParams
		p.TargetStatus = &UserBookStatus{
			ID:       777,
			BookID:   501,
			StatusID: HardcoverStatusCurrentlyReading,
		}

		prop := EvaluateStatus(p)
		if prop.Action != ActionNoOp {
			t.Fatalf("expected action no-op, got %q", prop.Action)
		}
		if prop.Reason != ReasonUnchangedSinceBaseline {
			t.Fatalf("expected reason %s, got %s", ReasonUnchangedSinceBaseline, prop.Reason)
		}
	})

	t.Run("target disappeared conflict", func(t *testing.T) {
		p := baseParams
		p.TargetStatus = nil

		prop := EvaluateStatus(p)
		if prop.Action != ActionConflict {
			t.Fatalf("expected action conflict, got %q", prop.Action)
		}
		if prop.Reason != ReasonTargetDisappeared {
			t.Fatalf("expected reason %s, got %s", ReasonTargetDisappeared, prop.Reason)
		}
	})
}

func TestCompetingSourceAuthority_US6(t *testing.T) {
	now := time.Date(2026, 10, 27, 12, 0, 0, 0, time.UTC)
	baseParams := StatusEvaluationParams{
		Provider:             "weread",
		SourceAccount:        "mine",
		SourceFingerprint:    "weread:10001",
		BookID:               "10001",
		SourceBook:           Book{Title: "Test Book"},
		SourceProgress:       Progress{Status: "reading", Percent: 50, Started: true},
		TargetAccount:        "default",
		TargetFingerprint:    "hardcover:20002",
		WorkID:               501,
		EditionID:            601,
		HasCompetingMappings: true,
		PolicyVersion:        "v1",
		Now:                  now,
	}

	t.Run("unassigned authority on competing mappings conflicts", func(t *testing.T) {
		p := baseParams
		p.Authority = nil

		prop := EvaluateStatus(p)
		if prop.Action != ActionConflict {
			t.Fatalf("expected action conflict, got %q", prop.Action)
		}
		if prop.Reason != ReasonCompetingSourceAuthority {
			t.Fatalf("expected reason %s, got %s", ReasonCompetingSourceAuthority, prop.Reason)
		}
		if prop.AuthorityStatus != "unassigned" {
			t.Fatalf("expected authority status unassigned, got %q", prop.AuthorityStatus)
		}
	})

	t.Run("competing source with authority assigned to other source conflicts", func(t *testing.T) {
		p := baseParams
		p.Authority = &TargetAuthority{
			TargetAccount:      "default",
			WorkID:             501,
			AuthorizedProvider: "kindle", // Authorized to another provider!
			AuthorizedAccount:  "mine",
			AuthorizedBookID:   "123999",
			PolicyVersion:      "v1",
			PrivacyMode:        "default",
			CreatedAt:          now,
			UpdatedAt:          now,
		}

		prop := EvaluateStatus(p)
		if prop.Action != ActionConflict {
			t.Fatalf("expected action conflict, got %q", prop.Action)
		}
		if prop.Reason != ReasonCompetingSourceAuthority {
			t.Fatalf("expected reason %s, got %s", ReasonCompetingSourceAuthority, prop.Reason)
		}
		if prop.AuthorityStatus != "competing" {
			t.Fatalf("expected authority status competing, got %q", prop.AuthorityStatus)
		}
	})

	t.Run("matching assigned authority proceeds to evaluation", func(t *testing.T) {
		p := baseParams
		p.Authority = &TargetAuthority{
			TargetAccount:      "default",
			WorkID:             501,
			AuthorizedProvider: "weread",
			AuthorizedAccount:  "mine",
			AuthorizedBookID:   "10001",
			PolicyVersion:      "v1",
			PrivacyMode:        "default",
			CreatedAt:          now,
			UpdatedAt:          now,
		}

		prop := EvaluateStatus(p)
		if prop.Action != ActionCreate {
			t.Fatalf("expected action create, got %q", prop.Action)
		}
		if prop.AuthorityStatus != "authorized" {
			t.Fatalf("expected authority status authorized, got %q", prop.AuthorityStatus)
		}
	})
}

func TestPrivateReadingGuards_US8(t *testing.T) {
	now := time.Date(2026, 10, 27, 12, 0, 0, 0, time.UTC)
	baseParams := StatusEvaluationParams{
		Provider:          "weread",
		SourceAccount:     "mine",
		SourceFingerprint: "weread:10001",
		BookID:            "10001",
		SourceBook:        Book{Title: "Private Book"},
		SourceProgress:    Progress{Status: "reading", Percent: 50, Started: true},
		IsPrivate:         true,
		TargetAccount:     "default",
		TargetFingerprint: "hardcover:20002",
		WorkID:            501,
		EditionID:         601,
		PolicyVersion:     "v1",
		Now:               now,
	}

	t.Run("default policy excludes private source book", func(t *testing.T) {
		p := baseParams
		p.AllowPrivateSync = false

		prop := EvaluateStatus(p)
		if prop.Action != ActionHold {
			t.Fatalf("expected action hold, got %q", prop.Action)
		}
		if prop.Reason != ReasonPrivateBookExcluded {
			t.Fatalf("expected reason %s, got %s", ReasonPrivateBookExcluded, prop.Reason)
		}
	})

	t.Run("private sync allowed but public target conflicts", func(t *testing.T) {
		p := baseParams
		p.AllowPrivateSync = true
		p.TargetStatus = &UserBookStatus{
			ID:               777,
			BookID:           501,
			StatusID:         HardcoverStatusCurrentlyReading,
			PrivacySettingID: intPtr(1), // 1 = Public
		}

		prop := EvaluateStatus(p)
		if prop.Action != ActionConflict {
			t.Fatalf("expected action conflict, got %q", prop.Action)
		}
		if prop.Reason != ReasonPublicTargetPrivacy {
			t.Fatalf("expected reason %s, got %s", ReasonPublicTargetPrivacy, prop.Reason)
		}
	})

	t.Run("private sync allowed with private target proceeds", func(t *testing.T) {
		p := baseParams
		p.AllowPrivateSync = true
		p.TargetStatus = &UserBookStatus{
			ID:               777,
			BookID:           501,
			StatusID:         HardcoverStatusCurrentlyReading,
			PrivacySettingID: intPtr(3), // 3 = Private
		}

		prop := EvaluateStatus(p)
		if prop.Action != ActionNoOp {
			t.Fatalf("expected action no-op, got %q", prop.Action)
		}
		if prop.PrivacySettingID != 3 {
			t.Fatalf("expected privacy setting id 3, got %d", prop.PrivacySettingID)
		}
	})
}

func TestPreviewPolicyEdgeCases(t *testing.T) {
	now := time.Date(2026, 10, 27, 12, 0, 0, 0, time.UTC)
	baseParams := StatusEvaluationParams{
		Provider:          "weread",
		SourceAccount:     "mine",
		SourceFingerprint: "weread:10001",
		BookID:            "10001",
		SourceBook:        Book{Title: "Test Book"},
		SourceProgress:    Progress{Status: "reading", Percent: 50, Started: true},
		TargetAccount:     "default",
		TargetFingerprint: "hardcover:20002",
		WorkID:            501,
		EditionID:         601,
		PolicyVersion:     "v1",
		Now:               now,
	}

	t.Run("unmapped source status holds", func(t *testing.T) {
		p := baseParams
		p.SourceProgress = Progress{Status: "abandoned"}

		prop := EvaluateStatus(p)
		if prop.Action != ActionHold {
			t.Fatalf("expected action hold, got %q", prop.Action)
		}
		if prop.Reason != ReasonUnmappedSourceStatus {
			t.Fatalf("expected reason %s, got %s", ReasonUnmappedSourceStatus, prop.Reason)
		}
	})

	t.Run("policy version mismatch in baseline holds", func(t *testing.T) {
		p := baseParams
		p.PolicyVersion = "v2"
		p.Baseline = &StatusBaseline{
			Provider:          "weread",
			SourceAccount:     "mine",
			BookID:            "10001",
			TargetAccount:     "default",
			WorkID:            501,
			EditionID:         601,
			SourceStatusID:    HardcoverStatusCurrentlyReading,
			TargetStatusID:    HardcoverStatusCurrentlyReading,
			PolicyVersion:     "v1", // Old policy version!
			SourceFingerprint: "weread:10001",
			TargetFingerprint: "hardcover:20002",
			LastSyncedAt:      now.Add(-24 * time.Hour),
		}

		prop := EvaluateStatus(p)
		if prop.Action != ActionHold {
			t.Fatalf("expected action hold, got %q", prop.Action)
		}
		if prop.Reason != ReasonPolicyVersionMismatch {
			t.Fatalf("expected reason %s, got %s", ReasonPolicyVersionMismatch, prop.Reason)
		}
	})
}

func TestPreviewSyncZeroMutationsAndFingerprints(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.sqlite")
	store, err := OpenStore(path)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	ctx := context.Background()
	now := time.Date(2026, 10, 27, 12, 0, 0, 0, time.UTC)

	// Setup confirmed mapping
	mapping := Mapping{
		Provider:      "weread",
		SourceAccount: "mine",
		BookID:        "123456",
		TargetAccount: "default",
		WorkID:        999,
		EditionID:     888,
		SourceTitle:   "Mapped Title",
		SourceISBN:    "9780123456789",
	}
	if err := store.Confirm(ctx, mapping, false); err != nil {
		t.Fatal(err)
	}

	source := &mockPreviewSource{
		book:        Book{ID: "123456", Title: "Mapped Title"},
		progress:    Progress{Status: "reading", Percent: 50, Started: true},
		fingerprint: "weread:99999",
	}
	target := &mockPreviewTarget{
		status:      nil, // Target missing -> action create
		fingerprint: "hardcover:88888",
	}

	// 1. Initial preview run binds fingerprints and evaluates proposal
	prop, err := PreviewSync(ctx, store, source, target, "weread", "mine", "123456", "default", PreviewSyncOptions{
		PolicyVersion: "v1",
		Now:           now,
	})
	if err != nil {
		t.Fatalf("preview sync failed: %v", err)
	}
	if prop.Action != ActionCreate {
		t.Fatalf("expected action create, got %q", prop.Action)
	}
	if target.mutationsCount != 0 {
		t.Fatalf("zero-mutation invariant violated: %d mutations performed", target.mutationsCount)
	}

	// Verify fingerprints bound in store
	sFp, err := store.GetAccountFingerprint(ctx, "weread", "mine")
	if err != nil || sFp == nil || sFp.Fingerprint != "weread:99999" {
		t.Fatalf("source fingerprint not properly bound: %+v, err %v", sFp, err)
	}
	tFp, err := store.GetAccountFingerprint(ctx, "hardcover", "default")
	if err != nil || tFp == nil || tFp.Fingerprint != "hardcover:88888" {
		t.Fatalf("target fingerprint not properly bound: %+v, err %v", tFp, err)
	}

	// 2. Second preview run with same fingerprints succeeds
	prop2, err := PreviewSync(ctx, store, source, target, "weread", "mine", "123456", "default", PreviewSyncOptions{
		PolicyVersion: "v1",
		Now:           now.Add(1 * time.Hour),
	})
	if err != nil {
		t.Fatalf("second preview sync failed: %v", err)
	}
	if prop2.Action != ActionCreate {
		t.Fatalf("expected action create, got %q", prop2.Action)
	}

	// 3. Third preview run with mismatched source fingerprint fails closed
	badSource := &mockPreviewSource{
		book:        Book{ID: "123456", Title: "Mapped Title"},
		progress:    Progress{Status: "reading", Percent: 50, Started: true},
		fingerprint: "weread:00000", // Mismatched!
	}
	_, err = PreviewSync(ctx, store, badSource, target, "weread", "mine", "123456", "default", PreviewSyncOptions{
		PolicyVersion: "v1",
		Now:           now.Add(2 * time.Hour),
	})
	if err == nil || !errors.Is(err, ErrAccountFingerprintMismatch) {
		t.Fatalf("expected ErrAccountFingerprintMismatch on source mismatch, got: %v", err)
	}

	// 4. Mismatched target fingerprint fails closed
	badTarget := &mockPreviewTarget{
		status:      nil,
		fingerprint: "hardcover:00000", // Mismatched!
	}
	_, err = PreviewSync(ctx, store, source, badTarget, "weread", "mine", "123456", "default", PreviewSyncOptions{
		PolicyVersion: "v1",
		Now:           now.Add(3 * time.Hour),
	})
	if err == nil || !errors.Is(err, ErrAccountFingerprintMismatch) {
		t.Fatalf("expected ErrAccountFingerprintMismatch on target mismatch, got: %v", err)
	}
}
