package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"path/filepath"
	"testing"
	"time"

	"github.com/azusachino/drifter/internal/companion"
)

func TestCLITargetStatus(t *testing.T) {
	tgt := &cliMockPreviewTarget{
		status: &companion.UserBookStatus{
			ID:       101,
			BookID:   42,
			StatusID: companion.HardcoverStatusCurrentlyReading,
		},
		fingerprint: "hardcover:test",
	}

	var out, stderr bytes.Buffer
	err := run(context.Background(), []string{
		"target", "status",
		"--work-id", "42",
	}, &out, &stderr, clients{
		previewTarget: func() (companion.PreviewTarget, error) { return tgt, nil },
	})
	if err != nil {
		t.Fatalf("target status failed: %v", err)
	}

	var res map[string]any
	if err := json.Unmarshal(out.Bytes(), &res); err != nil {
		t.Fatalf("failed to decode JSON: %v, raw: %s", err, out.String())
	}
	if res["work_id"] != float64(42) || res["status_id"] != float64(companion.HardcoverStatusCurrentlyReading) {
		t.Fatalf("unexpected target status output: %+v", res)
	}
	if res["on_shelf"] != true {
		t.Fatalf("expected on_shelf=true, got %+v", res)
	}
}

func TestCLITargetStatusNotOnShelf(t *testing.T) {
	tgt := &cliMockPreviewTarget{
		status:      nil,
		fingerprint: "hardcover:test",
	}

	var out, stderr bytes.Buffer
	err := run(context.Background(), []string{
		"target", "status",
		"--work-id", "99",
	}, &out, &stderr, clients{
		previewTarget: func() (companion.PreviewTarget, error) { return tgt, nil },
	})
	if err != nil {
		t.Fatalf("target status failed: %v", err)
	}

	var res map[string]any
	if err := json.Unmarshal(out.Bytes(), &res); err != nil {
		t.Fatalf("failed to decode JSON: %v", err)
	}
	if res["on_shelf"] != false || res["status"] != "none" {
		t.Fatalf("expected on_shelf=false and status=none, got %+v", res)
	}
}

func TestCLITargetSetStatus(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "state.sqlite")
	store, err := companion.OpenStore(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	now := time.Now().UTC()
	err = store.SaveStatusBaseline(ctx, companion.StatusBaseline{
		Provider:          "weread",
		SourceAccount:     "default",
		BookID:            "123456",
		TargetAccount:     "default",
		WorkID:            42,
		SourceStatusID:    companion.HardcoverStatusCurrentlyReading,
		TargetStatusID:    companion.HardcoverStatusWantToRead,
		PolicyVersion:     "v1",
		SourceFingerprint: "weread:test",
		TargetFingerprint: "hardcover:test",
		LastSyncedAt:      now,
	})
	if err != nil {
		t.Fatal(err)
	}
	store.Close()

	tgt := &cliMockApplyTarget{
		cliMockPreviewTarget: cliMockPreviewTarget{fingerprint: "hardcover:test"},
	}

	var out, stderr bytes.Buffer
	err = run(ctx, []string{
		"target", "set-status",
		"--work-id", "42",
		"--status", "reading",
		"--db", dbPath,
	}, &out, &stderr, clients{
		applyTarget: func() (companion.ApplyTarget, error) { return tgt, nil },
	})
	if err != nil {
		t.Fatalf("target set-status failed: %v", err)
	}

	var res map[string]any
	if err := json.Unmarshal(out.Bytes(), &res); err != nil {
		t.Fatalf("failed to decode JSON: %v", err)
	}
	if res["current_status_id"] != float64(companion.HardcoverStatusCurrentlyReading) {
		t.Fatalf("expected status currently reading, got %+v", res)
	}

	// Verify target set-status with --read-date
	out.Reset()
	err = run(ctx, []string{
		"target", "set-status",
		"--work-id", "42",
		"--status", "read",
		"--read-date", "2025-10-15",
		"--db", dbPath,
	}, &out, &stderr, clients{
		applyTarget: func() (companion.ApplyTarget, error) { return tgt, nil },
	})
	if err != nil {
		t.Fatalf("target set-status with read-date failed: %v", err)
	}
	lastMut := tgt.mutations[len(tgt.mutations)-1]
	if lastMut.LastReadDate == nil || *lastMut.LastReadDate != "2025-10-15" {
		t.Fatalf("expected last_read_date 2025-10-15, got %v", lastMut.LastReadDate)
	}

	// Verify local baseline was updated
	storeRead, err := companion.OpenStoreForRead(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer storeRead.Close()
	baselines, err := storeRead.ListStatusBaselines(ctx)
	if err != nil || len(baselines) != 1 {
		t.Fatalf("failed to list baselines: %v, len=%d", err, len(baselines))
	}
	if baselines[0].TargetStatusID != companion.HardcoverStatusRead {
		t.Fatalf("expected updated baseline target_status_id=%d, got %d",
			companion.HardcoverStatusRead, baselines[0].TargetStatusID)
	}
}

func TestCLIBaselinesAndResetBaseline(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "state.sqlite")
	store, err := companion.OpenStore(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	now := time.Now().UTC()
	err = store.SaveStatusBaseline(ctx, companion.StatusBaseline{
		Provider:          "weread",
		SourceAccount:     "default",
		BookID:            "123456",
		TargetAccount:     "default",
		WorkID:            42,
		SourceStatusID:    companion.HardcoverStatusCurrentlyReading,
		TargetStatusID:    companion.HardcoverStatusCurrentlyReading,
		PolicyVersion:     "v1",
		SourceFingerprint: "weread:test",
		TargetFingerprint: "hardcover:test",
		LastSyncedAt:      now,
	})
	if err != nil {
		t.Fatal(err)
	}
	store.Close()

	// List baselines
	var out, stderr bytes.Buffer
	err = run(ctx, []string{
		"baselines",
		"--db", dbPath,
	}, &out, &stderr, clients{})
	if err != nil {
		t.Fatalf("baselines command failed: %v", err)
	}

	var rows []companion.StatusBaseline
	if err := json.Unmarshal(out.Bytes(), &rows); err != nil || len(rows) != 1 {
		t.Fatalf("expected 1 baseline row, got %v (raw: %s)", err, out.String())
	}
	if rows[0].BookID != "123456" {
		t.Fatalf("expected 123456, got %s", rows[0].BookID)
	}

	// Reset baseline
	out.Reset()
	err = run(ctx, []string{
		"reset-baseline",
		"--book-id", "123456",
		"--db", dbPath,
	}, &out, &stderr, clients{})
	if err != nil {
		t.Fatalf("reset-baseline command failed: %v", err)
	}

	var resetRes map[string]any
	if err := json.Unmarshal(out.Bytes(), &resetRes); err != nil {
		t.Fatalf("failed to decode JSON: %v", err)
	}
	if resetRes["reset"] != true {
		t.Fatalf("expected reset=true, got %+v", resetRes)
	}

	// Verify baselines is now empty
	out.Reset()
	err = run(ctx, []string{
		"baselines",
		"--db", dbPath,
	}, &out, &stderr, clients{})
	if err != nil {
		t.Fatalf("baselines command failed: %v", err)
	}
	var emptyRows []companion.StatusBaseline
	if err := json.Unmarshal(out.Bytes(), &emptyRows); err != nil || len(emptyRows) != 0 {
		t.Fatalf("expected 0 baseline rows after reset, got %d", len(emptyRows))
	}
}

func TestCLIHoldsAndClearHold(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "state.sqlite")
	store, err := companion.OpenStore(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	now := time.Now().UTC()

	// Insert hold
	runID, err := store.CreateSyncRun(ctx, "default", "default", "v1", now)
	if err != nil {
		t.Fatal(err)
	}
	_, err = store.RecordSyncHold(ctx, companion.SyncHold{
		RunID:         runID,
		Provider:      "weread",
		SourceAccount: "default",
		BookID:        "123456",
		TargetAccount: "default",
		WorkID:        42,
		Reason:        "user_conflict",
		Message:       "manual conflict",
		Evidence:      "{}",
		CreatedAt:     now,
	})
	if err != nil {
		t.Fatal(err)
	}
	store.Close()

	// List holds
	var out, stderr bytes.Buffer
	err = run(ctx, []string{
		"holds",
		"--db", dbPath,
	}, &out, &stderr, clients{})
	if err != nil {
		t.Fatalf("holds command failed: %v", err)
	}

	var holds []companion.SyncHold
	if err := json.Unmarshal(out.Bytes(), &holds); err != nil || len(holds) != 1 {
		t.Fatalf("expected 1 hold row, got %v (raw: %s)", err, out.String())
	}
	if holds[0].BookID != "123456" {
		t.Fatalf("expected 123456, got %s", holds[0].BookID)
	}

	// Clear hold
	out.Reset()
	err = run(ctx, []string{
		"clear-hold",
		"--book-id", "123456",
		"--db", dbPath,
	}, &out, &stderr, clients{})
	if err != nil {
		t.Fatalf("clear-hold command failed: %v", err)
	}

	var clearRes map[string]any
	if err := json.Unmarshal(out.Bytes(), &clearRes); err != nil {
		t.Fatalf("failed to decode JSON: %v", err)
	}
	if clearRes["cleared_count"] != float64(1) {
		t.Fatalf("expected cleared_count=1, got %+v", clearRes)
	}

	// Verify holds is now empty
	out.Reset()
	err = run(ctx, []string{
		"holds",
		"--db", dbPath,
	}, &out, &stderr, clients{})
	if err != nil {
		t.Fatalf("holds command failed: %v", err)
	}
	var emptyHolds []companion.SyncHold
	if err := json.Unmarshal(out.Bytes(), &emptyHolds); err != nil || len(emptyHolds) != 0 {
		t.Fatalf("expected 0 holds after clear, got %d", len(emptyHolds))
	}
}
