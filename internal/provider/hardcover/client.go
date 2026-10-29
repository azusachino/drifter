// Package hardcover implements the catalog destination adapter for Hardcover GraphQL API.
package hardcover

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/azusachino/drifter/internal/companion"
	"github.com/azusachino/drifter/internal/provider/transport"
)

// DefaultURL is the production Hardcover GraphQL endpoint.
const DefaultURL = "https://api.hardcover.app/v1/graphql"

// Client is the Hardcover destination provider client.
type Client struct {
	api    *transport.Client
	userMu sync.Mutex
	userID int64
}

var (
	_ companion.TargetProvider = (*Client)(nil)
	_ companion.PreviewTarget  = (*Client)(nil)
	_ companion.ApplyTarget    = (*Client)(nil)
)

// New constructs an authenticated Hardcover client using DefaultURL and a 1100ms rate-limit interval.
func New(token string) (*Client, error) {
	api, err := transport.NewClient("Hardcover", DefaultURL, token, 1100*time.Millisecond)
	if err != nil {
		return nil, err
	}
	return &Client{api: api}, nil
}

// NewWithTransport creates a client with a custom transport (useful for testing).
func NewWithTransport(api *transport.Client) *Client {
	return &Client{api: api}
}

// NewWithTransportAndUserID creates a client with a custom transport and pre-set user ID.
func NewWithTransportAndUserID(api *transport.Client, userID int64) *Client {
	return &Client{api: api, userID: userID}
}

func (c *Client) query(ctx context.Context, query string, variables any, target any) error {
	if !strings.HasPrefix(strings.TrimSpace(query), "query ") {
		return fmt.Errorf("only Hardcover queries are supported")
	}
	var response struct {
		Data   json.RawMessage   `json:"data"`
		Errors []json.RawMessage `json:"errors"`
	}
	if err := c.api.Post(ctx, map[string]any{"query": query, "variables": variables}, &response); err != nil {
		return err
	}
	if len(response.Errors) > 0 || len(response.Data) == 0 || string(response.Data) == "null" {
		return fmt.Errorf("Hardcover query rejected or data missing")
	}
	if err := json.Unmarshal(response.Data, target); err != nil {
		return fmt.Errorf("Hardcover query response has invalid fields: %w", err)
	}
	return nil
}

const editionFields = `id book_id title isbn_13 isbn_10 language {code2}`
const candidateFields = `id title slug contributions {author {name}} editions(limit: 10, order_by: {id: asc}) {` + editionFields + `}`

var titleParenthesesRegex = regexp.MustCompile(`\s*[(（][^()（）]+[)）]\s*$`)

func cleanSearchTitle(title string) string {
	cleaned := strings.TrimSpace(title)
	for {
		stripped := titleParenthesesRegex.ReplaceAllString(cleaned, "")
		stripped = strings.TrimSpace(stripped)
		if stripped == cleaned || stripped == "" {
			break
		}
		cleaned = stripped
	}
	return cleaned
}

func (c *Client) searchIDs(ctx context.Context, term string) ([]int64, error) {
	var response struct {
		Search *struct {
			IDs   []int64 `json:"ids"`
			Error *string `json:"error"`
		} `json:"search"`
	}
	if err := c.query(ctx, `query Search($term: String!) { search(query: $term, query_type: "Book", per_page: 10, page: 1) {ids error} }`, map[string]any{"term": term}, &response); err != nil {
		return nil, err
	}
	if response.Search == nil || (response.Search.Error != nil && *response.Search.Error != "") {
		return nil, fmt.Errorf("Hardcover search failed")
	}
	ids := response.Search.IDs
	if len(ids) > 10 {
		return nil, fmt.Errorf("Hardcover search exceeded requested limit")
	}
	for _, id := range ids {
		if id <= 0 {
			return nil, fmt.Errorf("Hardcover returned invalid work identity")
		}
	}
	return ids, nil
}

// Match searches Hardcover for editions matching book.ISBN or work titles matching search query.
func (c *Client) Match(ctx context.Context, book companion.Book, search string) (companion.MatchReport, error) {
	report := companion.MatchReport{
		Book:                 book,
		ISBNState:            "missing",
		Resolution:           "unresolved",
		ExactEditions:        []companion.Edition{},
		Candidates:           []companion.Candidate{},
		SearchLimit:          10,
		RequiresConfirmation: true,
	}
	if book.ISBN != "" {
		ids, err := companion.ISBNVariants(book.ISBN)
		if err != nil {
			report.ISBNState = "invalid"
		} else {
			report.ISBNState = "valid"
			var response struct {
				Editions []companion.Edition `json:"editions"`
			}
			q := `query Exact($ids: [String!]!) { editions(where: {_or: [{isbn_13: {_in: $ids}}, {isbn_10: {_in: $ids}}]}, limit: 100, order_by: {id: asc}) {` + editionFields + `} }`
			if err := c.query(ctx, q, map[string]any{"ids": ids}, &response); err != nil {
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

	ids, err := c.searchIDs(ctx, search)
	if err != nil {
		return report, err
	}
	if len(ids) == 0 && search == book.Title {
		if cleaned := cleanSearchTitle(search); cleaned != "" && cleaned != search {
			cleanIDs, err := c.searchIDs(ctx, cleaned)
			if err == nil && len(cleanIDs) > 0 {
				ids = cleanIDs
				report.SearchQuery = cleaned
			}
		}
	}
	if len(ids) == 0 {
		return report, nil
	}
	var works struct {
		Books []companion.Candidate `json:"books"`
	}
	q := `query Works($ids: [Int!]!) { books(where: {id: {_in: $ids}}, limit: 10) {` + candidateFields + `} }`
	if err := c.query(ctx, q, map[string]any{"ids": ids}, &works); err != nil {
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

// ValidateLink verifies whether workID exists and if editionID is non-zero, that it belongs to workID.
func (c *Client) ValidateLink(ctx context.Context, workID, editionID int64) error {
	if workID <= 0 || editionID < 0 {
		return fmt.Errorf("invalid target IDs")
	}
	var response struct {
		Books []struct {
			ID int64 `json:"id"`
		} `json:"books"`
	}
	if err := c.query(ctx, `query Work($id: Int!) { books(where: {id: {_eq: $id}}, limit: 1) {id} }`, map[string]any{"id": workID}, &response); err != nil {
		return err
	}
	if len(response.Books) != 1 || response.Books[0].ID != workID {
		return fmt.Errorf("Hardcover work not found")
	}
	if editionID == 0 {
		return nil
	}
	var editions struct {
		Editions []companion.Edition `json:"editions"`
	}
	q := `query Edition($work: Int!, $edition: Int!) { editions(where: {book_id: {_eq: $work}, id: {_eq: $edition}}, limit: 1) {` + editionFields + `} }`
	if err := c.query(ctx, q, map[string]any{"work": workID, "edition": editionID}, &editions); err != nil {
		return err
	}
	if len(editions.Editions) != 1 || editions.Editions[0].ID != editionID || editions.Editions[0].WorkID != workID {
		return fmt.Errorf("edition does not belong to the selected work")
	}
	return nil
}

// GetUserID queries and caches the authenticated user ID on Hardcover.
func (c *Client) GetUserID(ctx context.Context) (int64, error) {
	c.userMu.Lock()
	defer c.userMu.Unlock()
	if c.userID > 0 {
		return c.userID, nil
	}

	var response struct {
		Me json.RawMessage `json:"me"`
	}
	q := `query Me { me { id username } }`
	if err := c.query(ctx, q, nil, &response); err != nil {
		return 0, err
	}
	if len(response.Me) == 0 || string(response.Me) == "null" {
		return 0, fmt.Errorf("Hardcover user identity missing or invalid in query response")
	}

	// Try unmarshaling as array (live GraphQL query)
	var meList []struct {
		ID       int64  `json:"id"`
		Username string `json:"username"`
	}
	if err := json.Unmarshal(response.Me, &meList); err == nil && len(meList) > 0 {
		if meList[0].ID <= 0 {
			return 0, fmt.Errorf("Hardcover user identity missing or invalid in query response")
		}
		c.userID = meList[0].ID
		return c.userID, nil
	}

	// Try unmarshaling as single object
	var meObj struct {
		ID       int64  `json:"id"`
		Username string `json:"username"`
	}
	if err := json.Unmarshal(response.Me, &meObj); err == nil && meObj.ID > 0 {
		c.userID = meObj.ID
		return c.userID, nil
	}

	return 0, fmt.Errorf("Hardcover user identity missing or invalid in query response")
}

// ResolveAccountFingerprint queries the authenticated Hardcover user and returns its stable identity fingerprint.
func (c *Client) ResolveAccountFingerprint(ctx context.Context) (string, error) {
	id, err := c.GetUserID(ctx)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("hardcover:%d", id), nil
}
