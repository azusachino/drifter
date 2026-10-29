package companion

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"
)

func value[T any](v T) *T { return &v }

type pendingReader struct {
	shelf     ShelfResult
	shelfHook func()
	calls     []string
	lookup    func(string) (Book, error)
}

func (r *pendingReader) Shelf(context.Context) (ShelfResult, error) {
	if r.shelfHook != nil {
		r.shelfHook()
	}
	return r.shelf, nil
}
func (r *pendingReader) Book(_ context.Context, id string) (Book, error) {
	r.calls = append(r.calls, id)
	if r.lookup != nil {
		return r.lookup(id)
	}
	return Book{ID: id, Title: "Synthetic", TranslatorKnown: true}, nil
}

func TestPendingShelfFiltersBeforeMetadataAndClassifies(t *testing.T) {
	now := time.Unix(2000000000, 0)
	cutoff := now.Add(-90 * 24 * time.Hour).Unix()
	books := []ShelfBook{
		{ID: "123", LastOpened: value(now.Unix()), Secret: value(0)},
		{ID: "124", LastOpened: value(cutoff), Secret: value(0)},
		{ID: "125", LastOpened: value(cutoff - 1), Secret: value(0)},
		{ID: "126", Title: "Private title", LastOpened: value(now.Unix()), Secret: value(1)},
		{ID: "127", Title: "Unknown privacy", LastOpened: value(now.Unix())},
		{ID: "128", LastOpened: value(now.Unix()), Secret: value(0)},
		{ID: "129", LastOpened: value(int64(0)), Secret: value(0)},
		{ID: "CB_synthetic", Title: "Imported title", LastOpened: value(now.Unix()), Secret: value(0)},
	}
	calls := make(chan string, 20)
	w := &pendingReader{
		shelf:     ShelfResult{Books: books, Complete: true},
		shelfHook: func() { calls <- "/shelf/sync:<nil>" },
		lookup: func(id string) (Book, error) {
			calls <- fmt.Sprint("/book/info:", id)
			translator := ""
			if id == "123" {
				translator = "Synthetic translator"
			}
			return Book{ID: id, Title: "Synthetic title", Author: "Synthetic author", Translator: translator, TranslatorKnown: true}, nil
		},
	}
	mapping := testMapping()
	mapping.BookID = "128"
	other := mapping
	other.BookID, other.TargetAccount = "124", "different-target"
	options := PendingOptions{SourceAccount: "source", TargetAccount: "target", Days: 90, Now: now}
	report, err := Pending(context.Background(), w, []Mapping{mapping, other}, options)
	if err != nil || !report.Complete || len(report.Books) != 2 || len(report.Issues) != 0 {
		t.Fatalf("%+v, %v", report, err)
	}
	if report.Books[0].ID != "123" || report.Books[0].Classification != "translated" || report.Books[1].ID != "124" || report.Books[1].Classification != "native" {
		t.Fatalf("wrong ordering/classification: %+v", report.Books)
	}
	got := []string{}
	for len(calls) > 0 {
		got = append(got, <-calls)
	}
	if !reflect.DeepEqual(got, []string{"/shelf/sync:<nil>", "/book/info:123", "/book/info:124"}) {
		t.Fatalf("filtered books enriched: %v", got)
	}
	data, _ := json.Marshal(report)
	for _, private := range []string{"Private title", "Unknown privacy", "Imported title", "raw-intro-do-not-retain", "albums"} {
		if strings.Contains(string(data), private) {
			t.Fatal("unexpected personal/raw fields in report")
		}
	}
	options.IncludePrivate, options.IncludeImported = true, true
	report, err = Pending(context.Background(), w, nil, options)
	if err != nil || len(report.Books) != 5 {
		t.Fatalf("opt-in worklist: %+v %v", report, err)
	}
	var imported, private bool
	for _, book := range report.Books {
		imported = imported || book.ID == "CB_synthetic" && book.Classification == "imported"
		private = private || book.ID == "126" && book.Private
		if book.ID == "127" {
			t.Fatal("unknown privacy included by opt-in")
		}
	}
	if !imported || !private || ValidBookID("CB_synthetic") {
		t.Fatal("imported/private handling broadened link identity or lost classification")
	}
}

func TestPendingReportsUnknownMetadataAndActivityWithoutRawErrors(t *testing.T) {
	now := time.Unix(2000000000, 0)
	r := &pendingReader{shelf: ShelfResult{Complete: false}}
	for _, id := range []string{"123", "124", "125", "126", "127", "128"} {
		r.shelf.Books = append(r.shelf.Books, ShelfBook{ID: id, Secret: value(0), LastOpened: value(now.Unix())})
	}
	r.shelf.Books[3].LastOpened = nil
	r.shelf.Books[4].LastOpened = value(now.Unix() + 1)
	r.shelf.Books[5].LastOpened = value(int64(-1))
	r.lookup = func(id string) (Book, error) {
		if id == "123" {
			return Book{}, fmt.Errorf("sensitive-title private-test-token raw response")
		}
		if id == "124" {
			return Book{ID: id, Title: "Synthetic"}, nil
		}
		return Book{ID: id, Title: "Synthetic", TranslatorKnown: true}, nil
	}
	report, err := Pending(context.Background(), r, nil, PendingOptions{SourceAccount: "source", TargetAccount: "target", Days: 90, Now: now})
	if err != nil || report.Complete || len(report.Books) != 1 || report.Books[0].ID != "125" || len(report.Issues) != 6 {
		t.Fatalf("partial report: %+v %v", report, err)
	}
	data, _ := json.Marshal(report)
	if strings.Contains(string(data), "sensitive-title") || strings.Contains(string(data), "private-test-token") {
		t.Fatal("raw lookup error leaked")
	}
	if !reflect.DeepEqual(r.calls, []string{"123", "124", "125"}) {
		t.Fatal("invalid activity enriched", r.calls)
	}
}

func TestPendingRequestBudgetValidationAndCancellation(t *testing.T) {
	now := time.Unix(2000000000, 0)
	r := &pendingReader{shelf: ShelfResult{Complete: true}}
	for i := 1; i <= pendingLimit+1; i++ {
		r.shelf.Books = append(r.shelf.Books, ShelfBook{ID: strconv.Itoa(i), Secret: value(0), LastOpened: value(now.Unix())})
	}
	options := PendingOptions{SourceAccount: "source", TargetAccount: "target", Days: 90, Now: now}
	report, err := Pending(context.Background(), r, nil, options)
	if err != nil || report.Complete || len(report.Books) != pendingLimit || len(r.calls) != pendingLimit || report.Issues[0].Code != "worklist_limit" {
		t.Fatalf("unbounded worklist: %+v %v", report, err)
	}
	for _, days := range []int{0, -1, 36501} {
		options.Days = days
		if _, err := Pending(context.Background(), r, nil, options); err == nil {
			t.Fatal("invalid window accepted")
		}
	}
	options.Days = 90
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := Pending(ctx, r, nil, options); err == nil {
		t.Fatal("canceled worklist succeeded")
	}
}

func TestPendingSeekContinuesAcrossFailuresAndTies(t *testing.T) {
	now := time.Unix(2000000000, 0)
	r := &pendingReader{shelf: ShelfResult{Complete: true}}
	for i := 1; i <= pendingLimit+2; i++ {
		id := fmt.Sprintf("%03d", i)
		r.shelf.Books = append(r.shelf.Books, ShelfBook{ID: id, Secret: value(0), LastOpened: value(now.Unix())})
	}
	r.lookup = func(id string) (Book, error) {
		if id == "001" || id == "002" {
			return Book{}, fmt.Errorf("private lookup failure")
		}
		return Book{ID: id, Title: "Synthetic", TranslatorKnown: true}, nil
	}
	options := PendingOptions{SourceAccount: "source", TargetAccount: "target", Days: 90, Now: now}
	first, err := Pending(context.Background(), r, nil, options)
	if err != nil || len(first.Books) != 98 || len(r.calls) != 100 || first.NextCursor == "" {
		t.Fatalf("first page: books=%d calls=%d report=%+v err=%v", len(first.Books), len(r.calls), first, err)
	}
	options.After = first.NextCursor
	second, err := Pending(context.Background(), r, nil, options)
	if err != nil || len(second.Books) != 2 || second.Books[0].ID != "101" || second.Books[1].ID != "102" || second.NextCursor != "" {
		t.Fatalf("second page: %+v %v", second, err)
	}
	if len(r.calls) != 102 {
		t.Fatalf("previous rows re-enriched: %d calls", len(r.calls))
	}
	for _, bad := range []string{"not-base64", strings.Repeat("a", 2049)} {
		options.After = bad
		if _, err := Pending(context.Background(), r, nil, options); err == nil {
			t.Fatal("invalid cursor accepted")
		}
	}
	options.After = encodePendingCursor(pendingCursor{Version: 1, AsOf: now.Unix(), Days: 90, SourceAccount: "other", TargetAccount: "target", LastOpened: now.Unix(), BookID: "100"})
	if _, err := Pending(context.Background(), r, nil, options); err == nil {
		t.Fatal("wrong scope accepted")
	}
}

func TestPendingSeekCancellationDoesNotSkipUnattemptedRows(t *testing.T) {
	now := time.Unix(2000000000, 0)
	r := &pendingReader{shelf: ShelfResult{Complete: true}}
	for i := 1; i <= pendingLimit+1; i++ {
		r.shelf.Books = append(r.shelf.Books, ShelfBook{ID: fmt.Sprintf("%03d", i), Secret: value(0), LastOpened: value(now.Unix())})
	}
	ctx, cancel := context.WithCancel(context.Background())
	r.lookup = func(id string) (Book, error) {
		if id == "050" {
			cancel()
		}
		return Book{ID: id, Title: "Synthetic", TranslatorKnown: true}, nil
	}
	options := PendingOptions{SourceAccount: "source", TargetAccount: "target", Days: 90, Now: now}
	report, err := Pending(ctx, r, nil, options)
	if err == nil || report.NextCursor == "" {
		t.Fatalf("cancellation cursor: %+v %v", report, err)
	}
	if report.Complete || len(report.Books) != 49 {
		t.Fatalf("partial rows lost or marked complete: %+v", report)
	}
	failed, unattempted := []string{}, []string{}
	for _, issue := range report.Issues {
		switch issue.Code {
		case "metadata_lookup_failed":
			failed = append(failed, issue.BookID)
		case "unattempted":
			unattempted = append(unattempted, issue.BookID)
		}
	}
	if !reflect.DeepEqual(failed, []string{"050"}) || len(unattempted) != 50 || unattempted[0] != "051" || unattempted[49] != "100" {
		t.Fatalf("failed=%v unattempted=%v", failed, unattempted)
	}
	options.After = report.NextCursor
	ctx2, cancel2 := context.WithCancel(context.Background())
	defer cancel2()
	r.lookup = nil
	continued, err := Pending(ctx2, r, nil, options)
	if err != nil || len(continued.Books) != 51 || continued.Books[0].ID != "051" {
		t.Fatalf("continuation skipped work: %+v %v", continued, err)
	}
}

func TestPendingYearAndFinishedOnlyFiltering(t *testing.T) {
	t2025 := time.Date(2025, 6, 15, 12, 0, 0, 0, time.UTC).Unix()
	t2024 := time.Date(2024, 6, 15, 12, 0, 0, 0, time.UTC).Unix()
	books := []ShelfBook{
		{ID: "101", Title: "Book 2025 Finished", LastOpened: value(t2025), FinishReading: value(1), Secret: value(0)},
		{ID: "102", Title: "Book 2025 Incomplete", LastOpened: value(t2025), FinishReading: value(0), Secret: value(0)},
		{ID: "103", Title: "Book 2024 Finished", LastOpened: value(t2024), FinishReading: value(1), Secret: value(0)},
	}
	w := &pendingReader{
		shelf: ShelfResult{Books: books, Complete: true},
		lookup: func(id string) (Book, error) {
			return Book{ID: id, Title: "Title", Author: "Author", TranslatorKnown: true}, nil
		},
	}

	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

	// Filter by year 2025 and finished only
	report, err := Pending(context.Background(), w, nil, PendingOptions{
		SourceAccount: "src", TargetAccount: "dst", Days: 365, Year: 2025, FinishedOnly: true, Now: now,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(report.Books) != 1 || report.Books[0].ID != "101" {
		t.Fatalf("expected only book 101, got: %+v", report.Books)
	}
	if !report.Books[0].Finished {
		t.Fatalf("expected Finished=true for book 101")
	}
}
