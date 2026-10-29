// Package weread implements the intake provider adapter for WeRead API.
package weread

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/azusachino/drifter/internal/companion"
	"github.com/azusachino/drifter/internal/provider/transport"
)

// DefaultURL is the production WeRead agent gateway endpoint.
const DefaultURL = "https://i.weread.qq.com/api/agent/gateway"

// Client is the WeRead intake provider client.
type Client struct {
	api *transport.Client
}

var (
	_ companion.SourceProvider = (*Client)(nil)
	_ companion.PreviewSource  = (*Client)(nil)
)

// New constructs an authenticated WeRead client using DefaultURL and a 1s rate-limit interval.
func New(token string) (*Client, error) {
	api, err := transport.NewClient("WeRead", DefaultURL, token, time.Second)
	if err != nil {
		return nil, err
	}
	return &Client{api: api}, nil
}

// NewWithTransport creates a WeRead client with a custom transport (useful for testing).
func NewWithTransport(api *transport.Client) *Client {
	return &Client{api: api}
}

func (c *Client) call(ctx context.Context, name string, args map[string]any, target any) error {
	// Only explicit read operations exist; there is no generic exported gateway.
	switch name {
	case "/store/search", "/book/info", "/book/getprogress", "/shelf/sync", "/review/list/mine", "/user/notebooks":
	default:
		return fmt.Errorf("unsupported WeRead operation")
	}
	args["api_name"], args["skill_version"] = name, "1.0.5"
	var raw json.RawMessage
	if err := c.api.Post(ctx, args, &raw); err != nil {
		return err
	}
	var envelope struct {
		Errcode int             `json:"errcode"`
		Upgrade json.RawMessage `json:"upgrade_info"`
	}
	if err := json.Unmarshal(raw, &envelope); err != nil {
		return fmt.Errorf("invalid WeRead envelope")
	}
	if envelope.Errcode != 0 {
		return fmt.Errorf("WeRead rejected operation (code %d)", envelope.Errcode)
	}
	if len(envelope.Upgrade) > 0 && string(envelope.Upgrade) != "null" {
		return fmt.Errorf("WeRead requires compatibility review before continuing")
	}
	if err := json.Unmarshal(raw, target); err != nil {
		return fmt.Errorf("WeRead operation response has invalid fields")
	}
	return nil
}

// Search queries the WeRead store for books matching title.
func (c *Client) Search(ctx context.Context, title string) (companion.SearchResult, error) {
	if title == "" || len(title) > 1024 {
		return companion.SearchResult{}, fmt.Errorf("title required (maximum 1024 bytes)")
	}
	var response struct {
		HasMore *int `json:"hasMore"`
		Results []struct {
			Books []struct {
				Info struct {
					ID     string `json:"bookId"`
					Title  string `json:"title"`
					Author string `json:"author"`
					URL    string `json:"readerLink"`
				} `json:"bookInfo"`
			} `json:"books"`
		} `json:"results"`
	}
	if err := c.call(ctx, "/store/search", map[string]any{"keyword": title, "scope": 10}, &response); err != nil {
		return companion.SearchResult{}, err
	}
	if response.Results == nil || response.HasMore == nil || (*response.HasMore != 0 && *response.HasMore != 1) {
		return companion.SearchResult{}, fmt.Errorf("WeRead search results or pagination fields missing or invalid")
	}
	result := companion.SearchResult{Books: []companion.Book{}, HasMore: *response.HasMore != 0}
	for _, group := range response.Results {
		for _, row := range group.Books {
			b := row.Info
			if !companion.ValidBookID(b.ID) || b.Title == "" {
				return companion.SearchResult{}, fmt.Errorf("WeRead search returned invalid identity")
			}
			result.Books = append(result.Books, companion.Book{ID: b.ID, Title: b.Title, Author: b.Author, ReaderURL: b.URL})
		}
	}
	return result, nil
}

// Shelf retrieves the current shelf contents.
func (c *Client) Shelf(ctx context.Context) (companion.ShelfResult, error) {
	var response struct {
		Books   []companion.ShelfBook `json:"books"`
		Count   *int                  `json:"bookCount"`
		HasMore *int                  `json:"hasMore"`
	}
	if err := c.call(ctx, "/shelf/sync", map[string]any{}, &response); err != nil {
		return companion.ShelfResult{}, err
	}
	if response.Books == nil || (response.Count != nil && *response.Count < 0) ||
		(response.HasMore != nil && *response.HasMore != 0 && *response.HasMore != 1) {
		return companion.ShelfResult{}, fmt.Errorf("WeRead shelf fields missing or invalid")
	}
	seen := make(map[string]bool, len(response.Books))
	for _, book := range response.Books {
		if !companion.ValidShelfID(book.ID) || seen[book.ID] {
			return companion.ShelfResult{}, fmt.Errorf("WeRead shelf identity invalid or duplicated")
		}
		seen[book.ID] = true
	}
	complete := (response.Count == nil || *response.Count == len(response.Books)) &&
		(response.HasMore == nil || *response.HasMore == 0)
	return companion.ShelfResult{Books: response.Books, Complete: complete}, nil
}

// Book fetches detailed metadata for a single WeRead book by ID.
func (c *Client) Book(ctx context.Context, id string) (companion.Book, error) {
	if !companion.ValidBookID(id) {
		return companion.Book{}, fmt.Errorf("valid book ID required")
	}
	var row struct {
		ID         string  `json:"bookId"`
		Title      string  `json:"title"`
		Author     string  `json:"author"`
		Translator *string `json:"translator"`
		ISBN       string  `json:"isbn"`
		Publisher  string  `json:"publisher"`
		URL        string  `json:"readerLink"`
	}
	if err := c.call(ctx, "/book/info", map[string]any{"bookId": id}, &row); err != nil {
		return companion.Book{}, err
	}
	if row.ID != id || row.Title == "" {
		return companion.Book{}, fmt.Errorf("WeRead book identity missing or invalid in query response")
	}
	book := companion.Book{
		ID:              row.ID,
		Title:           row.Title,
		Author:          row.Author,
		ISBN:            row.ISBN,
		Publisher:       row.Publisher,
		ReaderURL:       row.URL,
		TranslatorKnown: true,
	}
	if row.Translator != nil {
		book.Translator = *row.Translator
	}
	return book, nil
}

// Progress retrieves personal reading progress for a WeRead book.
func (c *Client) Progress(ctx context.Context, id string) (companion.Progress, error) {
	if !companion.ValidBookID(id) {
		return companion.Progress{}, fmt.Errorf("valid book ID required")
	}
	var row struct {
		ID   string `json:"bookId"`
		Book *struct {
			Percent  *int  `json:"progress"`
			Started  *int  `json:"isStartReading"`
			Finished int64 `json:"finishTime"`
		} `json:"book"`
	}
	if err := c.call(ctx, "/book/getprogress", map[string]any{"bookId": id}, &row); err != nil {
		return companion.Progress{}, err
	}
	if row.ID != id || row.Book == nil || row.Book.Percent == nil || row.Book.Started == nil {
		return companion.Progress{}, fmt.Errorf("WeRead progress identity or required fields missing")
	}
	return companion.ProgressStatus(*row.Book.Percent, *row.Book.Started, row.Book.Finished)
}

// ValidVID checks whether a WeRead user VID string is non-empty, numeric, and within standard bounds (1-32 digits).
func ValidVID(vid string) bool {
	vid = strings.TrimSpace(vid)
	if vid == "" || len(vid) > 32 {
		return false
	}
	for _, r := range vid {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

func parseVID(raw any) string {
	switch v := raw.(type) {
	case string:
		return strings.TrimSpace(v)
	case float64:
		if v > 0 && v == float64(int64(v)) {
			return strconv.FormatInt(int64(v), 10)
		}
	case int64:
		if v > 0 {
			return strconv.FormatInt(v, 10)
		}
	case int:
		if v > 0 {
			return strconv.Itoa(v)
		}
	}
	return ""
}

// ResolveAccountFingerprint queries WeRead for the caller's user VID.
func (c *Client) ResolveAccountFingerprint(ctx context.Context, declaredVID string) (string, error) {
	if ValidVID(declaredVID) {
		return fmt.Sprintf("weread:%s", strings.TrimSpace(declaredVID)), nil
	}

	extractVID := func(reviews []struct {
		Review struct {
			UserVID any `json:"userVid"`
			Author  struct {
				UserVID any `json:"userVid"`
			} `json:"author"`
		} `json:"review"`
	}) string {
		for _, item := range reviews {
			raw := item.Review.UserVID
			if raw == nil {
				raw = item.Review.Author.UserVID
			}
			if raw != nil {
				vidStr := parseVID(raw)
				if ValidVID(vidStr) {
					return vidStr
				}
			}
		}
		return ""
	}

	var response struct {
		Reviews []struct {
			Review struct {
				UserVID any `json:"userVid"`
				Author  struct {
					UserVID any `json:"userVid"`
				} `json:"author"`
			} `json:"review"`
		} `json:"reviews"`
	}
	err := c.call(ctx, "/review/list/mine", map[string]any{}, &response)
	if err == nil {
		if vid := extractVID(response.Reviews); vid != "" {
			return fmt.Sprintf("weread:%s", vid), nil
		}
	}

	// If direct /review/list/mine was rejected or returned 0 reviews,
	// try discovering a book with notes/reviews via /user/notebooks.
	var nbResp struct {
		Books []struct {
			BookID      string `json:"bookId"`
			ReviewCount int    `json:"reviewCount"`
		} `json:"books"`
	}
	if nbErr := c.call(ctx, "/user/notebooks", map[string]any{"count": 20}, &nbResp); nbErr == nil && len(nbResp.Books) > 0 {
		var targetBookID string
		for _, b := range nbResp.Books {
			if b.ReviewCount > 0 && companion.ValidBookID(b.BookID) {
				targetBookID = b.BookID
				break
			}
		}
		if targetBookID == "" && companion.ValidBookID(nbResp.Books[0].BookID) {
			targetBookID = nbResp.Books[0].BookID
		}

		if targetBookID != "" {
			var bookRevResp struct {
				Reviews []struct {
					Review struct {
						UserVID any `json:"userVid"`
						Author  struct {
							UserVID any `json:"userVid"`
						} `json:"author"`
					} `json:"review"`
				} `json:"reviews"`
			}
			if revErr := c.call(ctx, "/review/list/mine", map[string]any{"bookid": targetBookID}, &bookRevResp); revErr == nil {
				if vid := extractVID(bookRevResp.Reviews); vid != "" {
					return fmt.Sprintf("weread:%s", vid), nil
				}
			}
		}
	}

	if err != nil {
		return "", fmt.Errorf("WeRead account identity resolution failed: %w (write a review or set WEREAD_VID / --source-vid to bind identity)", err)
	}
	return "", fmt.Errorf("WeRead account has zero reviews and no valid declared VID; write at least one review or set WEREAD_VID to bind account identity")
}
