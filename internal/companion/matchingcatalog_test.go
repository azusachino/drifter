package companion

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/azusachino/drifter/internal/matchingcatalog"
)

type catalogMatchTarget struct {
	matchCalls    int
	validateCalls int
}

func (target *catalogMatchTarget) Match(_ context.Context, book Book, query string) (MatchReport, error) {
	target.matchCalls++
	return MatchReport{Book: book, SearchQuery: query, RequiresConfirmation: true}, nil
}

func (target *catalogMatchTarget) ValidateLink(context.Context, int64, int64) error {
	target.validateCalls++
	return nil
}

func TestMatchCoreSurfacesExactCatalogEvidenceAndConflicts(t *testing.T) {
	catalogJSON := `{
	  "catalog_schema_version": 1,
	  "catalog_id": "synthetic.crosswalk",
	  "publisher": "Synthetic publisher",
	  "assertions": [
	    {"record_id":"claim-a","source":{"provider":"weread","id":"42"},"target":{"provider":"hardcover","work_id":77},"provenance":[{"source":"test fixture","reference":"row a"}]},
	    {"record_id":"claim-b","source":{"provider":"weread","id":"42"},"target":{"provider":"hardcover","work_id":88,"edition_id":89},"provenance":[{"source":"test fixture","reference":"row b"}]},
	    {"record_id":"other-book","source":{"provider":"weread","id":"99"},"target":{"provider":"hardcover","work_id":99},"provenance":[{"source":"test fixture","reference":"other"}]}
	  ],
	  "hints": [{"record_id":"hint-a","source":{"provider":"weread","id":"42"},"title":"Synthetic title","isbn":"9780306406157","confidence":0.5,"provenance":[{"source":"test fixture","reference":"hint a"}]}
	  ]
	}`
	path := filepath.Join(t.TempDir(), "catalog.json")
	if err := os.WriteFile(path, []byte(catalogJSON), 0o600); err != nil {
		t.Fatal(err)
	}
	secondPath := filepath.Join(t.TempDir(), "second-catalog.json")
	secondCatalog := `{"catalog_schema_version":1,"catalog_id":"synthetic.second","publisher":"Synthetic second","assertions":[{"record_id":"claim-c","source":{"provider":"weread","id":"42"},"target":{"provider":"hardcover","work_id":90},"provenance":[{"source":"second fixture","reference":"row c"}]}],"hints":[]}`
	if err := os.WriteFile(secondPath, []byte(secondCatalog), 0o600); err != nil {
		t.Fatal(err)
	}
	loader, err := matchingcatalog.NewLoader([]matchingcatalog.Subscription{
		{ID: "synthetic", File: path},
		{ID: "second", File: secondPath},
	}, matchingcatalog.LoaderOptions{})
	if err != nil {
		t.Fatal(err)
	}
	target := &catalogMatchTarget{}
	report, err := MatchWithOptions(context.Background(), operationSource{}, target, "42", "explicit query", MatchOptions{CatalogLoader: loader})
	if err != nil {
		t.Fatal(err)
	}
	if target.matchCalls != 1 || target.validateCalls != 0 {
		t.Fatalf("catalog claims changed provider or link behavior: target=%+v", target)
	}
	if report.CatalogsComplete == nil || !*report.CatalogsComplete || len(report.CatalogAssertions) != 3 || len(report.CatalogHints) != 1 || len(report.CatalogConflicts) != 1 || len(report.CatalogIssues) != 0 {
		t.Fatalf("catalog evidence missing or incomplete: %+v", report)
	}
	if report.CatalogAssertions[0].Provenance[0].Reference != "row a" || report.CatalogHints[0].Title != "Synthetic title" || report.CatalogHints[0].ISBN != "9780306406157" || report.CatalogConflicts[0].Claims[1].Target.WorkID != 88 || report.CatalogConflicts[0].Claims[2].SubscriptionID != "second" {
		t.Fatalf("catalog evidence lost provenance, hints, or alternatives: %+v", report)
	}
	if !report.RequiresConfirmation || report.SearchQuery != "explicit query" || report.Book.ID != "42" {
		t.Fatalf("catalog evidence changed matching or confirmation: %+v", report)
	}
}

func TestMatchWithoutCatalogsPreservesLegacyJSONAndRequests(t *testing.T) {
	target := &catalogMatchTarget{}
	legacy, err := Match(context.Background(), operationSource{}, target, "42", "query")
	if err != nil {
		t.Fatal(err)
	}
	legacyJSON, err := json.Marshal(legacy)
	if err != nil {
		t.Fatal(err)
	}
	withEmptyLoader, err := matchingcatalog.NewLoader(nil, matchingcatalog.LoaderOptions{})
	if err != nil {
		t.Fatal(err)
	}
	current, err := MatchWithOptions(context.Background(), operationSource{}, target, "42", "query", MatchOptions{CatalogLoader: withEmptyLoader})
	if err != nil {
		t.Fatal(err)
	}
	currentJSON, err := json.Marshal(current)
	if err != nil {
		t.Fatal(err)
	}
	if string(legacyJSON) != string(currentJSON) || target.matchCalls != 2 || target.validateCalls != 0 || strings.Contains(string(currentJSON), "catalogs_complete") {
		t.Fatalf("empty catalog config changed legacy output/requests: legacy=%s current=%s target=%+v", legacyJSON, currentJSON, target)
	}
}

func TestMatchKeepsProviderResultsWhenCatalogIsInvalid(t *testing.T) {
	path := filepath.Join(t.TempDir(), "catalog.json")
	if err := os.WriteFile(path, []byte(`{"catalog_schema_version": 1, "unexpected": true}`), 0o600); err != nil {
		t.Fatal(err)
	}
	loader, err := matchingcatalog.NewLoader([]matchingcatalog.Subscription{{ID: "safe-alias", File: path}}, matchingcatalog.LoaderOptions{})
	if err != nil {
		t.Fatal(err)
	}
	target := &catalogMatchTarget{}
	report, err := MatchWithOptions(context.Background(), operationSource{}, target, "42", "", MatchOptions{CatalogLoader: loader})
	if err != nil {
		t.Fatal(err)
	}
	if report.CatalogsComplete == nil || *report.CatalogsComplete || len(report.CatalogIssues) != 1 || report.CatalogIssues[0].SubscriptionID != "safe-alias" || report.CatalogIssues[0].Code != matchingcatalog.IssueInvalidDocument {
		t.Fatalf("invalid catalog issue was not attached: %+v", report)
	}
	if target.matchCalls != 1 || !report.RequiresConfirmation || report.Book.ID != "42" {
		t.Fatalf("provider result was not preserved: %+v target=%+v", report, target)
	}
}
