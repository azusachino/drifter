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

func testHardcover(t *testing.T, handler http.HandlerFunc) *Hardcover {
	t.Helper()
	return &Hardcover{api: testAPI(t, handler), userID: 183276}
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

func TestWeReadResolveAccountFingerprint(t *testing.T) {
	t.Run("resolves userVid from reviews", func(t *testing.T) {
		w := &WeRead{testAPI(t, func(rw http.ResponseWriter, r *http.Request) {
			var body map[string]any
			json.NewDecoder(r.Body).Decode(&body)
			if body["api_name"] != "/review/list/mine" {
				t.Errorf("unexpected api_name: %v", body["api_name"])
			}
			io.WriteString(rw, `{"errcode":0,"reviews":[{"review":{"userVid":12345678,"content":"notes"}}]}`)
		})}
		fp, err := w.ResolveAccountFingerprint(context.Background(), "")
		if err != nil || fp != "weread:12345678" {
			t.Fatalf("unexpected fingerprint: got %q, err: %v", fp, err)
		}
	})

	t.Run("resolves author userVid from reviews string", func(t *testing.T) {
		w := &WeRead{testAPI(t, func(rw http.ResponseWriter, _ *http.Request) {
			io.WriteString(rw, `{"errcode":0,"reviews":[{"review":{"author":{"userVid":"87654321"}}}]}`)
		})}
		fp, err := w.ResolveAccountFingerprint(context.Background(), "")
		if err != nil || fp != "weread:87654321" {
			t.Fatalf("unexpected fingerprint: got %q, err: %v", fp, err)
		}
	})

	t.Run("resolves valid declared vid when zero reviews exist", func(t *testing.T) {
		w := &WeRead{testAPI(t, func(rw http.ResponseWriter, _ *http.Request) {
			io.WriteString(rw, `{"errcode":0,"reviews":[]}`)
		})}
		fp, err := w.ResolveAccountFingerprint(context.Background(), "99887766")
		if err != nil || fp != "weread:99887766" {
			t.Fatalf("unexpected fingerprint: got %q, err: %v", fp, err)
		}
	})

	t.Run("resolves userVid via notebooks discovery when direct review list requires bookid", func(t *testing.T) {
		w := &WeRead{testAPI(t, func(rw http.ResponseWriter, r *http.Request) {
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
		})}
		fp, err := w.ResolveAccountFingerprint(context.Background(), "")
		if err != nil || fp != "weread:904656" {
			t.Fatalf("unexpected fingerprint: got %q, err: %v", fp, err)
		}
	})

	t.Run("fails closed when zero reviews and no declared vid", func(t *testing.T) {
		w := &WeRead{testAPI(t, func(rw http.ResponseWriter, _ *http.Request) {
			io.WriteString(rw, `{"errcode":0,"reviews":[]}`)
		})}
		if _, err := w.ResolveAccountFingerprint(context.Background(), ""); err == nil {
			t.Fatal("expected error on empty reviews without declared vid")
		}
	})

	t.Run("fails closed when zero reviews and invalid declared vid", func(t *testing.T) {
		w := &WeRead{testAPI(t, func(rw http.ResponseWriter, _ *http.Request) {
			io.WriteString(rw, `{"errcode":0,"reviews":[]}`)
		})}
		for _, bad := range []string{"abc", "12-34", " ", string(make([]byte, 33))} {
			if _, err := w.ResolveAccountFingerprint(context.Background(), bad); err == nil {
				t.Fatalf("expected error for invalid declared vid %q", bad)
			}
		}
	})
}

func TestWeReadBookTranslatorHandling(t *testing.T) {
	t.Run("omitted translator marks translator as known empty", func(t *testing.T) {
		w := &WeRead{testAPI(t, func(rw http.ResponseWriter, _ *http.Request) {
			io.WriteString(rw, `{"bookId":"123","title":"原生中文书","author":"中国作者"}`)
		})}
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
		w := &WeRead{testAPI(t, func(rw http.ResponseWriter, _ *http.Request) {
			io.WriteString(rw, `{"bookId":"456","title":"翻译书","author":"外国作者","translator":"译者名"}`)
		})}
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
