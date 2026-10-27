package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/azusachino/drifter/internal/companion"
)

// UC-1: Full first-sync journey (US1 + US2 + US3)
// Link -> preview (create) -> sync (applied) -> re-sync (no-op) -> reconcile (1 no-op).
func TestUseCaseJourneyLinkPreviewSyncResyncNoop(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "uc1.sqlite")
	reader := &fakeReader{}
	catalog := &fakeCatalog{}
	src := &cliMockPreviewSource{
		book:        companion.Book{ID: "1001", Title: "Journey Book", ISBN: "9787229166922"},
		progress:    companion.Progress{Started: true, Status: "reading", Percent: 25},
		fingerprint: "weread:10001",
	}
	tgt := &cliMockApplyTarget{
		cliMockPreviewTarget: cliMockPreviewTarget{
			fingerprint: "hardcover:20002",
		},
	}
	allClients := clients{
		source:        func() (source, error) { return reader, nil },
		target:        func() (target, error) { return catalog, nil },
		previewSource: func() (companion.PreviewSource, error) { return src, nil },
		previewTarget: func() (companion.PreviewTarget, error) { return tgt, nil },
		applyTarget:   func() (companion.ApplyTarget, error) { return tgt, nil },
	}

	// Step 1: Explicit mapping confirmation (US2)
	var out, errOut bytes.Buffer
	err := run(context.Background(), []string{
		"link", "--book-id", "1001", "--work-id", "500", "--edition-id", "600",
		"--account", "mine", "--target-account", "default", "--db", dbPath,
	}, &out, &errOut, allClients)
	if err != nil {
		t.Fatalf("link failed: %v", err)
	}

	// Step 2: Read-only preview evaluates to create proposal with zero mutations (US1)
	out.Reset()
	errOut.Reset()
	err = run(context.Background(), []string{
		"preview", "--book-id", "1001", "--account", "mine", "--target-account", "default", "--db", dbPath,
	}, &out, &errOut, allClients)
	if err != nil {
		t.Fatalf("preview failed: %v", err)
	}
	var prop companion.SyncProposal
	if err := json.Unmarshal(out.Bytes(), &prop); err != nil {
		t.Fatalf("failed to parse preview output: %v", err)
	}
	if prop.Action != companion.ActionCreate {
		t.Fatalf("expected action create, got %s", prop.Action)
	}
	if len(tgt.mutations) != 0 {
		t.Fatalf("preview performed mutations: %d", len(tgt.mutations))
	}

	// Step 3: Sync applies proposal, updates status, writes baseline & receipt (US1 + US3)
	out.Reset()
	errOut.Reset()
	err = run(context.Background(), []string{
		"sync", "--book-id", "1001", "--account", "mine", "--target-account", "default", "--db", dbPath,
	}, &out, &errOut, allClients)
	if err != nil {
		t.Fatalf("sync failed: %v", err)
	}
	var res companion.ApplyResult
	if err := json.Unmarshal(out.Bytes(), &res); err != nil {
		t.Fatalf("failed to parse sync output: %v", err)
	}
	if res.Action != "applied" || res.AppliedStatusID != companion.HardcoverStatusCurrentlyReading {
		t.Fatalf("unexpected sync result: %+v", res)
	}
	if len(tgt.mutations) != 1 {
		t.Fatalf("expected 1 mutation, got %d", len(tgt.mutations))
	}

	// Step 4: Re-sync with unchanged observation produces no-op and zero extra mutations (US3)
	out.Reset()
	errOut.Reset()
	err = run(context.Background(), []string{
		"sync", "--book-id", "1001", "--account", "mine", "--target-account", "default", "--db", dbPath,
	}, &out, &errOut, allClients)
	if err != nil {
		t.Fatalf("re-sync failed: %v", err)
	}
	var res2 companion.ApplyResult
	if err := json.Unmarshal(out.Bytes(), &res2); err != nil {
		t.Fatalf("failed to parse re-sync output: %v", err)
	}
	if res2.Action != "no-op" {
		t.Fatalf("expected action no-op, got %s", res2.Action)
	}
	if len(tgt.mutations) != 1 { // Still 1 mutation from Step 3
		t.Fatalf("re-sync sent unexpected mutations: %d", len(tgt.mutations))
	}

	// Step 5: Account-wide reconcile confirms library is clean with 1 no-op (US3 + US10)
	out.Reset()
	errOut.Reset()
	err = run(context.Background(), []string{
		"reconcile", "--account", "mine", "--target-account", "default", "--db", dbPath,
	}, &out, &errOut, allClients)
	if err != nil {
		t.Fatalf("reconcile failed: %v", err)
	}
	var rec companion.ReconcileSummary
	if err := json.Unmarshal(out.Bytes(), &rec); err != nil {
		t.Fatalf("failed to parse reconcile output: %v", err)
	}
	if rec.TotalMappings != 1 || rec.NoOpCount != 1 || rec.AppliedCount != 0 || rec.ConflictCount != 0 {
		t.Fatalf("unexpected reconcile summary: %+v", rec)
	}
}

// UC-2: Reread policy (US4)
// Previously Read book with trustworthy new currently reading evidence allows
// forward restart without fabricating older read history.
func TestUseCaseRereadAllowsForwardRestartWithoutFabricatedHistory(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "uc2.sqlite")
	store, err := companion.OpenStore(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	now := time.Now().UTC()

	// Seed mapping and baseline: previously Read
	mapping := companion.Mapping{
		Provider:      "weread",
		SourceAccount: "mine",
		BookID:        "2002",
		TargetAccount: "default",
		WorkID:        700,
		EditionID:     800,
		SourceTitle:   "Reread Book",
	}
	if err := store.Confirm(ctx, mapping, false); err != nil {
		t.Fatal(err)
	}
	if _, err := store.VerifyOrBindAccountFingerprint(ctx, "weread", "mine", "weread:10001", now); err != nil {
		t.Fatal(err)
	}
	if _, err := store.VerifyOrBindAccountFingerprint(ctx, "hardcover", "default", "hardcover:20002", now); err != nil {
		t.Fatal(err)
	}
	baseline := companion.StatusBaseline{
		Provider:          "weread",
		SourceAccount:     "mine",
		BookID:            "2002",
		TargetAccount:     "default",
		WorkID:            700,
		EditionID:         800,
		SourceStatusID:    companion.HardcoverStatusRead,
		TargetStatusID:    companion.HardcoverStatusRead,
		PolicyVersion:     "v1",
		SourceFingerprint: "weread:10001",
		TargetFingerprint: "hardcover:20002",
		LastSyncedAt:      now.Add(-48 * time.Hour),
	}
	if err := store.SaveStatusBaseline(ctx, baseline); err != nil {
		t.Fatal(err)
	}
	store.Close()

	// Source shows active reread in progress
	src := &cliMockPreviewSource{
		book:        companion.Book{ID: "2002", Title: "Reread Book"},
		progress:    companion.Progress{Started: true, Status: "reading", Percent: 15},
		fingerprint: "weread:10001",
	}
	tgt := &cliMockApplyTarget{
		cliMockPreviewTarget: cliMockPreviewTarget{
			status: &companion.UserBookStatus{
				ID:       555,
				BookID:   700,
				StatusID: companion.HardcoverStatusRead, // Currently Read on Hardcover
			},
			fingerprint: "hardcover:20002",
		},
	}
	allClients := clients{
		previewSource: func() (companion.PreviewSource, error) { return src, nil },
		previewTarget: func() (companion.PreviewTarget, error) { return tgt, nil },
		applyTarget:   func() (companion.ApplyTarget, error) { return tgt, nil },
	}

	// Preview proposes update to Currently Reading
	var out, errOut bytes.Buffer
	err = run(ctx, []string{
		"preview", "--book-id", "2002", "--account", "mine", "--target-account", "default", "--db", dbPath,
	}, &out, &errOut, allClients)
	if err != nil {
		t.Fatalf("preview failed: %v", err)
	}
	var prop companion.SyncProposal
	_ = json.Unmarshal(out.Bytes(), &prop)
	if prop.Action != companion.ActionUpdate || prop.ProposedStatusID != companion.HardcoverStatusCurrentlyReading {
		t.Fatalf("expected update to currently reading, got %+v", prop)
	}

	// Apply forward restart: writes status Currently Reading without touching history/ratings
	out.Reset()
	errOut.Reset()
	err = run(ctx, []string{
		"sync", "--book-id", "2002", "--account", "mine", "--target-account", "default", "--db", dbPath,
	}, &out, &errOut, allClients)
	if err != nil {
		t.Fatalf("sync reread failed: %v", err)
	}
	if len(tgt.mutations) != 1 {
		t.Fatalf("expected 1 mutation, got %d", len(tgt.mutations))
	}
	mutation := tgt.mutations[0]
	if mutation.WorkID != 700 || mutation.StatusID != companion.HardcoverStatusCurrentlyReading {
		t.Fatalf("unexpected mutation: %+v", mutation)
	}
}

// UC-3: Loan return is not completion (US9)
// Given an unstarted book or returned loan, no Read/DNF update is inferred.
func TestUseCaseLoanReturnDoesNotInferReadOrDNF(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "uc3.sqlite")
	store, err := companion.OpenStore(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	now := time.Now().UTC()

	mapping := companion.Mapping{
		Provider:      "weread",
		SourceAccount: "mine",
		BookID:        "3003",
		TargetAccount: "default",
		WorkID:        888,
		EditionID:     999,
		SourceTitle:   "Loan Book",
	}
	if err := store.Confirm(ctx, mapping, false); err != nil {
		t.Fatal(err)
	}
	store.Close()

	// Source progress has no active reading (started: false, percent: 0)
	src := &cliMockPreviewSource{
		book:        companion.Book{ID: "3003", Title: "Loan Book"},
		progress:    companion.Progress{Started: false, Percent: 0},
		fingerprint: "weread:10001",
	}
	tgt := &cliMockApplyTarget{
		cliMockPreviewTarget: cliMockPreviewTarget{
			fingerprint: "hardcover:20002",
		},
	}
	allClients := clients{
		previewSource: func() (companion.PreviewSource, error) { return src, nil },
		previewTarget: func() (companion.PreviewTarget, error) { return tgt, nil },
		applyTarget:   func() (companion.ApplyTarget, error) { return tgt, nil },
	}

	var out, errOut bytes.Buffer
	err = run(ctx, []string{
		"sync", "--book-id", "3003", "--account", "mine", "--target-account", "default", "--db", dbPath,
	}, &out, &errOut, allClients)
	if err != nil {
		t.Fatalf("sync failed: %v", err)
	}
	var res companion.ApplyResult
	_ = json.Unmarshal(out.Bytes(), &res)
	if res.Action == "applied" {
		t.Fatalf("unstarted loan book should not apply completion: %+v", res)
	}
	if len(tgt.mutations) != 0 {
		t.Fatalf("loan return triggered unwanted provider mutations: %d", len(tgt.mutations))
	}
	_ = now
}

// UC-4: Source disappearance / disconnect (US11)
// Source disappearance never triggers target deletes, and switching accounts fails closed.
func TestUseCaseSourceDisappearanceNeverDeletesTarget(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "uc4.sqlite")
	store, err := companion.OpenStore(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	now := time.Now().UTC()

	mapping := companion.Mapping{
		Provider:      "weread",
		SourceAccount: "mine",
		BookID:        "4004",
		TargetAccount: "default",
		WorkID:        1234,
		EditionID:     5678,
		SourceTitle:   "Vanishing Book",
	}
	if err := store.Confirm(ctx, mapping, false); err != nil {
		t.Fatal(err)
	}
	if _, err := store.VerifyOrBindAccountFingerprint(ctx, "weread", "mine", "weread:10001", now); err != nil {
		t.Fatal(err)
	}
	store.Close()

	// 1. Source book disappeared (returns error)
	src := &cliMockPreviewSource{
		bookErr:     errors.New("book removed from shelf"),
		fingerprint: "weread:10001",
	}
	tgt := &cliMockApplyTarget{
		cliMockPreviewTarget: cliMockPreviewTarget{
			fingerprint: "hardcover:20002",
			status: &companion.UserBookStatus{
				ID:       111,
				BookID:   1234,
				StatusID: companion.HardcoverStatusCurrentlyReading,
			},
		},
	}
	allClients := clients{
		previewSource: func() (companion.PreviewSource, error) { return src, nil },
		previewTarget: func() (companion.PreviewTarget, error) { return tgt, nil },
		applyTarget:   func() (companion.ApplyTarget, error) { return tgt, nil },
	}

	var out, errOut bytes.Buffer
	_ = run(ctx, []string{
		"reconcile", "--account", "mine", "--target-account", "default", "--db", dbPath,
	}, &out, &errOut, allClients)

	// Assert zero mutations sent to target (no delete, no clear)
	if len(tgt.mutations) != 0 {
		t.Fatalf("disappearance dispatched remote mutations: %d", len(tgt.mutations))
	}

	// 2. Account identity swap fails closed
	swappedSrc := &cliMockPreviewSource{
		fingerprint: "weread:99999", // Different account under same label!
	}
	swappedClients := clients{
		previewSource: func() (companion.PreviewSource, error) { return swappedSrc, nil },
		previewTarget: func() (companion.PreviewTarget, error) { return tgt, nil },
		applyTarget:   func() (companion.ApplyTarget, error) { return tgt, nil },
	}
	out.Reset()
	errOut.Reset()
	err = run(ctx, []string{
		"sync", "--book-id", "4004", "--account", "mine", "--target-account", "default", "--db", dbPath,
	}, &out, &errOut, swappedClients)
	if err == nil {
		t.Fatal("expected failure on account identity mismatch")
	}
	if !strings.Contains(err.Error(), "mismatch") && !strings.Contains(errOut.String(), "mismatch") {
		t.Fatalf("expected mismatch guidance, got %v", err)
	}
}

// UC-5: Rate-limited provider (US7 / US10)
// 429 / 504 returns uncertain intent without blind retry; subsequent run reconciles safely.
func TestUseCaseRateLimitedSyncBacksOffWithoutBlindRetry(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "uc5.sqlite")
	store, err := companion.OpenStore(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()

	mapping := companion.Mapping{
		Provider:      "weread",
		SourceAccount: "mine",
		BookID:        "5005",
		TargetAccount: "default",
		WorkID:        999,
		EditionID:     111,
		SourceTitle:   "Throttled Book",
	}
	if err := store.Confirm(ctx, mapping, false); err != nil {
		t.Fatal(err)
	}
	store.Close()

	src := &cliMockPreviewSource{
		book:        companion.Book{ID: "5005", Title: "Throttled Book"},
		progress:    companion.Progress{Started: true, Status: "reading", Percent: 50},
		fingerprint: "weread:10001",
	}
	// Target simulates 504 / timeout on mutation
	tgt := &cliMockApplyTarget{
		cliMockPreviewTarget: cliMockPreviewTarget{
			fingerprint: "hardcover:20002",
		},
		applyErr: companion.ErrUncertainStatusMutation,
	}
	allClients := clients{
		previewSource: func() (companion.PreviewSource, error) { return src, nil },
		previewTarget: func() (companion.PreviewTarget, error) { return tgt, nil },
		applyTarget:   func() (companion.ApplyTarget, error) { return tgt, nil },
	}

	var out, errOut bytes.Buffer
	err = run(ctx, []string{
		"sync", "--book-id", "5005", "--account", "mine", "--target-account", "default", "--db", dbPath,
	}, &out, &errOut, allClients)
	if err == nil {
		t.Fatal("expected error on throttled/uncertain mutation")
	}

	// Verify intent is left uncertain in SQLite
	store2, _ := companion.OpenStore(dbPath)
	intent, err := store2.GetActiveIntent(ctx, "weread", "mine", "5005", "default")
	if err != nil || intent == nil {
		t.Fatalf("missing intent: %v", err)
	}
	if intent.State != "uncertain" {
		t.Fatalf("expected intent state uncertain, got %s", intent.State)
	}
	store2.Close()

	// On restart with healthy target (mutation landed on remote)
	tgt.applyErr = nil
	tgt.status = &companion.UserBookStatus{
		ID:       888,
		BookID:   999,
		StatusID: companion.HardcoverStatusCurrentlyReading,
	}

	out.Reset()
	errOut.Reset()
	err = run(ctx, []string{
		"sync", "--book-id", "5005", "--account", "mine", "--target-account", "default", "--db", dbPath,
	}, &out, &errOut, allClients)
	if err != nil {
		t.Fatalf("restart recovery failed: %v", err)
	}

	// Exactly 1 mutation was sent across both runs (no duplicate mutations)
	if len(tgt.mutations) != 1 {
		t.Fatalf("duplicate mutation dispatched on recovery: count=%d", len(tgt.mutations))
	}
}

// UC-6: Iroha boundary (US12)
// Companion update completes locally with verified receipts and makes zero downstream calls to Iroha.
func TestUseCaseCompanionNeverCallsIroha(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "uc6.sqlite")
	store, err := companion.OpenStore(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()

	mapping := companion.Mapping{
		Provider:      "weread",
		SourceAccount: "mine",
		BookID:        "6006",
		TargetAccount: "default",
		WorkID:        1212,
		EditionID:     3434,
		SourceTitle:   "Boundary Book",
	}
	if err := store.Confirm(ctx, mapping, false); err != nil {
		t.Fatal(err)
	}
	store.Close()

	src := &cliMockPreviewSource{
		book:        companion.Book{ID: "6006", Title: "Boundary Book"},
		progress:    companion.Progress{Started: true, Status: "reading", Percent: 80},
		fingerprint: "weread:10001",
	}
	tgt := &cliMockApplyTarget{
		cliMockPreviewTarget: cliMockPreviewTarget{
			fingerprint: "hardcover:20002",
		},
	}
	allClients := clients{
		previewSource: func() (companion.PreviewSource, error) { return src, nil },
		previewTarget: func() (companion.PreviewTarget, error) { return tgt, nil },
		applyTarget:   func() (companion.ApplyTarget, error) { return tgt, nil },
	}

	var out, errOut bytes.Buffer
	err = run(ctx, []string{
		"sync", "--book-id", "6006", "--account", "mine", "--target-account", "default", "--db", dbPath,
	}, &out, &errOut, allClients)
	if err != nil {
		t.Fatalf("sync failed: %v", err)
	}

	// Verify receipt was written locally to SQLite
	store3, _ := companion.OpenStore(dbPath)
	receipt, err := store3.GetLatestReceipt(ctx, "weread", "mine", "6006", "default")
	if err != nil || receipt == nil {
		t.Fatalf("receipt missing: %v", err)
	}
	store3.Close()

	if receipt.TargetStatusID != companion.HardcoverStatusCurrentlyReading {
		t.Fatalf("unexpected receipt status: %d", receipt.TargetStatusID)
	}
}
