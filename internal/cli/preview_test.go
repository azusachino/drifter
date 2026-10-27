package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/azusachino/drifter/internal/companion"
)

type cliMockPreviewSource struct {
	book        companion.Book
	progress    companion.Progress
	fingerprint string
	bookErr     error
	progressErr error
	fpErr       error
}

func (m *cliMockPreviewSource) Book(_ context.Context, _ string) (companion.Book, error) {
	return m.book, m.bookErr
}

func (m *cliMockPreviewSource) Progress(_ context.Context, _ string) (companion.Progress, error) {
	return m.progress, m.progressErr
}

func (m *cliMockPreviewSource) ResolveAccountFingerprint(_ context.Context, _ string) (string, error) {
	return m.fingerprint, m.fpErr
}

type cliMockPreviewTarget struct {
	status      *companion.UserBookStatus
	fingerprint string
	statusErr   error
	fpErr       error
}

func (m *cliMockPreviewTarget) GetUserBookStatus(_ context.Context, _ int64) (*companion.UserBookStatus, error) {
	return m.status, m.statusErr
}

func (m *cliMockPreviewTarget) ResolveAccountFingerprint(_ context.Context) (string, error) {
	return m.fingerprint, m.fpErr
}

func TestCLIPreviewMissingRequiredFlags(t *testing.T) {
	cases := []struct {
		name string
		args []string
	}{
		{"no flags", []string{"preview"}},
		{"missing book-id", []string{"preview", "--account", "mine", "--target-account", "default"}},
		{"missing account", []string{"preview", "--book-id", "123", "--target-account", "default"}},
		{"missing target-account", []string{"preview", "--book-id", "123", "--account", "mine"}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var out, stderr bytes.Buffer
			err := run(context.Background(), tc.args, &out, &stderr, clients{})
			if err == nil {
				t.Fatalf("expected error for %s, got nil", tc.name)
			}
		})
	}
}

func TestCLIPreviewUnconfirmedMapping(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "state.sqlite")
	store, err := companion.OpenStore(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	store.Close()

	src := &cliMockPreviewSource{
		fingerprint: "weread:10001",
	}
	tgt := &cliMockPreviewTarget{
		fingerprint: "hardcover:20002",
	}

	var out, stderr bytes.Buffer
	err = run(context.Background(), []string{
		"preview",
		"--book-id", "123456",
		"--account", "mine",
		"--target-account", "default",
		"--db", dbPath,
	}, &out, &stderr, clients{
		previewSource: func() (companion.PreviewSource, error) { return src, nil },
		previewTarget: func() (companion.PreviewTarget, error) { return tgt, nil },
	})
	if err == nil || !strings.Contains(err.Error(), "no confirmed mapping found") {
		t.Fatalf("expected unconfirmed mapping error, got: %v", err)
	}
}

func TestCLIPreviewSuccessfulCreateProposal(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "state.sqlite")
	store, err := companion.OpenStore(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	mapping := companion.Mapping{
		Provider:      "weread",
		SourceAccount: "mine",
		BookID:        "123456",
		TargetAccount: "default",
		WorkID:        500,
		EditionID:     600,
		SourceTitle:   "Preview Book",
		SourceISBN:    "9780123456789",
	}
	if err := store.Confirm(ctx, mapping, false); err != nil {
		t.Fatal(err)
	}
	store.Close()

	src := &cliMockPreviewSource{
		book:        companion.Book{ID: "123456", Title: "Preview Book"},
		progress:    companion.Progress{Status: "reading", Percent: 33, Started: true},
		fingerprint: "weread:10001",
	}
	tgt := &cliMockPreviewTarget{
		status:      nil, // Target missing -> action create
		fingerprint: "hardcover:20002",
	}

	var out, stderr bytes.Buffer
	err = run(context.Background(), []string{
		"preview",
		"--book-id", "123456",
		"--account", "mine",
		"--target-account", "default",
		"--db", dbPath,
	}, &out, &stderr, clients{
		previewSource: func() (companion.PreviewSource, error) { return src, nil },
		previewTarget: func() (companion.PreviewTarget, error) { return tgt, nil },
	})
	if err != nil {
		t.Fatalf("cli preview failed: %v", err)
	}

	var prop companion.SyncProposal
	if err := json.Unmarshal(out.Bytes(), &prop); err != nil {
		t.Fatalf("failed to parse JSON output: %v, raw: %s", err, out.String())
	}
	if prop.Action != companion.ActionCreate {
		t.Fatalf("expected action create, got %q", prop.Action)
	}
	if prop.Reason != companion.ReasonTargetMissing {
		t.Fatalf("expected reason %s, got %s", companion.ReasonTargetMissing, prop.Reason)
	}
	if prop.WorkID != 500 || prop.ProposedStatusID != companion.HardcoverStatusCurrentlyReading {
		t.Fatalf("unexpected proposal payload: %+v", prop)
	}
}

func TestCLIPreviewFingerprintMismatchRefusal(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "state.sqlite")
	store, err := companion.OpenStore(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	now := time.Now().UTC()
	mapping := companion.Mapping{
		Provider:      "weread",
		SourceAccount: "mine",
		BookID:        "123456",
		TargetAccount: "default",
		WorkID:        500,
		EditionID:     600,
		SourceTitle:   "Preview Book",
		SourceISBN:    "9780123456789",
	}
	if err := store.Confirm(ctx, mapping, false); err != nil {
		t.Fatal(err)
	}
	// Pre-bind different fingerprint
	if _, err := store.VerifyOrBindAccountFingerprint(ctx, "weread", "mine", "weread:ORIGINAL", now); err != nil {
		t.Fatal(err)
	}
	store.Close()

	src := &cliMockPreviewSource{
		book:        companion.Book{ID: "123456", Title: "Preview Book"},
		progress:    companion.Progress{Status: "reading", Percent: 33, Started: true},
		fingerprint: "weread:DIFFERENT",
	}
	tgt := &cliMockPreviewTarget{
		status:      nil,
		fingerprint: "hardcover:20002",
	}

	var out, stderr bytes.Buffer
	err = run(context.Background(), []string{
		"preview",
		"--book-id", "123456",
		"--account", "mine",
		"--target-account", "default",
		"--db", dbPath,
	}, &out, &stderr, clients{
		previewSource: func() (companion.PreviewSource, error) { return src, nil },
		previewTarget: func() (companion.PreviewTarget, error) { return tgt, nil },
	})
	if err == nil || !strings.Contains(err.Error(), "mismatch") {
		t.Fatalf("expected fingerprint mismatch error, got: %v", err)
	}
}
