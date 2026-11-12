package companion

import (
	"context"
	"testing"

	"github.com/azusachino/drifter/internal/advisor"
)

type mockAdvisor struct {
	explainFunc func(ctx context.Context, req advisor.ConflictExplanationRequest) (advisor.ConflictExplanationResponse, error)
	resolveFunc func(ctx context.Context, req advisor.OriginalWorkRequest) (advisor.OriginalWorkResponse, error)
}

func (m *mockAdvisor) ExplainConflict(ctx context.Context, req advisor.ConflictExplanationRequest) (advisor.ConflictExplanationResponse, error) {
	if m.explainFunc != nil {
		return m.explainFunc(ctx, req)
	}
	return advisor.ConflictExplanationResponse{}, nil
}

func (m *mockAdvisor) ResolveOriginalWork(ctx context.Context, req advisor.OriginalWorkRequest) (advisor.OriginalWorkResponse, error) {
	if m.resolveFunc != nil {
		return m.resolveFunc(ctx, req)
	}
	return advisor.OriginalWorkResponse{Abstained: true, Confidence: 0}, nil
}

func TestMatchWithOptionsFallbackToAdvisorOriginalTitle(t *testing.T) {
	source := isbnHintSource{}
	target := &exactMatchTarget{
		exact: func(book Book, search string) (MatchReport, error) {
			if search == "Resolved Foreign Title" {
				return MatchReport{
					Book:                 book,
					Candidates:           []Candidate{{ID: 999, Title: "Resolved Foreign Title"}},
					Resolution:           "work_candidates",
					RequiresConfirmation: true,
				}, nil
			}
			// Initial search with empty/native query yields 0 results
			return MatchReport{
				Book:                 book,
				Resolution:           "unresolved",
				RequiresConfirmation: true,
			}, nil
		},
	}

	adv := &mockAdvisor{
		resolveFunc: func(ctx context.Context, req advisor.OriginalWorkRequest) (advisor.OriginalWorkResponse, error) {
			return advisor.OriginalWorkResponse{
				OriginalTitle:  "Resolved Foreign Title",
				OriginalAuthor: "Original Author",
				Confidence:     0.9,
				Abstained:      false,
				Rationale:      "Translated work",
			}, nil
		},
	}

	report, err := MatchWithOptions(context.Background(), source, target, "42", "", MatchOptions{
		Advisor: adv,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(report.Candidates) != 1 || report.Candidates[0].ID != 999 {
		t.Fatalf("expected candidate 999, got %+v", report.Candidates)
	}
	if report.AdvisorResult == nil || report.AdvisorResult.OriginalTitle != "Resolved Foreign Title" {
		t.Fatalf("expected AdvisorResult attached, got %+v", report.AdvisorResult)
	}
}

func TestMatchWithOptionsAdvisorAbstains(t *testing.T) {
	source := isbnHintSource{}
	target := &exactMatchTarget{
		exact: func(book Book, search string) (MatchReport, error) {
			return MatchReport{
				Book:                 book,
				Resolution:           "unresolved",
				RequiresConfirmation: true,
			}, nil
		},
	}

	adv := &mockAdvisor{
		resolveFunc: func(ctx context.Context, req advisor.OriginalWorkRequest) (advisor.OriginalWorkResponse, error) {
			return advisor.OriginalWorkResponse{
				Confidence: 0,
				Abstained:  true,
				Rationale:  "Domestic work",
			}, nil
		},
	}

	report, err := MatchWithOptions(context.Background(), source, target, "42", "", MatchOptions{
		Advisor: adv,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(report.Candidates) != 0 {
		t.Fatalf("expected 0 candidates, got %+v", report.Candidates)
	}
	if report.AdvisorResult == nil || !report.AdvisorResult.Abstained {
		t.Fatalf("expected abstained AdvisorResult, got %+v", report.AdvisorResult)
	}
}
