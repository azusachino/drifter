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

	h := &Hardcover{testAPI(t, func(rw http.ResponseWriter, r *http.Request) {
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
	})}

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
	h := &Hardcover{testAPI(t, func(rw http.ResponseWriter, r *http.Request) {
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
	})}

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
	h := &Hardcover{testAPI(t, func(rw http.ResponseWriter, _ *http.Request) {
		t.Fatal("no network call should be sent when variables contain unapproved fields")
	})}

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
	h := &Hardcover{testAPI(t, func(rw http.ResponseWriter, r *http.Request) {
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
	})}

	_, err := h.ApplyStatusMutation(context.Background(), StatusMutationRequest{WorkID: 88, StatusID: HardcoverStatusCurrentlyReading})
	if err == nil || !errors.Is(err, ErrUncertainStatusMutation) {
		t.Fatalf("expected ErrUncertainStatusMutation on 504, got: %v", err)
	}
}

func TestHardcoverStatusMutationValidation(t *testing.T) {
	h := &Hardcover{testAPI(t, func(rw http.ResponseWriter, _ *http.Request) {
		t.Fatal("no network call should be made on validation failure")
	})}

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
