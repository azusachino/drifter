package companion

import (
	"net/url"

	"github.com/azusachino/drifter/internal/catalog"
	"github.com/azusachino/drifter/internal/isbn"
)

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
	ISBNMetadata         *isbn.Metadata     `json:"isbn_metadata,omitempty"`
}
