package weread

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/azusachino/drifter/internal/provider/transport"
)

func testClient(t *testing.T, handler http.HandlerFunc) *Client {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	api, err := transport.NewClientWithHTTP("WeRead", server.URL, "private-test-token", 0, server.Client())
	if err != nil {
		t.Fatal(err)
	}
	return NewWithTransport(api)
}

func TestWeReadSearchAndWhitelist(t *testing.T) {
	w := testClient(t, func(rw http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer private-test-token" {
			t.Error("missing auth")
		}
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
		}
		if body["api_name"] != "/store/search" || body["scope"] != float64(10) || body["skill_version"] != "1.0.5" {
			t.Error(body)
		}
		io.WriteString(rw, `{"hasMore":1,"results":[{"scope":17,"books":[{"bookInfo":{"bookId":"123","title":"书","author":"作者","readerLink":"https://weread.qq.com/example","installId":"do-not-retain"}}]}]}`)
	})
	got, err := w.Search(context.Background(), "书")
	if err != nil || !got.HasMore || len(got.Books) != 1 {
		t.Fatalf("%+v %v", got, err)
	}
	data, _ := json.Marshal(got)
	if strings.Contains(string(data), "installId") {
		t.Fatal("unexpected field retained")
	}
}

func TestShelfValidatesIDsDuplicatesAndCompleteness(t *testing.T) {
	for _, tc := range []struct {
		body     string
		fail     bool
		complete bool
	}{
		{`{"books":[],"bookCount":0}`, false, true},
		{`{"books":[]}`, false, true},
		{`{"books":[],"bookCount":1}`, false, false},
		{`{"books":[],"hasMore":1}`, false, false},
		{`{"books":[],"bookCount":0,"hasMore":1}`, false, false},
		{`{"books":[{"bookId":"123","secret":0},{"bookId":"CB_abc-1","secret":1}],"bookCount":2}`, false, true},
		{`{"books":[{"bookId":"123"}],"hasMore":1}`, false, false},
		{`{"books":[{"bookId":"123"}],"bookCount":2}`, false, false},
		{`{"books":[],"bookCount":-1}`, true, false},
		{`{"books":[],"hasMore":2}`, true, false},
		{`{"books":null}`, true, false},
		{`{"books":[{"bookId":"123"},{"bookId":"123"}]}`, true, false},
		{`{"books":[{"bookId":"CB_"}]}`, true, false},
		{`{"books":[{"bookId":"CB_unsafe/title"}]}`, true, false},
		{`{"books":[{"bookId":"123","secret":"private-test-token"}]}`, true, false},
		{`{"errcode":2,"errmsg":"private-test-token"}`, true, false},
		{`{"upgrade_info":{"message":"new"},"books":[]}`, true, false},
	} {
		w := testClient(t, func(rw http.ResponseWriter, _ *http.Request) { io.WriteString(rw, tc.body) })
		shelf, err := w.Shelf(context.Background())
		if (err != nil) != tc.fail || err == nil && shelf.Complete != tc.complete {
			t.Fatalf("%s: %+v %v", tc.body, shelf, err)
		}
		if err != nil && strings.Contains(err.Error(), "private-test-token") {
			t.Fatal("provider body leaked")
		}
	}
}

func TestWeReadFailsClosed(t *testing.T) {
	for _, body := range []string{`{"errcode":2,"errmsg":"private-test-token"}`, `{"upgrade_info":{"message":"upgrade"}}`, `{}`, `{"bookId":"999","title":"wrong"}`} {
		t.Run(body, func(t *testing.T) {
			w := testClient(t, func(rw http.ResponseWriter, _ *http.Request) { io.WriteString(rw, body) })
			_, err := w.Book(context.Background(), "123")
			if err == nil || strings.Contains(err.Error(), "private-test-token") {
				t.Fatalf("unsafe error %v", err)
			}
		})
	}
}

func TestProgressZeroCanBeStarted(t *testing.T) {
	w := testClient(t, func(rw http.ResponseWriter, _ *http.Request) {
		io.WriteString(rw, `{"bookId":"123","book":{"progress":0,"isStartReading":1,"summary":"do-not-retain"}}`)
	})
	p, err := w.Progress(context.Background(), "123")
	if err != nil || p.Status != "reading" {
		t.Fatalf("%+v %v", p, err)
	}
}

func TestSearchRequiresResultsAndPagination(t *testing.T) {
	for _, body := range []string{`{}`, `{"results":null,"hasMore":0}`, `{"results":[]}`, `{"results":[],"hasMore":2}`, `{"results":[{"books":[{"bookInfo":{"bookId":"0","title":"invalid"}}]}],"hasMore":0}`} {
		w := testClient(t, func(rw http.ResponseWriter, _ *http.Request) { io.WriteString(rw, body) })
		if _, err := w.Search(context.Background(), "synthetic"); err == nil {
			t.Fatalf("malformed search accepted: %s", body)
		}
	}
	w := testClient(t, func(rw http.ResponseWriter, _ *http.Request) { io.WriteString(rw, `{"results":[],"hasMore":0}`) })
	result, err := w.Search(context.Background(), "synthetic")
	if err != nil || result.Books == nil || len(result.Books) != 0 || result.HasMore {
		t.Fatalf("valid empty search rejected: %+v %v", result, err)
	}
}

func TestProgressRejectsMissingOrInvalidFacts(t *testing.T) {
	for _, body := range []string{
		`{}`, `{"bookId":"123","book":null}`, `{"bookId":"123","book":{"progress":0}}`,
		`{"bookId":"123","book":{"isStartReading":1}}`,
		`{"bookId":"999","book":{"progress":0,"isStartReading":1}}`,
		`{"bookId":"123","book":{"progress":101,"isStartReading":1}}`,
		`{"bookId":"123","book":{"progress":0,"isStartReading":2}}`,
		`{"bookId":"123","book":{"progress":100,"isStartReading":1,"finishTime":-1}}`,
	} {
		w := testClient(t, func(rw http.ResponseWriter, _ *http.Request) { io.WriteString(rw, body) })
		if _, err := w.Progress(context.Background(), "123"); err == nil {
			t.Fatalf("invalid progress accepted: %s", body)
		}
	}
}

func TestWeReadOperationAllowlist(t *testing.T) {
	w := &Client{}
	for _, operation := range []string{"/shelf/delete", "/book/update", "", "mutation"} {
		if err := w.call(context.Background(), operation, map[string]any{}, nil); err == nil {
			t.Fatal("unsupported operation accepted")
		}
	}
}

func TestWeReadResolveAccountFingerprint(t *testing.T) {
	t.Run("resolves userVid from reviews", func(t *testing.T) {
		w := testClient(t, func(rw http.ResponseWriter, r *http.Request) {
			var body map[string]any
			json.NewDecoder(r.Body).Decode(&body)
			if body["api_name"] != "/review/list/mine" {
				t.Errorf("unexpected api_name: %v", body["api_name"])
			}
			io.WriteString(rw, `{"errcode":0,"reviews":[{"review":{"userVid":12345678,"content":"notes"}}]}`)
		})
		fp, err := w.ResolveAccountFingerprint(context.Background(), "")
		if err != nil || fp != "weread:12345678" {
			t.Fatalf("unexpected fingerprint: got %q, err: %v", fp, err)
		}
	})

	t.Run("resolves author userVid from reviews string", func(t *testing.T) {
		w := testClient(t, func(rw http.ResponseWriter, _ *http.Request) {
			io.WriteString(rw, `{"errcode":0,"reviews":[{"review":{"author":{"userVid":"87654321"}}}]}`)
		})
		fp, err := w.ResolveAccountFingerprint(context.Background(), "")
		if err != nil || fp != "weread:87654321" {
			t.Fatalf("unexpected fingerprint: got %q, err: %v", fp, err)
		}
	})

	t.Run("resolves valid declared vid when zero reviews exist", func(t *testing.T) {
		w := testClient(t, func(rw http.ResponseWriter, _ *http.Request) {
			io.WriteString(rw, `{"errcode":0,"reviews":[]}`)
		})
		fp, err := w.ResolveAccountFingerprint(context.Background(), "99887766")
		if err != nil || fp != "weread:99887766" {
			t.Fatalf("unexpected fingerprint: got %q, err: %v", fp, err)
		}
	})

	t.Run("resolves userVid via notebooks discovery when direct review list requires bookid", func(t *testing.T) {
		w := testClient(t, func(rw http.ResponseWriter, r *http.Request) {
			var body map[string]any
			json.NewDecoder(r.Body).Decode(&body)
			switch body["api_name"] {
			case "/review/list/mine":
				if bookid, ok := body["bookid"].(string); ok && bookid == "3300108679" {
					io.WriteString(rw, `{"errcode":0,"reviews":[{"review":{"userVid":904656}}]}`)
				} else {
					io.WriteString(rw, `{"errcode":-2003,"errmsg":"缺少必填参数: bookid"}`)
				}
			case "/user/notebooks":
				io.WriteString(rw, `{"errcode":0,"books":[{"bookId":"3300108679","reviewCount":1}]}`)
			default:
				t.Errorf("unexpected api_name: %v", body["api_name"])
			}
		})
		fp, err := w.ResolveAccountFingerprint(context.Background(), "")
		if err != nil || fp != "weread:904656" {
			t.Fatalf("unexpected fingerprint: got %q, err: %v", fp, err)
		}
	})

	t.Run("fails closed when zero reviews and no declared vid", func(t *testing.T) {
		w := testClient(t, func(rw http.ResponseWriter, _ *http.Request) {
			io.WriteString(rw, `{"errcode":0,"reviews":[]}`)
		})
		if _, err := w.ResolveAccountFingerprint(context.Background(), ""); err == nil {
			t.Fatal("expected error on empty reviews without declared vid")
		}
	})

	t.Run("fails closed when zero reviews and invalid declared vid", func(t *testing.T) {
		w := testClient(t, func(rw http.ResponseWriter, _ *http.Request) {
			io.WriteString(rw, `{"errcode":0,"reviews":[]}`)
		})
		for _, bad := range []string{"abc", "12-34", " ", string(make([]byte, 33))} {
			if _, err := w.ResolveAccountFingerprint(context.Background(), bad); err == nil {
				t.Fatalf("expected error for invalid declared vid %q", bad)
			}
		}
	})
}

func TestWeReadBookTranslatorHandling(t *testing.T) {
	t.Run("omitted translator marks translator as known empty", func(t *testing.T) {
		w := testClient(t, func(rw http.ResponseWriter, _ *http.Request) {
			io.WriteString(rw, `{"bookId":"123","title":"原生中文书","author":"中国作者"}`)
		})
		book, err := w.Book(context.Background(), "123")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !book.TranslatorKnown {
			t.Fatal("expected TranslatorKnown to be true for book with omitted translator field")
		}
		if book.Translator != "" {
			t.Fatalf("expected empty translator, got %q", book.Translator)
		}
	})

	t.Run("provided translator marks translator as known with value", func(t *testing.T) {
		w := testClient(t, func(rw http.ResponseWriter, _ *http.Request) {
			io.WriteString(rw, `{"bookId":"456","title":"翻译书","author":"外国作者","translator":"译者名"}`)
		})
		book, err := w.Book(context.Background(), "456")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !book.TranslatorKnown {
			t.Fatal("expected TranslatorKnown to be true for book with translator field")
		}
		if book.Translator != "译者名" {
			t.Fatalf("expected translator '译者名', got %q", book.Translator)
		}
	})
}
