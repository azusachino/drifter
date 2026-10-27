package companion

import (
	"context"
	"encoding/json"
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

var (
	ErrInvalidStatus           = errors.New("invalid Hardcover reading status")
	ErrUnapprovedMutationField = errors.New("mutation variables contain unapproved non-status fields")
	ErrUncertainStatusMutation = errors.New("uncertain status mutation outcome: operation timed out or connection dropped; hold for reconciliation")
)

// UserBookReadEntry represents a historical reading attempt entry on Hardcover.
type UserBookReadEntry struct {
	ID           int64   `json:"id"`
	StartedDate  *string `json:"started_date,omitempty"`
	FinishedDate *string `json:"finished_date,omitempty"`
}

// UserBookStatus represents the current reading state of a work on Hardcover.
type UserBookStatus struct {
	ID               int64               `json:"id"`
	BookID           int64               `json:"book_id"`
	EditionID        *int64              `json:"edition_id,omitempty"`
	StatusID         int                 `json:"status_id"`
	Rating           *int                `json:"rating,omitempty"`
	Review           string              `json:"review,omitempty"`
	PrivacySettingID *int                `json:"privacy_setting_id,omitempty"`
	UserBookReads    []UserBookReadEntry `json:"user_book_reads,omitempty"`
}

// StatusMutationRequest specifies an approved status change for a confirmed work.
type StatusMutationRequest struct {
	WorkID           int64
	EditionID        int64 // optional; 0 means work-only
	StatusID         int
	PrivacySettingID int // optional (e.g. 1=public, 3=private); 0 means unstated
}

// StatusMutationOutcome describes the result of an applied or skipped status mutation.
type StatusMutationOutcome struct {
	Skipped        bool  `json:"skipped"` // true if already matching (no mutation sent)
	PreviousStatus int   `json:"previous_status"`
	CurrentStatus  int   `json:"current_status"`
	UserBookID     int64 `json:"user_book_id"`
}

var approvedMutationFields = map[string]bool{
	"id":                 true,
	"book_id":            true,
	"edition_id":         true,
	"status_id":          true,
	"privacy_setting_id": true,
	"input":              true,
}

// validateMutationFields ensures only approved status and identity fields are sent in GraphQL variables.
func validateMutationFields(vars map[string]any) error {
	for k, v := range vars {
		if !approvedMutationFields[k] {
			return fmt.Errorf("%w: field %q is forbidden in status mutations", ErrUnapprovedMutationField, k)
		}
		if nested, ok := v.(map[string]any); ok {
			if err := validateMutationFields(nested); err != nil {
				return err
			}
		}
	}
	return nil
}

func isTimeoutOrGatewayError(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return true
	}
	errStr := strings.ToLower(err.Error())
	return strings.Contains(errStr, "timeout") || strings.Contains(errStr, "504") || strings.Contains(errStr, "gateway")
}

// mutate executes a GraphQL mutation, enforcing that only approved status fields are sent.
func (h *Hardcover) mutate(ctx context.Context, mutation string, variables map[string]any, target any) error {
	trimmed := strings.TrimSpace(mutation)
	if !strings.HasPrefix(trimmed, "mutation ") {
		return fmt.Errorf("only Hardcover mutations are supported")
	}
	if err := validateMutationFields(variables); err != nil {
		return err
	}
	var response struct {
		Data   json.RawMessage   `json:"data"`
		Errors []json.RawMessage `json:"errors"`
	}
	if err := h.api.post(ctx, map[string]any{"query": mutation, "variables": variables}, &response); err != nil {
		if isTimeoutOrGatewayError(err) {
			return fmt.Errorf("%w: %v", ErrUncertainStatusMutation, err)
		}
		return err
	}
	if len(response.Errors) > 0 || len(response.Data) == 0 || string(response.Data) == "null" {
		return fmt.Errorf("Hardcover mutation rejected or data missing")
	}
	if err := json.Unmarshal(response.Data, target); err != nil {
		return fmt.Errorf("Hardcover mutation response has invalid fields")
	}
	return nil
}

const userBookStatusFields = `id book_id edition_id status_id rating review privacy_setting_id user_book_reads(order_by: {id: asc}) { id started_date finished_date }`

// GetUserBookStatus retrieves the current user_book record for a work on Hardcover.
// Returns nil, nil if no user_book record exists for this work.
func (h *Hardcover) GetUserBookStatus(ctx context.Context, workID int64) (*UserBookStatus, error) {
	if workID <= 0 {
		return nil, fmt.Errorf("invalid work ID")
	}
	var response struct {
		UserBooks []UserBookStatus `json:"user_books"`
	}
	q := `query UserBook($work: Int!) { user_books(where: {book_id: {_eq: $work}}, limit: 1) { ` + userBookStatusFields + ` } }`
	if err := h.query(ctx, q, map[string]any{"work": workID}, &response); err != nil {
		return nil, err
	}
	if len(response.UserBooks) == 0 {
		return nil, nil
	}
	return &response.UserBooks[0], nil
}

// ApplyStatusMutation executes a status-only mutation against Hardcover with strict field preservation.
// If the record already matches the requested status, zero GraphQL mutations are executed.
func (h *Hardcover) ApplyStatusMutation(ctx context.Context, req StatusMutationRequest) (StatusMutationOutcome, error) {
	if req.WorkID <= 0 {
		return StatusMutationOutcome{}, fmt.Errorf("positive work ID required")
	}
	if !ValidHardcoverStatus(req.StatusID) {
		return StatusMutationOutcome{}, ErrInvalidStatus
	}

	existing, err := h.GetUserBookStatus(ctx, req.WorkID)
	if err != nil {
		return StatusMutationOutcome{}, err
	}

	// 1. Idempotency check: if status already matches, perform no mutation.
	if existing != nil && existing.StatusID == req.StatusID {
		return StatusMutationOutcome{
			Skipped:        true,
			PreviousStatus: existing.StatusID,
			CurrentStatus:  existing.StatusID,
			UserBookID:     existing.ID,
		}, nil
	}

	// 2. Existing record: update status_id only.
	if existing != nil {
		var resp struct {
			Update *struct {
				ID       int64   `json:"id"`
				StatusID int     `json:"status_id"`
				Error    *string `json:"error"`
			} `json:"update_user_book"`
		}
		m := `mutation UpdateUserBook($id: Int!, $status_id: Int!) { update_user_book(id: $id, input: {status_id: $status_id}) { id status_id error } }`
		vars := map[string]any{"id": existing.ID, "status_id": req.StatusID}
		if err := h.mutate(ctx, m, vars, &resp); err != nil {
			return StatusMutationOutcome{}, err
		}
		if resp.Update == nil || (resp.Update.Error != nil && *resp.Update.Error != "") {
			return StatusMutationOutcome{}, fmt.Errorf("Hardcover update_user_book failed")
		}
		return StatusMutationOutcome{
			Skipped:        false,
			PreviousStatus: existing.StatusID,
			CurrentStatus:  resp.Update.StatusID,
			UserBookID:     resp.Update.ID,
		}, nil
	}

	// 3. New record: insert user_book with approved fields only.
	input := map[string]any{
		"book_id":   req.WorkID,
		"status_id": req.StatusID,
	}
	if req.EditionID > 0 {
		input["edition_id"] = req.EditionID
	}
	if req.PrivacySettingID > 0 {
		input["privacy_setting_id"] = req.PrivacySettingID
	}

	var resp struct {
		Insert *struct {
			ID       int64   `json:"id"`
			StatusID int     `json:"status_id"`
			Error    *string `json:"error"`
		} `json:"insert_user_book"`
	}
	m := `mutation InsertUserBook($input: UsersBooksInput!) { insert_user_book(input: $input) { id status_id error } }`
	vars := map[string]any{"input": input}
	if err := h.mutate(ctx, m, vars, &resp); err != nil {
		return StatusMutationOutcome{}, err
	}
	if resp.Insert == nil || (resp.Insert.Error != nil && *resp.Insert.Error != "") {
		return StatusMutationOutcome{}, fmt.Errorf("Hardcover insert_user_book failed")
	}
	return StatusMutationOutcome{
		Skipped:        false,
		PreviousStatus: 0,
		CurrentStatus:  resp.Insert.StatusID,
		UserBookID:     resp.Insert.ID,
	}, nil
}
