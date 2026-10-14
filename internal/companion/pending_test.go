package companion

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"
)

func value[T any](v T) *T { return &v }

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
	w := &WeRead{testAPI(t, func(rw http.ResponseWriter, r *http.Request) {
		var args map[string]any
		if err := json.NewDecoder(r.Body).Decode(&args); err != nil {
			t.Error(err)
		}
		calls <- fmt.Sprint(args["api_name"], ":", args["bookId"])
		if args["api_name"] == "/shelf/sync" {
			json.NewEncoder(rw).Encode(map[string]any{"books": books, "bookCount": len(books), "albums": []any{map[string]any{"private": "discard"}}, "mp": map[string]any{"discard": true}})
			return
		}
		if args["api_name"] != "/book/info" {
			t.Error("unexpected provider operation", args)
		}
		translator := ""
		if args["bookId"] == "123" {
			translator = "Synthetic translator"
		}
		json.NewEncoder(rw).Encode(map[string]any{"bookId": args["bookId"], "title": "Synthetic title", "author": "Synthetic author", "translator": translator, "intro": "raw-intro-do-not-retain"})
	})}
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

func TestShelfCompletenessAndInvalidFields(t *testing.T) {
	for _, tc := range []struct {
		body           string
		fail, complete bool
	}{
		{`{"books":[],"bookCount":0}`, false, true},
		{`{"books":[]}`, false, false},
		{`{"books":[],"bookCount":1}`, false, false},
		{`{"books":[],"bookCount":0,"hasMore":1}`, false, false},
		{`{"books":[],"bookCount":-1}`, true, false},
		{`{"books":[],"hasMore":2}`, true, false},
		{`{"books":null}`, true, false},
		{`{"books":[{"bookId":"123"},{"bookId":"123"}]}`, true, false},
		{`{"books":[{"bookId":"CB_"}]}`, true, false},
		{`{"books":[{"bookId":"CB_unsafe/title"}]}`, true, false},
		{`{"books":[{"bookId":"123","secret":"private-test-token"}]}`, true, false},
		{`{"errcode":2,"errmsg":"private-test-token"}`, true, false},
		{`{"upgrade_info":{"message":"new"},"books":[]}`, true, false},
	} {
		w := &WeRead{testAPI(t, func(rw http.ResponseWriter, _ *http.Request) { io.WriteString(rw, tc.body) })}
		shelf, err := w.Shelf(context.Background())
		if (err != nil) != tc.fail || err == nil && shelf.Complete != tc.complete {
			t.Fatalf("%s: %+v %v", tc.body, shelf, err)
		}
		if err != nil && strings.Contains(err.Error(), "private-test-token") {
			t.Fatal("provider body leaked")
		}
	}
}

type pendingReader struct {
	shelf  ShelfResult
	calls  []string
	lookup func(string) (Book, error)
}

func (r *pendingReader) Shelf(context.Context) (ShelfResult, error) { return r.shelf, nil }
func (r *pendingReader) Book(_ context.Context, id string) (Book, error) {
	r.calls = append(r.calls, id)
	if r.lookup != nil {
		return r.lookup(id)
	}
	return Book{ID: id, Title: "Synthetic", TranslatorKnown: true}, nil
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
