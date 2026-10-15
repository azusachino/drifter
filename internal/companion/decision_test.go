package companion

import (
	"context"
	"path/filepath"
	"testing"
)

func TestDecisionStoreAccountIsolationResetAndValidation(t *testing.T) {
	s, err := OpenStore(filepath.Join(t.TempDir(), "state.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ctx := context.Background()
	first := Decision{Provider: "weread", SourceAccount: "source", BookID: "42", TargetAccount: "target", Kind: "exclude"}
	if err := s.SaveDecision(ctx, first); err != nil {
		t.Fatal(err)
	}
	other := first
	other.TargetAccount = "other"
	other.Kind = "skip"
	if err := s.SaveDecision(ctx, other); err != nil {
		t.Fatal(err)
	}
	got, err := s.ListDecisions(ctx, "weread", "source", "target")
	if err != nil || len(got) != 1 || got[0] != first {
		t.Fatalf("decisions: %+v %v", got, err)
	}
	first.Kind = "not-in-catalog"
	if err := s.SaveDecision(ctx, first); err != nil {
		t.Fatal(err)
	}
	if err := s.ResetDecision(ctx, "weread", "source", "42", "target"); err != nil {
		t.Fatal(err)
	}
	if err := s.ResetDecision(ctx, "weread", "source", "42", "target"); err != nil {
		t.Fatal(err)
	}
	got, err = s.ListDecisions(ctx, "weread", "source", "target")
	if err != nil || len(got) != 0 {
		t.Fatalf("reset: %+v %v", got, err)
	}
	first.Kind = "unknown"
	if err := s.SaveDecision(ctx, first); err == nil {
		t.Fatal("accepted invalid decision kind")
	}
}

func TestDecisionMigrationFromVersionOnePreservesMappingsAndReadOnlySchema(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.sqlite")
	db := fixtureDB(t, path)
	if _, err := db.Exec(legacyMappingSchema); err != nil {
		t.Fatal(err)
	}
	m := testMapping()
	if _, err := db.Exec(`INSERT INTO mappings VALUES (?,?,?,?,?,?,?,?)`, m.Provider, m.SourceAccount, m.BookID, m.TargetAccount, m.WorkID, m.EditionID, m.SourceTitle, m.SourceISBN); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("PRAGMA user_version=1"); err != nil {
		t.Fatal(err)
	}
	db.Close()
	s, err := OpenStore(path)
	if err != nil {
		t.Fatal(err)
	}
	rows, err := s.List(context.Background())
	if err != nil || len(rows) != 1 || rows[0] != m {
		t.Fatalf("mapping lost: %+v %v", rows, err)
	}
	if _, err := s.db.Exec(`INSERT INTO decisions VALUES ('weread','source','42','target','skip')`); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	ro, err := OpenStoreReadOnly(path)
	if err != nil {
		t.Fatal(err)
	}
	defer ro.Close()
	if _, err := ro.ListDecisions(context.Background(), "weread", "source", "target"); err != nil {
		t.Fatal(err)
	}
	if err := ro.SaveDecision(context.Background(), Decision{Provider: "weread", SourceAccount: "source", BookID: "43", TargetAccount: "target", Kind: "skip"}); err == nil {
		t.Fatal("read-only write accepted")
	}
}

func TestFailedVersionOneDecisionMigrationRollsBack(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.sqlite")
	db := fixtureDB(t, path)
	if _, err := db.Exec(legacyMappingSchema + "; CREATE TABLE decisions (bad TEXT); PRAGMA user_version=1;"); err != nil {
		t.Fatal(err)
	}
	if s, err := OpenStore(path); err == nil {
		s.Close()
		t.Fatal("conflicting schema accepted")
	}
	if schemaVersion(t, db) != 1 {
		t.Fatal("failed migration advanced version")
	}
	var name string
	if err := db.QueryRow(`SELECT name FROM pragma_table_info('decisions') LIMIT 1`).Scan(&name); err != nil || name != "bad" {
		t.Fatalf("migration changed existing table: %q %v", name, err)
	}
}
