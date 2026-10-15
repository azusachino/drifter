package companion

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"
)

// ShelfBook holds only facts needed to select an ebook worklist. Albums and
// article collections are outside this command's explicitly ebook-only scope.
type ShelfBook struct {
	ID         string `json:"bookId"`
	Title      string `json:"title"`
	Author     string `json:"author"`
	LastOpened *int64 `json:"readUpdateTime"`
	Secret     *int   `json:"secret"`
}

type ShelfResult struct {
	Books    []ShelfBook
	Complete bool
}

// Imported IDs are valid shelf identities, not supported link identities.
func validShelfID(id string) bool {
	if ValidBookID(id) {
		return true
	}
	if !strings.HasPrefix(id, "CB_") || len(id) <= 3 || len(id) > 128 {
		return false
	}
	for _, r := range id[3:] {
		if !(r >= '0' && r <= '9' || r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r == '_' || r == '-') {
			return false
		}
	}
	return true
}

func (w *WeRead) Shelf(ctx context.Context) (ShelfResult, error) {
	var response struct {
		Books   []ShelfBook `json:"books"`
		Count   *int        `json:"bookCount"`
		HasMore *int        `json:"hasMore"`
	}
	if err := w.call(ctx, "/shelf/sync", map[string]any{}, &response); err != nil {
		return ShelfResult{}, err
	}
	if response.Books == nil || (response.Count != nil && *response.Count < 0) ||
		(response.HasMore != nil && *response.HasMore != 0 && *response.HasMore != 1) {
		return ShelfResult{}, fmt.Errorf("WeRead shelf fields missing or invalid")
	}
	seen := make(map[string]bool, len(response.Books))
	for _, book := range response.Books {
		if !validShelfID(book.ID) || seen[book.ID] {
			return ShelfResult{}, fmt.Errorf("WeRead shelf identity invalid or duplicated")
		}
		seen[book.ID] = true
	}
	complete := response.Count != nil && *response.Count == len(response.Books) &&
		(response.HasMore == nil || *response.HasMore == 0)
	return ShelfResult{Books: response.Books, Complete: complete}, nil
}

type PendingOptions struct {
	SourceAccount   string
	TargetAccount   string
	Days            int
	IncludePrivate  bool
	IncludeImported bool
	Now             time.Time
	After           string
}

type PendingBook struct {
	Book
	Classification string `json:"classification"`
	LastOpened     int64  `json:"last_opened"`
	Private        bool   `json:"private"`
}

type PendingIssue struct {
	BookID string `json:"book_id,omitempty"`
	Code   string `json:"code"`
}

type PendingReport struct {
	Books      []PendingBook  `json:"books"`
	Complete   bool           `json:"complete"`
	Issues     []PendingIssue `json:"issues"`
	NextCursor string         `json:"next_cursor,omitempty"`
}

type pendingCursor struct {
	Version         int    `json:"v"`
	AsOf            int64  `json:"as_of"`
	Days            int    `json:"days"`
	SourceAccount   string `json:"source_account"`
	TargetAccount   string `json:"target_account"`
	IncludePrivate  bool   `json:"include_private"`
	IncludeImported bool   `json:"include_imported"`
	LastOpened      int64  `json:"last_opened"`
	BookID          string `json:"book_id"`
}

func encodePendingCursor(c pendingCursor) string {
	data, _ := json.Marshal(c)
	return base64.RawURLEncoding.EncodeToString(data)
}

func ValidatePendingCursor(options PendingOptions) error {
	if options.After == "" {
		return nil
	}
	_, err := decodePendingCursor(options.After, options)
	return err
}

func decodePendingCursor(raw string, options PendingOptions) (pendingCursor, error) {
	var c pendingCursor
	if len(raw) > 2048 {
		return c, fmt.Errorf("invalid pending cursor")
	}
	data, err := base64.RawURLEncoding.DecodeString(raw)
	if err != nil || json.Unmarshal(data, &c) != nil {
		return c, fmt.Errorf("invalid pending cursor")
	}
	if c.Version != 1 || c.Days != options.Days || c.SourceAccount != options.SourceAccount || c.TargetAccount != options.TargetAccount || c.IncludePrivate != options.IncludePrivate || c.IncludeImported != options.IncludeImported || c.AsOf <= 0 || c.AsOf > options.Now.Unix() || c.LastOpened <= 0 || c.LastOpened > c.AsOf || !validShelfID(c.BookID) {
		return c, fmt.Errorf("invalid pending cursor")
	}
	return c, nil
}

type worklistSource interface {
	Shelf(context.Context) (ShelfResult, error)
	Book(context.Context, string) (Book, error)
}

// At most 100 book-info requests per invocation; confirm a batch or narrow the
// window before rerunning. No persistent shelf snapshot or progress archive.
const pendingLimit = 100

func Pending(ctx context.Context, reader worklistSource, mappings []Mapping, options PendingOptions) (PendingReport, error) {
	return PendingWithDecisions(ctx, reader, mappings, nil, options)
}

func PendingWithDecisions(ctx context.Context, reader worklistSource, mappings []Mapping, decisions []Decision, options PendingOptions) (PendingReport, error) {
	report := PendingReport{Books: []PendingBook{}, Issues: []PendingIssue{}}
	if !ValidLabel(options.SourceAccount) || !ValidLabel(options.TargetAccount) || options.Days < 1 || options.Days > 36500 || options.Now.IsZero() {
		return report, fmt.Errorf("pending requires account labels, time and a window of 1–36500 days")
	}
	if err := ctx.Err(); err != nil {
		return report, err
	}
	shelf, err := reader.Shelf(ctx)
	if err != nil {
		return report, err
	}
	report.Complete = shelf.Complete
	asOf := options.Now
	var cursor pendingCursor
	if options.After != "" {
		cursor, err = decodePendingCursor(options.After, options)
		if err != nil {
			return report, err
		}
		asOf = time.Unix(cursor.AsOf, 0)
	}
	if !shelf.Complete {
		report.Issues = append(report.Issues, PendingIssue{Code: "incomplete_shelf"})
	}
	mapped := make(map[string]bool)
	for _, d := range decisions {
		if d.Provider == "weread" && d.SourceAccount == options.SourceAccount && d.TargetAccount == options.TargetAccount && (d.Kind == "exclude" || d.Kind == "not-in-catalog") {
			mapped[d.BookID] = true
		}
	}
	for _, m := range mappings {
		if m.Provider == "weread" && m.SourceAccount == options.SourceAccount && m.TargetAccount == options.TargetAccount {
			mapped[m.BookID] = true
		}
	}
	// Filter privacy before inspecting activity, titles or metadata.
	eligible := []ShelfBook{}
	cutoff, now := asOf.Add(-time.Duration(options.Days)*24*time.Hour).Unix(), asOf.Unix()
	for _, row := range shelf.Books {
		if row.Secret == nil || (*row.Secret != 0 && *row.Secret != 1) || (*row.Secret == 1 && !options.IncludePrivate) {
			continue
		}
		if mapped[row.ID] || (strings.HasPrefix(row.ID, "CB_") && !options.IncludeImported) {
			continue
		}
		if row.LastOpened == nil || *row.LastOpened < 0 || *row.LastOpened > now {
			report.Complete = false
			report.Issues = append(report.Issues, PendingIssue{row.ID, "invalid_activity"})
			continue
		}
		if *row.LastOpened == 0 || *row.LastOpened < cutoff {
			continue
		}
		if options.After != "" && (*row.LastOpened > cursor.LastOpened || (*row.LastOpened == cursor.LastOpened && row.ID <= cursor.BookID)) {
			continue
		}
		eligible = append(eligible, row)
	}
	sort.Slice(eligible, func(i, j int) bool {
		if *eligible[i].LastOpened != *eligible[j].LastOpened {
			return *eligible[i].LastOpened > *eligible[j].LastOpened
		}
		return eligible[i].ID < eligible[j].ID
	})
	more := len(eligible) > pendingLimit
	if more {
		eligible = eligible[:pendingLimit]
	}
	if more {
		report.Complete = false
		report.Issues = append(report.Issues, PendingIssue{Code: "worklist_limit"})
	}
	for _, row := range eligible {
		if err := ctx.Err(); err != nil {
			return report, err
		}
		cursor = pendingCursor{Version: 1, AsOf: asOf.Unix(), Days: options.Days, SourceAccount: options.SourceAccount, TargetAccount: options.TargetAccount, IncludePrivate: options.IncludePrivate, IncludeImported: options.IncludeImported, LastOpened: *row.LastOpened, BookID: row.ID}
		report.NextCursor = encodePendingCursor(cursor)
		book := Book{ID: row.ID, Title: row.Title, Author: row.Author}
		classification := "imported"
		if !strings.HasPrefix(row.ID, "CB_") {
			book, err = reader.Book(ctx, row.ID)
			if err := ctx.Err(); err != nil {
				return report, err
			}
			if err != nil || book.ID != row.ID || strings.TrimSpace(book.Title) == "" {
				report.Complete = false
				report.Issues = append(report.Issues, PendingIssue{row.ID, "metadata_lookup_failed"})
				continue
			}
			if !book.TranslatorKnown {
				report.Complete = false
				report.Issues = append(report.Issues, PendingIssue{row.ID, "translator_unknown"})
				continue
			}
			classification = "native"
			if strings.TrimSpace(book.Translator) != "" {
				classification = "translated"
			}
		} else if strings.TrimSpace(book.Title) == "" {
			report.Complete = false
			report.Issues = append(report.Issues, PendingIssue{row.ID, "metadata_missing"})
			continue
		}
		report.Books = append(report.Books, PendingBook{book, classification, *row.LastOpened, *row.Secret == 1})
	}
	if !more {
		report.NextCursor = ""
	}
	return report, nil
}
