package companion

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"os"
	"strings"
	"time"
	"unicode"

	"github.com/azusachino/drifter/internal/catalog"
	_ "github.com/mattn/go-sqlite3"
)

type Store struct {
	db       *sql.DB
	path     string
	owner    *DatabaseOwnership
	readOnly bool
}

var (
	ErrInvalidMapping     = errors.New("invalid mapping")
	ErrInvalidMappingKey  = errors.New("invalid mapping key")
	ErrMappingConflict    = errors.New("mapping already exists with different target IDs")
	errCatalogExportLimit = errors.New("catalog export record limit exceeded")
)

// MappingTarget is the expected target tuple for a guarded mapping mutation.
type MappingTarget struct {
	WorkID    int64
	EditionID int64
}

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

const storeVersion = 6

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
	if err := rows.Close(); err != nil {
		return err
	}
	rows, err = db.Query(`SELECT provider,source_account,book_id,target_account,kind FROM decisions LIMIT 0`)
	if err != nil {
		return err
	}
	if err := rows.Close(); err != nil {
		return err
	}
	rows, err = db.Query(`SELECT provider,account,fingerprint,created_at,last_verified_at FROM account_fingerprints LIMIT 0`)
	if err != nil {
		return err
	}
	if err := rows.Close(); err != nil {
		return err
	}
	rows, err = db.Query(`SELECT provider,source_account,book_id,target_account,work_id,edition_id,source_status_id,target_status_id,policy_version,source_fingerprint,target_fingerprint,last_synced_at FROM status_baselines LIMIT 0`)
	if err != nil {
		return err
	}
	if err := rows.Close(); err != nil {
		return err
	}
	rows, err = db.Query(`SELECT target_account,work_id,authorized_provider,authorized_account,authorized_book_id,policy_version,privacy_mode,created_at,updated_at FROM target_authorities LIMIT 0`)
	if err != nil {
		return err
	}
	if err := rows.Close(); err != nil {
		return err
	}
	rows, err = db.Query(`SELECT id,provider,source_account,book_id,target_account,work_id,edition_id,proposed_status_id,policy_version,source_fingerprint,target_fingerprint,state,created_at,updated_at FROM sync_intents LIMIT 0`)
	if err != nil {
		return err
	}
	if err := rows.Close(); err != nil {
		return err
	}
	rows, err = db.Query(`SELECT id,intent_id,provider,source_account,book_id,target_account,work_id,edition_id,target_status_id,applied_at FROM sync_receipts LIMIT 0`)
	if err != nil {
		return err
	}
	if err := rows.Close(); err != nil {
		return err
	}
	rows, err = db.Query(`SELECT id,source_account,target_account,policy_version,status,total_count,applied_count,noop_count,conflict_count,hold_count,error_count,started_at,finished_at FROM sync_runs LIMIT 0`)
	if err != nil {
		return err
	}
	if err := rows.Close(); err != nil {
		return err
	}
	rows, err = db.Query(`SELECT id,run_id,provider,source_account,book_id,target_account,work_id,reason,message,evidence,created_at FROM sync_holds LIMIT 0`)
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
		version = 1
	}
	if version == 1 {
		if _, err := tx.Exec(`CREATE TABLE decisions (
			provider TEXT NOT NULL, source_account TEXT NOT NULL, book_id TEXT NOT NULL,
			target_account TEXT NOT NULL, kind TEXT NOT NULL CHECK(kind IN ('skip','exclude','not-in-catalog')),
			PRIMARY KEY(provider, source_account, book_id, target_account))`); err != nil {
			return err
		}
		if _, err := tx.Exec("PRAGMA user_version = 2"); err != nil {
			return err
		}
		version = 2
	}
	if version == 2 {
		if _, err := tx.Exec(`CREATE TABLE account_fingerprints (
			provider TEXT NOT NULL, account TEXT NOT NULL, fingerprint TEXT NOT NULL,
			created_at TEXT NOT NULL, last_verified_at TEXT NOT NULL,
			PRIMARY KEY(provider, account))`); err != nil {
			return err
		}
		if _, err := tx.Exec("PRAGMA user_version = 3"); err != nil {
			return err
		}
		version = 3
	}
	if version == 3 {
		if _, err := tx.Exec(`CREATE TABLE status_baselines (
			provider TEXT NOT NULL, source_account TEXT NOT NULL, book_id TEXT NOT NULL,
			target_account TEXT NOT NULL, work_id INTEGER NOT NULL CHECK(work_id > 0),
			edition_id INTEGER NOT NULL CHECK(edition_id >= 0),
			source_status_id INTEGER NOT NULL CHECK(source_status_id >= 1 AND source_status_id <= 5),
			target_status_id INTEGER NOT NULL CHECK(target_status_id >= 1 AND target_status_id <= 5),
			policy_version TEXT NOT NULL, source_fingerprint TEXT NOT NULL,
			target_fingerprint TEXT NOT NULL, last_synced_at TEXT NOT NULL,
			PRIMARY KEY(provider, source_account, book_id, target_account))`); err != nil {
			return err
		}
		if _, err := tx.Exec(`CREATE TABLE target_authorities (
			target_account TEXT NOT NULL, work_id INTEGER NOT NULL CHECK(work_id > 0),
			authorized_provider TEXT NOT NULL, authorized_account TEXT NOT NULL,
			authorized_book_id TEXT NOT NULL, policy_version TEXT NOT NULL,
			privacy_mode TEXT NOT NULL CHECK(privacy_mode IN ('default', 'private-target', 'exclude')),
			created_at TEXT NOT NULL, updated_at TEXT NOT NULL,
			PRIMARY KEY(target_account, work_id))`); err != nil {
			return err
		}
		if _, err := tx.Exec("PRAGMA user_version = 4"); err != nil {
			return err
		}
		version = 4
	}
	if version == 4 {
		if _, err := tx.Exec(`CREATE TABLE sync_intents (
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
			created_at TEXT NOT NULL, updated_at TEXT NOT NULL)`); err != nil {
			return err
		}
		if _, err := tx.Exec(`CREATE INDEX idx_sync_intents_lookup ON sync_intents(provider, source_account, book_id, target_account)`); err != nil {
			return err
		}
		if _, err := tx.Exec(`CREATE TABLE sync_receipts (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			intent_id INTEGER NOT NULL REFERENCES sync_intents(id),
			provider TEXT NOT NULL, source_account TEXT NOT NULL, book_id TEXT NOT NULL,
			target_account TEXT NOT NULL, work_id INTEGER NOT NULL CHECK(work_id > 0),
			edition_id INTEGER NOT NULL CHECK(edition_id >= 0),
			target_status_id INTEGER NOT NULL CHECK(target_status_id >= 1 AND target_status_id <= 5),
			applied_at TEXT NOT NULL)`); err != nil {
			return err
		}
		if _, err := tx.Exec("PRAGMA user_version = 5"); err != nil {
			return err
		}
		version = 5
	}
	if version == 5 {
		if _, err := tx.Exec(`CREATE TABLE sync_runs (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			source_account TEXT NOT NULL, target_account TEXT NOT NULL,
			policy_version TEXT NOT NULL,
			status TEXT NOT NULL CHECK(status IN ('in_progress', 'completed', 'partial', 'failed')),
			total_count INTEGER NOT NULL DEFAULT 0,
			applied_count INTEGER NOT NULL DEFAULT 0,
			noop_count INTEGER NOT NULL DEFAULT 0,
			conflict_count INTEGER NOT NULL DEFAULT 0,
			hold_count INTEGER NOT NULL DEFAULT 0,
			error_count INTEGER NOT NULL DEFAULT 0,
			started_at TEXT NOT NULL, finished_at TEXT)`); err != nil {
			return err
		}
		if _, err := tx.Exec(`CREATE TABLE sync_holds (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			run_id INTEGER NOT NULL REFERENCES sync_runs(id),
			provider TEXT NOT NULL, source_account TEXT NOT NULL, book_id TEXT NOT NULL,
			target_account TEXT NOT NULL, work_id INTEGER NOT NULL,
			reason TEXT NOT NULL, message TEXT NOT NULL, evidence TEXT NOT NULL,
			created_at TEXT NOT NULL)`); err != nil {
			return err
		}
		if _, err := tx.Exec(`CREATE INDEX idx_sync_holds_lookup ON sync_holds(provider, source_account, book_id, target_account)`); err != nil {
			return err
		}
		if _, err := tx.Exec("PRAGMA user_version = 6"); err != nil {
			return err
		}
		version = 6
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

// ReplaceExpected updates only an existing mapping whose current target matches expected.
func (s *Store) ReplaceExpected(ctx context.Context, m Mapping, expected MappingTarget) error {
	if !m.valid() || expected.WorkID <= 0 || expected.EditionID < 0 {
		return ErrInvalidMapping
	}
	release, err := s.writeGuard()
	if err != nil {
		return err
	}
	defer release()
	r, err := s.db.ExecContext(ctx, `UPDATE mappings SET work_id=?,edition_id=?,source_title=?,source_isbn=? WHERE provider=? AND source_account=? AND book_id=? AND target_account=? AND work_id=? AND edition_id=?`, m.WorkID, m.EditionID, m.SourceTitle, m.SourceISBN, m.Provider, m.SourceAccount, m.BookID, m.TargetAccount, expected.WorkID, expected.EditionID)
	if err != nil {
		return err
	}
	n, err := r.RowsAffected()
	if err != nil {
		return err
	}
	if n == 1 {
		return nil
	}
	return ErrMappingConflict
}

// UnlinkExpected removes a matching mapping; absence is idempotent, but a newer target conflicts.
func (s *Store) UnlinkExpected(ctx context.Context, provider, sourceAccount, bookID, targetAccount string, expected MappingTarget) error {
	if !ValidLabel(provider) || !ValidLabel(sourceAccount) || !ValidLabel(targetAccount) || !ValidBookID(bookID) || expected.WorkID <= 0 || expected.EditionID < 0 {
		return ErrInvalidMappingKey
	}
	release, err := s.writeGuard()
	if err != nil {
		return err
	}
	defer release()
	result, err := s.db.ExecContext(ctx, `DELETE FROM mappings WHERE provider=? AND source_account=? AND book_id=? AND target_account=? AND work_id=? AND edition_id=?`, provider, sourceAccount, bookID, targetAccount, expected.WorkID, expected.EditionID)
	if err != nil {
		return err
	}
	n, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if n == 1 {
		return nil
	}
	var exists int
	err = s.db.QueryRowContext(ctx, `SELECT 1 FROM mappings WHERE provider=? AND source_account=? AND book_id=? AND target_account=?`, provider, sourceAccount, bookID, targetAccount).Scan(&exists)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	return ErrMappingConflict
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

// listCatalogMappings selects only public catalog fields and reads at most one
// row beyond the shared v1 record limit to detect overflow.
func (s *Store) listCatalogMappings(ctx context.Context, sourceAccount, targetAccount string) ([]Mapping, error) {
	limit := catalog.MaxRecordsPerCatalog
	rows, err := s.db.QueryContext(ctx, `SELECT provider,book_id,work_id,edition_id
		FROM mappings WHERE source_account=? AND target_account=?
		ORDER BY provider,book_id,work_id,edition_id LIMIT ?`, sourceAccount, targetAccount, limit+1)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := make([]Mapping, 0, limit)
	for rows.Next() {
		if len(result) == limit {
			return nil, errCatalogExportLimit
		}
		var m Mapping
		if err := rows.Scan(&m.Provider, &m.BookID, &m.WorkID, &m.EditionID); err != nil {
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

// GetMapping retrieves a confirmed mapping by key, returning nil, nil if none exists.
func (s *Store) GetMapping(ctx context.Context, provider, sourceAccount, bookID, targetAccount string) (*Mapping, error) {
	if !ValidLabel(provider) || !ValidLabel(sourceAccount) || !ValidLabel(targetAccount) || !ValidBookID(bookID) {
		return nil, ErrInvalidMappingKey
	}
	var m Mapping
	err := s.db.QueryRowContext(ctx, `SELECT provider,source_account,book_id,target_account,work_id,edition_id,source_title,source_isbn
		FROM mappings WHERE provider=? AND source_account=? AND book_id=? AND target_account=?`,
		provider, sourceAccount, bookID, targetAccount).
		Scan(&m.Provider, &m.SourceAccount, &m.BookID, &m.TargetAccount, &m.WorkID, &m.EditionID, &m.SourceTitle, &m.SourceISBN)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &m, nil
}

// ListMappingsForTarget returns all mappings in targetAccount that point to workID.
func (s *Store) ListMappingsForTarget(ctx context.Context, targetAccount string, workID int64) ([]Mapping, error) {
	if !ValidLabel(targetAccount) || workID <= 0 {
		return nil, ErrInvalidMappingKey
	}
	rows, err := s.db.QueryContext(ctx, `SELECT provider,source_account,book_id,target_account,work_id,edition_id,source_title,source_isbn
		FROM mappings WHERE target_account=? AND work_id=? ORDER BY provider,source_account,book_id`, targetAccount, workID)
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

// ListMappingsForAccount returns confirmed mappings for an account pair.
func (s *Store) ListMappingsForAccount(ctx context.Context, provider, sourceAccount, targetAccount string) ([]Mapping, error) {
	if !ValidLabel(provider) || !ValidLabel(sourceAccount) || !ValidLabel(targetAccount) {
		return nil, ErrInvalidMappingKey
	}
	rows, err := s.db.QueryContext(ctx, `SELECT provider,source_account,book_id,target_account,work_id,edition_id,source_title,source_isbn
		FROM mappings WHERE provider=? AND source_account=? AND target_account=? ORDER BY book_id`,
		provider, sourceAccount, targetAccount)
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

// GetStatusBaseline retrieves the accepted baseline for a book mapping, returning nil, nil if none exists.
func (s *Store) GetStatusBaseline(ctx context.Context, provider, sourceAccount, bookID, targetAccount string) (*StatusBaseline, error) {
	if !ValidLabel(provider) || !ValidLabel(sourceAccount) || !ValidLabel(targetAccount) || !ValidBookID(bookID) {
		return nil, ErrInvalidMappingKey
	}
	var b StatusBaseline
	var syncedAtStr string
	err := s.db.QueryRowContext(ctx, `SELECT provider,source_account,book_id,target_account,work_id,edition_id,
		source_status_id,target_status_id,policy_version,source_fingerprint,target_fingerprint,last_synced_at
		FROM status_baselines WHERE provider=? AND source_account=? AND book_id=? AND target_account=?`,
		provider, sourceAccount, bookID, targetAccount).
		Scan(&b.Provider, &b.SourceAccount, &b.BookID, &b.TargetAccount, &b.WorkID, &b.EditionID,
			&b.SourceStatusID, &b.TargetStatusID, &b.PolicyVersion, &b.SourceFingerprint, &b.TargetFingerprint, &syncedAtStr)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	t, err := time.Parse(time.RFC3339, syncedAtStr)
	if err != nil {
		return nil, fmt.Errorf("corrupt timestamp in status_baselines: %w", err)
	}
	b.LastSyncedAt = t
	return &b, nil
}

// SaveStatusBaseline writes or updates the accepted baseline for a book mapping.
func (s *Store) SaveStatusBaseline(ctx context.Context, b StatusBaseline) error {
	if !b.valid() {
		return fmt.Errorf("invalid status baseline")
	}
	release, err := s.writeGuard()
	if err != nil {
		return err
	}
	defer release()
	syncedAtStr := b.LastSyncedAt.UTC().Format(time.RFC3339)
	_, err = s.db.ExecContext(ctx, `INSERT INTO status_baselines (provider, source_account, book_id, target_account,
		work_id, edition_id, source_status_id, target_status_id, policy_version, source_fingerprint, target_fingerprint, last_synced_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(provider, source_account, book_id, target_account) DO UPDATE SET
		work_id=excluded.work_id, edition_id=excluded.edition_id,
		source_status_id=excluded.source_status_id, target_status_id=excluded.target_status_id,
		policy_version=excluded.policy_version, source_fingerprint=excluded.source_fingerprint,
		target_fingerprint=excluded.target_fingerprint, last_synced_at=excluded.last_synced_at`,
		b.Provider, b.SourceAccount, b.BookID, b.TargetAccount,
		b.WorkID, b.EditionID, b.SourceStatusID, b.TargetStatusID,
		b.PolicyVersion, b.SourceFingerprint, b.TargetFingerprint, syncedAtStr)
	return err
}

// GetTargetAuthority returns the status write authority for a target work, returning nil, nil if none is set.
func (s *Store) GetTargetAuthority(ctx context.Context, targetAccount string, workID int64) (*TargetAuthority, error) {
	if !ValidLabel(targetAccount) || workID <= 0 {
		return nil, ErrInvalidMappingKey
	}
	var a TargetAuthority
	var createdStr, updatedStr string
	err := s.db.QueryRowContext(ctx, `SELECT target_account,work_id,authorized_provider,authorized_account,authorized_book_id,
		policy_version,privacy_mode,created_at,updated_at
		FROM target_authorities WHERE target_account=? AND work_id=?`, targetAccount, workID).
		Scan(&a.TargetAccount, &a.WorkID, &a.AuthorizedProvider, &a.AuthorizedAccount, &a.AuthorizedBookID,
			&a.PolicyVersion, &a.PrivacyMode, &createdStr, &updatedStr)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	ct, err := time.Parse(time.RFC3339, createdStr)
	if err != nil {
		return nil, fmt.Errorf("corrupt created_at in target_authorities: %w", err)
	}
	ut, err := time.Parse(time.RFC3339, updatedStr)
	if err != nil {
		return nil, fmt.Errorf("corrupt updated_at in target_authorities: %w", err)
	}
	a.CreatedAt = ct
	a.UpdatedAt = ut
	return &a, nil
}

// SetTargetAuthority sets or updates the status write authority for a target work.
func (s *Store) SetTargetAuthority(ctx context.Context, a TargetAuthority) error {
	if !a.valid() {
		return fmt.Errorf("invalid target authority")
	}
	release, err := s.writeGuard()
	if err != nil {
		return err
	}
	defer release()
	createdStr := a.CreatedAt.UTC().Format(time.RFC3339)
	updatedStr := a.UpdatedAt.UTC().Format(time.RFC3339)
	_, err = s.db.ExecContext(ctx, `INSERT INTO target_authorities (target_account, work_id, authorized_provider, authorized_account,
		authorized_book_id, policy_version, privacy_mode, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(target_account, work_id) DO UPDATE SET
		authorized_provider=excluded.authorized_provider, authorized_account=excluded.authorized_account,
		authorized_book_id=excluded.authorized_book_id, policy_version=excluded.policy_version,
		privacy_mode=excluded.privacy_mode, updated_at=excluded.updated_at`,
		a.TargetAccount, a.WorkID, a.AuthorizedProvider, a.AuthorizedAccount,
		a.AuthorizedBookID, a.PolicyVersion, a.PrivacyMode, createdStr, updatedStr)
	return err
}

// SyncIntent records a durable intent to mutate target reading status before dispatch.
type SyncIntent struct {
	ID                int64     `json:"id"`
	Provider          string    `json:"provider"`
	SourceAccount     string    `json:"source_account"`
	BookID            string    `json:"book_id"`
	TargetAccount     string    `json:"target_account"`
	WorkID            int64     `json:"work_id"`
	EditionID         int64     `json:"edition_id,omitempty"`
	ProposedStatusID  int       `json:"proposed_status_id"`
	PrivacySettingID  *int      `json:"privacy_setting_id,omitempty"`
	PolicyVersion     string    `json:"policy_version"`
	SourceFingerprint string    `json:"source_fingerprint"`
	TargetFingerprint string    `json:"target_fingerprint"`
	State             string    `json:"state"` // "pending", "in_flight", "applied", "failed", "uncertain"
	ErrorMessage      string    `json:"error_message,omitempty"`
	CreatedAt         time.Time `json:"created_at"`
	UpdatedAt         time.Time `json:"updated_at"`
}

func (i SyncIntent) valid() bool {
	return ValidLabel(i.Provider) && ValidLabel(i.SourceAccount) && ValidLabel(i.TargetAccount) &&
		ValidBookID(i.BookID) && i.WorkID > 0 && i.EditionID >= 0 &&
		ValidHardcoverStatus(i.ProposedStatusID) && i.PolicyVersion != "" &&
		ValidFingerprint(i.SourceFingerprint) && ValidFingerprint(i.TargetFingerprint) &&
		(i.State == "pending" || i.State == "in_flight" || i.State == "applied" || i.State == "failed" || i.State == "uncertain") &&
		!i.CreatedAt.IsZero() && !i.UpdatedAt.IsZero()
}

// SyncReceipt records an immutable confirmation of an applied status mutation.
type SyncReceipt struct {
	ID             int64     `json:"id"`
	IntentID       int64     `json:"intent_id"`
	Provider       string    `json:"provider"`
	SourceAccount  string    `json:"source_account"`
	BookID         string    `json:"book_id"`
	TargetAccount  string    `json:"target_account"`
	WorkID         int64     `json:"work_id"`
	EditionID      int64     `json:"edition_id,omitempty"`
	TargetStatusID int       `json:"target_status_id"`
	AppliedAt      time.Time `json:"applied_at"`
}

func (r SyncReceipt) valid() bool {
	return r.IntentID > 0 && ValidLabel(r.Provider) && ValidLabel(r.SourceAccount) &&
		ValidLabel(r.TargetAccount) && ValidBookID(r.BookID) && r.WorkID > 0 && r.EditionID >= 0 &&
		ValidHardcoverStatus(r.TargetStatusID) && !r.AppliedAt.IsZero()
}

// RecordIntent persists a new write intent, returning the assigned intent ID.
func (s *Store) RecordIntent(ctx context.Context, intent SyncIntent) (int64, error) {
	if !intent.valid() {
		return 0, fmt.Errorf("invalid sync intent")
	}
	release, err := s.writeGuard()
	if err != nil {
		return 0, err
	}
	defer release()
	createdStr := intent.CreatedAt.UTC().Format(time.RFC3339)
	updatedStr := intent.UpdatedAt.UTC().Format(time.RFC3339)
	r, err := s.db.ExecContext(ctx, `INSERT INTO sync_intents (
		provider, source_account, book_id, target_account, work_id, edition_id,
		proposed_status_id, privacy_setting_id, policy_version, source_fingerprint,
		target_fingerprint, state, error_message, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		intent.Provider, intent.SourceAccount, intent.BookID, intent.TargetAccount,
		intent.WorkID, intent.EditionID, intent.ProposedStatusID, intent.PrivacySettingID,
		intent.PolicyVersion, intent.SourceFingerprint, intent.TargetFingerprint,
		intent.State, intent.ErrorMessage, createdStr, updatedStr)
	if err != nil {
		return 0, err
	}
	return r.LastInsertId()
}

// UpdateIntentState updates the state and error message of an existing intent.
func (s *Store) UpdateIntentState(ctx context.Context, id int64, state, errorMessage string, updatedAt time.Time) error {
	if id <= 0 || (state != "pending" && state != "in_flight" && state != "applied" && state != "failed" && state != "uncertain") {
		return fmt.Errorf("invalid intent state update")
	}
	release, err := s.writeGuard()
	if err != nil {
		return err
	}
	defer release()
	updatedStr := updatedAt.UTC().Format(time.RFC3339)
	_, err = s.db.ExecContext(ctx, `UPDATE sync_intents SET state=?, error_message=?, updated_at=? WHERE id=?`,
		state, errorMessage, updatedStr, id)
	return err
}

// GetActiveIntent returns the latest pending, in_flight, or uncertain intent for a mapping, if any.
func (s *Store) GetActiveIntent(ctx context.Context, provider, sourceAccount, bookID, targetAccount string) (*SyncIntent, error) {
	if !ValidLabel(provider) || !ValidLabel(sourceAccount) || !ValidLabel(targetAccount) || !ValidBookID(bookID) {
		return nil, ErrInvalidMappingKey
	}
	var i SyncIntent
	var priv sql.NullInt64
	var errStr sql.NullString
	var createdStr, updatedStr string
	err := s.db.QueryRowContext(ctx, `SELECT id, provider, source_account, book_id, target_account, work_id, edition_id,
		proposed_status_id, privacy_setting_id, policy_version, source_fingerprint, target_fingerprint,
		state, error_message, created_at, updated_at
		FROM sync_intents WHERE provider=? AND source_account=? AND book_id=? AND target_account=?
		AND state IN ('pending', 'in_flight', 'uncertain') ORDER BY id DESC LIMIT 1`,
		provider, sourceAccount, bookID, targetAccount).
		Scan(&i.ID, &i.Provider, &i.SourceAccount, &i.BookID, &i.TargetAccount, &i.WorkID, &i.EditionID,
			&i.ProposedStatusID, &priv, &i.PolicyVersion, &i.SourceFingerprint, &i.TargetFingerprint,
			&i.State, &errStr, &createdStr, &updatedStr)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if priv.Valid {
		pVal := int(priv.Int64)
		i.PrivacySettingID = &pVal
	}
	if errStr.Valid {
		i.ErrorMessage = errStr.String
	}
	ct, err := time.Parse(time.RFC3339, createdStr)
	if err != nil {
		return nil, fmt.Errorf("corrupt created_at in sync_intents: %w", err)
	}
	ut, err := time.Parse(time.RFC3339, updatedStr)
	if err != nil {
		return nil, fmt.Errorf("corrupt updated_at in sync_intents: %w", err)
	}
	i.CreatedAt = ct
	i.UpdatedAt = ut
	return &i, nil
}

// RecordReceipt writes an immutable audit receipt for an applied status mutation.
func (s *Store) RecordReceipt(ctx context.Context, r SyncReceipt) (int64, error) {
	if !r.valid() {
		return 0, fmt.Errorf("invalid sync receipt")
	}
	release, err := s.writeGuard()
	if err != nil {
		return 0, err
	}
	defer release()
	appliedStr := r.AppliedAt.UTC().Format(time.RFC3339)
	res, err := s.db.ExecContext(ctx, `INSERT INTO sync_receipts (
		intent_id, provider, source_account, book_id, target_account,
		work_id, edition_id, target_status_id, applied_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		r.IntentID, r.Provider, r.SourceAccount, r.BookID, r.TargetAccount,
		r.WorkID, r.EditionID, r.TargetStatusID, appliedStr)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

// GetLatestReceipt returns the most recent receipt for a mapping, returning nil, nil if none exists.
func (s *Store) GetLatestReceipt(ctx context.Context, provider, sourceAccount, bookID, targetAccount string) (*SyncReceipt, error) {
	if !ValidLabel(provider) || !ValidLabel(sourceAccount) || !ValidLabel(targetAccount) || !ValidBookID(bookID) {
		return nil, ErrInvalidMappingKey
	}
	var r SyncReceipt
	var appliedStr string
	err := s.db.QueryRowContext(ctx, `SELECT id, intent_id, provider, source_account, book_id, target_account,
		work_id, edition_id, target_status_id, applied_at
		FROM sync_receipts WHERE provider=? AND source_account=? AND book_id=? AND target_account=?
		ORDER BY id DESC LIMIT 1`,
		provider, sourceAccount, bookID, targetAccount).
		Scan(&r.ID, &r.IntentID, &r.Provider, &r.SourceAccount, &r.BookID, &r.TargetAccount,
			&r.WorkID, &r.EditionID, &r.TargetStatusID, &appliedStr)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	at, err := time.Parse(time.RFC3339, appliedStr)
	if err != nil {
		return nil, fmt.Errorf("corrupt applied_at in sync_receipts: %w", err)
	}
	r.AppliedAt = at
	return &r, nil
}

// SyncRun records a monotonic continuous or batch reconciliation run.
type SyncRun struct {
	ID            int64      `json:"id"`
	SourceAccount string     `json:"source_account"`
	TargetAccount string     `json:"target_account"`
	PolicyVersion string     `json:"policy_version"`
	Status        string     `json:"status"` // "in_progress", "completed", "partial", "failed"
	TotalCount    int        `json:"total_count"`
	AppliedCount  int        `json:"applied_count"`
	NoOpCount     int        `json:"noop_count"`
	ConflictCount int        `json:"conflict_count"`
	HoldCount     int        `json:"hold_count"`
	ErrorCount    int        `json:"error_count"`
	StartedAt     time.Time  `json:"started_at"`
	FinishedAt    *time.Time `json:"finished_at,omitempty"`
}

// SyncHold records a held or conflicting book during a reconciliation run.
type SyncHold struct {
	ID            int64     `json:"id"`
	RunID         int64     `json:"run_id"`
	Provider      string    `json:"provider"`
	SourceAccount string    `json:"source_account"`
	BookID        string    `json:"book_id"`
	TargetAccount string    `json:"target_account"`
	WorkID        int64     `json:"work_id"`
	Reason        string    `json:"reason"`
	Message       string    `json:"message"`
	Evidence      string    `json:"evidence"` // JSON-encoded ConflictEvidence if present
	CreatedAt     time.Time `json:"created_at"`
}

// CreateSyncRun records the start of a monotonic reconciliation run.
func (s *Store) CreateSyncRun(ctx context.Context, sourceAccount, targetAccount, policyVersion string, startedAt time.Time) (int64, error) {
	if !ValidLabel(sourceAccount) || !ValidLabel(targetAccount) || policyVersion == "" {
		return 0, ErrInvalidMappingKey
	}
	release, err := s.writeGuard()
	if err != nil {
		return 0, err
	}
	defer release()
	startedStr := startedAt.UTC().Format(time.RFC3339)
	r, err := s.db.ExecContext(ctx, `INSERT INTO sync_runs (
		source_account, target_account, policy_version, status, started_at)
		VALUES (?, ?, ?, 'in_progress', ?)`,
		sourceAccount, targetAccount, policyVersion, startedStr)
	if err != nil {
		return 0, err
	}
	return r.LastInsertId()
}

// UpdateSyncRun updates statistics and completion status of a reconciliation run.
func (s *Store) UpdateSyncRun(ctx context.Context, runID int64, status string, total, applied, noop, conflict, hold, errCount int, finishedAt time.Time) error {
	if runID <= 0 || (status != "in_progress" && status != "completed" && status != "partial" && status != "failed") {
		return fmt.Errorf("invalid sync run status update")
	}
	release, err := s.writeGuard()
	if err != nil {
		return err
	}
	defer release()
	finStr := finishedAt.UTC().Format(time.RFC3339)
	_, err = s.db.ExecContext(ctx, `UPDATE sync_runs SET
		status=?, total_count=?, applied_count=?, noop_count=?, conflict_count=?, hold_count=?, error_count=?, finished_at=?
		WHERE id=?`,
		status, total, applied, noop, conflict, hold, errCount, finStr, runID)
	return err
}

// GetLatestSyncRun returns the most recent sync run, optionally filtered by account pair.
func (s *Store) GetLatestSyncRun(ctx context.Context, sourceAccount, targetAccount string) (*SyncRun, error) {
	var r SyncRun
	var startStr string
	var finStr sql.NullString
	var row *sql.Row
	if sourceAccount != "" && targetAccount != "" {
		if !ValidLabel(sourceAccount) || !ValidLabel(targetAccount) {
			return nil, ErrInvalidMappingKey
		}
		row = s.db.QueryRowContext(ctx, `SELECT id, source_account, target_account, policy_version, status,
			total_count, applied_count, noop_count, conflict_count, hold_count, error_count, started_at, finished_at
			FROM sync_runs WHERE source_account=? AND target_account=? ORDER BY id DESC LIMIT 1`,
			sourceAccount, targetAccount)
	} else {
		row = s.db.QueryRowContext(ctx, `SELECT id, source_account, target_account, policy_version, status,
			total_count, applied_count, noop_count, conflict_count, hold_count, error_count, started_at, finished_at
			FROM sync_runs ORDER BY id DESC LIMIT 1`)
	}
	err := row.Scan(&r.ID, &r.SourceAccount, &r.TargetAccount, &r.PolicyVersion, &r.Status,
		&r.TotalCount, &r.AppliedCount, &r.NoOpCount, &r.ConflictCount, &r.HoldCount, &r.ErrorCount,
		&startStr, &finStr)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	st, err := time.Parse(time.RFC3339, startStr)
	if err != nil {
		return nil, fmt.Errorf("corrupt started_at in sync_runs: %w", err)
	}
	r.StartedAt = st
	if finStr.Valid {
		ft, err := time.Parse(time.RFC3339, finStr.String)
		if err != nil {
			return nil, fmt.Errorf("corrupt finished_at in sync_runs: %w", err)
		}
		r.FinishedAt = &ft
	}
	return &r, nil
}

// RecordSyncHold records an individual hold or conflict observed during a run.
func (s *Store) RecordSyncHold(ctx context.Context, hold SyncHold) (int64, error) {
	if hold.RunID <= 0 || !ValidLabel(hold.Provider) || !ValidLabel(hold.SourceAccount) || !ValidLabel(hold.TargetAccount) || !ValidBookID(hold.BookID) || hold.WorkID <= 0 {
		return 0, fmt.Errorf("invalid sync hold record")
	}
	release, err := s.writeGuard()
	if err != nil {
		return 0, err
	}
	defer release()
	createdStr := hold.CreatedAt.UTC().Format(time.RFC3339)
	r, err := s.db.ExecContext(ctx, `INSERT INTO sync_holds (
		run_id, provider, source_account, book_id, target_account, work_id, reason, message, evidence, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		hold.RunID, hold.Provider, hold.SourceAccount, hold.BookID, hold.TargetAccount, hold.WorkID,
		hold.Reason, hold.Message, hold.Evidence, createdStr)
	if err != nil {
		return 0, err
	}
	return r.LastInsertId()
}

// ListSyncHolds returns the latest active holds, optionally filtered by account pair.
func (s *Store) ListSyncHolds(ctx context.Context, sourceAccount, targetAccount string) ([]SyncHold, error) {
	var rows *sql.Rows
	var err error
	if sourceAccount != "" && targetAccount != "" {
		if !ValidLabel(sourceAccount) || !ValidLabel(targetAccount) {
			return nil, ErrInvalidMappingKey
		}
		rows, err = s.db.QueryContext(ctx, `SELECT id, run_id, provider, source_account, book_id, target_account, work_id, reason, message, evidence, created_at
			FROM sync_holds WHERE source_account=? AND target_account=? ORDER BY id DESC`, sourceAccount, targetAccount)
	} else {
		rows, err = s.db.QueryContext(ctx, `SELECT id, run_id, provider, source_account, book_id, target_account, work_id, reason, message, evidence, created_at
			FROM sync_holds ORDER BY id DESC LIMIT 50`)
	}
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var holds []SyncHold
	for rows.Next() {
		var h SyncHold
		var createdStr string
		if err := rows.Scan(&h.ID, &h.RunID, &h.Provider, &h.SourceAccount, &h.BookID, &h.TargetAccount, &h.WorkID, &h.Reason, &h.Message, &h.Evidence, &createdStr); err != nil {
			return nil, err
		}
		t, err := time.Parse(time.RFC3339, createdStr)
		if err != nil {
			return nil, fmt.Errorf("corrupt created_at in sync_holds: %w", err)
		}
		h.CreatedAt = t
		holds = append(holds, h)
	}
	return holds, rows.Err()
}

// ListSyncRuns returns recent sync runs, optionally filtered by account pair.
func (s *Store) ListSyncRuns(ctx context.Context, sourceAccount, targetAccount string, limit int) ([]SyncRun, error) {
	if limit <= 0 || limit > 100 {
		limit = 50
	}
	var rows *sql.Rows
	var err error
	if sourceAccount != "" && targetAccount != "" {
		if !ValidLabel(sourceAccount) || !ValidLabel(targetAccount) {
			return nil, ErrInvalidMappingKey
		}
		rows, err = s.db.QueryContext(ctx, `SELECT id, source_account, target_account, policy_version, status,
			total_count, applied_count, noop_count, conflict_count, hold_count, error_count, started_at, finished_at
			FROM sync_runs WHERE source_account=? AND target_account=? ORDER BY id DESC LIMIT ?`,
			sourceAccount, targetAccount, limit)
	} else {
		rows, err = s.db.QueryContext(ctx, `SELECT id, source_account, target_account, policy_version, status,
			total_count, applied_count, noop_count, conflict_count, hold_count, error_count, started_at, finished_at
			FROM sync_runs ORDER BY id DESC LIMIT ?`, limit)
	}
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var runs []SyncRun
	for rows.Next() {
		var r SyncRun
		var startStr string
		var finStr sql.NullString
		if err := rows.Scan(&r.ID, &r.SourceAccount, &r.TargetAccount, &r.PolicyVersion, &r.Status,
			&r.TotalCount, &r.AppliedCount, &r.NoOpCount, &r.ConflictCount, &r.HoldCount, &r.ErrorCount,
			&startStr, &finStr); err != nil {
			return nil, err
		}
		st, err := time.Parse(time.RFC3339, startStr)
		if err != nil {
			return nil, fmt.Errorf("corrupt started_at in sync_runs: %w", err)
		}
		r.StartedAt = st
		if finStr.Valid {
			ft, err := time.Parse(time.RFC3339, finStr.String)
			if err != nil {
				return nil, fmt.Errorf("corrupt finished_at in sync_runs: %w", err)
			}
			r.FinishedAt = &ft
		}
		runs = append(runs, r)
	}
	return runs, rows.Err()
}

// ListStatusBaselines returns all status baselines.
func (s *Store) ListStatusBaselines(ctx context.Context) ([]StatusBaseline, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT provider, source_account, book_id, target_account, work_id, edition_id,
		source_status_id, target_status_id, policy_version, source_fingerprint, target_fingerprint, last_synced_at
		FROM status_baselines ORDER BY provider, source_account, book_id, target_account`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var list []StatusBaseline
	for rows.Next() {
		var b StatusBaseline
		var syncStr string
		if err := rows.Scan(&b.Provider, &b.SourceAccount, &b.BookID, &b.TargetAccount, &b.WorkID, &b.EditionID,
			&b.SourceStatusID, &b.TargetStatusID, &b.PolicyVersion, &b.SourceFingerprint, &b.TargetFingerprint, &syncStr); err != nil {
			return nil, err
		}
		st, err := time.Parse(time.RFC3339, syncStr)
		if err != nil {
			return nil, fmt.Errorf("corrupt last_synced_at in status_baselines: %w", err)
		}
		b.LastSyncedAt = st
		list = append(list, b)
	}
	return list, rows.Err()
}
