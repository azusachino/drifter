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

const storeVersion = 4

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
