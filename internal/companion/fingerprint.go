package companion

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode"
)

var (
	ErrAccountFingerprintMismatch = errors.New("account fingerprint mismatch: credentials belong to a different account than recorded for this namespace")
	ErrInvalidAccountFingerprint  = errors.New("invalid account fingerprint")
)

// AccountFingerprint represents a verified binding between an account namespace label and a stable provider identity.
type AccountFingerprint struct {
	Provider       string    `json:"provider"`
	Account        string    `json:"account"`
	Fingerprint    string    `json:"fingerprint"`
	CreatedAt      time.Time `json:"created_at"`
	LastVerifiedAt time.Time `json:"last_verified_at"`
}

// ValidFingerprint checks whether a fingerprint string is non-empty, trimmed, within bounds, and contains no control characters.
func ValidFingerprint(fp string) bool {
	if strings.TrimSpace(fp) != fp || fp == "" || len(fp) > 128 {
		return false
	}
	for _, r := range fp {
		if unicode.IsControl(r) {
			return false
		}
	}
	return true
}

func (af AccountFingerprint) valid() bool {
	return ValidLabel(af.Provider) && ValidLabel(af.Account) && ValidFingerprint(af.Fingerprint) &&
		!af.CreatedAt.IsZero() && !af.LastVerifiedAt.IsZero()
}

// VerifyOrBindAccountFingerprint verifies that currentFingerprint matches the recorded fingerprint
// for the given provider and account namespace. If no fingerprint is recorded yet, it binds currentFingerprint.
// If a fingerprint is already recorded and matches, it updates last_verified_at.
// If a fingerprint is already recorded and does not match, it returns ErrAccountFingerprintMismatch.
func (s *Store) VerifyOrBindAccountFingerprint(ctx context.Context, provider, account, currentFingerprint string, now time.Time) (*AccountFingerprint, error) {
	if !ValidLabel(provider) || !ValidLabel(account) || !ValidFingerprint(currentFingerprint) {
		return nil, ErrInvalidAccountFingerprint
	}
	if now.IsZero() {
		now = time.Now()
	}
	nowUTC := now.UTC()
	nowStr := nowUTC.Format(time.RFC3339)

	release, err := s.writeGuard()
	if err != nil {
		return nil, err
	}
	defer release()

	var existing AccountFingerprint
	var createdAtStr, lastVerifiedStr string
	err = s.db.QueryRowContext(ctx, `SELECT provider, account, fingerprint, created_at, last_verified_at
		FROM account_fingerprints WHERE provider = ? AND account = ?`, provider, account).
		Scan(&existing.Provider, &existing.Account, &existing.Fingerprint, &createdAtStr, &lastVerifiedStr)

	if errors.Is(err, sql.ErrNoRows) {
		_, err := s.db.ExecContext(ctx, `INSERT INTO account_fingerprints (provider, account, fingerprint, created_at, last_verified_at)
			VALUES (?, ?, ?, ?, ?)`, provider, account, currentFingerprint, nowStr, nowStr)
		if err != nil {
			return nil, err
		}
		return &AccountFingerprint{
			Provider:       provider,
			Account:        account,
			Fingerprint:    currentFingerprint,
			CreatedAt:      nowUTC,
			LastVerifiedAt: nowUTC,
		}, nil
	}
	if err != nil {
		return nil, err
	}

	if existing.Fingerprint != currentFingerprint {
		return nil, fmt.Errorf("%w: recorded %q, got %q for %s:%s",
			ErrAccountFingerprintMismatch, existing.Fingerprint, currentFingerprint, provider, account)
	}

	_, err = s.db.ExecContext(ctx, `UPDATE account_fingerprints SET last_verified_at = ? WHERE provider = ? AND account = ?`,
		nowStr, provider, account)
	if err != nil {
		return nil, err
	}

	createdTime, err := time.Parse(time.RFC3339, createdAtStr)
	if err != nil {
		createdTime = nowUTC
	}
	existing.CreatedAt = createdTime
	existing.LastVerifiedAt = nowUTC
	return &existing, nil
}

// GetAccountFingerprint retrieves a recorded account fingerprint, returning nil, nil if none is bound yet.
func (s *Store) GetAccountFingerprint(ctx context.Context, provider, account string) (*AccountFingerprint, error) {
	if !ValidLabel(provider) || !ValidLabel(account) {
		return nil, ErrInvalidMappingKey
	}
	var fp AccountFingerprint
	var createdAtStr, lastVerifiedStr string
	err := s.db.QueryRowContext(ctx, `SELECT provider, account, fingerprint, created_at, last_verified_at
		FROM account_fingerprints WHERE provider = ? AND account = ?`, provider, account).
		Scan(&fp.Provider, &fp.Account, &fp.Fingerprint, &createdAtStr, &lastVerifiedStr)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	createdTime, err := time.Parse(time.RFC3339, createdAtStr)
	if err != nil {
		return nil, fmt.Errorf("corrupt created_at timestamp in account_fingerprints: %w", err)
	}
	lastVerifiedTime, err := time.Parse(time.RFC3339, lastVerifiedStr)
	if err != nil {
		return nil, fmt.Errorf("corrupt last_verified_at timestamp in account_fingerprints: %w", err)
	}
	fp.CreatedAt = createdTime
	fp.LastVerifiedAt = lastVerifiedTime
	return &fp, nil
}

// ListAccountFingerprints returns all recorded account fingerprints ordered by provider and account.
func (s *Store) ListAccountFingerprints(ctx context.Context) ([]AccountFingerprint, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT provider, account, fingerprint, created_at, last_verified_at
		FROM account_fingerprints ORDER BY provider, account`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var result []AccountFingerprint
	for rows.Next() {
		var fp AccountFingerprint
		var createdAtStr, lastVerifiedStr string
		if err := rows.Scan(&fp.Provider, &fp.Account, &fp.Fingerprint, &createdAtStr, &lastVerifiedStr); err != nil {
			return nil, err
		}
		createdTime, err := time.Parse(time.RFC3339, createdAtStr)
		if err != nil {
			return nil, fmt.Errorf("corrupt created_at timestamp in account_fingerprints: %w", err)
		}
		lastVerifiedTime, err := time.Parse(time.RFC3339, lastVerifiedStr)
		if err != nil {
			return nil, fmt.Errorf("corrupt last_verified_at timestamp in account_fingerprints: %w", err)
		}
		fp.CreatedAt = createdTime
		fp.LastVerifiedAt = lastVerifiedTime
		result = append(result, fp)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if result == nil {
		result = []AccountFingerprint{}
	}
	return result, nil
}
