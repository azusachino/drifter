package companion

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func testAPI(t *testing.T, handler http.HandlerFunc) *api {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	a, err := newAPI("test", server.URL, "private-test-token", 0)
	if err != nil {
		t.Fatal(err)
	}
	return a
}

func TestWeReadSearchAndWhitelist(t *testing.T) {
	w := &WeRead{testAPI(t, func(rw http.ResponseWriter, r *http.Request) {
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
	})}
	got, err := w.Search(context.Background(), "书")
	if err != nil || !got.HasMore || len(got.Books) != 1 {
		t.Fatalf("%+v %v", got, err)
	}
	data, _ := json.Marshal(got)
	if strings.Contains(string(data), "installId") {
		t.Fatal("unexpected field retained")
	}
}

func TestWeReadFailsClosed(t *testing.T) {
	for _, body := range []string{`{"errcode":2,"errmsg":"private-test-token"}`, `{"upgrade_info":{"message":"upgrade"}}`, `{}`, `{"bookId":"999","title":"wrong"}`} {
		t.Run(body, func(t *testing.T) {
			w := &WeRead{testAPI(t, func(rw http.ResponseWriter, _ *http.Request) { io.WriteString(rw, body) })}
			_, err := w.Book(context.Background(), "123")
			if err == nil || strings.Contains(err.Error(), "private-test-token") {
				t.Fatalf("unsafe error %v", err)
			}
		})
	}
}

func TestProgressZeroCanBeStarted(t *testing.T) {
	w := &WeRead{testAPI(t, func(rw http.ResponseWriter, _ *http.Request) {
		io.WriteString(rw, `{"bookId":"123","book":{"progress":0,"isStartReading":1,"summary":"do-not-retain"}}`)
	})}
	p, err := w.Progress(context.Background(), "123")
	if err != nil || p.Status != "reading" {
		t.Fatalf("%+v %v", p, err)
	}
}

func TestHTTPRejectsRedirectsErrorsAndOversizedBodies(t *testing.T) {
	for _, status := range []int{http.StatusFound, http.StatusUnauthorized, http.StatusTooManyRequests} {
		a := testAPI(t, func(rw http.ResponseWriter, _ *http.Request) {
			rw.Header().Set("Location", "https://example.com")
			rw.WriteHeader(status)
			io.WriteString(rw, "private-test-token")
		})
		var got any
		if err := a.post(context.Background(), map[string]string{}, &got); err == nil || strings.Contains(err.Error(), "private-test-token") {
			t.Fatal(err)
		}
	}
	a := testAPI(t, func(rw http.ResponseWriter, _ *http.Request) {
		io.WriteString(rw, strings.Repeat("x", responseLimit+1))
	})
	var got any
	if err := a.post(context.Background(), nil, &got); err == nil {
		t.Fatal("oversized response accepted")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := a.post(ctx, nil, &got); err == nil {
		t.Fatal("cancellation ignored")
	}
}
