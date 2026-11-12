package companion

import (
	"context"
	"testing"
	"time"

	"github.com/azusachino/drifter/internal/advisor"
)

func TestPreviewSyncPopulatesAdvisorRecommendationOnConflict(t *testing.T) {
	ctx := context.Background()
	now := time.Now().UTC()
	mapping := Mapping{
		Provider:      "weread",
		SourceAccount: "mine",
		BookID:        "123456",
		TargetAccount: "default",
		WorkID:        777,
		EditionID:     888,
	}
	store, _ := setupTestStoreWithMapping(t, mapping)
	defer store.Close()

	// Differing status on bootstrap causes ActionConflict
	src := &mockPreviewSource{
		book:        Book{ID: "123456", Title: "Conflict Book", Author: "Author A"},
		progress:    Progress{Status: "read", FinishedAt: now.Unix()},
		fingerprint: "weread:mine",
	}
	tgt := &mockApplyTarget{
		status:      &UserBookStatus{ID: 1, BookID: 777, StatusID: HardcoverStatusCurrentlyReading},
		fingerprint: "hardcover:default",
	}

	adv := &mockAdvisor{
		explainFunc: func(ctx context.Context, req advisor.ConflictExplanationRequest) (advisor.ConflictExplanationResponse, error) {
			return advisor.ConflictExplanationResponse{
				Summary:           "Source is read while target is currently reading",
				RecommendedAction: "adopt_source",
				Rationale:         "Source is finished",
			}, nil
		},
	}

	proposal, err := PreviewSync(ctx, store, src, tgt, "weread", "mine", "123456", "default", PreviewSyncOptions{
		Advisor: adv,
		Now:     now,
	})
	if err != nil {
		t.Fatalf("PreviewSync error: %v", err)
	}
	if proposal.Action != ActionConflict {
		t.Fatalf("expected conflict, got %s", proposal.Action)
	}
	if proposal.AdvisorRecommendation == nil || proposal.AdvisorRecommendation.RecommendedAction != "adopt_source" {
		t.Fatalf("expected AdvisorRecommendation with adopt_source, got %+v", proposal.AdvisorRecommendation)
	}
}

func TestApplySyncWithAdvisorAutoResolvesAdoptSource(t *testing.T) {
	ctx := context.Background()
	now := time.Now().UTC()
	mapping := Mapping{
		Provider:      "weread",
		SourceAccount: "mine",
		BookID:        "123456",
		TargetAccount: "default",
		WorkID:        777,
		EditionID:     888,
	}
	store, _ := setupTestStoreWithMapping(t, mapping)
	defer store.Close()

	src := &mockPreviewSource{
		book:        Book{ID: "123456", Title: "Conflict Book", Author: "Author A"},
		progress:    Progress{Status: "read", FinishedAt: now.Unix()},
		fingerprint: "weread:mine",
	}
	tgt := &mockApplyTarget{
		status:      &UserBookStatus{ID: 1, BookID: 777, StatusID: HardcoverStatusCurrentlyReading},
		fingerprint: "hardcover:default",
	}

	adv := &mockAdvisor{
		explainFunc: func(ctx context.Context, req advisor.ConflictExplanationRequest) (advisor.ConflictExplanationResponse, error) {
			return advisor.ConflictExplanationResponse{
				Summary:           "Source is read while target is currently reading",
				RecommendedAction: "adopt_source",
				Rationale:         "Source is finished",
			}, nil
		},
	}

	res, err := ApplySync(ctx, store, src, tgt, "weread", "mine", "123456", "default", ApplySyncOptions{
		Advisor:     adv,
		WithAdvisor: true,
		Now:         now,
	})
	if err != nil {
		t.Fatalf("ApplySync failed: %v", err)
	}
	if res.Action != "applied" {
		t.Fatalf("expected applied action with advisor resolution, got %s (msg: %s)", res.Action, res.Message)
	}
	if res.AppliedStatusID != HardcoverStatusRead {
		t.Fatalf("expected applied status Read (%d), got %d", HardcoverStatusRead, res.AppliedStatusID)
	}
	if len(tgt.mutations) != 1 || tgt.mutations[0].StatusID != HardcoverStatusRead {
		t.Fatalf("expected 1 mutation with status Read, got %+v", tgt.mutations)
	}
}
