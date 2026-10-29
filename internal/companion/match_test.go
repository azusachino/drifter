package companion

import (
	"context"
	"testing"
)

type isbnHintSource struct{ Source }

func (isbnHintSource) Book(context.Context, string) (Book, error) {
	return Book{ID: "42", Title: "Native title", ISBN: "9780306406157"}, nil
}

type exactMatchTarget struct {
	Target
	calls int
	exact func(book Book, search string) (MatchReport, error)
}

func (m *exactMatchTarget) Match(_ context.Context, book Book, search string) (MatchReport, error) {
	m.calls++
	if m.exact != nil {
		return m.exact(book, search)
	}
	return MatchReport{
		Book:                 book,
		ExactEditions:        []Edition{{ID: 88, WorkID: 77}},
		Resolution:           "exact_edition_candidate",
		RequiresConfirmation: true,
	}, nil
}

func TestMatchSelectedQueryPreservesExactISBNShortCircuit(t *testing.T) {
	target := &exactMatchTarget{}
	report, err := MatchWithOptions(context.Background(), isbnHintSource{}, target, "42", "Selected original title", MatchOptions{ExpectedAuthor: "Selected author"})
	if err != nil || target.calls != 1 || len(report.ExactEditions) != 1 || report.ExactEditions[0].ID != 88 || len(report.Candidates) != 0 || report.SearchQuery != "" || !report.RequiresConfirmation {
		t.Fatalf("calls=%d report=%+v err=%v", target.calls, report, err)
	}
}
