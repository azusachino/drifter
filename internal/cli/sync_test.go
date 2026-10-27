package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"path/filepath"
	"testing"

	"github.com/azusachino/drifter/internal/companion"
)

type cliMockApplyTarget struct {
	cliMockPreviewTarget
	mutations []companion.StatusMutationRequest
	applyErr  error
}

func (m *cliMockApplyTarget) ApplyStatusMutation(_ context.Context, req companion.StatusMutationRequest) (companion.StatusMutationOutcome, error) {
	m.mutations = append(m.mutations, req)
	if m.applyErr != nil {
		return companion.StatusMutationOutcome{}, m.applyErr
	}
	m.status = &companion.UserBookStatus{
		ID:       999,
		BookID:   req.WorkID,
		StatusID: req.StatusID,
	}
	return companion.StatusMutationOutcome{
		CurrentStatus: req.StatusID,
		UserBookID:    999,
	}, nil
}

func TestCLISyncMissingFlags(t *testing.T) {
	var out, stderr bytes.Buffer
	err := run(context.Background(), []string{"sync"}, &out, &stderr, clients{})
	if err == nil {
		t.Fatal("expected error on missing sync flags")
	}
}

func TestCLISyncDryRunDoesNotMutate(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "state.sqlite")
	store, err := companion.OpenStore(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	mapping := companion.Mapping{
		Provider:      "weread",
		SourceAccount: "mine",
		BookID:        "123456",
		TargetAccount: "default",
		WorkID:        500,
		EditionID:     600,
		SourceTitle:   "Dry Run Book",
		SourceISBN:    "9780123456789",
	}
	if err := store.Confirm(context.Background(), mapping, false); err != nil {
		t.Fatal(err)
	}
	store.Close()

	src := &cliMockPreviewSource{
		book:        companion.Book{ID: "123456", Title: "Dry Run Book"},
		progress:    companion.Progress{Status: "reading", Percent: 50, Started: true},
		fingerprint: "weread:10001",
	}
	tgt := &cliMockApplyTarget{
		cliMockPreviewTarget: cliMockPreviewTarget{
			fingerprint: "hardcover:20002",
		},
	}

	var out, stderr bytes.Buffer
	err = run(context.Background(), []string{
		"sync",
		"--book-id", "123456",
		"--account", "mine",
		"--target-account", "default",
		"--db", dbPath,
		"--dry-run",
	}, &out, &stderr, clients{
		previewSource: func() (companion.PreviewSource, error) { return src, nil },
		previewTarget: func() (companion.PreviewTarget, error) { return tgt, nil },
		applyTarget:   func() (companion.ApplyTarget, error) { return tgt, nil },
	})
	if err != nil {
		t.Fatalf("dry-run sync failed: %v", err)
	}

	var prop companion.SyncProposal
	if err := json.Unmarshal(out.Bytes(), &prop); err != nil {
		t.Fatalf("failed to decode dry-run output: %v", err)
	}
	if prop.Action != companion.ActionCreate {
		t.Fatalf("expected action create, got %q", prop.Action)
	}
	if len(tgt.mutations) != 0 {
		t.Fatalf("dry-run performed mutations: %d", len(tgt.mutations))
	}
}

func TestCLISyncSuccessfulApply(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "state.sqlite")
	store, err := companion.OpenStore(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	mapping := companion.Mapping{
		Provider:      "weread",
		SourceAccount: "mine",
		BookID:        "123456",
		TargetAccount: "default",
		WorkID:        500,
		EditionID:     600,
		SourceTitle:   "Sync Book",
		SourceISBN:    "9780123456789",
	}
	if err := store.Confirm(context.Background(), mapping, false); err != nil {
		t.Fatal(err)
	}
	store.Close()

	src := &cliMockPreviewSource{
		book:        companion.Book{ID: "123456", Title: "Sync Book"},
		progress:    companion.Progress{Status: "reading", Percent: 50, Started: true},
		fingerprint: "weread:10001",
	}
	tgt := &cliMockApplyTarget{
		cliMockPreviewTarget: cliMockPreviewTarget{
			fingerprint: "hardcover:20002",
		},
	}

	var out, stderr bytes.Buffer
	err = run(context.Background(), []string{
		"sync",
		"--book-id", "123456",
		"--account", "mine",
		"--target-account", "default",
		"--db", dbPath,
	}, &out, &stderr, clients{
		previewSource: func() (companion.PreviewSource, error) { return src, nil },
		previewTarget: func() (companion.PreviewTarget, error) { return tgt, nil },
		applyTarget:   func() (companion.ApplyTarget, error) { return tgt, nil },
	})
	if err != nil {
		t.Fatalf("sync failed: %v", err)
	}

	var res companion.ApplyResult
	if err := json.Unmarshal(out.Bytes(), &res); err != nil {
		t.Fatalf("failed to decode sync result: %v", err)
	}
	if res.Action != "applied" {
		t.Fatalf("expected action applied, got %q", res.Action)
	}
	if res.AppliedStatusID != companion.HardcoverStatusCurrentlyReading {
		t.Fatalf("expected applied status currently reading, got %d", res.AppliedStatusID)
	}
	if len(tgt.mutations) != 1 {
		t.Fatalf("expected 1 mutation, got %d", len(tgt.mutations))
	}
}

func TestCLISyncZeroFlagsDefaultAccounts(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "state.sqlite")
	store, err := companion.OpenStore(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	mapping := companion.Mapping{
		Provider:      "weread",
		SourceAccount: companion.DefaultSourceAccount,
		BookID:        "123456",
		TargetAccount: companion.DefaultTargetAccount,
		WorkID:        500,
		EditionID:     600,
		SourceTitle:   "Sync Book",
		SourceISBN:    "9780123456789",
	}
	if err := store.Confirm(context.Background(), mapping, false); err != nil {
		t.Fatal(err)
	}
	store.Close()

	src := &cliMockPreviewSource{
		book:        companion.Book{ID: "123456", Title: "Sync Book"},
		progress:    companion.Progress{Status: "reading", Percent: 50, Started: true},
		fingerprint: "weread:10001",
	}
	tgt := &cliMockApplyTarget{
		cliMockPreviewTarget: cliMockPreviewTarget{
			fingerprint: "hardcover:20002",
		},
	}

	var out, stderr bytes.Buffer
	err = run(context.Background(), []string{
		"sync",
		"--book-id", "123456",
		"--db", dbPath,
	}, &out, &stderr, clients{
		previewSource: func() (companion.PreviewSource, error) { return src, nil },
		previewTarget: func() (companion.PreviewTarget, error) { return tgt, nil },
		applyTarget:   func() (companion.ApplyTarget, error) { return tgt, nil },
	})
	if err != nil {
		t.Fatalf("sync with zero account flags failed: %v", err)
	}

	var res companion.ApplyResult
	if err := json.Unmarshal(out.Bytes(), &res); err != nil {
		t.Fatalf("failed to decode sync result: %v", err)
	}
	if res.Action != "applied" {
		t.Fatalf("expected action applied, got %q", res.Action)
	}
}
