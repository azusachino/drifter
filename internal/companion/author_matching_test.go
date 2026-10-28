package companion

import (
	"context"
	"encoding/json"
	"reflect"
	"testing"

	"github.com/azusachino/drifter/internal/catalog"
)

type authorMatchTarget struct {
	catalogMatchTarget
	report MatchReport
	book   Book
	query  string
}

func (target *authorMatchTarget) Match(_ context.Context, book Book, query string) (MatchReport, error) {
	target.matchCalls++
	target.book, target.query = book, query
	return target.report, nil
}

func TestMatchExpectedAuthorStablePartition(t *testing.T) {
	var candidates []Candidate
	if err := json.Unmarshal([]byte(`[
		{"id":1,"contributions":[{"author":{"name":"Other"}}]},
		{"id":2,"contributions":[{"author":{"name":"  ÉMILIE\tdu\nCHÂTELET "}}]},
		{"id":3},
		{"id":4,"contributions":[{"author":{"name":"Other"}},{"author":{"name":"émilie du châtelet"}}]},
		{"id":5,"contributions":[{"author":{"name":"Emilie du Chatelet"}}]},
		{"id":6,"contributions":[{"author":{"name":"Émilie du Châtelet Jr."}}]},
		{"id":7,"contributions":[{"author":{"name":" \t "}}]},
		{"id":8,"contributions":[{"author":{"name":"Σ"}}]}
	]`), &candidates); err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name   string
		author string
		ids    []int64
	}{
		{"explicit author", "\u2003émilie  du\tchâtelet\u00a0", []int64{2, 4, 1, 3, 5, 6, 7, 8}},
		{"Unicode case folding", "ς", []int64{8, 1, 2, 3, 4, 5, 6, 7}},
		{"nationality prefix stripped", "[美] émilie du châtelet", []int64{2, 4, 1, 3, 5, 6, 7, 8}},
		{"absent author", "", []int64{1, 2, 3, 4, 5, 6, 7, 8}},
		{"whitespace author", " \t\u2003", []int64{1, 2, 3, 4, 5, 6, 7, 8}},
		{"wrong author", "Nobody", []int64{1, 2, 3, 4, 5, 6, 7, 8}},
	} {
		t.Run(test.name, func(t *testing.T) {
			original := MatchReport{Candidates: candidates, SearchQuery: "provider query", SearchLimit: 10, RequiresConfirmation: true}
			target := &authorMatchTarget{report: original}
			target.report.Candidates = append([]Candidate(nil), candidates...)
			report, err := MatchWithOptions(context.Background(), operationSource{}, target, "42", "explicit query", MatchOptions{ExpectedAuthor: test.author})
			if err != nil {
				t.Fatal(err)
			}
			want := original
			want.Candidates = make([]Candidate, 0, len(test.ids))
			for _, id := range test.ids {
				want.Candidates = append(want.Candidates, candidates[id-1])
			}
			if !reflect.DeepEqual(report, want) {
				t.Fatalf("partition changed candidate content or report: got %+v want %+v", report, want)
			}
			if target.matchCalls != 1 || target.validateCalls != 0 || target.query != "explicit query" || target.book.ID != "42" {
				t.Fatalf("ranking changed requests or validated a link: %+v", target)
			}
			if !reflect.DeepEqual(target.report, original) {
				t.Fatal("ranking mutated provider-owned report")
			}
		})
	}
}

func TestMatchExpectedAuthorPreservesExactEditions(t *testing.T) {
	for _, resolution := range []string{"exact_edition_candidate", "ambiguous_identifier"} {
		t.Run(resolution, func(t *testing.T) {
			original := MatchReport{ISBNState: "valid", Resolution: resolution, RequiresConfirmation: true,
				ExactEditions: []Edition{{ID: 8, WorkID: 3}, {ID: 4, WorkID: 2}}, Candidates: []Candidate{}}
			target := &authorMatchTarget{report: original}
			report, err := MatchWithOptions(context.Background(), operationSource{}, target, "42", "", MatchOptions{ExpectedAuthor: "Author"})
			if err != nil || !reflect.DeepEqual(report, original) || target.matchCalls != 1 || target.validateCalls != 0 {
				t.Fatalf("author changed exact edition evidence: report=%+v err=%v target=%+v", report, err, target)
			}
		})
	}
}

func TestMatchExpectedAuthorWithIncompleteAndUnconfiguredCatalogs(t *testing.T) {
	for _, configured := range []bool{false, true} {
		subscriptions := []catalog.Subscription(nil)
		if configured {
			subscriptions = []catalog.Subscription{{ID: "missing", File: t.TempDir() + "/missing.json"}}
		}
		loader, err := catalog.NewLoader(subscriptions, catalog.LoaderOptions{})
		if err != nil {
			t.Fatal(err)
		}
		var candidates []Candidate
		if err := json.Unmarshal([]byte(`[{"id":1},{"id":2,"contributions":[{"author":{"name":"Author"}}]}]`), &candidates); err != nil {
			t.Fatal(err)
		}
		target := &authorMatchTarget{report: MatchReport{Candidates: candidates, RequiresConfirmation: true}}
		baseline, err := MatchWithOptions(context.Background(), operationSource{}, target, "42", "query", MatchOptions{CatalogLoader: loader})
		if err != nil {
			t.Fatal(err)
		}
		ranked, err := MatchWithOptions(context.Background(), operationSource{}, target, "42", "query", MatchOptions{CatalogLoader: loader, ExpectedAuthor: "Author"})
		if err != nil {
			t.Fatal(err)
		}
		want := baseline
		want.Candidates = []Candidate{candidates[1], candidates[0]}
		if !reflect.DeepEqual(ranked, want) || target.matchCalls != 2 || target.validateCalls != 0 {
			t.Fatalf("ranking changed catalog evidence or requests: report=%+v want=%+v target=%+v", ranked, want, target)
		}
		if configured && (ranked.CatalogsComplete == nil || *ranked.CatalogsComplete || len(ranked.CatalogIssues) != 1) {
			t.Fatalf("catalog incompleteness lost: %+v", ranked)
		}
		if !configured {
			legacy, err := Match(context.Background(), operationSource{}, target, "42", "query")
			if err != nil || !reflect.DeepEqual(legacy, baseline) {
				t.Fatalf("default options changed standalone matching: legacy=%+v baseline=%+v err=%v", legacy, baseline, err)
			}
		}
	}
}
