package companion

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"
)

type multiMockSource struct {
	books       map[string]Book
	progresses  map[string]Progress
	fingerprint string
}

func (m *multiMockSource) Book(_ context.Context, id string) (Book, error) {
	b, ok := m.books[id]
	if !ok {
		return Book{}, errors.New("book not found")
	}
	return b, nil
}

func (m *multiMockSource) Progress(_ context.Context, id string) (Progress, error) {
	p, ok := m.progresses[id]
	if !ok {
		return Progress{}, errors.New("progress not found")
	}
	return p, nil
}

func (m *multiMockSource) ResolveAccountFingerprint(_ context.Context, _ string) (string, error) {
	return m.fingerprint, nil
}

type multiMockTarget struct {
	statuses    map[int64]*UserBookStatus
	fingerprint string
	mutations   []StatusMutationRequest
	failWorkID  int64
}

func (m *multiMockTarget) GetUserBookStatus(_ context.Context, workID int64) (*UserBookStatus, error) {
	return m.statuses[workID], nil
}

func (m *multiMockTarget) ResolveAccountFingerprint(_ context.Context) (string, error) {
	return m.fingerprint, nil
}

func (m *multiMockTarget) ApplyStatusMutation(_ context.Context, req StatusMutationRequest) (StatusMutationOutcome, error) {
	if req.WorkID == m.failWorkID {
		return StatusMutationOutcome{}, errors.New("provider error on work")
	}
	m.mutations = append(m.mutations, req)
	m.statuses[req.WorkID] = &UserBookStatus{
		ID:       req.WorkID * 10,
		BookID:   req.WorkID,
		StatusID: req.StatusID,
	}
	return StatusMutationOutcome{
		CurrentStatus: req.StatusID,
		UserBookID:    req.WorkID * 10,
	}, nil
}

func TestReconcileAccount_FullSuccessAndMonotonicRun(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.sqlite")
	store, err := OpenStore(path)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	ctx := context.Background()
	now := time.Date(2026, 10, 27, 12, 0, 0, 0, time.UTC)

	// Seed 2 mappings
	m1 := Mapping{
		Provider:      "weread",
		SourceAccount: "mine",
		BookID:        "101",
		TargetAccount: "default",
		WorkID:        1001,
		EditionID:     2001,
		SourceTitle:   "Book One",
		SourceISBN:    "9780000000001",
	}
	m2 := Mapping{
		Provider:      "weread",
		SourceAccount: "mine",
		BookID:        "102",
		TargetAccount: "default",
		WorkID:        1002,
		EditionID:     2002,
		SourceTitle:   "Book Two",
		SourceISBN:    "9780000000002",
	}
	if err := store.Confirm(ctx, m1, false); err != nil {
		t.Fatal(err)
	}
	if err := store.Confirm(ctx, m2, false); err != nil {
		t.Fatal(err)
	}

	src := &multiMockSource{
		books: map[string]Book{
			"101": {ID: "101", Title: "Book One"},
			"102": {ID: "102", Title: "Book Two"},
		},
		progresses: map[string]Progress{
			"101": {Status: "reading", Percent: 15, Started: true},
			"102": {Status: "read", Percent: 100, Started: true},
		},
		fingerprint: "weread:10001",
	}
	tgt := &multiMockTarget{
		statuses: map[int64]*UserBookStatus{
			1001: nil,                                                    // Missing -> create
			1002: {ID: 555, BookID: 1002, StatusID: HardcoverStatusRead}, // Matching -> no-op
		},
		fingerprint: "hardcover:20002",
	}

	summary, err := ReconcileAccount(ctx, store, src, tgt, ReconcileOptions{
		SourceAccount: "mine",
		TargetAccount: "default",
		PolicyVersion: "v1",
		Now:           now,
	})
	if err != nil {
		t.Fatalf("ReconcileAccount failed: %v", err)
	}

	if summary.Status != "completed" {
		t.Fatalf("expected status completed, got %q", summary.Status)
	}
	if summary.TotalMappings != 2 || summary.AppliedCount != 1 || summary.NoOpCount != 1 {
		t.Fatalf("unexpected summary counts: %+v", summary)
	}
	if summary.RunID <= 0 {
		t.Fatalf("expected positive run ID, got %d", summary.RunID)
	}

	// Verify run persisted in SQLite
	run, err := store.GetLatestSyncRun(ctx, "mine", "default")
	if err != nil || run == nil || run.ID != summary.RunID || run.Status != "completed" {
		t.Fatalf("sync run not persisted correctly: %+v, err %v", run, err)
	}
	if run.AppliedCount != 1 || run.NoOpCount != 1 {
		t.Fatalf("unexpected counts in stored run: %+v", run)
	}
}

func TestReconcileAccount_PartialFailureIsolation_US10(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.sqlite")
	store, err := OpenStore(path)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	ctx := context.Background()
	now := time.Date(2026, 10, 27, 12, 0, 0, 0, time.UTC)

	// Seed 2 mappings
	m1 := Mapping{
		Provider:      "weread",
		SourceAccount: "mine",
		BookID:        "101",
		TargetAccount: "default",
		WorkID:        1001,
		EditionID:     2001,
		SourceTitle:   "Book Failing",
		SourceISBN:    "9780000000001",
	}
	m2 := Mapping{
		Provider:      "weread",
		SourceAccount: "mine",
		BookID:        "102",
		TargetAccount: "default",
		WorkID:        1002,
		EditionID:     2002,
		SourceTitle:   "Book Succeeding",
		SourceISBN:    "9780000000002",
	}
	if err := store.Confirm(ctx, m1, false); err != nil {
		t.Fatal(err)
	}
	if err := store.Confirm(ctx, m2, false); err != nil {
		t.Fatal(err)
	}

	src := &multiMockSource{
		books: map[string]Book{
			"101": {ID: "101", Title: "Book Failing"},
			"102": {ID: "102", Title: "Book Succeeding"},
		},
		progresses: map[string]Progress{
			"101": {Status: "reading", Percent: 10, Started: true},
			"102": {Status: "reading", Percent: 50, Started: true},
		},
		fingerprint: "weread:10001",
	}
	tgt := &multiMockTarget{
		statuses: map[int64]*UserBookStatus{
			1001: nil,
			1002: nil,
		},
		failWorkID:  1001, // 1001 fails!
		fingerprint: "hardcover:20002",
	}

	summary, err := ReconcileAccount(ctx, store, src, tgt, ReconcileOptions{
		SourceAccount: "mine",
		TargetAccount: "default",
		PolicyVersion: "v1",
		Now:           now,
	})
	if err != nil {
		t.Fatalf("ReconcileAccount returned error instead of partial run: %v", err)
	}

	// Book 1001 failed, but book 1002 succeeded!
	if summary.Status != "partial" {
		t.Fatalf("expected status partial, got %q", summary.Status)
	}
	if summary.AppliedCount != 1 || summary.ErrorCount != 1 {
		t.Fatalf("expected 1 applied and 1 error, got %+v", summary)
	}

	// Verify run record has status "partial"
	run, err := store.GetLatestSyncRun(ctx, "mine", "default")
	if err != nil || run == nil || run.Status != "partial" {
		t.Fatalf("stored run status mismatch: %+v, err %v", run, err)
	}
}

func TestReconcileAccount_ConflictHoldsPersisted(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.sqlite")
	store, err := OpenStore(path)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	ctx := context.Background()
	now := time.Date(2026, 10, 27, 12, 0, 0, 0, time.UTC)

	// Mapping with manual edit on target
	m := Mapping{
		Provider:      "weread",
		SourceAccount: "mine",
		BookID:        "101",
		TargetAccount: "default",
		WorkID:        1001,
		EditionID:     2001,
		SourceTitle:   "Manual Edit Book",
		SourceISBN:    "9780000000001",
	}
	if err := store.Confirm(ctx, m, false); err != nil {
		t.Fatal(err)
	}
	if err := store.SaveStatusBaseline(ctx, StatusBaseline{
		Provider:          "weread",
		SourceAccount:     "mine",
		BookID:            "101",
		TargetAccount:     "default",
		WorkID:            1001,
		EditionID:         2001,
		SourceStatusID:    HardcoverStatusCurrentlyReading,
		TargetStatusID:    HardcoverStatusCurrentlyReading,
		PolicyVersion:     "v1",
		SourceFingerprint: "weread:10001",
		TargetFingerprint: "hardcover:20002",
		LastSyncedAt:      now.Add(-24 * time.Hour),
	}); err != nil {
		t.Fatal(err)
	}

	src := &multiMockSource{
		books: map[string]Book{
			"101": {ID: "101", Title: "Manual Edit Book"},
		},
		progresses: map[string]Progress{
			"101": {Status: "reading", Percent: 50, Started: true},
		},
		fingerprint: "weread:10001",
	}
	tgt := &multiMockTarget{
		statuses: map[int64]*UserBookStatus{
			1001: {ID: 1001, BookID: 1001, StatusID: HardcoverStatusRead}, // Manually edited on Hardcover
		},
		fingerprint: "hardcover:20002",
	}

	summary, err := ReconcileAccount(ctx, store, src, tgt, ReconcileOptions{
		SourceAccount: "mine",
		TargetAccount: "default",
		PolicyVersion: "v1",
		Now:           now,
	})
	if err != nil {
		t.Fatalf("ReconcileAccount failed: %v", err)
	}

	if summary.ConflictCount != 1 || len(summary.Holds) != 1 {
		t.Fatalf("expected 1 conflict hold, got %+v", summary)
	}

	// Verify hold was saved in sync_holds table
	holds, err := store.ListSyncHolds(ctx, "mine", "default")
	if err != nil || len(holds) != 1 {
		t.Fatalf("holds not saved in store: %+v, err %v", holds, err)
	}
	if holds[0].Reason != ReasonManualTargetEdit {
		t.Fatalf("expected reason %s, got %s", ReasonManualTargetEdit, holds[0].Reason)
	}
}
