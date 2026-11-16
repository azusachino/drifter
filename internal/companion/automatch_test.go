package companion

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/azusachino/drifter/internal/advisor"
)

type automatchMockSource struct {
	books map[string]Book
	shelf ShelfResult
}

func (s *automatchMockSource) Shelf(ctx context.Context) (ShelfResult, error) {
	return s.shelf, nil
}

func (s *automatchMockSource) Search(ctx context.Context, query string) (SearchResult, error) {
	return SearchResult{}, nil
}

func (s *automatchMockSource) Book(ctx context.Context, id string) (Book, error) {
	if b, ok := s.books[id]; ok {
		return b, nil
	}
	return Book{ID: id, Title: "Default Title"}, nil
}

func (s *automatchMockSource) Progress(ctx context.Context, id string) (Progress, error) {
	return Progress{Percent: 50, Started: true}, nil
}

type automatchMockTarget struct {
	matchFunc    func(ctx context.Context, book Book, query string) (MatchReport, error)
	validateFunc func(ctx context.Context, workID, editionID int64) error
}

func (t *automatchMockTarget) Match(ctx context.Context, book Book, query string) (MatchReport, error) {
	if t.matchFunc != nil {
		return t.matchFunc(ctx, book, query)
	}
	return MatchReport{}, nil
}

func (t *automatchMockTarget) ValidateLink(ctx context.Context, workID, editionID int64) error {
	if t.validateFunc != nil {
		return t.validateFunc(ctx, workID, editionID)
	}
	return nil
}

func TestAutoMatchOwnedAdvisorHighConfidence(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "mappings.sqlite")
	owner, err := AcquireDatabaseOwnership(dbPath)
	if err != nil {
		t.Fatalf("acquire ownership: %v", err)
	}
	defer owner.Close()

	store, err := OpenOwnedStore(dbPath, owner)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer store.Close()

	source := &automatchMockSource{
		books: map[string]Book{
			"3300189649": {ID: "3300189649", Title: "如何了解一个人", Author: "[美]戴维·布鲁克斯"},
		},
	}
	target := &automatchMockTarget{
		matchFunc: func(ctx context.Context, book Book, query string) (MatchReport, error) {
			if query == "How to Know a Person" {
				cand := Candidate{
					ID:    1011275,
					Title: "How to Know a Person",
				}
				cand.Contributions = []struct {
					Author struct {
						Name string `json:"name"`
					} `json:"author"`
				}{
					{Author: struct {
						Name string `json:"name"`
					}{Name: "David Brooks"}},
				}
				return MatchReport{
					Book:       book,
					Candidates: []Candidate{cand},
				}, nil
			}
			return MatchReport{Book: book}, nil
		},
	}

	adv := &mockAdvisor{
		resolveFunc: func(ctx context.Context, req advisor.OriginalWorkRequest) (advisor.OriginalWorkResponse, error) {
			return advisor.OriginalWorkResponse{
				OriginalTitle:  "How to Know a Person",
				OriginalAuthor: "David Brooks",
				Confidence:     0.92,
				Abstained:      false,
			}, nil
		},
	}

	report, err := AutoMatchOwned(context.Background(), store, source, target, dbPath, owner, adv, AutoMatchOptions{
		BookID:        "3300189649",
		MinConfidence: 0.80,
	})
	if err != nil {
		t.Fatalf("automatch error: %v", err)
	}
	if report.Confirmed != 1 || len(report.Matches) != 1 {
		t.Fatalf("expected 1 confirmed match, got %+v", report)
	}
	if report.Matches[0].Status != "confirmed" || report.Matches[0].MatchedWorkID != 1011275 {
		t.Fatalf("unexpected match item: %+v", report.Matches[0])
	}

	// Verify persistence in SQLite store
	m, err := store.GetMapping(context.Background(), "weread", "default", "3300189649", "default")
	if err != nil || m == nil {
		t.Fatalf("mapping not found in store: %v", err)
	}
	if m.WorkID != 1011275 {
		t.Fatalf("expected work ID 1011275, got %d", m.WorkID)
	}
}

func TestAutoMatchOwnedAdvisorLowConfidenceSkipped(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "mappings.sqlite")
	owner, err := AcquireDatabaseOwnership(dbPath)
	if err != nil {
		t.Fatalf("acquire ownership: %v", err)
	}
	defer owner.Close()

	store, err := OpenOwnedStore(dbPath, owner)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer store.Close()

	source := &automatchMockSource{
		books: map[string]Book{
			"10002": {ID: "10002", Title: "不确定的一本书", Author: "未知作者"},
		},
	}
	target := &automatchMockTarget{
		matchFunc: func(ctx context.Context, book Book, query string) (MatchReport, error) {
			if query == "Some Book" {
				return MatchReport{Book: book, Candidates: []Candidate{{ID: 888, Title: "Some Book"}}}, nil
			}
			return MatchReport{Book: book}, nil
		},
	}

	adv := &mockAdvisor{
		resolveFunc: func(ctx context.Context, req advisor.OriginalWorkRequest) (advisor.OriginalWorkResponse, error) {
			return advisor.OriginalWorkResponse{
				OriginalTitle: "Some Book",
				Confidence:    0.65, // Below 0.80
				Abstained:     false,
			}, nil
		},
	}

	report, err := AutoMatchOwned(context.Background(), store, source, target, dbPath, owner, adv, AutoMatchOptions{
		BookID:        "10002",
		MinConfidence: 0.80,
	})
	if err != nil {
		t.Fatalf("automatch error: %v", err)
	}
	if report.Confirmed != 0 || report.Skipped != 1 {
		t.Fatalf("expected skipped, got %+v", report)
	}
	if report.Matches[0].Status != "low_confidence" {
		t.Fatalf("expected low_confidence status, got %s", report.Matches[0].Status)
	}

	// Verify not persisted
	m, _ := store.GetMapping(context.Background(), "weread", "default", "10002", "default")
	if m != nil {
		t.Fatalf("expected no mapping persisted, got %+v", m)
	}
}

func TestAutoMatchOwnedDryRunDoesNotPersist(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "mappings.sqlite")
	owner, err := AcquireDatabaseOwnership(dbPath)
	if err != nil {
		t.Fatalf("acquire ownership: %v", err)
	}
	defer owner.Close()

	store, err := OpenOwnedStore(dbPath, owner)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer store.Close()

	source := &automatchMockSource{
		books: map[string]Book{
			"10003": {ID: "10003", Title: "如何了解一个人", Author: "戴维·布鲁克斯"},
		},
	}
	target := &automatchMockTarget{
		matchFunc: func(ctx context.Context, book Book, query string) (MatchReport, error) {
			if query == "How to Know a Person" {
				return MatchReport{
					Book:       book,
					Candidates: []Candidate{{ID: 1011275, Title: "How to Know a Person"}},
				}, nil
			}
			return MatchReport{Book: book}, nil
		},
	}

	adv := &mockAdvisor{
		resolveFunc: func(ctx context.Context, req advisor.OriginalWorkRequest) (advisor.OriginalWorkResponse, error) {
			return advisor.OriginalWorkResponse{
				OriginalTitle: "How to Know a Person",
				Confidence:    0.95,
				Abstained:     false,
			}, nil
		},
	}

	report, err := AutoMatchOwned(context.Background(), store, source, target, dbPath, owner, adv, AutoMatchOptions{
		BookID:        "10003",
		MinConfidence: 0.80,
		DryRun:        true,
	})
	if err != nil {
		t.Fatalf("automatch error: %v", err)
	}
	if report.Confirmed != 1 || report.Matches[0].Status != "dry_run" {
		t.Fatalf("expected dry_run, got %+v", report)
	}

	// Verify not persisted
	m, _ := store.GetMapping(context.Background(), "weread", "default", "10003", "default")
	if m != nil {
		t.Fatalf("expected no mapping persisted in dry run, got %+v", m)
	}
}

func TestAutoMatchOwnedWorklistPending(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "mappings.sqlite")
	owner, err := AcquireDatabaseOwnership(dbPath)
	if err != nil {
		t.Fatalf("acquire ownership: %v", err)
	}
	defer owner.Close()

	store, err := OpenOwnedStore(dbPath, owner)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer store.Close()

	now := time.Now().UTC()
	t1 := now.Add(-time.Hour).Unix()
	t2 := now.Add(-2 * time.Hour).Unix()
	secret := 0
	source := &automatchMockSource{
		shelf: ShelfResult{
			Complete: true,
			Books: []ShelfBook{
				{
					ID:         "10001",
					Title:      "如何了解一个人",
					Author:     "戴维·布鲁克斯",
					LastOpened: &t1,
					Secret:     &secret,
				},
				{
					ID:         "10002",
					Title:      "国内原创技术书",
					Author:     "中国作者",
					LastOpened: &t2,
					Secret:     &secret,
				},
			},
		},
		books: map[string]Book{
			"10001": {ID: "10001", Title: "如何了解一个人", Author: "戴维·布鲁克斯", TranslatorKnown: true},
			"10002": {ID: "10002", Title: "国内原创技术书", Author: "中国作者", TranslatorKnown: true},
		},
	}
	target := &automatchMockTarget{
		matchFunc: func(ctx context.Context, book Book, query string) (MatchReport, error) {
			if query == "How to Know a Person" {
				return MatchReport{
					Book:       book,
					Candidates: []Candidate{{ID: 1011275, Title: "How to Know a Person"}},
				}, nil
			}
			return MatchReport{Book: book}, nil
		},
	}

	adv := &mockAdvisor{
		resolveFunc: func(ctx context.Context, req advisor.OriginalWorkRequest) (advisor.OriginalWorkResponse, error) {
			if req.Title == "如何了解一个人" {
				return advisor.OriginalWorkResponse{
					OriginalTitle: "How to Know a Person",
					Confidence:    0.90,
					Abstained:     false,
				}, nil
			}
			return advisor.OriginalWorkResponse{
				Abstained: true,
				Rationale: "domestic work",
			}, nil
		},
	}

	report, err := AutoMatchOwned(context.Background(), store, source, target, dbPath, owner, adv, AutoMatchOptions{
		MinConfidence: 0.80,
		Days:          30,
		Now:           now,
	})
	if err != nil {
		t.Fatalf("automatch error: %v", err)
	}
	if report.Attempted != 2 || report.Confirmed != 1 || report.Skipped != 1 {
		t.Fatalf("expected 2 attempted, 1 confirmed, 1 skipped, got %+v", report)
	}
	m, err := store.GetMapping(context.Background(), "weread", "default", "10001", "default")
	if err != nil || m == nil || m.WorkID != 1011275 {
		t.Fatalf("expected mapping for 10001, got %+v", m)
	}
}
