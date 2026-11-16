package companion

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/azusachino/drifter/internal/advisor"
)

type automatchMockSource struct {
	books      map[string]Book
	shelf      ShelfResult
	shelfCalls int
	bookCalls  map[string]int
}

func (s *automatchMockSource) Shelf(ctx context.Context) (ShelfResult, error) {
	s.shelfCalls++
	return s.shelf, nil
}

func (s *automatchMockSource) Search(ctx context.Context, query string) (SearchResult, error) {
	return SearchResult{}, nil
}

func (s *automatchMockSource) Book(ctx context.Context, id string) (Book, error) {
	if s.bookCalls == nil {
		s.bookCalls = make(map[string]int)
	}
	s.bookCalls[id]++
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

func TestDiscoverAutoMatchCandidatesFilteringSortingAndCooldown(t *testing.T) {
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "discover.sqlite")
	store, err := OpenStore(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	now := time.Date(2026, 11, 16, 12, 0, 0, 0, time.UTC)

	// Existing confirmed mapping -> should be filtered out
	if err := store.Confirm(ctx, Mapping{
		Provider: "weread", SourceAccount: "default", BookID: "100",
		TargetAccount: "default", WorkID: 42, SourceTitle: "Mapped Book",
	}, false); err != nil {
		t.Fatal(err)
	}
	// Existing exclude decision -> should be filtered out
	if err := store.SaveDecision(ctx, Decision{
		Provider: "weread", SourceAccount: "default", BookID: "101",
		TargetAccount: "default", Kind: "exclude",
	}); err != nil {
		t.Fatal(err)
	}
	// Active cooldown attempt -> should be filtered out unless Force=true
	if err := store.RecordAutoMatchAttempt(ctx, AutoMatchAttempt{
		Provider: "weread", SourceAccount: "default", BookID: "102",
		TargetAccount: "default", LastStatus: "abstained", AttemptedAt: now.Add(-24 * time.Hour),
	}); err != nil {
		t.Fatal(err)
	}

	sec0 := 0
	sec1 := 1
	fin0 := 0
	fin1 := 1
	recent1 := now.Add(-2 * 24 * time.Hour).Unix()
	recent2 := now.Add(-1 * 24 * time.Hour).Unix()
	older15d := now.Add(-15 * 24 * time.Hour).Unix()
	zeroTime := int64(0)

	source := &automatchMockSource{
		shelf: ShelfResult{
			Complete: true,
			Books: []ShelfBook{
				{ID: "CB_imported1", Title: "Imported", LastOpened: &recent1, Secret: &sec0},
				{ID: "999", Title: "Private Book", LastOpened: &recent1, Secret: &sec1},
				{ID: "100", Title: "Mapped Book", LastOpened: &recent1, Secret: &sec0},
				{ID: "101", Title: "Excluded Book", LastOpened: &recent1, Secret: &sec0},
				{ID: "102", Title: "Cooldown Book", LastOpened: &recent1, Secret: &sec0, FinishReading: &fin1},
				{ID: "201", Title: "Recent Unfinished Newer", LastOpened: &recent2, Secret: &sec0, FinishReading: &fin0},
				{ID: "202", Title: "Recent Finished Older", LastOpened: &recent1, Secret: &sec0, FinishReading: &fin1},
				{ID: "301", Title: "Older 15d Finished", LastOpened: &older15d, Secret: &sec0, FinishReading: &fin1},
				{ID: "401", Title: "Unread Zero LastOpened", LastOpened: &zeroTime, Secret: &sec0, FinishReading: &fin0},
			},
		},
	}

	// 1. Default Days=7 (recent): only 202 and 201 qualify; sorted Finished DESC, LastOpened DESC -> 202 then 201
	recentCandidates, err := DiscoverAutoMatchCandidates(ctx, store, source, AutoMatchOptions{
		Now: now, // Days defaults to 7
	})
	if err != nil {
		t.Fatalf("DiscoverAutoMatchCandidates (recent) failed: %v", err)
	}
	if source.shelfCalls != 1 || len(source.bookCalls) != 0 {
		t.Fatalf("expected 1 Shelf call and 0 Book calls during discovery, got shelf=%d, book=%v", source.shelfCalls, source.bookCalls)
	}
	if len(recentCandidates) != 2 || recentCandidates[0].ID != "202" || recentCandidates[1].ID != "201" {
		t.Fatalf("unexpected recent candidates (expected [202, 201]): %+v", recentCandidates)
	}

	// 2. Lifetime (Days=36500, FinishedOnly=false): includes 15d old (301) and unread LastOpened==0 (401),
	// sorted Finished DESC, LastOpened DESC, ID ASC -> [202 (fin, -2d), 301 (fin, -15d), 201 (unfin, -1d), 401 (unfin, 0)]
	lifetimeCandidates, err := DiscoverAutoMatchCandidates(ctx, store, source, AutoMatchOptions{
		Days: 36500,
		Now:  now,
	})
	if err != nil {
		t.Fatalf("DiscoverAutoMatchCandidates (lifetime) failed: %v", err)
	}
	wantIDs := []string{"202", "301", "201", "401"}
	if len(lifetimeCandidates) != len(wantIDs) {
		t.Fatalf("expected %d lifetime candidates, got %+v", len(wantIDs), lifetimeCandidates)
	}
	for i, want := range wantIDs {
		if lifetimeCandidates[i].ID != want {
			t.Fatalf("lifetime candidate[%d] = %s, want %s (full: %+v)", i, lifetimeCandidates[i].ID, want, lifetimeCandidates)
		}
	}

	// 3. Lifetime with Force=true includes cooldown book 102 (finished, -2d, ID "102" < "202")
	forcedCandidates, err := DiscoverAutoMatchCandidates(ctx, store, source, AutoMatchOptions{
		Days:         36500,
		FinishedOnly: true,
		Force:        true,
		Now:          now,
	})
	if err != nil {
		t.Fatalf("DiscoverAutoMatchCandidates (force+finished) failed: %v", err)
	}
	wantForced := []string{"102", "202", "301"}
	if len(forcedCandidates) != len(wantForced) {
		t.Fatalf("expected %v, got %+v", wantForced, forcedCandidates)
	}
	for i, want := range wantForced {
		if forcedCandidates[i].ID != want {
			t.Fatalf("forced candidate[%d] = %s, want %s", i, forcedCandidates[i].ID, want)
		}
	}
}

func TestAutoMatchBookWithStoreFetchesBookOnceAndRecordsCooldown(t *testing.T) {
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "singlebook.sqlite")
	store, err := OpenStore(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	now := time.Date(2026, 11, 16, 13, 0, 0, 0, time.UTC)
	source := &automatchMockSource{
		books: map[string]Book{
			"501": {ID: "501", Title: "Confirmed Book", Author: "Author A", ISBN: "9780306406157"},
			"502": {ID: "502", Title: "Abstained Book", Author: "Author B"},
		},
	}
	target := &automatchMockTarget{
		matchFunc: func(ctx context.Context, book Book, query string) (MatchReport, error) {
			if book.ID == "501" {
				return MatchReport{
					Book:          book,
					ExactEditions: []Edition{{ID: 11, WorkID: 22, Title: "Confirmed Book"}},
				}, nil
			}
			return MatchReport{Book: book}, nil
		},
	}

	// 1. Confirmed match calls source.Book exactly once (not 3x) and persists to store
	item1 := AutoMatchBookWithStore(ctx, store, source, target, nil, ShelfRow{ID: "501", Title: "Confirmed Book"}, AutoMatchOptions{
		Now: now,
	})
	if item1.Status != "confirmed" || item1.MatchedWorkID != 22 {
		t.Fatalf("expected confirmed match for 501, got %+v", item1)
	}
	if source.bookCalls["501"] != 1 {
		t.Fatalf("expected exactly 1 source.Book call for 501, got %d", source.bookCalls["501"])
	}
	m, err := store.GetMapping(ctx, "weread", "default", "501", "default")
	if err != nil || m == nil || m.WorkID != 22 {
		t.Fatalf("expected persisted mapping for 501, got %+v, err=%v", m, err)
	}

	// 2. Abstained non-match calls source.Book once and records cooldown in auto_match_attempts
	item2 := AutoMatchBookWithStore(ctx, store, source, target, nil, ShelfRow{ID: "502", Title: "Abstained Book"}, AutoMatchOptions{
		Now: now,
	})
	if item2.Status != "abstained" {
		t.Fatalf("expected abstained status for 502, got %+v", item2)
	}
	if source.bookCalls["502"] != 1 {
		t.Fatalf("expected 1 source.Book call for 502, got %d", source.bookCalls["502"])
	}
	inCooldown, err := store.IsAutoMatchAttemptInCooldown(ctx, "weread", "default", "502", "default", 0.80, false, now)
	if err != nil || !inCooldown {
		t.Fatalf("expected 502 to be recorded in cooldown, got inCooldown=%v, err=%v", inCooldown, err)
	}
}

func TestAutoMatchBookWithStoreSurfacesRateLimitWithoutRecordingCooldown(t *testing.T) {
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "ratelimit.sqlite")
	store, err := OpenStore(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	now := time.Date(2026, 11, 16, 14, 0, 0, 0, time.UTC)
	source := &automatchMockSource{
		books: map[string]Book{
			"601": {ID: "601", Title: "克拉拉与太阳", Author: "石黑一雄", TranslatorKnown: true},
		},
	}
	target := &automatchMockTarget{
		matchFunc: func(ctx context.Context, book Book, query string) (MatchReport, error) {
			return MatchReport{Book: book}, nil
		},
	}
	adv := &mockAdvisor{
		resolveFunc: func(ctx context.Context, req advisor.OriginalWorkRequest) (advisor.OriginalWorkResponse, error) {
			return advisor.OriginalWorkResponse{}, &advisor.RateLimitError{
				RetryAfter: 30 * time.Second,
				Detail:     "quota exceeded",
			}
		},
	}

	item := AutoMatchBookWithStore(ctx, store, source, target, adv, ShelfRow{ID: "601", Title: "克拉拉与太阳"}, AutoMatchOptions{
		Now: now,
	})
	if item.Status != "error" || !item.RateLimited {
		t.Fatalf("expected status=error and RateLimited=true, got %+v", item)
	}
	if item.RetryAfter != 30*time.Second {
		t.Fatalf("expected RetryAfter=30s, got %v", item.RetryAfter)
	}
	inCooldown, err := store.IsAutoMatchAttemptInCooldown(ctx, "weread", "default", "601", "default", 0.80, false, now)
	if err != nil {
		t.Fatalf("IsAutoMatchAttemptInCooldown error: %v", err)
	}
	if inCooldown {
		t.Fatal("rate-limited book must NOT be recorded in auto_match_attempts cooldown")
	}
}
