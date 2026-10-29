package hardcover

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/azusachino/drifter/internal/companion"
	"github.com/azusachino/drifter/internal/provider/transport"
)

func testClient(t *testing.T, handler http.HandlerFunc) *Client {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	api, err := transport.NewClientWithHTTP("Hardcover", server.URL, "private-test-token", 0, server.Client())
	if err != nil {
		t.Fatal(err)
	}
	return NewWithTransportAndUserID(api, 183276)
}

func TestHardcoverMatchRequiresConfirmation(t *testing.T) {
	h := testClient(t, func(rw http.ResponseWriter, r *http.Request) {
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
	})
	r, err := h.Match(context.Background(), companion.Book{Title: "中文", ISBN: "9787512523821"}, "English candidate")
	if err != nil || r.Resolution != "work_candidates" || !r.RequiresConfirmation || len(r.Candidates) != 2 || r.Candidates[0].ID != 2 {
		t.Fatalf("%+v %v", r, err)
	}
}

func TestCleanSearchTitle(t *testing.T) {
	cases := []struct {
		input string
		want  string
	}{
		{"克拉拉与太阳（彩虹版石黑一雄作品）", "克拉拉与太阳"},
		{"长安的荔枝（同名影视原著）", "长安的荔枝"},
		{"生命的礼物 (关于爱)", "生命的礼物"},
		{"Normal Book Title", "Normal Book Title"},
		{"", ""},
	}
	for _, tc := range cases {
		if got := cleanSearchTitle(tc.input); got != tc.want {
			t.Errorf("cleanSearchTitle(%q) = %q; want %q", tc.input, got, tc.want)
		}
	}
}

func TestDuplicateISBNIsAmbiguous(t *testing.T) {
	h := testClient(t, func(rw http.ResponseWriter, _ *http.Request) {
		io.WriteString(rw, `{"data":{"editions":[{"id":1,"book_id":2},{"id":3,"book_id":4}]}}`)
	})
	r, err := h.Match(context.Background(), companion.Book{ISBN: "0593716965"}, "")
	if err != nil || r.Resolution != "ambiguous_identifier" || !r.RequiresConfirmation {
		t.Fatalf("%+v %v", r, err)
	}
}

func TestMissingAndInvalidISBNDoNotInventMatch(t *testing.T) {
	for _, isbn := range []string{"", "B0768ZM5QH", "9787512523822"} {
		h := testClient(t, func(rw http.ResponseWriter, r *http.Request) {
			body, _ := io.ReadAll(r.Body)
			if strings.Contains(string(body), "query Exact") {
				t.Error("invalid exact lookup")
			}
			io.WriteString(rw, `{"data":{"search":{"ids":[],"error":null}}}`)
		})
		r, err := h.Match(context.Background(), companion.Book{Title: "book", ISBN: isbn}, "")
		if err != nil || r.Resolution != "unresolved" || len(r.Candidates) != 0 {
			t.Fatalf("%+v %v", r, err)
		}
	}
}

func TestHardcoverRejectsGraphQLAndActionErrors(t *testing.T) {
	for _, body := range []string{`{"errors":[{"message":"private-test-token"}]}`, `{"data":null}`, `{"data":{"search":{"error":"private-test-token"}}}`} {
		h := testClient(t, func(rw http.ResponseWriter, _ *http.Request) { io.WriteString(rw, body) })
		_, err := h.Match(context.Background(), companion.Book{Title: "book"}, "")
		if err == nil || strings.Contains(err.Error(), "private-test-token") {
			t.Fatalf("unsafe error %v", err)
		}
	}
}

func TestValidateEditionMembership(t *testing.T) {
	h := testClient(t, func(rw http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		if strings.Contains(string(body), "query Edition") {
			io.WriteString(rw, `{"data":{"editions":[]}}`)
		} else {
			io.WriteString(rw, `{"data":{"books":[{"id":2}]}}`)
		}
	})
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

func TestHardcoverRejectsMissingEditionList(t *testing.T) {
	for _, body := range []string{`{"data":{}}`, `{"data":{"editions":null}}`, `{"data":{"editions":[{"id":0,"book_id":2}]}}`} {
		h := testClient(t, func(rw http.ResponseWriter, _ *http.Request) { io.WriteString(rw, body) })
		if _, err := h.Match(context.Background(), companion.Book{Title: "synthetic", ISBN: "9780306406157"}, ""); err == nil {
			t.Fatalf("invalid edition response accepted: %s", body)
		}
	}
}

func TestHardcoverRejectsWrongReturnedMembership(t *testing.T) {
	h := testClient(t, func(rw http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		if strings.Contains(string(body), "query Edition") {
			io.WriteString(rw, `{"data":{"editions":[{"id":99,"book_id":3}]}}`)
		} else {
			io.WriteString(rw, `{"data":{"books":[{"id":2}]}}`)
		}
	})
	if err := h.ValidateLink(context.Background(), 2, 99); err == nil {
		t.Fatal("edition from another work accepted")
	}
}

func TestHardcoverResolveAccountFingerprint(t *testing.T) {
	t.Run("resolves valid user ID", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
			var body struct {
				Query string `json:"query"`
			}
			json.NewDecoder(r.Body).Decode(&body)
			if !strings.Contains(body.Query, "query Me") {
				t.Errorf("unexpected query: %s", body.Query)
			}
			io.WriteString(rw, `{"data":{"me":{"id":4242,"username":"reader"}}}`)
		}))
		t.Cleanup(server.Close)
		api, err := transport.NewClientWithHTTP("Hardcover", server.URL, "private-test-token", 0, server.Client())
		if err != nil {
			t.Fatal(err)
		}
		h := NewWithTransport(api)

		fp, err := h.ResolveAccountFingerprint(context.Background())
		if err != nil || fp != "hardcover:4242" {
			t.Fatalf("fingerprint resolution: got %q, err: %v", fp, err)
		}
	})

	t.Run("rejects missing user ID", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, _ *http.Request) {
			io.WriteString(rw, `{"data":{"me":null}}`)
		}))
		t.Cleanup(server.Close)
		api, err := transport.NewClientWithHTTP("Hardcover", server.URL, "private-test-token", 0, server.Client())
		if err != nil {
			t.Fatal(err)
		}
		h := NewWithTransport(api)

		if _, err := h.ResolveAccountFingerprint(context.Background()); err == nil {
			t.Fatal("expected error on null me response")
		}
	})

	t.Run("rejects zero or negative user ID", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, _ *http.Request) {
			io.WriteString(rw, `{"data":{"me":{"id":0,"username":"reader"}}}`)
		}))
		t.Cleanup(server.Close)
		api, err := transport.NewClientWithHTTP("Hardcover", server.URL, "private-test-token", 0, server.Client())
		if err != nil {
			t.Fatal(err)
		}
		h := NewWithTransport(api)

		if _, err := h.ResolveAccountFingerprint(context.Background()); err == nil {
			t.Fatal("expected error on zero user ID")
		}
	})
}
