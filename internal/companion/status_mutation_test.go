package companion

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
)

func TestHardcoverStatusMutationTransitions(t *testing.T) {
	var callLog []string
	var lastMutationVars map[string]any

	// Synthetic Hardcover API double maintaining state
	state := struct {
		userBook *UserBookStatus
	}{}

	h := testHardcover(t, func(rw http.ResponseWriter, r *http.Request) {
		var body struct {
			Query     string         `json:"query"`
			Variables map[string]any `json:"variables"`
		}
		json.NewDecoder(r.Body).Decode(&body)

		trimmed := strings.TrimSpace(body.Query)
		switch {
		case strings.HasPrefix(trimmed, "query UserBook"):
			callLog = append(callLog, "query_user_book")
			if state.userBook == nil {
				io.WriteString(rw, `{"data":{"user_books":[]}}`)
			} else {
				data, _ := json.Marshal(map[string]any{
					"data": map[string]any{
						"user_books": []any{state.userBook},
					},
				})
				rw.Write(data)
			}

		case strings.HasPrefix(trimmed, "mutation InsertUserBook"):
			callLog = append(callLog, "insert_user_book")
			lastMutationVars = body.Variables
			obj, ok := body.Variables["object"].(map[string]any)
			if !ok {
				obj = body.Variables["input"].(map[string]any)
			}
			statusID := int(obj["status_id"].(float64))
			bookID := int64(obj["book_id"].(float64))
			ratingVal := 5
			state.userBook = &UserBookStatus{
				ID:       101,
				BookID:   bookID,
				StatusID: statusID,
				Rating:   &ratingVal,
				Review:   "user notes must stay untouched",
			}
			io.WriteString(rw, `{"data":{"insert_user_book":{"id":101,"status_id":1,"error":null,"user_book":{"id":101,"status_id":1}}}}`)

		case strings.HasPrefix(trimmed, "mutation UpdateUserBook"):
			callLog = append(callLog, "update_user_book")
			lastMutationVars = body.Variables
			var statusID int
			if obj, ok := body.Variables["object"].(map[string]any); ok {
				statusID = int(obj["status_id"].(float64))
			} else {
				statusID = int(body.Variables["status_id"].(float64))
			}
			state.userBook.StatusID = statusID
			resp, _ := json.Marshal(map[string]any{
				"data": map[string]any{
					"update_user_book": map[string]any{
						"id":        state.userBook.ID,
						"status_id": statusID,
						"error":     nil,
						"user_book": map[string]any{
							"id":        state.userBook.ID,
							"status_id": statusID,
						},
					},
				},
			})
			rw.Write(resp)

		default:
			t.Errorf("unexpected query/mutation: %s", body.Query)
		}
	})

	ctx := context.Background()

	// 1. First sync: insert Want to Read (1)
	res1, err := h.ApplyStatusMutation(ctx, StatusMutationRequest{WorkID: 42, StatusID: HardcoverStatusWantToRead})
	if err != nil {
		t.Fatalf("insert failed: %v", err)
	}
	if res1.Skipped || res1.CurrentStatus != HardcoverStatusWantToRead || res1.UserBookID != 101 {
		t.Fatalf("unexpected res1: %+v", res1)
	}
	var insertBookID float64
	if obj, ok := lastMutationVars["object"].(map[string]any); ok {
		insertBookID = obj["book_id"].(float64)
	} else if input, ok := lastMutationVars["input"].(map[string]any); ok {
		insertBookID = input["book_id"].(float64)
	}
	if insertBookID != float64(42) {
		t.Fatalf("unexpected insert variables: %+v", lastMutationVars)
	}

	// 2. Transition: Want to Read (1) -> Currently Reading (2)
	res2, err := h.ApplyStatusMutation(ctx, StatusMutationRequest{WorkID: 42, StatusID: HardcoverStatusCurrentlyReading})
	if err != nil {
		t.Fatalf("update to reading failed: %v", err)
	}
	if res2.Skipped || res2.PreviousStatus != 1 || res2.CurrentStatus != 2 {
		t.Fatalf("unexpected res2: %+v", res2)
	}
	var updateStatusID float64
	if obj, ok := lastMutationVars["object"].(map[string]any); ok {
		updateStatusID = obj["status_id"].(float64)
	} else if sid, ok := lastMutationVars["status_id"].(float64); ok {
		updateStatusID = sid
	}
	if lastMutationVars["id"] != float64(101) || updateStatusID != float64(2) {
		t.Fatalf("unexpected update variables: %+v", lastMutationVars)
	}

	// 3. Transition: Currently Reading (2) -> Read (3)
	// Simulate user having read history logs and rating
	started := "2026-01-01"
	finished := "2026-02-01"
	state.userBook.UserBookReads = []UserBookReadEntry{
		{ID: 501, StartedAt: &started, FinishedAt: &finished, StartedDate: &started, FinishedDate: &finished},
	}
	res3, err := h.ApplyStatusMutation(ctx, StatusMutationRequest{WorkID: 42, StatusID: HardcoverStatusRead})
	if err != nil {
		t.Fatalf("update to read failed: %v", err)
	}
	if res3.Skipped || res3.PreviousStatus != 2 || res3.CurrentStatus != 3 {
		t.Fatalf("unexpected res3: %+v", res3)
	}

	// 4. Reread transition: Read (3) -> Currently Reading (2)
	res4, err := h.ApplyStatusMutation(ctx, StatusMutationRequest{WorkID: 42, StatusID: HardcoverStatusCurrentlyReading})
	if err != nil {
		t.Fatalf("reread transition failed: %v", err)
	}
	if res4.Skipped || res4.PreviousStatus != 3 || res4.CurrentStatus != 2 {
		t.Fatalf("unexpected res4: %+v", res4)
	}
	// Verify user book rating, review, and prior read logs are preserved
	if *state.userBook.Rating != 5 || state.userBook.Review != "user notes must stay untouched" {
		t.Fatalf("user metadata overwritten! rating=%v review=%q", state.userBook.Rating, state.userBook.Review)
	}
	if len(state.userBook.UserBookReads) != 1 || state.userBook.UserBookReads[0].ID != 501 {
		t.Fatalf("user_book_reads corrupted: %+v", state.userBook.UserBookReads)
	}
}

func TestHardcoverStatusMutationIdempotentNoOp(t *testing.T) {
	mutationCalls := 0
	h := testHardcover(t, func(rw http.ResponseWriter, r *http.Request) {
		var body struct {
			Query string `json:"query"`
		}
		json.NewDecoder(r.Body).Decode(&body)
		trimmed := strings.TrimSpace(body.Query)

		if strings.HasPrefix(trimmed, "query UserBook") {
			// Already at status 3 (Read)
			io.WriteString(rw, `{"data":{"user_books":[{"id":202,"book_id":99,"status_id":3}]}}`)
			return
		}
		if strings.HasPrefix(trimmed, "mutation ") {
			mutationCalls++
			io.WriteString(rw, `{"data":{"update_user_book":{"id":202,"status_id":3,"error":null}}}`)
			return
		}
		t.Errorf("unexpected query: %s", body.Query)
	})

	res, err := h.ApplyStatusMutation(context.Background(), StatusMutationRequest{WorkID: 99, StatusID: HardcoverStatusRead})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !res.Skipped {
		t.Fatalf("expected mutation to be skipped, got: %+v", res)
	}
	if res.CurrentStatus != 3 || res.PreviousStatus != 3 || res.UserBookID != 202 {
		t.Fatalf("unexpected outcome: %+v", res)
	}
	if mutationCalls != 0 {
		t.Fatalf("expected 0 GraphQL mutations for no-op, got %d", mutationCalls)
	}
}

func TestHardcoverStatusMutationRejectsUnapprovedFields(t *testing.T) {
	h := testHardcover(t, func(rw http.ResponseWriter, _ *http.Request) {
		t.Fatal("no network call should be sent when variables contain unapproved fields")
	})

	unapprovedVars := []map[string]any{
		{"rating": 5, "id": 1, "status_id": 2},
		{"review": "awesome", "id": 1, "status_id": 2},
		{"notes": "page 10", "id": 1, "status_id": 2},
		{"input": map[string]any{"rating": 4, "book_id": 1, "status_id": 2}},
	}

	for _, vars := range unapprovedVars {
		var resp any
		err := h.mutate(context.Background(), "mutation Update { update_user_book { id } }", vars, &resp)
		if err == nil || !errors.Is(err, ErrUnapprovedMutationField) {
			t.Fatalf("expected ErrUnapprovedMutationField for vars %+v, got: %v", vars, err)
		}
	}
}

func TestHardcoverStatusMutationUncertainTimeoutHold(t *testing.T) {
	h := testHardcover(t, func(rw http.ResponseWriter, r *http.Request) {
		var body struct {
			Query string `json:"query"`
		}
		json.NewDecoder(r.Body).Decode(&body)
		if strings.HasPrefix(strings.TrimSpace(body.Query), "query UserBook") {
			io.WriteString(rw, `{"data":{"user_books":[]}}`)
			return
		}
		// Return 504 Gateway Timeout during mutation
		rw.WriteHeader(http.StatusGatewayTimeout)
		io.WriteString(rw, `Gateway Timeout`)
	})

	_, err := h.ApplyStatusMutation(context.Background(), StatusMutationRequest{WorkID: 88, StatusID: HardcoverStatusCurrentlyReading})
	if err == nil || !errors.Is(err, ErrUncertainStatusMutation) {
		t.Fatalf("expected ErrUncertainStatusMutation on 504, got: %v", err)
	}
}

func TestHardcoverStatusMutationValidation(t *testing.T) {
	h := testHardcover(t, func(rw http.ResponseWriter, _ *http.Request) {
		t.Fatal("no network call should be made on validation failure")
	})

	ctx := context.Background()

	// Invalid work ID
	if _, err := h.ApplyStatusMutation(ctx, StatusMutationRequest{WorkID: 0, StatusID: 1}); err == nil {
		t.Error("accepted zero work ID")
	}
	if _, err := h.ApplyStatusMutation(ctx, StatusMutationRequest{WorkID: -5, StatusID: 1}); err == nil {
		t.Error("accepted negative work ID")
	}

	// Invalid status ID
	for _, badStatus := range []int{0, -1, 6, 99} {
		if _, err := h.ApplyStatusMutation(ctx, StatusMutationRequest{WorkID: 10, StatusID: badStatus}); err == nil || !errors.Is(err, ErrInvalidStatus) {
			t.Errorf("expected ErrInvalidStatus for status %d, got: %v", badStatus, err)
		}
	}
}

func TestParseHardcoverStatus(t *testing.T) {
	tests := []struct {
		input    string
		expected int
	}{
		{"1", HardcoverStatusWantToRead},
		{"want_to_read", HardcoverStatusWantToRead},
		{"want-to-read", HardcoverStatusWantToRead},
		{"want to read", HardcoverStatusWantToRead},
		{"to-read", HardcoverStatusWantToRead},
		{"2", HardcoverStatusCurrentlyReading},
		{"reading", HardcoverStatusCurrentlyReading},
		{"currently_reading", HardcoverStatusCurrentlyReading},
		{"currently-reading", HardcoverStatusCurrentlyReading},
		{"3", HardcoverStatusRead},
		{"read", HardcoverStatusRead},
		{"finished", HardcoverStatusRead},
		{"4", HardcoverStatusDidNotFinish},
		{"dnf", HardcoverStatusDidNotFinish},
		{"did_not_finish", HardcoverStatusDidNotFinish},
		{"did-not-finish", HardcoverStatusDidNotFinish},
		{"5", HardcoverStatusOnHold},
		{"paused", HardcoverStatusOnHold},
		{"on_hold", HardcoverStatusOnHold},
		{"on-hold", HardcoverStatusOnHold},
		{"hold", HardcoverStatusOnHold},
		// Case and whitespace insensitivity
		{"  READ  ", HardcoverStatusRead},
		{"Currently-Reading", HardcoverStatusCurrentlyReading},
		{"DNF", HardcoverStatusDidNotFinish},
	}

	for _, tc := range tests {
		got, err := ParseHardcoverStatus(tc.input)
		if err != nil || got != tc.expected {
			t.Errorf("ParseHardcoverStatus(%q) = %d, err=%v; want %d", tc.input, got, err, tc.expected)
		}
	}

	// Invalid inputs
	for _, bad := range []string{"", "invalid", "0", "6", "deleted", "abandoned"} {
		if _, err := ParseHardcoverStatus(bad); err == nil || !errors.Is(err, ErrInvalidStatus) {
			t.Errorf("ParseHardcoverStatus(%q) expected ErrInvalidStatus, got err=%v", bad, err)
		}
	}
}

func TestHardcoverStatusMutationWithLastReadDate(t *testing.T) {
	var observedDate string
	var readMutationCalled bool
	h := testHardcover(t, func(rw http.ResponseWriter, r *http.Request) {
		var body struct {
			Query     string         `json:"query"`
			Variables map[string]any `json:"variables"`
		}
		json.NewDecoder(r.Body).Decode(&body)
		trimmed := strings.TrimSpace(body.Query)
		switch {
		case strings.HasPrefix(trimmed, "query UserBook"):
			io.WriteString(rw, `{"data":{"user_books":[]}}`)
		case strings.HasPrefix(trimmed, "mutation InsertUserBook"):
			if obj, ok := body.Variables["object"].(map[string]any); ok {
				if lrd, ok := obj["last_read_date"].(string); ok {
					observedDate = lrd
				}
			}
			// Hardcover creates a user_book_read with today's date automatically
			io.WriteString(rw, `{"data":{"insert_user_book":{"id":200,"status_id":3,"user_book":{"id":200,"status_id":3,"user_book_reads":[{"id":301,"finished_at":"2026-10-28"}]}}}}`)
		case strings.HasPrefix(trimmed, "mutation UpdateRead"):
			readMutationCalled = true
			if obj, ok := body.Variables["object"].(map[string]any); ok {
				if fin, ok := obj["finished_at"].(string); ok {
					observedDate = fin
				}
			}
			io.WriteString(rw, `{"data":{"update_user_book_read":{"id":301,"error":null,"user_book_read":{"id":301,"finished_at":"`+observedDate+`"}}}}`)
		default:
			t.Errorf("unexpected query: %s", body.Query)
		}
	})

	date := "2025-07-16"
	res, err := h.ApplyStatusMutation(context.Background(), StatusMutationRequest{
		WorkID:       42,
		StatusID:     HardcoverStatusRead,
		LastReadDate: &date,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.CurrentStatus != HardcoverStatusRead || res.UserBookID != 200 {
		t.Fatalf("unexpected outcome: %+v", res)
	}
	if observedDate != "2025-07-16" {
		t.Fatalf("expected last_read_date 2025-07-16, got %q", observedDate)
	}
	if !readMutationCalled {
		t.Fatal("expected update_user_book_read mutation to be called to correct read date")
	}
}

func TestGetUserBookStatusFiltersByAuthenticatedUserID(t *testing.T) {
	var observedVars map[string]any
	h := testHardcover(t, func(rw http.ResponseWriter, r *http.Request) {
		var body struct {
			Query     string         `json:"query"`
			Variables map[string]any `json:"variables"`
		}
		json.NewDecoder(r.Body).Decode(&body)
		if strings.HasPrefix(strings.TrimSpace(body.Query), "query UserBook") {
			observedVars = body.Variables
			io.WriteString(rw, `{"data":{"user_books":[{"id":555,"book_id":42,"status_id":3}]}}`)
			return
		}
		t.Errorf("unexpected query: %s", body.Query)
	})

	status, err := h.GetUserBookStatus(context.Background(), 42)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if status == nil || status.ID != 555 {
		t.Fatalf("unexpected status result: %+v", status)
	}
	if observedVars["work"] != float64(42) {
		t.Fatalf("expected work variable 42, got %v", observedVars["work"])
	}
	if observedVars["user_id"] != float64(183276) {
		t.Fatalf("expected user_id variable 183276, got %v", observedVars["user_id"])
	}
}
