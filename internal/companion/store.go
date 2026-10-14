package companion

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"os"
	"strings"
	"unicode"

	_ "github.com/mattn/go-sqlite3"
)

type Store struct {
	db       *sql.DB
	path     string
	owner    *DatabaseOwnership
	readOnly bool
}

var (
	ErrInvalidMapping    = errors.New("invalid mapping")
	ErrInvalidMappingKey = errors.New("invalid mapping key")
	ErrMappingConflict   = errors.New("mapping already exists with different target IDs")
)

type Mapping struct {
	Provider      string `json:"provider"`
	SourceAccount string `json:"source_account"`
	BookID        string `json:"book_id"`
	TargetAccount string `json:"target_account"`
	WorkID        int64  `json:"work_id"`
	EditionID     int64  `json:"edition_id,omitempty"`
	SourceTitle   string `json:"source_title"`
	SourceISBN    string `json:"source_isbn,omitempty"`
}

func ValidLabel(label string) bool {
	if strings.TrimSpace(label) != label || label == "" || len(label) > 64 {
		return false
	}
	for _, r := range label {
		if unicode.IsControl(r) {
			return false
		}
	}
	return true
}

func (m Mapping) valid() bool {
	return ValidLabel(m.Provider) && ValidLabel(m.SourceAccount) && ValidLabel(m.TargetAccount) && ValidBookID(m.BookID) && m.WorkID > 0 && m.EditionID >= 0
}

func OpenStore(path string) (*Store, error) {
	if err := checkLockSupport(); err != nil {
		return nil, err
	}
	canonical, err := canonicalDatabasePath(path, true)
	if err != nil {
		return nil, err
	}
	lock, err := acquireDatabaseFileLock(canonical, true)
	if err != nil {
		return nil, err
	}
	defer lock.Close()
	return openStore(canonical, false, nil)
}

// OpenStoreForRead preserves the historical initialize-if-needed behavior when
// no owner is active, but falls back to a schema-checked read-only handle when
// another process owns or is writing the database.
func OpenStoreForRead(path string) (*Store, error) {
	store, err := OpenStore(path)
	if errors.Is(err, ErrDatabaseOwned) {
		return OpenStoreReadOnly(path)
	}
	if err != nil {
		return nil, err
	}
	store.readOnly = true
	return store, nil
}

// OpenStoreReadOnly never creates files or migrates schema.
func OpenStoreReadOnly(path string) (*Store, error) {
	if err := checkLockSupport(); err != nil {
		return nil, err
	}
	canonical, err := canonicalDatabasePath(path, false)
	if err != nil {
		return nil, err
	}
	return openStore(canonical, true, nil)
}

// OpenOwnedStore opens state under an already-held daemon ownership lease.
func OpenOwnedStore(path string, owner *DatabaseOwnership) (*Store, error) {
	canonical, err := canonicalDatabasePath(path, true)
	if err != nil {
		return nil, err
	}
	release, err := owner.acquire(canonical)
	if err != nil {
		return nil, err
	}
	defer release()
	return openStore(canonical, false, owner)
}

func openStore(path string, readOnly bool, owner *DatabaseOwnership) (*Store, error) {
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm() != 0o600 {
		return nil, fmt.Errorf("database must be a regular, non-symlink mode-600 file")
	}
	if err := requireSingleLink(info); err != nil {
		return nil, err
	}
	query := "_busy_timeout=5000&_foreign_keys=on&_journal_mode=DELETE&_secure_delete=on"
	if readOnly {
		query = "mode=ro&_busy_timeout=5000"
	}
	u := url.URL{Scheme: "file", Path: path, RawQuery: query}
	db, err := sql.Open("sqlite3", u.String())
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	if readOnly {
		err = validateStoreSchema(db)
	} else {
		err = migrateStore(db)
	}
	if err != nil {
		db.Close()
		if readOnly {
			return nil, fmt.Errorf("mapping database requires initialization or migration")
		}
		return nil, fmt.Errorf("cannot initialize mapping database: %w", err)
	}
	return &Store{db: db, path: path, owner: owner, readOnly: readOnly}, nil
}

const storeVersion = 1

func validateStoreSchema(db *sql.DB) error {
	var version int
	if err := db.QueryRow("PRAGMA user_version").Scan(&version); err != nil {
		return err
	}
	if version != storeVersion {
		return fmt.Errorf("unsupported database schema version %d (supported: %d)", version, storeVersion)
	}
	rows, err := db.Query(`SELECT provider,source_account,book_id,target_account,
		work_id,edition_id,source_title,source_isbn FROM mappings LIMIT 0`)
	if err != nil {
		return err
	}
	return rows.Close()
}

func migrateStore(db *sql.DB) error {
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var version int
	if err := tx.QueryRow("PRAGMA user_version").Scan(&version); err != nil {
		return err
	}
	if version < 0 || version > storeVersion {
		return fmt.Errorf("unsupported database schema version %d (supported: %d)", version, storeVersion)
	}
	if version == 0 {
		if _, err := tx.Exec(`CREATE TABLE IF NOT EXISTS mappings (
			provider TEXT NOT NULL, source_account TEXT NOT NULL, book_id TEXT NOT NULL,
			target_account TEXT NOT NULL, work_id INTEGER NOT NULL CHECK(work_id > 0),
			edition_id INTEGER NOT NULL CHECK(edition_id >= 0), source_title TEXT NOT NULL, source_isbn TEXT NOT NULL,
			PRIMARY KEY(provider, source_account, book_id, target_account))`); err != nil {
			return err
		}
	}
	// Check required columns without reading or rewriting personal metadata.
	rows, err := tx.Query(`SELECT provider,source_account,book_id,target_account,
		work_id,edition_id,source_title,source_isbn FROM mappings LIMIT 0`)
	if err != nil {
		return err
	}
	if err := rows.Close(); err != nil {
		return err
	}
	if version == 0 {
		if _, err := tx.Exec("PRAGMA user_version = 1"); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (s *Store) Close() error { return s.db.Close() }

func (s *Store) writeGuard() (func(), error) {
	if s.readOnly {
		return nil, fmt.Errorf("mapping database is read-only")
	}
	if s.owner != nil {
		return s.owner.acquire(s.path)
	}
	guard, err := acquireDatabaseFileLock(s.path, false)
	if err != nil {
		return nil, err
	}
	return func() { _ = guard.Close() }, nil
}

func (s *Store) Confirm(ctx context.Context, m Mapping, replace bool) error {
	if !m.valid() {
		return ErrInvalidMapping
	}
	release, err := s.writeGuard()
	if err != nil {
		return err
	}
	defer release()
	query := `INSERT INTO mappings VALUES (?, ?, ?, ?, ?, ?, ?, ?) ON CONFLICT(provider,source_account,book_id,target_account) DO NOTHING`
	if replace {
		query = `INSERT INTO mappings VALUES (?, ?, ?, ?, ?, ?, ?, ?) ON CONFLICT(provider,source_account,book_id,target_account) DO UPDATE SET work_id=excluded.work_id, edition_id=excluded.edition_id, source_title=excluded.source_title, source_isbn=excluded.source_isbn`
	}
	r, err := s.db.ExecContext(ctx, query, m.Provider, m.SourceAccount, m.BookID, m.TargetAccount, m.WorkID, m.EditionID, m.SourceTitle, m.SourceISBN)
	if err != nil {
		return err
	}
	n, err := r.RowsAffected()
	if err != nil {
		return err
	}
	if n > 0 {
		return nil
	}
	var work, edition int64
	err = s.db.QueryRowContext(ctx, `SELECT work_id, edition_id FROM mappings WHERE provider=? AND source_account=? AND book_id=? AND target_account=?`, m.Provider, m.SourceAccount, m.BookID, m.TargetAccount).Scan(&work, &edition)
	if err != nil {
		return err
	}
	if work != m.WorkID || edition != m.EditionID {
		return fmt.Errorf("%w; use explicit replacement or unlink it", ErrMappingConflict)
	}
	return nil
}

func (s *Store) List(ctx context.Context) ([]Mapping, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT provider,source_account,book_id,target_account,work_id,edition_id,source_title,source_isbn FROM mappings ORDER BY provider,source_account,book_id,target_account`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []Mapping{}
	for rows.Next() {
		var m Mapping
		if err := rows.Scan(&m.Provider, &m.SourceAccount, &m.BookID, &m.TargetAccount, &m.WorkID, &m.EditionID, &m.SourceTitle, &m.SourceISBN); err != nil {
			return nil, err
		}
		result = append(result, m)
	}
	return result, rows.Err()
}

func (s *Store) Unlink(ctx context.Context, provider, sourceAccount, bookID, targetAccount string) error {
	if !ValidLabel(provider) || !ValidLabel(sourceAccount) || !ValidLabel(targetAccount) || !ValidBookID(bookID) {
		return ErrInvalidMappingKey
	}
	release, err := s.writeGuard()
	if err != nil {
		return err
	}
	defer release()
	_, err = s.db.ExecContext(ctx, `DELETE FROM mappings WHERE provider=? AND source_account=? AND book_id=? AND target_account=?`, provider, sourceAccount, bookID, targetAccount)
	return err
}
