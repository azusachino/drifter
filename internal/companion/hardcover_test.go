package companion

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
)

func TestHardcoverMatchRequiresConfirmation(t *testing.T) {
	h := &Hardcover{testAPI(t, func(rw http.ResponseWriter, r *http.Request) {
		var body struct {
			Query string `json:"query"`
		}
		json.NewDecoder(r.Body).Decode(&body)
		switch {
		case strings.Contains(body.Query, "query Exact"):
			io.WriteString(rw, `{"data":{"editions":[]}}`)
		case strings.Contains(body.Query, "query Search"):
			io.WriteString(rw, `{"data":{"search":{"ids":[2,1],"error":null}}}`)
		case strings.Contains(body.Query, "query Works"):
			io.WriteString(rw, `{"data":{"books":[{"id":1,"title":"one"},{"id":2,"title":"two"}]}}`)
		default:
			t.Errorf("unexpected query %s", body.Query)
		}
	})}
	r, err := h.Match(context.Background(), Book{Title: "中文", ISBN: "9787512523821"}, "English candidate")
	if err != nil || r.Resolution != "work_candidates" || !r.RequiresConfirmation || len(r.Candidates) != 2 || r.Candidates[0].ID != 2 {
		t.Fatalf("%+v %v", r, err)
	}
}

func TestDuplicateISBNIsAmbiguous(t *testing.T) {
	h := &Hardcover{testAPI(t, func(rw http.ResponseWriter, _ *http.Request) {
		io.WriteString(rw, `{"data":{"editions":[{"id":1,"book_id":2},{"id":3,"book_id":4}]}}`)
	})}
	r, err := h.Match(context.Background(), Book{ISBN: "0593716965"}, "")
	if err != nil || r.Resolution != "ambiguous_identifier" || !r.RequiresConfirmation {
		t.Fatalf("%+v %v", r, err)
	}
}

func TestMissingAndInvalidISBNDoNotInventMatch(t *testing.T) {
	for _, isbn := range []string{"", "B0768ZM5QH", "9787512523822"} {
		h := &Hardcover{testAPI(t, func(rw http.ResponseWriter, r *http.Request) {
			body, _ := io.ReadAll(r.Body)
			if strings.Contains(string(body), "query Exact") {
				t.Error("invalid exact lookup")
			}
			io.WriteString(rw, `{"data":{"search":{"ids":[],"error":null}}}`)
		})}
		r, err := h.Match(context.Background(), Book{Title: "book", ISBN: isbn}, "")
		if err != nil || r.Resolution != "unresolved" || len(r.Candidates) != 0 {
			t.Fatalf("%+v %v", r, err)
		}
	}
}

func TestHardcoverRejectsGraphQLAndActionErrors(t *testing.T) {
	for _, body := range []string{`{"errors":[{"message":"private-test-token"}]}`, `{"data":null}`, `{"data":{"search":{"error":"private-test-token"}}}`} {
		h := &Hardcover{testAPI(t, func(rw http.ResponseWriter, _ *http.Request) { io.WriteString(rw, body) })}
		_, err := h.Match(context.Background(), Book{Title: "book"}, "")
		if err == nil || strings.Contains(err.Error(), "private-test-token") {
			t.Fatalf("unsafe error %v", err)
		}
	}
}

func TestValidateEditionMembership(t *testing.T) {
	h := &Hardcover{testAPI(t, func(rw http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		if strings.Contains(string(body), "query Edition") {
			io.WriteString(rw, `{"data":{"editions":[]}}`)
		} else {
			io.WriteString(rw, `{"data":{"books":[{"id":2}]}}`)
		}
	})}
	if err := h.ValidateLink(context.Background(), 2, 0); err != nil {
		t.Fatal(err)
	}
	if err := h.ValidateLink(context.Background(), 2, 99); err == nil {
		t.Fatal("wrong edition accepted")
	}
	if err := h.query(context.Background(), "mutation Forbidden {x}", nil, nil); err == nil {
		t.Fatal("mutation accepted")
	}
}

// A review-selected query is an override for title search, not native ISBN evidence.
func TestMatchSelectedQueryPreservesExactISBNShortCircuit(t *testing.T) {
	calls := 0
	target := &Hardcover{testAPI(t, func(rw http.ResponseWriter, r *http.Request) {
		calls++
		var body struct {
			Query string `json:"query"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
		}
		if !strings.Contains(body.Query, "query Exact") {
			t.Errorf("selected query bypassed ISBN: %s", body.Query)
		}
		io.WriteString(rw, `{"data":{"editions":[{"id":88,"book_id":77}]}}`)
	})}
	report, err := MatchWithOptions(context.Background(), isbnHintSource{}, target, "42", "Selected original title", MatchOptions{ExpectedAuthor: "Selected author"})
	if err != nil || calls != 1 || len(report.ExactEditions) != 1 || report.ExactEditions[0].ID != 88 || len(report.Candidates) != 0 || report.SearchQuery != "" || !report.RequiresConfirmation {
		t.Fatalf("calls=%d report=%+v err=%v", calls, report, err)
	}
}

type isbnHintSource struct{ Source }

func (isbnHintSource) Book(context.Context, string) (Book, error) {
	return Book{ID: "42", Title: "Native title", ISBN: "9780306406157"}, nil
}

func TestHardcoverResolveAccountFingerprint(t *testing.T) {
	t.Run("resolves valid user ID", func(t *testing.T) {
		h := &Hardcover{testAPI(t, func(rw http.ResponseWriter, r *http.Request) {
			var body struct {
				Query string `json:"query"`
			}
			json.NewDecoder(r.Body).Decode(&body)
			if !strings.Contains(body.Query, "query Me") {
				t.Errorf("unexpected query: %s", body.Query)
			}
			io.WriteString(rw, `{"data":{"me":{"id":4242,"username":"reader"}}}`)
		})}
		fp, err := h.ResolveAccountFingerprint(context.Background())
		if err != nil || fp != "hardcover:4242" {
			t.Fatalf("fingerprint resolution: got %q, err: %v", fp, err)
		}
	})

	t.Run("rejects missing user ID", func(t *testing.T) {
		h := &Hardcover{testAPI(t, func(rw http.ResponseWriter, _ *http.Request) {
			io.WriteString(rw, `{"data":{"me":null}}`)
		})}
		if _, err := h.ResolveAccountFingerprint(context.Background()); err == nil {
			t.Fatal("expected error on null me response")
		}
	})

	t.Run("rejects zero or negative user ID", func(t *testing.T) {
		h := &Hardcover{testAPI(t, func(rw http.ResponseWriter, _ *http.Request) {
			io.WriteString(rw, `{"data":{"me":{"id":0,"username":"reader"}}}`)
		})}
		if _, err := h.ResolveAccountFingerprint(context.Background()); err == nil {
			t.Fatal("expected error on zero user ID")
		}
	})
}
