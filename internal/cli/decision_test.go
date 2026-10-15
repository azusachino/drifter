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

func TestCLIDecisionsPersistResetAndPendingSuppression(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.sqlite")
	runCmd := func(args ...string) error {
		return run(context.Background(), args, &bytes.Buffer{}, &bytes.Buffer{}, clients{})
	}
	base := []string{"--account", "src", "--target-account", "dst", "--db", path}
	for _, pair := range [][2]string{{"1", "exclude"}, {"2", "not-in-catalog"}, {"3", "skip"}} {
		args := append([]string{"decide", "--book-id", pair[0], "--decision", pair[1]}, base...)
		if err := runCmd(args...); err != nil {
			t.Fatal(err)
		}
	}
	if err := runCmd(append([]string{"decisions"}, base...)...); err != nil {
		t.Fatal(err)
	}
	public := 0
	now := time.Now().Unix()
	reader := &fakeReader{shelf: &companion.ShelfResult{Complete: true, Books: []companion.ShelfBook{
		{ID: "1", Secret: &public, LastOpened: &now}, {ID: "2", Secret: &public, LastOpened: &now}, {ID: "3", Secret: &public, LastOpened: &now},
	}}}
	out := &bytes.Buffer{}
	if err := run(context.Background(), append([]string{"pending"}, base...), out, &bytes.Buffer{}, clients{source: func() (source, error) { return reader, nil }}); err != nil {
		t.Fatal(err)
	}
	var report companion.PendingReport
	if err := json.Unmarshal(out.Bytes(), &report); err != nil || len(report.Books) != 1 || report.Books[0].ID != "3" {
		t.Fatalf("suppression/skip behavior: %+v %v", report, err)
	}
	reset := append([]string{"reset-decision", "--book-id", "1"}, base...)
	if err := runCmd(reset...); err != nil {
		t.Fatal(err)
	}
	if err := runCmd(reset...); err != nil {
		t.Fatal("absent reset not idempotent", err)
	}
	if err := runCmd(append([]string{"decide", "--book-id", "4", "--decision", "bad"}, base...)...); err == nil {
		t.Fatal("unknown decision accepted")
	}
}

func TestCLIDecisionWriterRefusesOwnedDBAndReaderDoesNotNeedProviders(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.sqlite")
	initialized, err := companion.OpenStore(path)
	if err != nil {
		t.Fatal(err)
	}
	initialized.Close()
	owner, err := companion.AcquireDatabaseOwnership(path)
	if err != nil {
		t.Fatal(err)
	}
	defer owner.Close()
	args := []string{"decide", "--book-id", "1", "--account", "src", "--target-account", "dst", "--decision", "skip", "--db", path}
	if err := run(context.Background(), args, &bytes.Buffer{}, &bytes.Buffer{}, clients{}); err == nil {
		t.Fatal("writer ignored active owner")
	}
	readArgs := []string{"decisions", "--account", "src", "--target-account", "dst", "--db", path}
	if err := run(context.Background(), readArgs, &bytes.Buffer{}, &bytes.Buffer{}, clients{}); err != nil {
		t.Fatal("read-only decision listing failed under owner", err)
	}
}
