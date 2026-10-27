package companion

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"
)

type mockApplyTarget struct {
	status           *UserBookStatus
	fingerprint      string
	mutations        []StatusMutationRequest
	mutationErr      error
	readBackStatus   *UserBookStatus
	readBackErr      error
	getUserBookCalls int
}

func (m *mockApplyTarget) GetUserBookStatus(_ context.Context, _ int64) (*UserBookStatus, error) {
	m.getUserBookCalls++
	if m.readBackStatus != nil && m.getUserBookCalls > 1 {
		return m.readBackStatus, m.readBackErr
	}
	return m.status, nil
}

func (m *mockApplyTarget) ResolveAccountFingerprint(_ context.Context) (string, error) {
	return m.fingerprint, nil
}

func (m *mockApplyTarget) ApplyStatusMutation(_ context.Context, req StatusMutationRequest) (StatusMutationOutcome, error) {
	m.mutations = append(m.mutations, req)
	if m.mutationErr != nil {
		return StatusMutationOutcome{}, m.mutationErr
	}
	m.status = &UserBookStatus{
		ID:       999,
		BookID:   req.WorkID,
		StatusID: req.StatusID,
	}
	return StatusMutationOutcome{
		CurrentStatus: req.StatusID,
		UserBookID:    999,
	}, nil
}

func setupTestStoreWithMapping(t *testing.T, mapping Mapping) (*Store, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "state.sqlite")
	store, err := OpenStore(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Confirm(context.Background(), mapping, false); err != nil {
		t.Fatal(err)
	}
	return store, path
}

func TestApplySyncCreateBootstrap_US1(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 10, 27, 12, 0, 0, 0, time.UTC)
	mapping := Mapping{
		Provider:      "weread",
		SourceAccount: "mine",
		BookID:        "123456",
		TargetAccount: "default",
		WorkID:        500,
		EditionID:     600,
		SourceTitle:   "First Sync Title",
		SourceISBN:    "9780123456789",
	}
	store, _ := setupTestStoreWithMapping(t, mapping)
	defer store.Close()

	src := &mockPreviewSource{
		book:        Book{ID: "123456", Title: "First Sync Title"},
		progress:    Progress{Status: "reading", Percent: 25, Started: true},
		fingerprint: "weread:10001",
	}
	tgt := &mockApplyTarget{
		status:      nil, // Missing target record -> bootstrap create
		fingerprint: "hardcover:20002",
	}

	res, err := ApplySync(ctx, store, src, tgt, "weread", "mine", "123456", "default", ApplySyncOptions{
		PolicyVersion: "v1",
		Now:           now,
	})
	if err != nil {
		t.Fatalf("ApplySync failed: %v", err)
	}

	if res.Action != "applied" {
		t.Fatalf("expected action applied, got %q", res.Action)
	}
	if res.AppliedStatusID != HardcoverStatusCurrentlyReading {
		t.Fatalf("expected applied status %d, got %d", HardcoverStatusCurrentlyReading, res.AppliedStatusID)
	}
	if res.IntentID <= 0 || res.ReceiptID <= 0 {
		t.Fatalf("expected positive intent and receipt IDs: intent=%d, receipt=%d", res.IntentID, res.ReceiptID)
	}

	// Verify target received exactly one mutation with approved fields only
	if len(tgt.mutations) != 1 {
		t.Fatalf("expected 1 mutation, got %d", len(tgt.mutations))
	}
	req := tgt.mutations[0]
	if req.WorkID != 500 || req.EditionID != 600 || req.StatusID != HardcoverStatusCurrentlyReading {
		t.Fatalf("unexpected mutation request: %+v", req)
	}

	// Verify baseline was saved
	baseline, err := store.GetStatusBaseline(ctx, "weread", "mine", "123456", "default")
	if err != nil || baseline == nil {
		t.Fatalf("baseline not saved: %+v, err %v", baseline, err)
	}
	if baseline.TargetStatusID != HardcoverStatusCurrentlyReading || baseline.SourceStatusID != HardcoverStatusCurrentlyReading {
		t.Fatalf("unexpected baseline: %+v", baseline)
	}

	// Verify intent transitioned to applied
	active, err := store.GetActiveIntent(ctx, "weread", "mine", "123456", "default")
	if err != nil || active != nil {
		t.Fatalf("active intent should be nil after completion, got %+v", active)
	}

	// Verify receipt saved
	receipt, err := store.GetLatestReceipt(ctx, "weread", "mine", "123456", "default")
	if err != nil || receipt == nil || receipt.ID != res.ReceiptID {
		t.Fatalf("receipt not saved: %+v, err %v", receipt, err)
	}
}

func TestApplySyncUpdateSourceAdvanced(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 10, 27, 12, 0, 0, 0, time.UTC)
	mapping := Mapping{
		Provider:      "weread",
		SourceAccount: "mine",
		BookID:        "123456",
		TargetAccount: "default",
		WorkID:        500,
		EditionID:     600,
		SourceTitle:   "Advanced Book",
		SourceISBN:    "9780123456789",
	}
	store, _ := setupTestStoreWithMapping(t, mapping)
	defer store.Close()

	// Pre-seed baseline as CurrentlyReading
	baseline := StatusBaseline{
		Provider:          "weread",
		SourceAccount:     "mine",
		BookID:            "123456",
		TargetAccount:     "default",
		WorkID:            500,
		EditionID:         600,
		SourceStatusID:    HardcoverStatusCurrentlyReading,
		TargetStatusID:    HardcoverStatusCurrentlyReading,
		PolicyVersion:     "v1",
		SourceFingerprint: "weread:10001",
		TargetFingerprint: "hardcover:20002",
		LastSyncedAt:      now.Add(-24 * time.Hour),
	}
	if err := store.SaveStatusBaseline(ctx, baseline); err != nil {
		t.Fatal(err)
	}

	// Source advanced to "read"
	src := &mockPreviewSource{
		book:        Book{ID: "123456", Title: "Advanced Book"},
		progress:    Progress{Status: "read", Percent: 100, Started: true},
		fingerprint: "weread:10001",
	}
	tgt := &mockApplyTarget{
		status: &UserBookStatus{
			ID:       999,
			BookID:   500,
			StatusID: HardcoverStatusCurrentlyReading,
		},
		fingerprint: "hardcover:20002",
	}

	res, err := ApplySync(ctx, store, src, tgt, "weread", "mine", "123456", "default", ApplySyncOptions{
		PolicyVersion: "v1",
		Now:           now,
	})
	if err != nil {
		t.Fatalf("ApplySync failed: %v", err)
	}

	if res.Action != "applied" {
		t.Fatalf("expected action applied, got %q", res.Action)
	}
	if res.PriorStatusID != HardcoverStatusCurrentlyReading || res.AppliedStatusID != HardcoverStatusRead {
		t.Fatalf("expected transition from %d to %d, got prior=%d applied=%d",
			HardcoverStatusCurrentlyReading, HardcoverStatusRead, res.PriorStatusID, res.AppliedStatusID)
	}

	// Verify updated baseline
	b, err := store.GetStatusBaseline(ctx, "weread", "mine", "123456", "default")
	if err != nil || b == nil || b.TargetStatusID != HardcoverStatusRead {
		t.Fatalf("baseline not updated to Read: %+v, err %v", b, err)
	}
}

func TestApplySyncRefusesConflictAndHoldWithoutWrites(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 10, 27, 12, 0, 0, 0, time.UTC)
	mapping := Mapping{
		Provider:      "weread",
		SourceAccount: "mine",
		BookID:        "123456",
		TargetAccount: "default",
		WorkID:        500,
		EditionID:     600,
		SourceTitle:   "Conflict Book",
		SourceISBN:    "9780123456789",
	}
	store, _ := setupTestStoreWithMapping(t, mapping)
	defer store.Close()

	// Seed baseline as CurrentlyReading
	baseline := StatusBaseline{
		Provider:          "weread",
		SourceAccount:     "mine",
		BookID:            "123456",
		TargetAccount:     "default",
		WorkID:            500,
		EditionID:         600,
		SourceStatusID:    HardcoverStatusCurrentlyReading,
		TargetStatusID:    HardcoverStatusCurrentlyReading,
		PolicyVersion:     "v1",
		SourceFingerprint: "weread:10001",
		TargetFingerprint: "hardcover:20002",
		LastSyncedAt:      now.Add(-24 * time.Hour),
	}
	if err := store.SaveStatusBaseline(ctx, baseline); err != nil {
		t.Fatal(err)
	}

	// Target changed manually on Hardcover to DNF while source is still reading
	src := &mockPreviewSource{
		book:        Book{ID: "123456", Title: "Conflict Book"},
		progress:    Progress{Status: "reading", Percent: 50, Started: true},
		fingerprint: "weread:10001",
	}
	tgt := &mockApplyTarget{
		status: &UserBookStatus{
			ID:       999,
			BookID:   500,
			StatusID: HardcoverStatusDidNotFinish,
		},
		fingerprint: "hardcover:20002",
	}

	res, err := ApplySync(ctx, store, src, tgt, "weread", "mine", "123456", "default", ApplySyncOptions{
		PolicyVersion: "v1",
		Now:           now,
	})
	if err != nil {
		t.Fatalf("unexpected error on conflict evaluation: %v", err)
	}

	if res.Action != "conflict" {
		t.Fatalf("expected action conflict, got %q", res.Action)
	}
	if res.Reason != ReasonManualTargetEdit {
		t.Fatalf("expected reason %s, got %s", ReasonManualTargetEdit, res.Reason)
	}
	if res.ConflictEvidence == nil {
		t.Fatal("expected conflict evidence")
	}

	// Zero mutations sent!
	if len(tgt.mutations) != 0 {
		t.Fatalf("zero-mutation invariant violated: %d mutations sent", len(tgt.mutations))
	}

	// Zero intents recorded!
	active, err := store.GetActiveIntent(ctx, "weread", "mine", "123456", "default")
	if err != nil || active != nil {
		t.Fatalf("expected zero active intents, got %+v", active)
	}
}

func TestApplySyncUncertainTimeoutHold_US7(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 10, 27, 12, 0, 0, 0, time.UTC)
	mapping := Mapping{
		Provider:      "weread",
		SourceAccount: "mine",
		BookID:        "123456",
		TargetAccount: "default",
		WorkID:        500,
		EditionID:     600,
		SourceTitle:   "Timeout Book",
		SourceISBN:    "9780123456789",
	}
	store, _ := setupTestStoreWithMapping(t, mapping)
	defer store.Close()

	src := &mockPreviewSource{
		book:        Book{ID: "123456", Title: "Timeout Book"},
		progress:    Progress{Status: "reading", Percent: 20, Started: true},
		fingerprint: "weread:10001",
	}
	tgt := &mockApplyTarget{
		status:      nil,
		fingerprint: "hardcover:20002",
		mutationErr: ErrUncertainStatusMutation, // Simulates 504 Gateway Timeout
	}

	res, err := ApplySync(ctx, store, src, tgt, "weread", "mine", "123456", "default", ApplySyncOptions{
		PolicyVersion: "v1",
		Now:           now,
	})
	if err == nil || !errors.Is(err, ErrUncertainStatusMutation) {
		t.Fatalf("expected ErrUncertainStatusMutation, got: %v", err)
	}
	if res.Action != "uncertain" {
		t.Fatalf("expected action uncertain, got %q", res.Action)
	}

	// Verify intent is recorded in SQLite with state "uncertain"
	active, err := store.GetActiveIntent(ctx, "weread", "mine", "123456", "default")
	if err != nil || active == nil {
		t.Fatalf("expected active uncertain intent, got %+v, err %v", active, err)
	}
	if active.State != "uncertain" {
		t.Fatalf("expected intent state uncertain, got %q", active.State)
	}

	// Verify receipt is NOT recorded
	receipt, err := store.GetLatestReceipt(ctx, "weread", "mine", "123456", "default")
	if err != nil || receipt != nil {
		t.Fatalf("receipt should not exist for uncertain intent: %+v", receipt)
	}

	// Verify baseline is NOT updated
	baseline, err := store.GetStatusBaseline(ctx, "weread", "mine", "123456", "default")
	if err != nil || baseline != nil {
		t.Fatalf("baseline should not exist for uncertain intent: %+v", baseline)
	}
}

func TestApplySyncRestartReconcilesUncertainIntent_US7(t *testing.T) {
	ctx := context.Background()
	t0 := time.Date(2026, 10, 27, 12, 0, 0, 0, time.UTC)
	t1 := t0.Add(1 * time.Hour)
	mapping := Mapping{
		Provider:      "weread",
		SourceAccount: "mine",
		BookID:        "123456",
		TargetAccount: "default",
		WorkID:        500,
		EditionID:     600,
		SourceTitle:   "Restart Book",
		SourceISBN:    "9780123456789",
	}
	store, _ := setupTestStoreWithMapping(t, mapping)
	defer store.Close()

	// Pre-seed an uncertain intent from a prior crashed/timed-out run
	intent := SyncIntent{
		Provider:          "weread",
		SourceAccount:     "mine",
		BookID:            "123456",
		TargetAccount:     "default",
		WorkID:            500,
		EditionID:         600,
		ProposedStatusID:  HardcoverStatusCurrentlyReading,
		PolicyVersion:     "v1",
		SourceFingerprint: "weread:10001",
		TargetFingerprint: "hardcover:20002",
		State:             "uncertain",
		ErrorMessage:      "gateway timeout",
		CreatedAt:         t0,
		UpdatedAt:         t0,
	}
	intentID, err := store.RecordIntent(ctx, intent)
	if err != nil {
		t.Fatal(err)
	}

	src := &mockPreviewSource{
		book:        Book{ID: "123456", Title: "Restart Book"},
		progress:    Progress{Status: "reading", Percent: 30, Started: true},
		fingerprint: "weread:10001",
	}

	t.Run("reconciles successfully when mutation landed before timeout", func(t *testing.T) {
		// Target actually HAS the proposed status!
		tgt := &mockApplyTarget{
			status: &UserBookStatus{
				ID:       999,
				BookID:   500,
				StatusID: HardcoverStatusCurrentlyReading,
			},
			fingerprint: "hardcover:20002",
		}

		res, err := ApplySync(ctx, store, src, tgt, "weread", "mine", "123456", "default", ApplySyncOptions{
			PolicyVersion: "v1",
			Now:           t1,
		})
		if err != nil {
			t.Fatalf("ApplySync reconciliation failed: %v", err)
		}

		if res.Action != "applied" || !res.Reconciled {
			t.Fatalf("expected action applied and reconciled=true, got %+v", res)
		}
		if res.IntentID != intentID {
			t.Fatalf("expected intent ID %d, got %d", intentID, res.IntentID)
		}

		// Zero new mutations sent!
		if len(tgt.mutations) != 0 {
			t.Fatalf("reconciliation should not send duplicate mutations: %d sent", len(tgt.mutations))
		}

		// Baseline is now established
		b, err := store.GetStatusBaseline(ctx, "weread", "mine", "123456", "default")
		if err != nil || b == nil || b.TargetStatusID != HardcoverStatusCurrentlyReading {
			t.Fatalf("baseline not established on reconciliation: %+v, err %v", b, err)
		}

		// Active intent cleared
		active, err := store.GetActiveIntent(ctx, "weread", "mine", "123456", "default")
		if err != nil || active != nil {
			t.Fatalf("active intent should be cleared, got %+v", active)
		}
	})
}
