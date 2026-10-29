package companion

import (
	"errors"
	"fmt"
	"strings"
)

const (
	HardcoverStatusWantToRead       = 1
	HardcoverStatusCurrentlyReading = 2
	HardcoverStatusRead             = 3
	HardcoverStatusDidNotFinish     = 4
	HardcoverStatusOnHold           = 5
)

// ValidHardcoverStatus returns true if status is one of the recognized Hardcover status IDs (1..5).
func ValidHardcoverStatus(status int) bool {
	return status >= 1 && status <= 5
}

// ParseHardcoverStatus parses a string status label or numeric ID into a valid Hardcover status ID (1..5).
func ParseHardcoverStatus(raw string) (int, error) {
	raw = strings.TrimSpace(strings.ToLower(raw))
	switch raw {
	case "1", "want_to_read", "want-to-read", "want to read", "wanttoread", "to-read", "toread":
		return HardcoverStatusWantToRead, nil
	case "2", "currently_reading", "currently-reading", "currently reading", "reading":
		return HardcoverStatusCurrentlyReading, nil
	case "3", "read", "finished":
		return HardcoverStatusRead, nil
	case "4", "did_not_finish", "did-not-finish", "dnf":
		return HardcoverStatusDidNotFinish, nil
	case "5", "on_hold", "on-hold", "hold", "paused":
		return HardcoverStatusOnHold, nil
	default:
		return 0, fmt.Errorf("%w: %q (expected want-to-read, reading, read, dnf, paused, or 1-5)", ErrInvalidStatus, raw)
	}
}

var (
	ErrInvalidStatus           = errors.New("invalid Hardcover reading status")
	ErrUnapprovedMutationField = errors.New("mutation variables contain unapproved non-status fields")
	ErrUncertainStatusMutation = errors.New("uncertain status mutation outcome: operation timed out or connection dropped; hold for reconciliation")
)

// UserBookReadEntry represents a historical reading attempt entry on Hardcover.
type UserBookReadEntry struct {
	ID           int64   `json:"id"`
	StartedAt    *string `json:"started_at,omitempty"`
	FinishedAt   *string `json:"finished_at,omitempty"`
	StartedDate  *string `json:"started_date,omitempty"`
	FinishedDate *string `json:"finished_date,omitempty"`
}

// UserBookStatus represents the current reading state of a work on Hardcover.
type UserBookStatus struct {
	ID               int64               `json:"id"`
	BookID           int64               `json:"book_id"`
	EditionID        *int64              `json:"edition_id,omitempty"`
	StatusID         int                 `json:"status_id"`
	Rating           *float64            `json:"rating,omitempty"`
	Review           *string             `json:"review,omitempty"`
	PrivacySettingID *int                `json:"privacy_setting_id,omitempty"`
	UserBookReads    []UserBookReadEntry `json:"user_book_reads,omitempty"`
}

// StatusMutationRequest specifies an approved status change for a confirmed work.
type StatusMutationRequest struct {
	WorkID           int64
	EditionID        int64 // optional; 0 means work-only
	StatusID         int
	PrivacySettingID int     // optional (e.g. 1=public, 3=private); 0 means unstated
	LastReadDate     *string // optional completion date string in "YYYY-MM-DD" format
}

// StatusMutationOutcome describes the result of an applied or skipped status mutation.
type StatusMutationOutcome struct {
	Skipped        bool  `json:"skipped"` // true if already matching (no mutation sent)
	PreviousStatus int   `json:"previous_status"`
	CurrentStatus  int   `json:"current_status"`
	UserBookID     int64 `json:"user_book_id"`
}
