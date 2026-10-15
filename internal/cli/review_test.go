package cli

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/azusachino/drifter/internal/companion"
)

func reviewFixture() *fakeReader {
	last := time.Now().Unix()
	secret := 0
	return &fakeReader{shelf: &companion.ShelfResult{Complete: true, Books: []companion.ShelfBook{{ID: "42", LastOpened: &last, Secret: &secret, Title: "Title\x1b[31m", Author: "Author"}}}}
}

func TestReviewSavesDecisionAndSkipsItAfterRestart(t *testing.T) {
	db := filepath.Join(t.TempDir(), "state.sqlite")
	reader := reviewFixture()
	var out, stderr bytes.Buffer
	providers := clients{source: func() (source, error) { return reader, nil }}
	args := []string{"review", "--account", "source", "--target-account", "target", "--db", db}
	if err := runWithInput(context.Background(), args, &out, &stderr, providers, strings.NewReader("\nexclude\n")); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out.String(), "\x1b[31m") {
		t.Fatalf("terminal escape not sanitized: %q", out.String())
	}
	out.Reset()
	if err := runWithInput(context.Background(), args, &out, &stderr, providers, strings.NewReader("")); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out.String(), "Book 42") {
		t.Fatalf("saved exclusion returned on restart: %s", out.String())
	}
}

func TestReviewConfigDatabasePathOverridesFlag(t *testing.T) {
	dir := t.TempDir()
	configured := filepath.Join(dir, "configured.sqlite")
	flagged := filepath.Join(dir, "flagged.sqlite")
	configPath := filepath.Join(dir, "config.toml")
	if err := os.WriteFile(configPath, []byte("[database]\npath = "+strconv.Quote(configured)+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	providers := clients{source: func() (source, error) { return reviewFixture(), nil }}
	args := []string{"review", "--account", "source", "--target-account", "target", "--db", flagged, "--config", configPath}
	if err := runWithInput(context.Background(), args, io.Discard, io.Discard, providers, strings.NewReader("")); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(configured); err != nil {
		t.Fatalf("configured DB was not used: %v", err)
	}
	if _, err := os.Stat(flagged); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("--db unexpectedly used: %v", err)
	}
}

func TestReviewAcquiresOwnershipBeforeProviderFactory(t *testing.T) {
	db := filepath.Join(t.TempDir(), "state.sqlite")
	owner, err := companion.AcquireDatabaseOwnership(db)
	if err != nil {
		t.Fatal(err)
	}
	defer owner.Close()
	called := false
	providers := clients{source: func() (source, error) { called = true; return nil, errors.New("unexpected") }}
	err = runWithInput(context.Background(), []string{"review", "--account", "source", "--target-account", "target", "--db", db}, io.Discard, io.Discard, providers, strings.NewReader(""))
	if err == nil || called {
		t.Fatalf("err=%v factory called=%v", err, called)
	}
}

func TestReviewEOFDoesNotSaveDecisionAndReleasesLease(t *testing.T) {
	db := filepath.Join(t.TempDir(), "state.sqlite")
	providers := clients{source: func() (source, error) { return reviewFixture(), nil }}
	args := []string{"review", "--account", "source", "--target-account", "target", "--db", db}
	if err := runWithInput(context.Background(), args, io.Discard, io.Discard, providers, strings.NewReader("")); err != nil {
		t.Fatal(err)
	}
	assertReviewNoDecisionAndUnlocked(t, db)
}

type blockedInput struct {
	started chan struct{}
	release chan struct{}
}

func (r blockedInput) Read([]byte) (int, error) {
	close(r.started)
	<-r.release
	return 0, io.EOF
}

func TestReviewCancellationWhileInputBlockedReleasesLease(t *testing.T) {
	db := filepath.Join(t.TempDir(), "state.sqlite")
	ctx, cancel := context.WithCancel(context.Background())
	input := blockedInput{started: make(chan struct{}), release: make(chan struct{})}
	providers := clients{source: func() (source, error) { return reviewFixture(), nil }}
	done := make(chan error, 1)
	go func() {
		done <- runWithInput(ctx, []string{"review", "--account", "source", "--target-account", "target", "--db", db}, io.Discard, io.Discard, providers, input)
	}()
	<-input.started
	cancel()
	close(input.release)
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("got %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("review did not cancel while input blocked")
	}
	assertReviewNoDecisionAndUnlocked(t, db)
}

func assertReviewNoDecisionAndUnlocked(t *testing.T, db string) {
	t.Helper()
	store, err := companion.OpenStoreForRead(db)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	rows, err := store.ListDecisions(context.Background(), "weread", "source", "target")
	if err != nil || len(rows) != 0 {
		t.Fatalf("rows=%v err=%v", rows, err)
	}
	if err := companion.CheckDatabaseWriteAvailable(db); err != nil {
		t.Fatalf("lease not released: %v", err)
	}
}
