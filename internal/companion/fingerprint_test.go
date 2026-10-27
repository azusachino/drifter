package companion

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"
)

func TestAccountFingerprintBindingReauthAndMismatch(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.sqlite")
	s, err := OpenStore(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	ctx := context.Background()
	t0 := time.Date(2026, 10, 27, 12, 0, 0, 0, time.UTC)
	t1 := t0.Add(1 * time.Hour)

	// 1. Initial bind
	bound, err := s.VerifyOrBindAccountFingerprint(ctx, "hardcover", "default", "hardcover:12345", t0)
	if err != nil {
		t.Fatalf("initial bind failed: %v", err)
	}
	if bound.Provider != "hardcover" || bound.Account != "default" || bound.Fingerprint != "hardcover:12345" {
		t.Fatalf("unexpected bound record: %+v", bound)
	}
	if !bound.CreatedAt.Equal(t0) || !bound.LastVerifiedAt.Equal(t0) {
		t.Fatalf("unexpected timestamps: created=%v verified=%v", bound.CreatedAt, bound.LastVerifiedAt)
	}

	// 2. Query bound fingerprint
	got, err := s.GetAccountFingerprint(ctx, "hardcover", "default")
	if err != nil {
		t.Fatalf("get failed: %v", err)
	}
	if got == nil || got.Fingerprint != "hardcover:12345" || !got.CreatedAt.Equal(t0) || !got.LastVerifiedAt.Equal(t0) {
		t.Fatalf("unexpected get record: %+v", got)
	}

	// 3. Re-authentication with matching fingerprint updates last_verified_at
	reauthed, err := s.VerifyOrBindAccountFingerprint(ctx, "hardcover", "default", "hardcover:12345", t1)
	if err != nil {
		t.Fatalf("reauth failed: %v", err)
	}
	if !reauthed.CreatedAt.Equal(t0) || !reauthed.LastVerifiedAt.Equal(t1) {
		t.Fatalf("reauth timestamps: created=%v verified=%v", reauthed.CreatedAt, reauthed.LastVerifiedAt)
	}

	// 4. Mismatch refuses execution with ErrAccountFingerprintMismatch
	_, err = s.VerifyOrBindAccountFingerprint(ctx, "hardcover", "default", "hardcover:99999", t1)
	if err == nil || !errors.Is(err, ErrAccountFingerprintMismatch) {
		t.Fatalf("expected ErrAccountFingerprintMismatch, got: %v", err)
	}

	// Verify the original fingerprint was not mutated or overwritten
	checked, err := s.GetAccountFingerprint(ctx, "hardcover", "default")
	if err != nil {
		t.Fatal(err)
	}
	if checked.Fingerprint != "hardcover:12345" {
		t.Fatalf("fingerprint corrupted after mismatch: %s", checked.Fingerprint)
	}

	// 5. Different namespace label binds independently
	other, err := s.VerifyOrBindAccountFingerprint(ctx, "weread", "mine", "weread:98765", t0)
	if err != nil {
		t.Fatalf("other namespace bind failed: %v", err)
	}
	if other.Fingerprint != "weread:98765" {
		t.Fatalf("unexpected other fingerprint: %+v", other)
	}

	// 6. List returns both in order
	list, err := s.ListAccountFingerprints(ctx)
	if err != nil {
		t.Fatalf("list failed: %v", err)
	}
	if len(list) != 2 {
		t.Fatalf("expected 2 fingerprints, got %d: %+v", len(list), list)
	}
	if list[0].Provider != "hardcover" || list[1].Provider != "weread" {
		t.Fatalf("unexpected list ordering: %+v", list)
	}
}

func TestAccountFingerprintValidation(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.sqlite")
	s, err := OpenStore(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	ctx := context.Background()
	now := time.Now()

	invalidCases := []struct {
		provider string
		account  string
		fp       string
	}{
		{"", "default", "hardcover:1"},
		{"hardcover", "", "hardcover:1"},
		{"hardcover", "default", ""},
		{"hardcover", "default", "   "},
		{"hardcover", "default", "hardcover:1\x00bad"},
		{"hardcover", "default", "hardcover:1\nbad"},
		{"hardcover", "default", string(make([]byte, 129))},
	}

	for _, c := range invalidCases {
		if _, err := s.VerifyOrBindAccountFingerprint(ctx, c.provider, c.account, c.fp, now); err == nil {
			t.Errorf("accepted invalid input: provider=%q account=%q fp=%q", c.provider, c.account, c.fp)
		}
	}

	// Missing fingerprint returns nil, nil
	got, err := s.GetAccountFingerprint(ctx, "hardcover", "unbound")
	if err != nil || got != nil {
		t.Fatalf("expected nil for unbound fingerprint, got: %+v, %v", got, err)
	}

	// Invalid label on Get returns error
	if _, err := s.GetAccountFingerprint(ctx, "", "default"); err == nil {
		t.Error("accepted empty provider on Get")
	}
}

func TestAccountFingerprintMigrationFromVersionTwoPreservesData(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.sqlite")
	db := fixtureDB(t, path)

	// Create legacy v2 database with mappings and decisions
	if _, err := db.Exec(legacyMappingSchema); err != nil {
		t.Fatal(err)
	}
	m := testMapping()
	if _, err := db.Exec(`INSERT INTO mappings VALUES (?,?,?,?,?,?,?,?)`,
		m.Provider, m.SourceAccount, m.BookID, m.TargetAccount,
		m.WorkID, m.EditionID, m.SourceTitle, m.SourceISBN); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`CREATE TABLE decisions (
		provider TEXT NOT NULL, source_account TEXT NOT NULL, book_id TEXT NOT NULL,
		target_account TEXT NOT NULL, kind TEXT NOT NULL CHECK(kind IN ('skip','exclude','not-in-catalog')),
		PRIMARY KEY(provider, source_account, book_id, target_account))`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO decisions VALUES ('weread', 'source', '42', 'target', 'exclude')`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("PRAGMA user_version = 2"); err != nil {
		t.Fatal(err)
	}
	db.Close()

	// OpenStore performs v2 -> v3 migration
	s, err := OpenStore(path)
	if err != nil {
		t.Fatalf("OpenStore migration failed: %v", err)
	}
	defer s.Close()

	if schemaVersion(t, s.db) != 5 {
		t.Fatalf("schema version not upgraded to 5: got %d", schemaVersion(t, s.db))
	}

	// Verify mappings preserved
	mappings, err := s.List(context.Background())
	if err != nil || len(mappings) != 1 || mappings[0] != m {
		t.Fatalf("mappings not preserved: %+v, %v", mappings, err)
	}

	// Verify decisions preserved
	decisions, err := s.ListDecisions(context.Background(), "weread", "source", "target")
	if err != nil || len(decisions) != 1 || decisions[0].Kind != "exclude" {
		t.Fatalf("decisions not preserved: %+v, %v", decisions, err)
	}

	// Verify account_fingerprints table is functional
	now := time.Date(2026, 10, 27, 12, 0, 0, 0, time.UTC)
	bound, err := s.VerifyOrBindAccountFingerprint(context.Background(), "hardcover", "target", "hardcover:777", now)
	if err != nil {
		t.Fatalf("failed to bind fingerprint on migrated database: %v", err)
	}
	if bound.Fingerprint != "hardcover:777" {
		t.Fatalf("unexpected fingerprint: %+v", bound)
	}
}

func TestFailedVersionTwoAccountFingerprintMigrationRollsBack(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.sqlite")
	db := fixtureDB(t, path)

	// Create conflicting account_fingerprints table on v2
	if _, err := db.Exec(legacyMappingSchema + `;
		CREATE TABLE decisions (provider TEXT, source_account TEXT, book_id TEXT, target_account TEXT, kind TEXT, PRIMARY KEY(provider, source_account, book_id, target_account));
		CREATE TABLE account_fingerprints (bad_column TEXT);
		PRAGMA user_version = 2;`); err != nil {
		t.Fatal(err)
	}

	// Opening store should fail due to conflicting table and roll back version
	if s, err := OpenStore(path); err == nil {
		s.Close()
		t.Fatal("conflicting schema accepted without error")
	}

	if schemaVersion(t, db) != 2 {
		t.Fatalf("failed migration advanced version: %d", schemaVersion(t, db))
	}

	var name string
	if err := db.QueryRow(`SELECT name FROM pragma_table_info('account_fingerprints') LIMIT 1`).Scan(&name); err != nil || name != "bad_column" {
		t.Fatalf("failed migration altered conflicting table: %q, %v", name, err)
	}
}
