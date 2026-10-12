package companion

import (
	"context"
	"encoding/json"
	"fmt"
	"time"
)

const weReadURL = "https://i.weread.qq.com/api/agent/gateway"

type WeRead struct{ api *api }

func NewWeRead(token string) (*WeRead, error) {
	a, err := newAPI("WeRead", weReadURL, token, time.Second)
	return &WeRead{a}, err
}

func (w *WeRead) call(ctx context.Context, name string, args map[string]any, target any) error {
	// Only explicit read operations exist; there is no generic exported gateway.
	switch name {
	case "/store/search", "/book/info", "/book/getprogress":
	default:
		return fmt.Errorf("unsupported WeRead operation")
	}
	args["api_name"], args["skill_version"] = name, "1.0.5"
	var raw json.RawMessage
	if err := w.api.post(ctx, args, &raw); err != nil {
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

type SearchResult struct {
	Books   []Book `json:"books"`
	HasMore bool   `json:"has_more"`
}

func (w *WeRead) Search(ctx context.Context, title string) (SearchResult, error) {
	if title == "" || len(title) > 1024 {
		return SearchResult{}, fmt.Errorf("title required (maximum 1024 bytes)")
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
	if err := w.call(ctx, "/store/search", map[string]any{"keyword": title, "scope": 10}, &response); err != nil {
		return SearchResult{}, err
	}
	if response.Results == nil || response.HasMore == nil || (*response.HasMore != 0 && *response.HasMore != 1) {
		return SearchResult{}, fmt.Errorf("WeRead search results or pagination fields missing or invalid")
	}
	result := SearchResult{Books: []Book{}, HasMore: *response.HasMore != 0}
	for _, group := range response.Results {
		for _, row := range group.Books {
			b := row.Info
			if !ValidBookID(b.ID) || b.Title == "" {
				return SearchResult{}, fmt.Errorf("WeRead search returned invalid identity")
			}
			result.Books = append(result.Books, Book{ID: b.ID, Title: b.Title, Author: b.Author, ReaderURL: b.URL})
		}
	}
	return result, nil
}

func (w *WeRead) Book(ctx context.Context, id string) (Book, error) {
	if !ValidBookID(id) {
		return Book{}, fmt.Errorf("invalid WeRead book ID")
	}
	var row struct {
		ID         string `json:"bookId"`
		Title      string `json:"title"`
		Author     string `json:"author"`
		Translator string `json:"translator"`
		ISBN       string `json:"isbn"`
		Publisher  string `json:"publisher"`
		URL        string `json:"readerLink"`
	}
	if err := w.call(ctx, "/book/info", map[string]any{"bookId": id}, &row); err != nil {
		return Book{}, err
	}
	if row.ID != id || row.Title == "" {
		return Book{}, fmt.Errorf("WeRead book identity missing or differs from request")
	}
	return Book{ID: row.ID, Title: row.Title, Author: row.Author, Translator: row.Translator, ISBN: row.ISBN, Publisher: row.Publisher, ReaderURL: row.URL}, nil
}

func (w *WeRead) Progress(ctx context.Context, id string) (Progress, error) {
	if !ValidBookID(id) {
		return Progress{}, fmt.Errorf("invalid WeRead book ID")
	}
	var row struct {
		ID   string `json:"bookId"`
		Book *struct {
			Percent  *int  `json:"progress"`
			Started  *int  `json:"isStartReading"`
			Finished int64 `json:"finishTime"`
		} `json:"book"`
	}
	if err := w.call(ctx, "/book/getprogress", map[string]any{"bookId": id}, &row); err != nil {
		return Progress{}, err
	}
	if row.ID != id || row.Book == nil || row.Book.Percent == nil || row.Book.Started == nil {
		return Progress{}, fmt.Errorf("WeRead progress identity or required fields missing")
	}
	return progressStatus(*row.Book.Percent, *row.Book.Started, row.Book.Finished)
}
