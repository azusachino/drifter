package companion

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"testing"
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
				if schemaVersion(t, s.db) != 1 {
					t.Fatal("database not upgraded to version 1")
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
	for _, version := range []int{2, -1} {
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
