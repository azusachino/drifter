package companion

import (
	"context"
	"fmt"
)

// Decision records a local review choice in its exact account namespace.
type Decision struct {
	Provider      string `json:"provider"`
	SourceAccount string `json:"source_account"`
	BookID        string `json:"book_id"`
	TargetAccount string `json:"target_account"`
	Kind          string `json:"kind"`
}

func (d Decision) valid() bool {
	return ValidLabel(d.Provider) && ValidLabel(d.SourceAccount) && ValidLabel(d.TargetAccount) && ValidBookID(d.BookID) &&
		(d.Kind == "skip" || d.Kind == "exclude" || d.Kind == "not-in-catalog")
}

// SaveDecision stores the owner's current choice for one source/target namespace.
func (s *Store) SaveDecision(ctx context.Context, d Decision) error {
	if !d.valid() {
		return fmt.Errorf("invalid decision")
	}
	release, err := s.writeGuard()
	if err != nil {
		return err
	}
	defer release()
	_, err = s.db.ExecContext(ctx, `INSERT INTO decisions VALUES (?,?,?,?,?) ON CONFLICT(provider,source_account,book_id,target_account) DO UPDATE SET kind=excluded.kind`, d.Provider, d.SourceAccount, d.BookID, d.TargetAccount, d.Kind)
	return err
}

// ResetDecision removes a choice; missing choices are idempotent.
func (s *Store) ResetDecision(ctx context.Context, provider, sourceAccount, bookID, targetAccount string) error {
	if !ValidLabel(provider) || !ValidLabel(sourceAccount) || !ValidLabel(targetAccount) || !ValidBookID(bookID) {
		return ErrInvalidMappingKey
	}
	release, err := s.writeGuard()
	if err != nil {
		return err
	}
	defer release()
	_, err = s.db.ExecContext(ctx, `DELETE FROM decisions WHERE provider=? AND source_account=? AND book_id=? AND target_account=?`, provider, sourceAccount, bookID, targetAccount)
	return err
}

// ListDecisions returns only choices in the requested source/target namespace.
func (s *Store) ListDecisions(ctx context.Context, provider, sourceAccount, targetAccount string) ([]Decision, error) {
	if !ValidLabel(provider) || !ValidLabel(sourceAccount) || !ValidLabel(targetAccount) {
		return nil, ErrInvalidMappingKey
	}
	rows, err := s.db.QueryContext(ctx, `SELECT provider,source_account,book_id,target_account,kind FROM decisions WHERE provider=? AND source_account=? AND target_account=? ORDER BY book_id`, provider, sourceAccount, targetAccount)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []Decision{}
	for rows.Next() {
		var d Decision
		if err := rows.Scan(&d.Provider, &d.SourceAccount, &d.BookID, &d.TargetAccount, &d.Kind); err != nil {
			return nil, err
		}
		result = append(result, d)
	}
	return result, rows.Err()
}
