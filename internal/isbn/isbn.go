// Package isbn provides authoritative bibliographical metadata resolution
// for ISBNs from external registries (Douban, OpenLibrary, custom HTTP endpoints).
package isbn

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/azusachino/drifter/internal/catalog"
	"github.com/azusachino/drifter/internal/config"
)

var (
	// ErrNotFound indicates that an ISBN could not be found in the authoritative provider.
	ErrNotFound = errors.New("ISBN not found")
	// ErrInvalid indicates that an ISBN does not pass checksum or format validation.
	ErrInvalid = errors.New("invalid ISBN")
)

// Metadata represents authoritative bibliographical metadata resolved for an ISBN.
type Metadata struct {
	ISBN           string `json:"isbn"`
	Title          string `json:"title,omitempty"`
	OriginalTitle  string `json:"original_title,omitempty"`
	Author         string `json:"author,omitempty"`
	OriginalAuthor string `json:"original_author,omitempty"`
	Translator     string `json:"translator,omitempty"`
	Publisher      string `json:"publisher,omitempty"`
	Year           int    `json:"year,omitempty"`
	Source         string `json:"source"`
}

// Provider defines the interface for looking up bibliographical metadata by ISBN.
type Provider interface {
	LookupISBN(ctx context.Context, isbn string) (*Metadata, error)
}

const (
	defaultHTTPTimeout = 30 * time.Second
	responseLimit      = 2 << 20 // 2 MiB response cap
)

// DoubanProvider resolves ISBN metadata by scraping Douban Books subject pages.
type DoubanProvider struct {
	client  *http.Client
	baseURL string
}

// NewDoubanProvider creates a Douban ISBN provider using the official Douban Books ISBN endpoint.
func NewDoubanProvider() *DoubanProvider {
	return NewDoubanProviderWithBaseURL("https://book.douban.com/isbn/")
}

// NewDoubanProviderWithBaseURL creates a Douban provider with a custom base URL (useful for testing).
func NewDoubanProviderWithBaseURL(baseURL string) *DoubanProvider {
	return &DoubanProvider{
		client: &http.Client{
			Timeout: defaultHTTPTimeout,
		},
		baseURL: baseURL,
	}
}

var (
	doubanJSONLDRegex     = regexp.MustCompile(`(?s)<script[^>]*type=["']application/ld\+json["'][^>]*>(.*?)</script>`)
	doubanInfoRegex       = regexp.MustCompile(`(?s)<div id=["']info["']>(.*?)</div>`)
	doubanOriginalTitleRe = regexp.MustCompile(`(?s)<span class=["']pl["']>\s*原作名:?\s*</span>\s*([^<\r\n]+?)(?:<br|\n|$)`)
	doubanPublisherRe     = regexp.MustCompile(`(?s)<span class=["']pl["']>\s*出版社:?\s*</span>\s*(?:<a[^>]*>)?([^<\r\n]+)(?:</a>)?`)
	doubanPubYearRe       = regexp.MustCompile(`(?s)<span class=["']pl["']>\s*出版年:?\s*</span>\s*([^<\r\n]+)`)
	doubanTranslatorRe    = regexp.MustCompile(`(?s)<span class=["']pl["']>\s*译者:?\s*</span>[\s\S]*?<a[^>]*>([^<]+)</a>`)
	doubanAuthorRe        = regexp.MustCompile(`(?s)<span class=["']pl["']>\s*作者:?\s*</span>[\s\S]*?<a[^>]*>([^<]+)</a>`)
	doubanAuthorIntroRe   = regexp.MustCompile(`(?s)<(?:div|span)[^>]*class=["']author-intro["'][^>]*>([\s\S]*?)</(?:div|span)>`)
	fourDigitYearRegex    = regexp.MustCompile(`\b(19\d\d|20\d\d)\b`)
	latinNameInParens     = regexp.MustCompile(`[（\(]([A-Za-z][A-Za-z\s\.\-']{2,60})[）\)]`)
)

func parseDoubanHTML(body string, isbn string) (*Metadata, error) {
	meta := &Metadata{
		ISBN:   isbn,
		Source: "douban",
	}

	// 1. Attempt parsing JSON-LD for canonical title and author
	for _, match := range doubanJSONLDRegex.FindAllStringSubmatch(body, -1) {
		if len(match) < 2 {
			continue
		}
		var doc struct {
			Type   any             `json:"@type"`
			Name   string          `json:"name"`
			Author json.RawMessage `json:"author"`
		}
		if err := json.Unmarshal([]byte(match[1]), &doc); err != nil {
			continue
		}
		if doc.Name != "" {
			meta.Title = html.UnescapeString(strings.TrimSpace(doc.Name))
		}
		if len(doc.Author) > 0 {
			var people []struct {
				Name string `json:"name"`
			}
			if err := json.Unmarshal(doc.Author, &people); err == nil && len(people) > 0 {
				names := make([]string, 0, len(people))
				for _, p := range people {
					if n := strings.TrimSpace(p.Name); n != "" {
						names = append(names, html.UnescapeString(n))
					}
				}
				if len(names) > 0 {
					meta.Author = strings.Join(names, ", ")
				}
			} else {
				var person struct {
					Name string `json:"name"`
				}
				if err := json.Unmarshal(doc.Author, &person); err == nil && person.Name != "" {
					meta.Author = html.UnescapeString(strings.TrimSpace(person.Name))
				}
			}
		}
		if meta.Title != "" {
			break
		}
	}

	// 2. Extract detailed fields from <div id="info">
	infoText := body
	if infoMatch := doubanInfoRegex.FindStringSubmatch(body); len(infoMatch) >= 2 {
		infoText = infoMatch[1]
	}

	if match := doubanOriginalTitleRe.FindStringSubmatch(infoText); len(match) >= 2 {
		meta.OriginalTitle = html.UnescapeString(strings.TrimSpace(match[1]))
	}
	if match := doubanPublisherRe.FindStringSubmatch(infoText); len(match) >= 2 {
		meta.Publisher = html.UnescapeString(strings.TrimSpace(match[1]))
	}
	if match := doubanPubYearRe.FindStringSubmatch(infoText); len(match) >= 2 {
		if yMatch := fourDigitYearRegex.FindString(match[1]); yMatch != "" {
			if y, err := strconv.Atoi(yMatch); err == nil {
				meta.Year = y
			}
		}
	}
	if match := doubanTranslatorRe.FindStringSubmatch(infoText); len(match) >= 2 {
		meta.Translator = html.UnescapeString(strings.TrimSpace(match[1]))
	}
	if meta.Author == "" {
		if match := doubanAuthorRe.FindStringSubmatch(infoText); len(match) >= 2 {
			meta.Author = html.UnescapeString(strings.TrimSpace(match[1]))
		}
	}

	// 3. Attempt Latin original author extraction if author has parens or author-intro has parens
	if meta.Author != "" {
		if match := latinNameInParens.FindStringSubmatch(meta.Author); len(match) >= 2 {
			meta.OriginalAuthor = strings.TrimSpace(match[1])
		}
	}
	if meta.OriginalAuthor == "" {
		if introMatch := doubanAuthorIntroRe.FindStringSubmatch(body); len(introMatch) >= 2 {
			if match := latinNameInParens.FindStringSubmatch(introMatch[1]); len(match) >= 2 {
				meta.OriginalAuthor = strings.TrimSpace(match[1])
			}
		}
	}

	if meta.Title == "" && meta.OriginalTitle == "" {
		return nil, ErrNotFound
	}
	return meta, nil
}

// LookupISBN resolves metadata for an ISBN from Douban.
func (p *DoubanProvider) LookupISBN(ctx context.Context, rawISBN string) (*Metadata, error) {
	norm, err := catalog.NormalizeISBN(rawISBN)
	if err != nil {
		return nil, fmt.Errorf("%w: %s", ErrInvalid, rawISBN)
	}

	reqURL := p.baseURL + norm
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, reqURL, nil)
	if err != nil {
		return nil, fmt.Errorf("douban request creation failed: %w", err)
	}
	req.Header.Set("User-Agent", "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0.0.0 Safari/537.36")
	req.Header.Set("Accept", "text/html,application/xhtml+xml,application/xml;q=0.9,*/*;q=0.8")
	req.Header.Set("Accept-Language", "zh-CN,zh;q=0.9,en;q=0.8")

	resp, err := p.client.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, fmt.Errorf("douban transport failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNotFound {
		return nil, ErrNotFound
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("douban HTTP %d", resp.StatusCode)
	}

	data, err := io.ReadAll(io.LimitReader(resp.Body, responseLimit+1))
	if err != nil || len(data) > responseLimit {
		return nil, fmt.Errorf("douban response unreadable or exceeded limit")
	}

	return parseDoubanHTML(string(data), norm)
}

// OpenLibraryProvider resolves ISBN metadata by querying openlibrary.org REST API.
type OpenLibraryProvider struct {
	client  *http.Client
	baseURL string
}

// NewOpenLibraryProvider creates an OpenLibrary ISBN provider using the official openlibrary.org endpoint.
func NewOpenLibraryProvider() *OpenLibraryProvider {
	return NewOpenLibraryProviderWithBaseURL("https://openlibrary.org/isbn/")
}

// NewOpenLibraryProviderWithBaseURL creates an OpenLibrary provider with a custom base URL.
func NewOpenLibraryProviderWithBaseURL(baseURL string) *OpenLibraryProvider {
	return &OpenLibraryProvider{
		client: &http.Client{
			Timeout: defaultHTTPTimeout,
		},
		baseURL: baseURL,
	}
}

// LookupISBN resolves metadata for an ISBN from OpenLibrary.
func (p *OpenLibraryProvider) LookupISBN(ctx context.Context, rawISBN string) (*Metadata, error) {
	norm, err := catalog.NormalizeISBN(rawISBN)
	if err != nil {
		return nil, fmt.Errorf("%w: %s", ErrInvalid, rawISBN)
	}

	reqURL := p.baseURL + norm + ".json"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, reqURL, nil)
	if err != nil {
		return nil, fmt.Errorf("openlibrary request creation failed: %w", err)
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "drifter/1.0 (+https://github.com/azusachino/drifter)")

	resp, err := p.client.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, fmt.Errorf("openlibrary transport failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNotFound {
		return nil, ErrNotFound
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("openlibrary HTTP %d", resp.StatusCode)
	}

	data, err := io.ReadAll(io.LimitReader(resp.Body, responseLimit+1))
	if err != nil || len(data) > responseLimit {
		return nil, fmt.Errorf("openlibrary response unreadable or exceeded limit")
	}

	var doc struct {
		Title       string   `json:"title"`
		Subtitle    string   `json:"subtitle"`
		PublishDate string   `json:"publish_date"`
		Publishers  []string `json:"publishers"`
		ByStatement string   `json:"by_statement"`
	}
	if err := json.Unmarshal(data, &doc); err != nil {
		return nil, fmt.Errorf("openlibrary response is not valid JSON: %w", err)
	}

	if doc.Title == "" {
		return nil, ErrNotFound
	}

	meta := &Metadata{
		ISBN:          norm,
		Title:         doc.Title,
		OriginalTitle: doc.Title,
		Source:        "openlibrary",
	}
	if doc.ByStatement != "" {
		meta.Author = strings.TrimSpace(doc.ByStatement)
	}
	if len(doc.Publishers) > 0 {
		meta.Publisher = strings.TrimSpace(doc.Publishers[0])
	}
	if doc.PublishDate != "" {
		if yMatch := fourDigitYearRegex.FindString(doc.PublishDate); yMatch != "" {
			if y, err := strconv.Atoi(yMatch); err == nil {
				meta.Year = y
			}
		}
	}
	return meta, nil
}

// CustomProvider resolves ISBN metadata against a user-configured URL template.
type CustomProvider struct {
	client      *http.Client
	urlTemplate string
}

// NewCustomProvider creates a provider that substitutes {isbn} in the given URL template.
func NewCustomProvider(urlTemplate string) *CustomProvider {
	return &CustomProvider{
		client: &http.Client{
			Timeout: defaultHTTPTimeout,
		},
		urlTemplate: urlTemplate,
	}
}

// LookupISBN resolves metadata for an ISBN using the custom URL template.
func (p *CustomProvider) LookupISBN(ctx context.Context, rawISBN string) (*Metadata, error) {
	norm, err := catalog.NormalizeISBN(rawISBN)
	if err != nil {
		return nil, fmt.Errorf("%w: %s", ErrInvalid, rawISBN)
	}

	targetURL := strings.ReplaceAll(p.urlTemplate, "{isbn}", url.PathEscape(norm))
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, targetURL, nil)
	if err != nil {
		return nil, fmt.Errorf("custom ISBN request creation failed: %w", err)
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "drifter/1.0 (+https://github.com/azusachino/drifter)")

	resp, err := p.client.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, fmt.Errorf("custom ISBN transport failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNotFound {
		return nil, ErrNotFound
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("custom ISBN HTTP %d", resp.StatusCode)
	}

	data, err := io.ReadAll(io.LimitReader(resp.Body, responseLimit+1))
	if err != nil || len(data) > responseLimit {
		return nil, fmt.Errorf("custom ISBN response unreadable or exceeded limit")
	}

	var meta Metadata
	if err := json.Unmarshal(data, &meta); err != nil {
		return nil, fmt.Errorf("custom ISBN response is not valid JSON: %w", err)
	}
	meta.ISBN = norm
	if meta.Source == "" {
		meta.Source = "custom"
	}
	if meta.Title == "" && meta.OriginalTitle == "" {
		return nil, ErrNotFound
	}
	return &meta, nil
}

// CompositeProvider dispatches ISBN lookups dynamically based on ISBN prefix:
// Chinese prefix (9787...) queries Douban first, falling back to OpenLibrary.
// Other prefixes query OpenLibrary first, falling back to Douban.
type CompositeProvider struct {
	douban      Provider
	openlibrary Provider
}

// NewDefaultProvider creates a composite provider with Douban and OpenLibrary.
func NewDefaultProvider() *CompositeProvider {
	return &CompositeProvider{
		douban:      NewDoubanProvider(),
		openlibrary: NewOpenLibraryProvider(),
	}
}

// NewCompositeProvider creates a composite provider with custom sub-providers.
func NewCompositeProvider(douban, openlibrary Provider) *CompositeProvider {
	return &CompositeProvider{
		douban:      douban,
		openlibrary: openlibrary,
	}
}

// LookupISBN resolves ISBN metadata using the default prefix-aware composite strategy.
func (p *CompositeProvider) LookupISBN(ctx context.Context, rawISBN string) (*Metadata, error) {
	norm, err := catalog.NormalizeISBN(rawISBN)
	if err != nil {
		return nil, fmt.Errorf("%w: %s", ErrInvalid, rawISBN)
	}

	isChineseISBN := strings.HasPrefix(norm, "9787") || strings.HasPrefix(norm, "9797")

	var primary, secondary Provider
	if isChineseISBN {
		primary, secondary = p.douban, p.openlibrary
	} else {
		primary, secondary = p.openlibrary, p.douban
	}

	if primary != nil {
		meta, err := primary.LookupISBN(ctx, norm)
		if err == nil && meta != nil {
			return meta, nil
		}
		if err != nil && !errors.Is(err, ErrNotFound) {
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
		}
	}

	if secondary != nil {
		meta, err := secondary.LookupISBN(ctx, norm)
		if err == nil && meta != nil {
			return meta, nil
		}
		if err != nil {
			return nil, err
		}
	}

	return nil, ErrNotFound
}

// NewProviderFromConfig constructs an ISBN Provider according to configuration settings.
func NewProviderFromConfig(cfg config.ISBNConfig) Provider {
	if !cfg.Enabled {
		return nil
	}
	switch strings.ToLower(strings.TrimSpace(cfg.Provider)) {
	case "douban":
		return NewDoubanProvider()
	case "openlibrary":
		return NewOpenLibraryProvider()
	case "custom":
		return NewCustomProvider(cfg.URL)
	case "", "default":
		return NewDefaultProvider()
	default:
		return nil
	}
}
