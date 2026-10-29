package hardcover

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/azusachino/drifter/internal/companion"
)

var approvedMutationFields = map[string]bool{
	"id":                 true,
	"book_id":            true,
	"edition_id":         true,
	"status_id":          true,
	"privacy_setting_id": true,
	"last_read_date":     true,
	"finished_at":        true,
	"started_at":         true,
	"user_book_id":       true,
	"user_book_read":     true,
	"input":              true,
	"object":             true,
}

// validateMutationFields ensures only approved status and identity fields are sent in GraphQL variables.
func validateMutationFields(vars map[string]any) error {
	for k, v := range vars {
		if !approvedMutationFields[k] {
			return fmt.Errorf("%w: field %q is forbidden in status mutations", companion.ErrUnapprovedMutationField, k)
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
func (c *Client) mutate(ctx context.Context, mutation string, variables map[string]any, target any) error {
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
	if err := c.api.Post(ctx, map[string]any{"query": mutation, "variables": variables}, &response); err != nil {
		if isTimeoutOrGatewayError(err) {
			return fmt.Errorf("%w: %v", companion.ErrUncertainStatusMutation, err)
		}
		return err
	}
	if len(response.Errors) > 0 || len(response.Data) == 0 || string(response.Data) == "null" {
		return fmt.Errorf("Hardcover mutation rejected or data missing")
	}
	if err := json.Unmarshal(response.Data, target); err != nil {
		return fmt.Errorf("Hardcover mutation response has invalid fields: %w", err)
	}
	return nil
}

const userBookStatusFields = `id book_id edition_id status_id rating review privacy_setting_id user_book_reads(order_by: {id: asc}) { id started_at finished_at }`

// GetUserBookStatus retrieves the current user_book record for a work on Hardcover for the authenticated user.
// Returns nil, nil if no user_book record exists for this work.
func (c *Client) GetUserBookStatus(ctx context.Context, workID int64) (*companion.UserBookStatus, error) {
	if workID <= 0 {
		return nil, fmt.Errorf("invalid work ID")
	}
	userID, err := c.GetUserID(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to resolve authenticated user for status lookup: %w", err)
	}
	var response struct {
		UserBooks []companion.UserBookStatus `json:"user_books"`
	}
	q := `query UserBook($work: Int!, $user_id: Int!) { user_books(where: {book_id: {_eq: $work}, user_id: {_eq: $user_id}}, limit: 1) { ` + userBookStatusFields + ` } }`
	if err := c.query(ctx, q, map[string]any{"work": workID, "user_id": userID}, &response); err != nil {
		return nil, err
	}
	if len(response.UserBooks) == 0 {
		return nil, nil
	}
	return &response.UserBooks[0], nil
}

// ApplyStatusMutation executes a status-only mutation against Hardcover with strict field preservation.
// If the record already matches the requested status, zero GraphQL mutations are executed.
func (c *Client) ApplyStatusMutation(ctx context.Context, req companion.StatusMutationRequest) (companion.StatusMutationOutcome, error) {
	if req.WorkID <= 0 {
		return companion.StatusMutationOutcome{}, fmt.Errorf("positive work ID required")
	}
	if !companion.ValidHardcoverStatus(req.StatusID) {
		return companion.StatusMutationOutcome{}, companion.ErrInvalidStatus
	}

	existing, err := c.GetUserBookStatus(ctx, req.WorkID)
	if err != nil {
		return companion.StatusMutationOutcome{}, err
	}

	// 1. Idempotency check: if status already matches, check if read date needs updating before returning.
	if existing != nil && existing.StatusID == req.StatusID {
		if req.StatusID == companion.HardcoverStatusRead && req.LastReadDate != nil && *req.LastReadDate != "" {
			if err := c.ensureReadDate(ctx, existing.ID, existing.UserBookReads, *req.LastReadDate); err != nil {
				return companion.StatusMutationOutcome{}, err
			}
		}
		return companion.StatusMutationOutcome{
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
				UserBook *struct {
					ID            int64                         `json:"id"`
					StatusID      int                           `json:"status_id"`
					UserBookReads []companion.UserBookReadEntry `json:"user_book_reads"`
				} `json:"user_book"`
			} `json:"update_user_book"`
		}
		m := `mutation UpdateUserBook($id: Int!, $object: UserBookUpdateInput!) { update_user_book(id: $id, object: $object) { id error user_book { id status_id user_book_reads(order_by: {id: asc}) { id started_at finished_at } } } }`
		object := map[string]any{"status_id": req.StatusID}
		if req.LastReadDate != nil && *req.LastReadDate != "" {
			object["last_read_date"] = *req.LastReadDate
		}
		vars := map[string]any{"id": existing.ID, "object": object}
		if err := c.mutate(ctx, m, vars, &resp); err != nil {
			return companion.StatusMutationOutcome{}, err
		}
		if resp.Update == nil || (resp.Update.Error != nil && *resp.Update.Error != "") {
			if resp.Update != nil && resp.Update.Error != nil {
				return companion.StatusMutationOutcome{}, fmt.Errorf("Hardcover update_user_book failed: %s", *resp.Update.Error)
			}
			return companion.StatusMutationOutcome{}, fmt.Errorf("Hardcover update_user_book failed")
		}
		updatedStatus := resp.Update.StatusID
		updatedID := resp.Update.ID
		var reads []companion.UserBookReadEntry
		if resp.Update.UserBook != nil {
			if resp.Update.UserBook.StatusID > 0 {
				updatedStatus = resp.Update.UserBook.StatusID
			}
			if resp.Update.UserBook.ID > 0 {
				updatedID = resp.Update.UserBook.ID
			}
			reads = resp.Update.UserBook.UserBookReads
		}
		if req.StatusID == companion.HardcoverStatusRead && req.LastReadDate != nil && *req.LastReadDate != "" {
			if err := c.ensureReadDate(ctx, updatedID, reads, *req.LastReadDate); err != nil {
				return companion.StatusMutationOutcome{}, err
			}
		}
		return companion.StatusMutationOutcome{
			Skipped:        false,
			PreviousStatus: existing.StatusID,
			CurrentStatus:  updatedStatus,
			UserBookID:     updatedID,
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
	if req.LastReadDate != nil && *req.LastReadDate != "" {
		input["last_read_date"] = *req.LastReadDate
	}

	var resp struct {
		Insert *struct {
			ID       int64   `json:"id"`
			StatusID int     `json:"status_id"`
			Error    *string `json:"error"`
			UserBook *struct {
				ID            int64                         `json:"id"`
				StatusID      int                           `json:"status_id"`
				UserBookReads []companion.UserBookReadEntry `json:"user_book_reads"`
			} `json:"user_book"`
		} `json:"insert_user_book"`
	}
	m := `mutation InsertUserBook($object: UserBookCreateInput!) { insert_user_book(object: $object) { id error user_book { id status_id user_book_reads(order_by: {id: asc}) { id started_at finished_at } } } }`
	vars := map[string]any{"object": input}
	if err := c.mutate(ctx, m, vars, &resp); err != nil {
		return companion.StatusMutationOutcome{}, err
	}
	if resp.Insert == nil || (resp.Insert.Error != nil && *resp.Insert.Error != "") {
		if resp.Insert != nil && resp.Insert.Error != nil {
			return companion.StatusMutationOutcome{}, fmt.Errorf("Hardcover insert_user_book failed: %s", *resp.Insert.Error)
		}
		return companion.StatusMutationOutcome{}, fmt.Errorf("Hardcover insert_user_book failed")
	}
	insertedStatus := resp.Insert.StatusID
	insertedID := resp.Insert.ID
	var reads []companion.UserBookReadEntry
	if resp.Insert.UserBook != nil {
		if resp.Insert.UserBook.StatusID > 0 {
			insertedStatus = resp.Insert.UserBook.StatusID
		}
		if resp.Insert.UserBook.ID > 0 {
			insertedID = resp.Insert.UserBook.ID
		}
		reads = resp.Insert.UserBook.UserBookReads
	}
	if req.StatusID == companion.HardcoverStatusRead && req.LastReadDate != nil && *req.LastReadDate != "" {
		if err := c.ensureReadDate(ctx, insertedID, reads, *req.LastReadDate); err != nil {
			return companion.StatusMutationOutcome{}, err
		}
	}
	return companion.StatusMutationOutcome{
		Skipped:        false,
		PreviousStatus: 0,
		CurrentStatus:  insertedStatus,
		UserBookID:     insertedID,
	}, nil
}

func (c *Client) ensureReadDate(ctx context.Context, userBookID int64, reads []companion.UserBookReadEntry, targetDate string) error {
	if targetDate == "" {
		return nil
	}
	if len(reads) > 0 {
		latest := reads[len(reads)-1]
		if latest.FinishedAt != nil && *latest.FinishedAt == targetDate {
			return nil
		}
		m := `mutation UpdateRead($id: Int!, $object: DatesReadInput!) { update_user_book_read(id: $id, object: $object) { id error user_book_read { id finished_at } } }`
		vars := map[string]any{
			"id": latest.ID,
			"object": map[string]any{
				"finished_at": targetDate,
			},
		}
		var resp struct {
			Update *struct {
				ID    int64   `json:"id"`
				Error *string `json:"error"`
			} `json:"update_user_book_read"`
		}
		if err := c.mutate(ctx, m, vars, &resp); err != nil {
			return err
		}
		if resp.Update == nil || (resp.Update.Error != nil && *resp.Update.Error != "") {
			if resp.Update != nil && resp.Update.Error != nil {
				return fmt.Errorf("Hardcover update_user_book_read failed: %s", *resp.Update.Error)
			}
			return fmt.Errorf("Hardcover update_user_book_read failed")
		}
		return nil
	}

	m := `mutation InsertRead($user_book_id: Int!, $user_book_read: DatesReadInput!) { insert_user_book_read(user_book_id: $user_book_id, user_book_read: $user_book_read) { id error } }`
	vars := map[string]any{
		"user_book_id": userBookID,
		"user_book_read": map[string]any{
			"finished_at": targetDate,
		},
	}
	var resp struct {
		Insert *struct {
			ID    int64   `json:"id"`
			Error *string `json:"error"`
		} `json:"insert_user_book_read"`
	}
	if err := c.mutate(ctx, m, vars, &resp); err != nil {
		return err
	}
	if resp.Insert == nil || (resp.Insert.Error != nil && *resp.Insert.Error != "") {
		if resp.Insert != nil && resp.Insert.Error != nil {
			return fmt.Errorf("Hardcover insert_user_book_read failed: %s", *resp.Insert.Error)
		}
		return fmt.Errorf("Hardcover insert_user_book_read failed")
	}
	return nil
}
