package isbn

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/azusachino/drifter/internal/config"
)

const sampleDoubanTranslatedHTML = `
<!DOCTYPE html>
<html>
<head>
<script type="application/ld+json">
{
  "@context": "http://schema.org",
  "@type": "Book",
  "name": "智人之上",
  "author": [
    {
      "@type": "Person",
      "name": "[以色列] 尤瓦尔·赫拉利"
    }
  ],
  "isbn": "9787521768527"
}
</script>
</head>
<body>
<div id="info">
    <span><span class="pl"> 作者</span>: <a href="/author/1168901">[以色列] 尤瓦尔·赫拉利</a></span><br/>
    <span><span class="pl"> 译者</span>: <a href="/search/林俊宏">林俊宏</a></span><br/>
    <span class="pl">出版社:</span> <a href="/press/2400">中信出版社</a><br/>
    <span class="pl">出品方:</span> <a href="/producers/287">中信·先见</a><br/>
    <span class="pl">出版年:</span> 2024-9<br/>
    <span class="pl">页数:</span> 560<br/>
    <span class="pl">定价:</span> 79.00元<br/>
    <span class="pl">原作名:</span> Nexus: A Brief History of Information Networks from the Stone Age to AI<br/>
    <span class="pl">ISBN:</span> 9787521768527<br/>
</div>
<div class="author-intro">
    <p>尤瓦尔·赫拉利（Yuval Noah Harari）</p>
    <p>历史学家、哲学家、全球畅销书作家。</p>
</div>
</body>
</html>
`

const sampleDoubanNativeHTML = `
<!DOCTYPE html>
<html>
<head>
<script type="application/ld+json">
{
  "@context": "http://schema.org",
  "@type": "Book",
  "name": "三体",
  "author": [
    {
      "@type": "Person",
      "name": "刘慈欣"
    }
  ],
  "isbn": "9787536692909"
}
</script>
</head>
<body>
<div id="info">
    <span><span class="pl"> 作者</span>: <a href="/author/1">刘慈欣</a></span><br/>
    <span class="pl">出版社:</span> 重庆出版社<br/>
    <span class="pl">出版年:</span> 2008-1<br/>
    <span class="pl">ISBN:</span> 9787536692909<br/>
</div>
</body>
</html>
`

func TestParseDoubanHTML(t *testing.T) {
	t.Run("translated-book-with-original-title-and-author", func(t *testing.T) {
		meta, err := parseDoubanHTML(sampleDoubanTranslatedHTML, "9787521768527")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if meta.ISBN != "9787521768527" {
			t.Errorf("expected ISBN 9787521768527, got %s", meta.ISBN)
		}
		if meta.Title != "智人之上" {
			t.Errorf("expected title 智人之上, got %s", meta.Title)
		}
		if meta.OriginalTitle != "Nexus: A Brief History of Information Networks from the Stone Age to AI" {
			t.Errorf("unexpected original title: %s", meta.OriginalTitle)
		}
		if meta.Author != "[以色列] 尤瓦尔·赫拉利" {
			t.Errorf("unexpected author: %s", meta.Author)
		}
		if meta.OriginalAuthor != "Yuval Noah Harari" {
			t.Errorf("expected original author Yuval Noah Harari, got %s", meta.OriginalAuthor)
		}
		if meta.Translator != "林俊宏" {
			t.Errorf("expected translator 林俊宏, got %s", meta.Translator)
		}
		if meta.Publisher != "中信出版社" {
			t.Errorf("expected publisher 中信出版社, got %s", meta.Publisher)
		}
		if meta.Year != 2024 {
			t.Errorf("expected year 2024, got %d", meta.Year)
		}
		if meta.Source != "douban" {
			t.Errorf("expected source douban, got %s", meta.Source)
		}
	})

	t.Run("native-book-without-original-title", func(t *testing.T) {
		meta, err := parseDoubanHTML(sampleDoubanNativeHTML, "9787536692909")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if meta.Title != "三体" {
			t.Errorf("expected title 三体, got %s", meta.Title)
		}
		if meta.OriginalTitle != "" {
			t.Errorf("expected empty original title, got %s", meta.OriginalTitle)
		}
		if meta.Author != "刘慈欣" {
			t.Errorf("expected author 刘慈欣, got %s", meta.Author)
		}
		if meta.Publisher != "重庆出版社" {
			t.Errorf("expected publisher 重庆出版社, got %s", meta.Publisher)
		}
		if meta.Year != 2008 {
			t.Errorf("expected year 2008, got %d", meta.Year)
		}
	})

	t.Run("empty-or-unrelated-html", func(t *testing.T) {
		_, err := parseDoubanHTML("<html><body>Not a book</body></html>", "9787521768527")
		if !errors.Is(err, ErrNotFound) {
			t.Fatalf("expected ErrNotFound, got %v", err)
		}
	})
}

func TestDoubanProviderHTTP(t *testing.T) {
	t.Run("success", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if !strings.HasSuffix(r.URL.Path, "9787521768527") {
				http.NotFound(w, r)
				return
			}
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			w.WriteHeader(http.StatusOK)
			w.Write([]byte(sampleDoubanTranslatedHTML))
		}))
		defer server.Close()

		p := NewDoubanProviderWithBaseURL(server.URL + "/")
		meta, err := p.LookupISBN(context.Background(), "9787521768527")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if meta.OriginalTitle != "Nexus: A Brief History of Information Networks from the Stone Age to AI" {
			t.Errorf("unexpected original title: %s", meta.OriginalTitle)
		}
	})

	t.Run("not-found", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			http.NotFound(w, r)
		}))
		defer server.Close()

		p := NewDoubanProviderWithBaseURL(server.URL + "/")
		_, err := p.LookupISBN(context.Background(), "9787521768527")
		if !errors.Is(err, ErrNotFound) {
			t.Fatalf("expected ErrNotFound, got %v", err)
		}
	})

	t.Run("http-error", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			http.Error(w, "server error", http.StatusInternalServerError)
		}))
		defer server.Close()

		p := NewDoubanProviderWithBaseURL(server.URL + "/")
		_, err := p.LookupISBN(context.Background(), "9787521768527")
		if err == nil || !strings.Contains(err.Error(), "HTTP 500") {
			t.Fatalf("expected HTTP 500 error, got %v", err)
		}
	})

	t.Run("invalid-isbn", func(t *testing.T) {
		p := NewDoubanProvider()
		_, err := p.LookupISBN(context.Background(), "12345")
		if !errors.Is(err, ErrInvalid) {
			t.Fatalf("expected ErrInvalid, got %v", err)
		}
	})
}

func TestOpenLibraryProviderHTTP(t *testing.T) {
	t.Run("success", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if !strings.HasSuffix(r.URL.Path, "9780134494166.json") {
				http.NotFound(w, r)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			w.Write([]byte(`{
				"title": "Clean Architecture",
				"subtitle": "A Craftsman's Guide to Software Structure and Design",
				"publish_date": "September 10, 2017",
				"publishers": ["Pearson"],
				"by_statement": "Robert C. Martin"
			}`))
		}))
		defer server.Close()

		p := NewOpenLibraryProviderWithBaseURL(server.URL + "/")
		meta, err := p.LookupISBN(context.Background(), "9780134494166")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if meta.Title != "Clean Architecture" {
			t.Errorf("expected title Clean Architecture, got %s", meta.Title)
		}
		if meta.Author != "Robert C. Martin" {
			t.Errorf("expected author Robert C. Martin, got %s", meta.Author)
		}
		if meta.Publisher != "Pearson" {
			t.Errorf("expected publisher Pearson, got %s", meta.Publisher)
		}
		if meta.Year != 2017 {
			t.Errorf("expected year 2017, got %d", meta.Year)
		}
		if meta.Source != "openlibrary" {
			t.Errorf("expected source openlibrary, got %s", meta.Source)
		}
	})

	t.Run("not-found", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			http.NotFound(w, r)
		}))
		defer server.Close()

		p := NewOpenLibraryProviderWithBaseURL(server.URL + "/")
		_, err := p.LookupISBN(context.Background(), "9780134494166")
		if !errors.Is(err, ErrNotFound) {
			t.Fatalf("expected ErrNotFound, got %v", err)
		}
	})

	t.Run("empty-title", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			w.Write([]byte(`{"publish_date": "2020"}`))
		}))
		defer server.Close()

		p := NewOpenLibraryProviderWithBaseURL(server.URL + "/")
		_, err := p.LookupISBN(context.Background(), "9780134494166")
		if !errors.Is(err, ErrNotFound) {
			t.Fatalf("expected ErrNotFound, got %v", err)
		}
	})
}

func TestCustomProviderHTTP(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/9787521768527") {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{
			"isbn": "9787521768527",
			"title": "智人之上",
			"original_title": "Nexus",
			"author": "Yuval Noah Harari",
			"year": 2024
		}`))
	}))
	defer server.Close()

	p := NewCustomProvider(server.URL + "/isbn/{isbn}")
	meta, err := p.LookupISBN(context.Background(), "9787521768527")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if meta.OriginalTitle != "Nexus" || meta.Source != "custom" {
		t.Errorf("unexpected metadata: %+v", meta)
	}
}

type fakeProvider struct {
	lookups []string
	results map[string]*Metadata
	errors  map[string]error
}

func (f *fakeProvider) LookupISBN(_ context.Context, isbn string) (*Metadata, error) {
	f.lookups = append(f.lookups, isbn)
	if err, ok := f.errors[isbn]; ok {
		return nil, err
	}
	if res, ok := f.results[isbn]; ok {
		return res, nil
	}
	return nil, ErrNotFound
}

func TestCompositeProviderStrategy(t *testing.T) {
	t.Run("chinese-isbn-queries-douban-first", func(t *testing.T) {
		douban := &fakeProvider{
			results: map[string]*Metadata{
				"9787521768527": {ISBN: "9787521768527", Title: "智人之上", Source: "douban"},
			},
		}
		openlib := &fakeProvider{}
		composite := NewCompositeProvider(douban, openlib)

		meta, err := composite.LookupISBN(context.Background(), "9787521768527")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if meta.Source != "douban" {
			t.Errorf("expected douban source, got %s", meta.Source)
		}
		if len(douban.lookups) != 1 || len(openlib.lookups) != 0 {
			t.Errorf("expected only douban lookup, got douban=%d openlib=%d", len(douban.lookups), len(openlib.lookups))
		}
	})

	t.Run("chinese-isbn-falls-back-to-openlibrary", func(t *testing.T) {
		douban := &fakeProvider{}
		openlib := &fakeProvider{
			results: map[string]*Metadata{
				"9787521768527": {ISBN: "9787521768527", Title: "Fallback", Source: "openlibrary"},
			},
		}
		composite := NewCompositeProvider(douban, openlib)

		meta, err := composite.LookupISBN(context.Background(), "9787521768527")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if meta.Source != "openlibrary" {
			t.Errorf("expected openlibrary fallback, got %s", meta.Source)
		}
		if len(douban.lookups) != 1 || len(openlib.lookups) != 1 {
			t.Errorf("expected both providers called, got douban=%d openlib=%d", len(douban.lookups), len(openlib.lookups))
		}
	})

	t.Run("western-isbn-queries-openlibrary-first", func(t *testing.T) {
		douban := &fakeProvider{}
		openlib := &fakeProvider{
			results: map[string]*Metadata{
				"9780134494166": {ISBN: "9780134494166", Title: "Clean Architecture", Source: "openlibrary"},
			},
		}
		composite := NewCompositeProvider(douban, openlib)

		meta, err := composite.LookupISBN(context.Background(), "9780134494166")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if meta.Source != "openlibrary" {
			t.Errorf("expected openlibrary source, got %s", meta.Source)
		}
		if len(openlib.lookups) != 1 || len(douban.lookups) != 0 {
			t.Errorf("expected only openlib lookup, got openlib=%d douban=%d", len(openlib.lookups), len(douban.lookups))
		}
	})

	t.Run("context-cancelled", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()

		douban := &fakeProvider{
			errors: map[string]error{"9787521768527": context.Canceled},
		}
		openlib := &fakeProvider{}
		composite := NewCompositeProvider(douban, openlib)

		_, err := composite.LookupISBN(ctx, "9787521768527")
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("expected context.Canceled, got %v", err)
		}
	})
}

func TestNewProviderFromConfig(t *testing.T) {
	if p := NewProviderFromConfig(config.ISBNConfig{Enabled: false}); p != nil {
		t.Fatal("expected nil provider when disabled")
	}
	if p := NewProviderFromConfig(config.ISBNConfig{Enabled: true, Provider: "douban"}); p == nil {
		t.Fatal("expected douban provider")
	}
	if p := NewProviderFromConfig(config.ISBNConfig{Enabled: true, Provider: "openlibrary"}); p == nil {
		t.Fatal("expected openlibrary provider")
	}
	if p := NewProviderFromConfig(config.ISBNConfig{Enabled: true, Provider: "custom", URL: "http://example.com/{isbn}"}); p == nil {
		t.Fatal("expected custom provider")
	}
	if p := NewProviderFromConfig(config.ISBNConfig{Enabled: true, Provider: "default"}); p == nil {
		t.Fatal("expected default composite provider")
	}
	if p := NewProviderFromConfig(config.ISBNConfig{Enabled: true, Provider: "unknown"}); p != nil {
		t.Fatal("expected nil provider for unknown provider")
	}
}
