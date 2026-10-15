package companion

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/azusachino/drifter/internal/catalog"
)

const hardcoverURL = "https://api.hardcover.app/v1/graphql"

type Hardcover struct{ api *api }

func NewHardcover(token string) (*Hardcover, error) {
	a, err := newAPI("Hardcover", hardcoverURL, token, 1100*time.Millisecond)
	return &Hardcover{a}, err
}

func (h *Hardcover) query(ctx context.Context, query string, variables any, target any) error {
	if !strings.HasPrefix(strings.TrimSpace(query), "query ") {
		return fmt.Errorf("only Hardcover queries are supported")
	}
	var response struct {
		Data   json.RawMessage   `json:"data"`
		Errors []json.RawMessage `json:"errors"`
	}
	if err := h.api.post(ctx, map[string]any{"query": query, "variables": variables}, &response); err != nil {
		return err
	}
	if len(response.Errors) > 0 || len(response.Data) == 0 || string(response.Data) == "null" {
		return fmt.Errorf("Hardcover query rejected or data missing")
	}
	if err := json.Unmarshal(response.Data, target); err != nil {
		return fmt.Errorf("Hardcover query response has invalid fields")
	}
	return nil
}

type Edition struct {
	ID       int64  `json:"id"`
	WorkID   int64  `json:"book_id"`
	Title    string `json:"title"`
	ISBN13   string `json:"isbn_13,omitempty"`
	ISBN10   string `json:"isbn_10,omitempty"`
	Language *struct {
		Code string `json:"code2"`
	} `json:"language,omitempty"`
}

type Candidate struct {
	ID            int64  `json:"id"`
	Title         string `json:"title"`
	Slug          string `json:"slug"`
	Contributions []struct {
		Author struct {
			Name string `json:"name"`
		} `json:"author"`
	} `json:"contributions"`
	Editions []Edition `json:"editions"`
}

func (c Candidate) URL() string { return "https://hardcover.app/books/" + url.PathEscape(c.Slug) }

const editionFields = `id book_id title isbn_13 isbn_10 language {code2}`
const candidateFields = `id title slug contributions {author {name}} editions(limit: 10, order_by: {id: asc}) {` + editionFields + `}`

func isbnVariants(raw string) ([]string, error) {
	isbn, err := NormalizeISBN(raw)
	if err != nil {
		return nil, err
	}
	ids := []string{isbn}
	if strings.HasPrefix(isbn, "978") {
		prefix, sum := isbn[3:12], 0
		for i, r := range prefix {
			sum += int(r-'0') * (10 - i)
		}
		check := (11 - sum%11) % 11
		last := fmt.Sprint(check)
		if check == 10 {
			last = "X"
		}
		ids = append(ids, prefix+last)
	}
	return ids, nil
}

type CatalogAssertion struct {
	SubscriptionID string               `json:"subscription_id"`
	CatalogID      string               `json:"catalog_id"`
	Publisher      string               `json:"publisher"`
	RecordID       string               `json:"record_id"`
	Source         catalog.SourceRef    `json:"source"`
	Target         catalog.TargetRef    `json:"target"`
	Provenance     []catalog.Provenance `json:"provenance"`
}

type CatalogHint struct {
	SubscriptionID string               `json:"subscription_id"`
	CatalogID      string               `json:"catalog_id"`
	Publisher      string               `json:"publisher"`
	RecordID       string               `json:"record_id"`
	Source         catalog.SourceRef    `json:"source"`
	Title          string               `json:"title,omitempty"`
	OriginalAuthor string               `json:"original_author,omitempty"`
	ISBN           string               `json:"isbn,omitempty"`
	Confidence     float64              `json:"confidence"`
	Provenance     []catalog.Provenance `json:"provenance"`
}

type CatalogConflict struct {
	Source catalog.SourceRef  `json:"source"`
	Claims []CatalogAssertion `json:"claims"`
}

type MatchReport struct {
	Book                 Book               `json:"source_book"`
	ISBNState            string             `json:"isbn_state"`
	Resolution           string             `json:"resolution"`
	ExactEditions        []Edition          `json:"exact_editions"`
	Candidates           []Candidate        `json:"work_candidates"`
	SearchQuery          string             `json:"search_query,omitempty"`
	SearchLimit          int                `json:"search_limit"`
	RequiresConfirmation bool               `json:"requires_confirmation"`
	CatalogsComplete     *bool              `json:"catalogs_complete,omitempty"`
	CatalogAssertions    []CatalogAssertion `json:"catalog_assertions,omitempty"`
	CatalogHints         []CatalogHint      `json:"catalog_hints,omitempty"`
	CatalogConflicts     []CatalogConflict  `json:"catalog_conflicts,omitempty"`
	CatalogIssues        []catalog.Issue    `json:"catalog_issues,omitempty"`
}

func (h *Hardcover) Match(ctx context.Context, book Book, search string) (MatchReport, error) {
	report := MatchReport{Book: book, ISBNState: "missing", Resolution: "unresolved", ExactEditions: []Edition{}, Candidates: []Candidate{}, SearchLimit: 10, RequiresConfirmation: true}
	if book.ISBN != "" {
		ids, err := isbnVariants(book.ISBN)
		if err != nil {
			report.ISBNState = "invalid"
		} else {
			report.ISBNState = "valid"
			var response struct {
				Editions []Edition `json:"editions"`
			}
			q := `query Exact($ids: [String!]!) { editions(where: {_or: [{isbn_13: {_in: $ids}}, {isbn_10: {_in: $ids}}]}, limit: 100, order_by: {id: asc}) {` + editionFields + `} }`
			if err := h.query(ctx, q, map[string]any{"ids": ids}, &response); err != nil {
				return report, err
			}
			if response.Editions == nil || len(response.Editions) > 100 {
				return report, fmt.Errorf("Hardcover edition results missing or exceeded requested limit")
			}
			for _, e := range response.Editions {
				if e.ID <= 0 || e.WorkID <= 0 {
					return report, fmt.Errorf("Hardcover returned invalid edition identity")
				}
			}
			report.ExactEditions = response.Editions
			if len(response.Editions) > 0 {
				report.Resolution = "exact_edition_candidate"
				if len(response.Editions) > 1 {
					report.Resolution = "ambiguous_identifier"
				}
				return report, nil
			}
		}
	}
	if search == "" {
		search = book.Title
	}
	if search == "" || len(search) > 1024 {
		return report, fmt.Errorf("search query required (maximum 1024 bytes)")
	}
	report.SearchQuery = search
	var response struct {
		Search *struct {
			IDs   []int64 `json:"ids"`
			Error *string `json:"error"`
		} `json:"search"`
	}
	if err := h.query(ctx, `query Search($term: String!) { search(query: $term, query_type: "Book", per_page: 10, page: 1) {ids error} }`, map[string]any{"term": search}, &response); err != nil {
		return report, err
	}
	if response.Search == nil || (response.Search.Error != nil && *response.Search.Error != "") {
		return report, fmt.Errorf("Hardcover search failed")
	}
	ids := response.Search.IDs
	if len(ids) > 10 {
		return report, fmt.Errorf("Hardcover search exceeded requested limit")
	}
	for _, id := range ids {
		if id <= 0 {
			return report, fmt.Errorf("Hardcover returned invalid work identity")
		}
	}
	if len(ids) == 0 {
		return report, nil
	}
	var works struct {
		Books []Candidate `json:"books"`
	}
	q := `query Works($ids: [Int!]!) { books(where: {id: {_in: $ids}}, limit: 10) {` + candidateFields + `} }`
	if err := h.query(ctx, q, map[string]any{"ids": ids}, &works); err != nil {
		return report, err
	}
	for _, id := range ids {
		for _, candidate := range works.Books {
			if candidate.ID == id {
				report.Candidates = append(report.Candidates, candidate)
			}
		}
	}
	if len(report.Candidates) != len(ids) {
		return report, fmt.Errorf("Hardcover search results could not all be resolved")
	}
	report.Resolution = "work_candidates"
	return report, nil
}

func (h *Hardcover) ValidateLink(ctx context.Context, workID, editionID int64) error {
	if workID <= 0 || editionID < 0 {
		return fmt.Errorf("invalid target IDs")
	}
	var response struct {
		Books []struct {
			ID int64 `json:"id"`
		} `json:"books"`
	}
	if err := h.query(ctx, `query Work($id: Int!) { books(where: {id: {_eq: $id}}, limit: 1) {id} }`, map[string]any{"id": workID}, &response); err != nil {
		return err
	}
	if len(response.Books) != 1 || response.Books[0].ID != workID {
		return fmt.Errorf("Hardcover work not found")
	}
	if editionID == 0 {
		return nil
	}
	var editions struct {
		Editions []Edition `json:"editions"`
	}
	q := `query Edition($work: Int!, $edition: Int!) { editions(where: {book_id: {_eq: $work}, id: {_eq: $edition}}, limit: 1) {` + editionFields + `} }`
	if err := h.query(ctx, q, map[string]any{"work": workID, "edition": editionID}, &editions); err != nil {
		return err
	}
	if len(editions.Editions) != 1 || editions.Editions[0].ID != editionID || editions.Editions[0].WorkID != workID {
		return fmt.Errorf("edition does not belong to the selected work")
	}
	return nil
}
