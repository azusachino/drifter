package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"path/filepath"
	"testing"

	"github.com/azusachino/drifter/internal/companion"
)

type cliMultiMockSource struct {
	books       map[string]companion.Book
	progresses  map[string]companion.Progress
	fingerprint string
}

func (m *cliMultiMockSource) Book(_ context.Context, id string) (companion.Book, error) {
	return m.books[id], nil
}

func (m *cliMultiMockSource) Progress(_ context.Context, id string) (companion.Progress, error) {
	return m.progresses[id], nil
}

func (m *cliMultiMockSource) ResolveAccountFingerprint(_ context.Context, _ string) (string, error) {
	return m.fingerprint, nil
}

type cliMultiMockTarget struct {
	statuses    map[int64]*companion.UserBookStatus
	fingerprint string
	mutations   []companion.StatusMutationRequest
}

func (m *cliMultiMockTarget) GetUserBookStatus(_ context.Context, workID int64) (*companion.UserBookStatus, error) {
	return m.statuses[workID], nil
}

func (m *cliMultiMockTarget) ResolveAccountFingerprint(_ context.Context) (string, error) {
	return m.fingerprint, nil
}

func (m *cliMultiMockTarget) ApplyStatusMutation(_ context.Context, req companion.StatusMutationRequest) (companion.StatusMutationOutcome, error) {
	m.mutations = append(m.mutations, req)
	m.statuses[req.WorkID] = &companion.UserBookStatus{
		ID:       req.WorkID * 10,
		BookID:   req.WorkID,
		StatusID: req.StatusID,
	}
	return companion.StatusMutationOutcome{
		CurrentStatus: req.StatusID,
		UserBookID:    req.WorkID * 10,
	}, nil
}

func TestCLIReconcileMissingFlags(t *testing.T) {
	var out, stderr bytes.Buffer
	err := run(context.Background(), []string{"reconcile"}, &out, &stderr, clients{})
	if err == nil {
		t.Fatal("expected error on missing reconcile flags")
	}
}

func TestCLIReconcileDryRunAndLiveRun(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "state.sqlite")
	store, err := companion.OpenStore(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	m := companion.Mapping{
		Provider:      "weread",
		SourceAccount: "mine",
		BookID:        "123",
		TargetAccount: "default",
		WorkID:        500,
		EditionID:     600,
		SourceTitle:   "CLI Reconcile Book",
		SourceISBN:    "9780123456789",
	}
	if err := store.Confirm(ctx, m, false); err != nil {
		t.Fatal(err)
	}
	store.Close()

	src := &cliMultiMockSource{
		books: map[string]companion.Book{
			"123": {ID: "123", Title: "CLI Reconcile Book"},
		},
		progresses: map[string]companion.Progress{
			"123": {Status: "reading", Percent: 40, Started: true},
		},
		fingerprint: "weread:10001",
	}
	tgt := &cliMultiMockTarget{
		statuses: map[int64]*companion.UserBookStatus{
			500: nil,
		},
		fingerprint: "hardcover:20002",
	}

	// 1. Dry run
	var outDry, stderrDry bytes.Buffer
	err = run(ctx, []string{
		"reconcile",
		"--account", "mine",
		"--target-account", "default",
		"--db", dbPath,
		"--dry-run",
	}, &outDry, &stderrDry, clients{
		previewSource: func() (companion.PreviewSource, error) { return src, nil },
		previewTarget: func() (companion.PreviewTarget, error) { return tgt, nil },
		applyTarget:   func() (companion.ApplyTarget, error) { return tgt, nil },
	})
	if err != nil {
		t.Fatalf("dry-run reconcile failed: %v", err)
	}

	var sumDry companion.ReconcileSummary
	if err := json.Unmarshal(outDry.Bytes(), &sumDry); err != nil {
		t.Fatalf("failed to decode dry-run output: %v", err)
	}
	if sumDry.TotalMappings != 1 || sumDry.AppliedCount != 1 || !sumDry.DryRun {
		t.Fatalf("unexpected dry-run summary: %+v", sumDry)
	}
	if len(tgt.mutations) != 0 {
		t.Fatalf("dry-run performed mutations: %d", len(tgt.mutations))
	}

	// 2. Live run
	var outLive, stderrLive bytes.Buffer
	err = run(ctx, []string{
		"reconcile",
		"--account", "mine",
		"--target-account", "default",
		"--db", dbPath,
	}, &outLive, &stderrLive, clients{
		previewSource: func() (companion.PreviewSource, error) { return src, nil },
		previewTarget: func() (companion.PreviewTarget, error) { return tgt, nil },
		applyTarget:   func() (companion.ApplyTarget, error) { return tgt, nil },
	})
	if err != nil {
		t.Fatalf("live reconcile failed: %v", err)
	}

	var sumLive companion.ReconcileSummary
	if err := json.Unmarshal(outLive.Bytes(), &sumLive); err != nil {
		t.Fatalf("failed to decode live run output: %v", err)
	}
	if sumLive.Status != "completed" || sumLive.AppliedCount != 1 || sumLive.RunID <= 0 {
		t.Fatalf("unexpected live summary: %+v", sumLive)
	}
	if len(tgt.mutations) != 1 {
		t.Fatalf("expected 1 mutation, got %d", len(tgt.mutations))
	}
}
