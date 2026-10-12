package companion

import (
	"context"
	"database/sql"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"unicode"

	_ "github.com/mattn/go-sqlite3"
)

type Store struct{ db *sql.DB }
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
	if path == "" {
		return nil, fmt.Errorf("database path required")
	}
	path, err := filepath.Abs(path)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	parent, err := os.Stat(filepath.Dir(path))
	if err != nil || parent.Mode().Perm()&0o022 != 0 {
		return nil, fmt.Errorf("database parent must not be writable by other users")
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_RDWR, 0o600)
	if err == nil {
		file.Close()
	} else if !os.IsExist(err) {
		return nil, err
	}
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm() != 0o600 {
		return nil, fmt.Errorf("database must be a regular, non-symlink mode-600 file")
	}
	u := url.URL{Scheme: "file", Path: path, RawQuery: "_busy_timeout=5000&_foreign_keys=on&_journal_mode=DELETE&_secure_delete=on"}
	db, err := sql.Open("sqlite3", u.String())
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	_, err = db.Exec(`CREATE TABLE IF NOT EXISTS mappings (
		provider TEXT NOT NULL, source_account TEXT NOT NULL, book_id TEXT NOT NULL,
		target_account TEXT NOT NULL, work_id INTEGER NOT NULL CHECK(work_id > 0),
		edition_id INTEGER NOT NULL CHECK(edition_id >= 0), source_title TEXT NOT NULL, source_isbn TEXT NOT NULL,
		PRIMARY KEY(provider, source_account, book_id, target_account))`)
	if err != nil {
		db.Close()
		return nil, fmt.Errorf("cannot initialize mapping database: %w", err)
	}
	return &Store{db}, nil
}

func (s *Store) Close() error { return s.db.Close() }

func (s *Store) Confirm(ctx context.Context, m Mapping, replace bool) error {
	if !m.valid() {
		return fmt.Errorf("invalid mapping identity or account labels")
	}
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
		return fmt.Errorf("mapping already exists; use explicit replacement or unlink it")
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
		return fmt.Errorf("invalid mapping key")
	}
	_, err := s.db.ExecContext(ctx, `DELETE FROM mappings WHERE provider=? AND source_account=? AND book_id=? AND target_account=?`, provider, sourceAccount, bookID, targetAccount)
	return err
}
