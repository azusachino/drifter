package companion

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func testMapping() Mapping {
	return Mapping{
		Provider: "weread", SourceAccount: "source", BookID: "123",
		TargetAccount: "target", WorkID: 42, SourceTitle: "Synthetic book",
	}
}

func TestStorePersistenceAndReplacement(t *testing.T) {
	ctx := context.Background()
	// URI-sensitive characters must not become connection parameters.
	path := filepath.Join(t.TempDir(), "books ?#%.sqlite")
	s, err := OpenStore(path)
	if err != nil {
		t.Fatal(err)
	}
	m := testMapping()
	if err := s.Confirm(ctx, m, false); err != nil {
		t.Fatal(err)
	}
	if err := s.Confirm(ctx, m, false); err != nil {
		t.Fatal(err)
	}
	changed := m
	changed.WorkID, changed.EditionID = 99, 100
	if err := s.Confirm(ctx, changed, false); err == nil {
		t.Fatal("different mapping silently replaced existing confirmation")
	}
	rows, err := s.List(ctx)
	if err != nil || !reflect.DeepEqual(rows, []Mapping{m}) {
		t.Fatalf("original confirmation changed: %+v, %v", rows, err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = OpenStore(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	rows, err = s.List(ctx)
	if err != nil || !reflect.DeepEqual(rows, []Mapping{m}) {
		t.Fatalf("confirmation did not survive reopen: %+v, %v", rows, err)
	}
	if err := s.Confirm(ctx, changed, true); err != nil {
		t.Fatal(err)
	}
	rows, err = s.List(ctx)
	if err != nil || !reflect.DeepEqual(rows, []Mapping{changed}) {
		t.Fatalf("explicit replacement failed: %+v, %v", rows, err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("database mode: %v", info.Mode())
	}
	var secureDelete int
	if err := s.db.QueryRow("PRAGMA secure_delete").Scan(&secureDelete); err != nil {
		t.Fatal(err)
	}
	if secureDelete != 1 {
		t.Fatal("secure_delete is not enabled")
	}
}

func TestStoreGuardedMutationsRejectStaleTargets(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "mappings.sqlite")
	a, err := OpenStore(path)
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	b, err := OpenStore(path)
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	original := testMapping()
	if err := a.Confirm(ctx, original, false); err != nil {
		t.Fatal(err)
	}
	expected := MappingTarget{WorkID: original.WorkID, EditionID: original.EditionID}
	newer := original
	newer.WorkID, newer.EditionID = 81, 9
	if err := b.Confirm(ctx, newer, true); err != nil {
		t.Fatal(err)
	}
	stale := original
	stale.WorkID = 77
	if err := a.ReplaceExpected(ctx, stale, expected); !errors.Is(err, ErrMappingConflict) {
		t.Fatalf("stale replace: %v", err)
	}
	if err := a.UnlinkExpected(ctx, original.Provider, original.SourceAccount, original.BookID, original.TargetAccount, expected); !errors.Is(err, ErrMappingConflict) {
		t.Fatalf("stale unlink: %v", err)
	}
	rows, err := a.List(ctx)
	if err != nil || len(rows) != 1 || rows[0].WorkID != newer.WorkID || rows[0].EditionID != newer.EditionID {
		t.Fatalf("newer row changed: %+v, %v", rows, err)
	}
	fresh := newer
	fresh.WorkID = 82
	if err := a.ReplaceExpected(ctx, fresh, MappingTarget{WorkID: newer.WorkID, EditionID: newer.EditionID}); err != nil {
		t.Fatalf("fresh replace: %v", err)
	}
	if err := a.UnlinkExpected(ctx, fresh.Provider, fresh.SourceAccount, fresh.BookID, fresh.TargetAccount, MappingTarget{WorkID: fresh.WorkID, EditionID: fresh.EditionID}); err != nil {
		t.Fatal(err)
	}
	if err := b.UnlinkExpected(ctx, fresh.Provider, fresh.SourceAccount, fresh.BookID, fresh.TargetAccount, MappingTarget{WorkID: fresh.WorkID, EditionID: fresh.EditionID}); err != nil {
		t.Fatalf("absent unlink not idempotent: %v", err)
	}
	if err := b.ReplaceExpected(ctx, fresh, MappingTarget{WorkID: fresh.WorkID, EditionID: fresh.EditionID}); !errors.Is(err, ErrMappingConflict) {
		t.Fatalf("deleted row resurrected: %v", err)
	}
}

func TestStoreAccountScopedUnlink(t *testing.T) {
	ctx := context.Background()
	s, err := OpenStore(filepath.Join(t.TempDir(), "state", "mappings.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	rows, err := s.List(ctx)
	if err != nil || rows == nil || len(rows) != 0 {
		t.Fatalf("empty list: %+v, %v", rows, err)
	}
	original := testMapping()
	sourceOther, targetOther, providerOther := original, original, original
	sourceOther.SourceAccount = "other-source"
	targetOther.TargetAccount = "other-target"
	providerOther.Provider = "manual-ku"
	for _, m := range []Mapping{original, sourceOther, targetOther, providerOther} {
		if err := s.Confirm(ctx, m, false); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.Unlink(ctx, original.Provider, original.SourceAccount, original.BookID, original.TargetAccount); err != nil {
		t.Fatal(err)
	}
	rows, err = s.List(ctx)
	if err != nil || len(rows) != 3 {
		t.Fatalf("unlink affected other scopes: %+v, %v", rows, err)
	}
	for _, m := range rows {
		if m == original {
			t.Fatal("selected mapping was not removed")
		}
	}
	if err := s.Unlink(ctx, original.Provider, original.SourceAccount, original.BookID, original.TargetAccount); err != nil {
		t.Fatal(err)
	}
	if err := s.Unlink(ctx, "weread", "", "123", "target"); err == nil {
		t.Fatal("invalid unlink accepted")
	}
}

func TestStoreRejectsInvalidMappings(t *testing.T) {
	s, err := OpenStore(filepath.Join(t.TempDir(), "mappings.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	for _, change := range []func(*Mapping){
		func(m *Mapping) { m.SourceAccount = "" },
		func(m *Mapping) { m.TargetAccount = " account" },
		func(m *Mapping) { m.Provider = "weread\n" },
		func(m *Mapping) { m.BookID = "0" },
		func(m *Mapping) { m.BookID = "123;DELETE" },
		func(m *Mapping) { m.WorkID = 0 },
		func(m *Mapping) { m.EditionID = -1 },
	} {
		m := testMapping()
		change(&m)
		if err := s.Confirm(context.Background(), m, true); err == nil {
			t.Fatalf("invalid mapping accepted: %+v", m)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := s.Confirm(ctx, testMapping(), false); err == nil {
		t.Fatal("canceled confirmation succeeded")
	}
}

func TestStoreTreatsMetadataAsData(t *testing.T) {
	s, err := OpenStore(filepath.Join(t.TempDir(), "mappings.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	m := testMapping()
	m.SourceTitle = "合成の本'); DROP TABLE mappings; --"
	if err := s.Confirm(context.Background(), m, false); err != nil {
		t.Fatal(err)
	}
	rows, err := s.List(context.Background())
	if err != nil || !reflect.DeepEqual(rows, []Mapping{m}) {
		t.Fatalf("metadata changed SQL: %+v, %v", rows, err)
	}
}

func TestStoreRejectsUnsafePaths(t *testing.T) {
	if _, err := OpenStore(""); err == nil {
		t.Fatal("empty path accepted")
	}
	for _, kind := range []string{"symlink", "directory", "public-file", "writable-parent", "corrupt-file"} {
		t.Run(kind, func(t *testing.T) {
			parent := t.TempDir()
			path := filepath.Join(parent, "mappings.sqlite")
			switch kind {
			case "symlink":
				target := filepath.Join(parent, "untouched.sqlite")
				if err := os.WriteFile(target, []byte("untouched"), 0o600); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(target, path); err != nil {
					t.Fatal(err)
				}
				defer func() {
					data, err := os.ReadFile(target)
					if err != nil || string(data) != "untouched" {
						t.Fatalf("symlink target modified: %q, %v", data, err)
					}
				}()
			case "directory":
				if err := os.Mkdir(path, 0o700); err != nil {
					t.Fatal(err)
				}
			case "public-file":
				if err := os.WriteFile(path, nil, 0o600); err != nil {
					t.Fatal(err)
				}
				if err := os.Chmod(path, 0o644); err != nil {
					t.Fatal(err)
				}
			case "writable-parent":
				if err := os.Chmod(parent, 0o777); err != nil {
					t.Fatal(err)
				}
				defer os.Chmod(parent, 0o700)
			case "corrupt-file":
				if err := os.WriteFile(path, []byte("not a database"), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			s, err := OpenStore(path)
			if err == nil {
				s.Close()
				t.Fatal("unsafe database path accepted")
			}
		})
	}
}

func TestValidLabel(t *testing.T) {
	for _, label := range []string{"", " leading", "trailing ", "a\nb", "a\x00b", strings.Repeat("a", 65)} {
		if ValidLabel(label) {
			t.Fatalf("invalid label accepted: %q", label)
		}
	}
	for _, label := range []string{"source", "my-account", "日本語", strings.Repeat("a", 64)} {
		if !ValidLabel(label) {
			t.Fatalf("valid label rejected: %q", label)
		}
	}
}

func TestStoreResolveAccounts(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "accounts.sqlite")
	s, err := OpenStore(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	// 1. Explicit accounts are returned directly
	src, dst, err := s.ResolveAccounts(ctx, "custom-src", "custom-dst")
	if err != nil || src != "custom-src" || dst != "custom-dst" {
		t.Fatalf("unexpected resolve: got (%q, %q), err: %v", src, dst, err)
	}

	// 2. Empty accounts on empty store fall back to default/default
	src, dst, err = s.ResolveAccounts(ctx, "", "")
	if err != nil || src != DefaultSourceAccount || dst != DefaultTargetAccount {
		t.Fatalf("unexpected fallback: got (%q, %q), err: %v", src, dst, err)
	}

	// 3. Store with a single unique mapping pair infers that pair
	m := Mapping{
		Provider: "weread", SourceAccount: "mine", BookID: "100",
		TargetAccount: "custom-hardcover", WorkID: 200, SourceTitle: "Title",
	}
	if err := s.Confirm(ctx, m, false); err != nil {
		t.Fatal(err)
	}
	src, dst, err = s.ResolveAccounts(ctx, "", "")
	if err != nil || src != "mine" || dst != "custom-hardcover" {
		t.Fatalf("unexpected inferred pair: got (%q, %q), err: %v", src, dst, err)
	}

	// Partially empty: specified source, empty target
	src, dst, err = s.ResolveAccounts(ctx, "override-src", "")
	if err != nil || src != "override-src" || dst != "custom-hardcover" {
		t.Fatalf("unexpected partial resolve: got (%q, %q), err: %v", src, dst, err)
	}
}

func TestStoreDeleteStatusBaseline(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "baselines.sqlite")
	s, err := OpenStore(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	b := StatusBaseline{
		Provider: "weread", SourceAccount: "src", BookID: "123",
		TargetAccount: "dst", WorkID: 42, EditionID: 10,
		SourceStatusID: 2, TargetStatusID: 2, PolicyVersion: "v1",
		SourceFingerprint: "weread:1", TargetFingerprint: "hardcover:2",
		LastSyncedAt: time.Now().UTC(),
	}
	if err := s.SaveStatusBaseline(ctx, b); err != nil {
		t.Fatal(err)
	}

	got, err := s.GetStatusBaseline(ctx, "weread", "src", "123", "dst")
	if err != nil || got == nil {
		t.Fatalf("expected baseline, got: %+v, err: %v", got, err)
	}

	deleted, err := s.DeleteStatusBaseline(ctx, "weread", "src", "123", "dst")
	if err != nil || !deleted {
		t.Fatalf("expected deletion, got deleted=%v, err=%v", deleted, err)
	}

	gotAfter, err := s.GetStatusBaseline(ctx, "weread", "src", "123", "dst")
	if err != nil || gotAfter != nil {
		t.Fatalf("expected baseline to be deleted, got: %+v, err: %v", gotAfter, err)
	}

	// Second delete returns false (not found)
	deletedAgain, err := s.DeleteStatusBaseline(ctx, "weread", "src", "123", "dst")
	if err != nil || deletedAgain {
		t.Fatalf("expected deleted=false on absent, got: %v, err: %v", deletedAgain, err)
	}
}

func TestStoreClearSyncHolds(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "holds.sqlite")
	s, err := OpenStore(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	runID, err := s.CreateSyncRun(ctx, "src", "dst", "v1", time.Now())
	if err != nil {
		t.Fatal(err)
	}

	h1 := SyncHold{
		RunID: runID, Provider: "weread", SourceAccount: "src", TargetAccount: "dst",
		BookID: "101", WorkID: 1, Reason: "conflict", Message: "conflict msg",
		CreatedAt: time.Now().UTC(),
	}
	h2 := SyncHold{
		RunID: runID, Provider: "weread", SourceAccount: "src", TargetAccount: "dst",
		BookID: "102", WorkID: 2, Reason: "uncertain", Message: "uncertain msg",
		CreatedAt: time.Now().UTC(),
	}
	if _, err := s.RecordSyncHold(ctx, h1); err != nil {
		t.Fatal(err)
	}
	if _, err := s.RecordSyncHold(ctx, h2); err != nil {
		t.Fatal(err)
	}

	holds, err := s.ListSyncHolds(ctx, "src", "dst")
	if err != nil || len(holds) != 2 {
		t.Fatalf("expected 2 holds, got %d, err: %v", len(holds), err)
	}

	// Clear specific hold for 101
	cleared, err := s.ClearSyncHolds(ctx, "weread", "src", "dst", "101")
	if err != nil || cleared != 1 {
		t.Fatalf("expected 1 cleared, got %d, err: %v", cleared, err)
	}

	holdsAfter, err := s.ListSyncHolds(ctx, "src", "dst")
	if err != nil || len(holdsAfter) != 1 || holdsAfter[0].BookID != "102" {
		t.Fatalf("expected 1 remaining hold for 102, got: %+v, err: %v", holdsAfter, err)
	}

	// Clear remaining holds for account pair
	clearedRest, err := s.ClearSyncHolds(ctx, "weread", "src", "dst", "")
	if err != nil || clearedRest != 1 {
		t.Fatalf("expected 1 cleared, got %d, err: %v", clearedRest, err)
	}

	holdsFinal, err := s.ListSyncHolds(ctx, "src", "dst")
	if err != nil || len(holdsFinal) != 0 {
		t.Fatalf("expected 0 holds, got %d, err: %v", len(holdsFinal), err)
	}
}
