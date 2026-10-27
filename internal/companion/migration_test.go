package companion

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"testing"
	"time"
)

// Keep the pre-versioning schema independent of the current migration.
const legacyMappingSchema = `CREATE TABLE mappings (
	provider TEXT NOT NULL, source_account TEXT NOT NULL, book_id TEXT NOT NULL,
	target_account TEXT NOT NULL, work_id INTEGER NOT NULL CHECK(work_id > 0),
	edition_id INTEGER NOT NULL CHECK(edition_id >= 0), source_title TEXT NOT NULL, source_isbn TEXT NOT NULL,
	PRIMARY KEY(provider, source_account, book_id, target_account))`

func fixtureDB(t *testing.T, path string) *sql.DB {
	t.Helper()
	if err := os.WriteFile(path, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite3", path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

func schemaVersion(t *testing.T, db *sql.DB) int {
	t.Helper()
	var version int
	if err := db.QueryRow("PRAGMA user_version").Scan(&version); err != nil {
		t.Fatal(err)
	}
	return version
}

func TestStoreVersionsNewAndLegacyDatabases(t *testing.T) {
	for _, legacy := range []bool{false, true} {
		name := "new"
		if legacy {
			name = "legacy"
		}
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "mappings.sqlite")
			want := []Mapping{}
			if legacy {
				db := fixtureDB(t, path)
				if _, err := db.Exec(legacyMappingSchema); err != nil {
					t.Fatal(err)
				}
				original := testMapping()
				other := original
				other.SourceAccount, other.TargetAccount = "other-source", "other-target"
				other.EditionID, other.SourceISBN = 43, "9780306406157"
				want = []Mapping{other, original}
				for _, m := range want {
					if _, err := db.Exec(`INSERT INTO mappings VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
						m.Provider, m.SourceAccount, m.BookID, m.TargetAccount,
						m.WorkID, m.EditionID, m.SourceTitle, m.SourceISBN); err != nil {
						t.Fatal(err)
					}
				}
				if schemaVersion(t, db) != 0 {
					t.Fatal("legacy fixture must be unversioned")
				}
				db.Close()
			}
			for range 2 {
				s, err := OpenStore(path)
				if err != nil {
					t.Fatal(err)
				}
				if schemaVersion(t, s.db) != 6 {
					t.Fatal("database not upgraded to version 6")
				}
				rows, err := s.List(context.Background())
				if err != nil || !reflect.DeepEqual(rows, want) {
					t.Fatalf("mapping preservation: %+v, %v", rows, err)
				}
				if err := s.Close(); err != nil {
					t.Fatal(err)
				}
			}
		})
	}
}

func TestStoreRefusesUnsupportedVersionsWithoutCreatingSchema(t *testing.T) {
	for _, version := range []int{7, -1} {
		path := filepath.Join(t.TempDir(), "mappings.sqlite")
		db := fixtureDB(t, path)
		if _, err := db.Exec("PRAGMA user_version = " + strconv.Itoa(version)); err != nil {
			t.Fatal(err)
		}
		if s, err := OpenStore(path); err == nil {
			s.Close()
			t.Fatalf("unsupported version %d accepted", version)
		}
		if schemaVersion(t, db) != version {
			t.Fatal("unsupported version changed")
		}
		var tables int
		if err := db.QueryRow("SELECT count(*) FROM sqlite_master WHERE name = 'mappings'").Scan(&tables); err != nil || tables != 0 {
			t.Fatalf("schema written before version refusal: %d, %v", tables, err)
		}
	}
}

func TestStoreFailedMigrationPreservesSchemaAndVersion(t *testing.T) {
	path := filepath.Join(t.TempDir(), "mappings.sqlite")
	db := fixtureDB(t, path)
	if _, err := db.Exec("CREATE TABLE mappings (marker TEXT); INSERT INTO mappings VALUES ('keep')"); err != nil {
		t.Fatal(err)
	}
	if s, err := OpenStore(path); err == nil {
		s.Close()
		t.Fatal("incompatible legacy schema accepted")
	}
	if schemaVersion(t, db) != 0 {
		t.Fatal("failed migration advanced version")
	}
	var marker string
	if err := db.QueryRow("SELECT marker FROM mappings").Scan(&marker); err != nil || marker != "keep" {
		t.Fatalf("failed migration changed existing data: %q, %v", marker, err)
	}
}

func TestStoreVersionedDatabaseCannotSilentlyRecreateMissingSchema(t *testing.T) {
	path := filepath.Join(t.TempDir(), "mappings.sqlite")
	db := fixtureDB(t, path)
	if _, err := db.Exec("PRAGMA user_version = 1"); err != nil {
		t.Fatal(err)
	}
	if s, err := OpenStore(path); err == nil {
		s.Close()
		t.Fatal("versioned database with missing mappings accepted")
	}
	var tables int
	if err := db.QueryRow("SELECT count(*) FROM sqlite_master WHERE name = 'mappings'").Scan(&tables); err != nil || tables != 0 {
		t.Fatalf("missing schema silently recreated: %d, %v", tables, err)
	}
}

func TestStatusBaselineAndAuthorityMigrationFromVersionThreePreservesData(t *testing.T) {
	path := filepath.Join(t.TempDir(), "mappings.sqlite")
	db := fixtureDB(t, path)

	v3Schema := `
CREATE TABLE mappings (
	provider TEXT NOT NULL, source_account TEXT NOT NULL, book_id TEXT NOT NULL,
	target_account TEXT NOT NULL, work_id INTEGER NOT NULL CHECK(work_id > 0),
	edition_id INTEGER NOT NULL CHECK(edition_id >= 0), source_title TEXT NOT NULL, source_isbn TEXT NOT NULL,
	PRIMARY KEY(provider, source_account, book_id, target_account));
CREATE TABLE user_books (
	target_account TEXT NOT NULL, work_id INTEGER NOT NULL CHECK(work_id > 0),
	user_book_id INTEGER NOT NULL CHECK(user_book_id > 0),
	PRIMARY KEY(target_account, work_id));
CREATE TABLE edition_searches (
	work_id INTEGER NOT NULL CHECK(work_id > 0),
	source_isbn TEXT NOT NULL, query TEXT NOT NULL,
	edition_id INTEGER NOT NULL CHECK(edition_id >= 0),
	cached_at TEXT NOT NULL,
	PRIMARY KEY(work_id, source_isbn, query));
CREATE TABLE account_fingerprints (
	provider TEXT NOT NULL, account TEXT NOT NULL,
	fingerprint TEXT NOT NULL, created_at TEXT NOT NULL,
	last_verified_at TEXT NOT NULL,
	PRIMARY KEY(provider, account));
PRAGMA user_version = 3;
`
	if _, err := db.Exec(v3Schema); err != nil {
		t.Fatal(err)
	}

	mapping := testMapping()
	if _, err := db.Exec(`INSERT INTO mappings VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		mapping.Provider, mapping.SourceAccount, mapping.BookID, mapping.TargetAccount,
		mapping.WorkID, mapping.EditionID, mapping.SourceTitle, mapping.SourceISBN); err != nil {
		t.Fatal(err)
	}

	t0 := "2026-10-27T12:00:00Z"
	if _, err := db.Exec(`INSERT INTO account_fingerprints VALUES (?, ?, ?, ?, ?)`,
		"weread", "mine", "weread:12345", t0, t0); err != nil {
		t.Fatal(err)
	}
	db.Close()

	ctx := context.Background()
	s, err := OpenStore(path)
	if err != nil {
		t.Fatalf("failed to open store from v3: %v", err)
	}
	defer s.Close()

	if schemaVersion(t, s.db) != 6 {
		t.Fatalf("expected version 6, got %d", schemaVersion(t, s.db))
	}

	// Verify existing data preserved
	mGot, err := s.GetMapping(ctx, mapping.Provider, mapping.SourceAccount, mapping.BookID, mapping.TargetAccount)
	if err != nil || mGot == nil || *mGot != mapping {
		t.Fatalf("mapping preservation failed: got %+v, err %v", mGot, err)
	}
	fpGot, err := s.GetAccountFingerprint(ctx, "weread", "mine")
	if err != nil || fpGot == nil || fpGot.Fingerprint != "weread:12345" {
		t.Fatalf("account fingerprint preservation failed: got %+v, err %v", fpGot, err)
	}

	// Verify v4 tables are functional
	now := time.Date(2026, 10, 27, 14, 0, 0, 0, time.UTC)
	baseline := StatusBaseline{
		Provider:          mapping.Provider,
		SourceAccount:     mapping.SourceAccount,
		BookID:            mapping.BookID,
		TargetAccount:     mapping.TargetAccount,
		WorkID:            mapping.WorkID,
		EditionID:         mapping.EditionID,
		SourceStatusID:    HardcoverStatusCurrentlyReading,
		TargetStatusID:    HardcoverStatusCurrentlyReading,
		PolicyVersion:     "v1",
		SourceFingerprint: "weread:12345",
		TargetFingerprint: "hardcover:67890",
		LastSyncedAt:      now,
	}
	if err := s.SaveStatusBaseline(ctx, baseline); err != nil {
		t.Fatalf("save status baseline failed: %v", err)
	}
	bGot, err := s.GetStatusBaseline(ctx, mapping.Provider, mapping.SourceAccount, mapping.BookID, mapping.TargetAccount)
	if err != nil || bGot == nil || !reflect.DeepEqual(*bGot, baseline) {
		t.Fatalf("get status baseline failed: got %+v, want %+v, err %v", bGot, baseline, err)
	}

	auth := TargetAuthority{
		TargetAccount:      mapping.TargetAccount,
		WorkID:             mapping.WorkID,
		AuthorizedProvider: mapping.Provider,
		AuthorizedAccount:  mapping.SourceAccount,
		AuthorizedBookID:   mapping.BookID,
		PolicyVersion:      "v1",
		PrivacyMode:        "default",
		CreatedAt:          now,
		UpdatedAt:          now,
	}
	if err := s.SetTargetAuthority(ctx, auth); err != nil {
		t.Fatalf("set target authority failed: %v", err)
	}
	aGot, err := s.GetTargetAuthority(ctx, mapping.TargetAccount, mapping.WorkID)
	if err != nil || aGot == nil || !reflect.DeepEqual(*aGot, auth) {
		t.Fatalf("get target authority failed: got %+v, want %+v, err %v", aGot, auth, err)
	}
}

func TestSyncIntentAndReceiptMigrationFromVersionFourPreservesData(t *testing.T) {
	path := filepath.Join(t.TempDir(), "mappings.sqlite")
	db := fixtureDB(t, path)

	v4Schema := `
CREATE TABLE mappings (
	provider TEXT NOT NULL, source_account TEXT NOT NULL, book_id TEXT NOT NULL,
	target_account TEXT NOT NULL, work_id INTEGER NOT NULL CHECK(work_id > 0),
	edition_id INTEGER NOT NULL CHECK(edition_id >= 0), source_title TEXT NOT NULL, source_isbn TEXT NOT NULL,
	PRIMARY KEY(provider, source_account, book_id, target_account));
CREATE TABLE decisions (
	provider TEXT NOT NULL, source_account TEXT NOT NULL, book_id TEXT NOT NULL,
	target_account TEXT NOT NULL, kind TEXT NOT NULL CHECK(kind IN ('skip','exclude','not-in-catalog')),
	PRIMARY KEY(provider, source_account, book_id, target_account));
CREATE TABLE account_fingerprints (
	provider TEXT NOT NULL, account TEXT NOT NULL,
	fingerprint TEXT NOT NULL, created_at TEXT NOT NULL,
	last_verified_at TEXT NOT NULL,
	PRIMARY KEY(provider, account));
CREATE TABLE status_baselines (
	provider TEXT NOT NULL, source_account TEXT NOT NULL, book_id TEXT NOT NULL,
	target_account TEXT NOT NULL, work_id INTEGER NOT NULL CHECK(work_id > 0),
	edition_id INTEGER NOT NULL CHECK(edition_id >= 0),
	source_status_id INTEGER NOT NULL CHECK(source_status_id >= 1 AND source_status_id <= 5),
	target_status_id INTEGER NOT NULL CHECK(target_status_id >= 1 AND target_status_id <= 5),
	policy_version TEXT NOT NULL, source_fingerprint TEXT NOT NULL,
	target_fingerprint TEXT NOT NULL, last_synced_at TEXT NOT NULL,
	PRIMARY KEY(provider, source_account, book_id, target_account));
CREATE TABLE target_authorities (
	target_account TEXT NOT NULL, work_id INTEGER NOT NULL CHECK(work_id > 0),
	authorized_provider TEXT NOT NULL, authorized_account TEXT NOT NULL,
	authorized_book_id TEXT NOT NULL, policy_version TEXT NOT NULL,
	privacy_mode TEXT NOT NULL CHECK(privacy_mode IN ('default', 'private-target', 'exclude')),
	created_at TEXT NOT NULL, updated_at TEXT NOT NULL,
	PRIMARY KEY(target_account, work_id));
PRAGMA user_version = 4;
`
	if _, err := db.Exec(v4Schema); err != nil {
		t.Fatal(err)
	}

	mapping := testMapping()
	if _, err := db.Exec(`INSERT INTO mappings VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		mapping.Provider, mapping.SourceAccount, mapping.BookID, mapping.TargetAccount,
		mapping.WorkID, mapping.EditionID, mapping.SourceTitle, mapping.SourceISBN); err != nil {
		t.Fatal(err)
	}

	nowStr := "2026-10-27T12:00:00Z"
	if _, err := db.Exec(`INSERT INTO status_baselines VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		mapping.Provider, mapping.SourceAccount, mapping.BookID, mapping.TargetAccount,
		mapping.WorkID, mapping.EditionID, 2, 2, "v1", "weread:10001", "hardcover:20002", nowStr); err != nil {
		t.Fatal(err)
	}
	db.Close()

	ctx := context.Background()
	s, err := OpenStore(path)
	if err != nil {
		t.Fatalf("failed to open store from v4: %v", err)
	}
	defer s.Close()

	if schemaVersion(t, s.db) != 6 {
		t.Fatalf("expected version 6, got %d", schemaVersion(t, s.db))
	}

	// Verify v4 baseline is preserved
	baseline, err := s.GetStatusBaseline(ctx, mapping.Provider, mapping.SourceAccount, mapping.BookID, mapping.TargetAccount)
	if err != nil || baseline == nil || baseline.WorkID != mapping.WorkID {
		t.Fatalf("baseline preservation failed: got %+v, err %v", baseline, err)
	}

	// Verify v5 sync intents and receipts work
	t0 := time.Date(2026, 10, 27, 13, 0, 0, 0, time.UTC)
	intent := SyncIntent{
		Provider:          mapping.Provider,
		SourceAccount:     mapping.SourceAccount,
		BookID:            mapping.BookID,
		TargetAccount:     mapping.TargetAccount,
		WorkID:            mapping.WorkID,
		EditionID:         mapping.EditionID,
		ProposedStatusID:  HardcoverStatusRead,
		PolicyVersion:     "v1",
		SourceFingerprint: "weread:10001",
		TargetFingerprint: "hardcover:20002",
		State:             "pending",
		CreatedAt:         t0,
		UpdatedAt:         t0,
	}
	intentID, err := s.RecordIntent(ctx, intent)
	if err != nil || intentID <= 0 {
		t.Fatalf("record intent failed: id=%d, err=%v", intentID, err)
	}

	active, err := s.GetActiveIntent(ctx, mapping.Provider, mapping.SourceAccount, mapping.BookID, mapping.TargetAccount)
	if err != nil || active == nil || active.ID != intentID || active.State != "pending" {
		t.Fatalf("get active intent failed: got %+v, err %v", active, err)
	}

	if err := s.UpdateIntentState(ctx, intentID, "applied", "", t0); err != nil {
		t.Fatalf("update intent state failed: %v", err)
	}

	receipt := SyncReceipt{
		IntentID:       intentID,
		Provider:       mapping.Provider,
		SourceAccount:  mapping.SourceAccount,
		BookID:         mapping.BookID,
		TargetAccount:  mapping.TargetAccount,
		WorkID:         mapping.WorkID,
		EditionID:      mapping.EditionID,
		TargetStatusID: HardcoverStatusRead,
		AppliedAt:      t0,
	}
	receiptID, err := s.RecordReceipt(ctx, receipt)
	if err != nil || receiptID <= 0 {
		t.Fatalf("record receipt failed: id=%d, err=%v", receiptID, err)
	}

	latestReceipt, err := s.GetLatestReceipt(ctx, mapping.Provider, mapping.SourceAccount, mapping.BookID, mapping.TargetAccount)
	if err != nil || latestReceipt == nil || latestReceipt.ID != receiptID {
		t.Fatalf("get latest receipt failed: got %+v, err %v", latestReceipt, err)
	}
}

func TestSyncRunAndHoldMigrationFromVersionFivePreservesData(t *testing.T) {
	path := filepath.Join(t.TempDir(), "mappings.sqlite")
	db := fixtureDB(t, path)

	v5Schema := `
CREATE TABLE mappings (
	provider TEXT NOT NULL, source_account TEXT NOT NULL, book_id TEXT NOT NULL,
	target_account TEXT NOT NULL, work_id INTEGER NOT NULL CHECK(work_id > 0),
	edition_id INTEGER NOT NULL CHECK(edition_id >= 0), source_title TEXT NOT NULL, source_isbn TEXT NOT NULL,
	PRIMARY KEY(provider, source_account, book_id, target_account));
CREATE TABLE decisions (
	provider TEXT NOT NULL, source_account TEXT NOT NULL, book_id TEXT NOT NULL,
	target_account TEXT NOT NULL, kind TEXT NOT NULL CHECK(kind IN ('skip','exclude','not-in-catalog')),
	PRIMARY KEY(provider, source_account, book_id, target_account));
CREATE TABLE account_fingerprints (
	provider TEXT NOT NULL, account TEXT NOT NULL,
	fingerprint TEXT NOT NULL, created_at TEXT NOT NULL,
	last_verified_at TEXT NOT NULL,
	PRIMARY KEY(provider, account));
CREATE TABLE status_baselines (
	provider TEXT NOT NULL, source_account TEXT NOT NULL, book_id TEXT NOT NULL,
	target_account TEXT NOT NULL, work_id INTEGER NOT NULL CHECK(work_id > 0),
	edition_id INTEGER NOT NULL CHECK(edition_id >= 0),
	source_status_id INTEGER NOT NULL CHECK(source_status_id >= 1 AND source_status_id <= 5),
	target_status_id INTEGER NOT NULL CHECK(target_status_id >= 1 AND target_status_id <= 5),
	policy_version TEXT NOT NULL, source_fingerprint TEXT NOT NULL,
	target_fingerprint TEXT NOT NULL, last_synced_at TEXT NOT NULL,
	PRIMARY KEY(provider, source_account, book_id, target_account));
CREATE TABLE target_authorities (
	target_account TEXT NOT NULL, work_id INTEGER NOT NULL CHECK(work_id > 0),
	authorized_provider TEXT NOT NULL, authorized_account TEXT NOT NULL,
	authorized_book_id TEXT NOT NULL, policy_version TEXT NOT NULL,
	privacy_mode TEXT NOT NULL CHECK(privacy_mode IN ('default', 'private-target', 'exclude')),
	created_at TEXT NOT NULL, updated_at TEXT NOT NULL,
	PRIMARY KEY(target_account, work_id));
CREATE TABLE sync_intents (
	id INTEGER PRIMARY KEY AUTOINCREMENT,
	provider TEXT NOT NULL, source_account TEXT NOT NULL, book_id TEXT NOT NULL,
	target_account TEXT NOT NULL, work_id INTEGER NOT NULL CHECK(work_id > 0),
	edition_id INTEGER NOT NULL CHECK(edition_id >= 0),
	proposed_status_id INTEGER NOT NULL CHECK(proposed_status_id >= 1 AND proposed_status_id <= 5),
	privacy_setting_id INTEGER,
	policy_version TEXT NOT NULL, source_fingerprint TEXT NOT NULL,
	target_fingerprint TEXT NOT NULL,
	state TEXT NOT NULL CHECK(state IN ('pending', 'in_flight', 'applied', 'failed', 'uncertain')),
	error_message TEXT,
	created_at TEXT NOT NULL, updated_at TEXT NOT NULL);
CREATE INDEX idx_sync_intents_lookup ON sync_intents(provider, source_account, book_id, target_account);
CREATE TABLE sync_receipts (
	id INTEGER PRIMARY KEY AUTOINCREMENT,
	intent_id INTEGER NOT NULL REFERENCES sync_intents(id),
	provider TEXT NOT NULL, source_account TEXT NOT NULL, book_id TEXT NOT NULL,
	target_account TEXT NOT NULL, work_id INTEGER NOT NULL CHECK(work_id > 0),
	edition_id INTEGER NOT NULL CHECK(edition_id >= 0),
	target_status_id INTEGER NOT NULL CHECK(target_status_id >= 1 AND target_status_id <= 5),
	applied_at TEXT NOT NULL);
PRAGMA user_version = 5;
`
	if _, err := db.Exec(v5Schema); err != nil {
		t.Fatal(err)
	}

	mapping := testMapping()
	if _, err := db.Exec(`INSERT INTO mappings VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		mapping.Provider, mapping.SourceAccount, mapping.BookID, mapping.TargetAccount,
		mapping.WorkID, mapping.EditionID, mapping.SourceTitle, mapping.SourceISBN); err != nil {
		t.Fatal(err)
	}

	nowStr := "2026-10-27T12:00:00Z"
	if _, err := db.Exec(`INSERT INTO sync_intents VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		1, mapping.Provider, mapping.SourceAccount, mapping.BookID, mapping.TargetAccount,
		mapping.WorkID, mapping.EditionID, 2, nil, "v1", "weread:10001", "hardcover:20002", "applied", nil, nowStr, nowStr); err != nil {
		t.Fatal(err)
	}
	db.Close()

	ctx := context.Background()
	s, err := OpenStore(path)
	if err != nil {
		t.Fatalf("failed to open store from v5: %v", err)
	}
	defer s.Close()

	if schemaVersion(t, s.db) != 6 {
		t.Fatalf("expected version 6, got %d", schemaVersion(t, s.db))
	}

	// Verify v6 sync_runs and sync_holds work
	t0 := time.Date(2026, 10, 27, 14, 0, 0, 0, time.UTC)
	runID, err := s.CreateSyncRun(ctx, mapping.SourceAccount, mapping.TargetAccount, "v1", t0)
	if err != nil || runID <= 0 {
		t.Fatalf("create sync run failed: id=%d, err=%v", runID, err)
	}

	holdID, err := s.RecordSyncHold(ctx, SyncHold{
		RunID:         runID,
		Provider:      mapping.Provider,
		SourceAccount: mapping.SourceAccount,
		BookID:        mapping.BookID,
		TargetAccount: mapping.TargetAccount,
		WorkID:        mapping.WorkID,
		Reason:        "manual_target_edit",
		Message:       "manual edit detected",
		Evidence:      `{"reason":"manual_target_edit"}`,
		CreatedAt:     t0,
	})
	if err != nil || holdID <= 0 {
		t.Fatalf("record sync hold failed: id=%d, err=%v", holdID, err)
	}

	if err := s.UpdateSyncRun(ctx, runID, "completed", 1, 0, 0, 1, 0, 0, t0.Add(5*time.Second)); err != nil {
		t.Fatalf("update sync run failed: %v", err)
	}

	latestRun, err := s.GetLatestSyncRun(ctx, mapping.SourceAccount, mapping.TargetAccount)
	if err != nil || latestRun == nil || latestRun.ID != runID || latestRun.Status != "completed" {
		t.Fatalf("get latest sync run failed: got %+v, err %v", latestRun, err)
	}

	holds, err := s.ListSyncHolds(ctx, mapping.SourceAccount, mapping.TargetAccount)
	if err != nil || len(holds) != 1 || holds[0].ID != holdID {
		t.Fatalf("list sync holds failed: got %+v, err %v", holds, err)
	}
}
