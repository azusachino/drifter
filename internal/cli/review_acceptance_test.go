package cli

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/azusachino/drifter/internal/companion"
)

func reviewBatch(n int) *fakeReader {
	r := reviewFixture()
	row := r.shelf.Books[0]
	r.shelf.Books = nil
	for i := 1; i <= n; i++ {
		next := row
		next.ID = fmt.Sprintf("%03d", i)
		r.shelf.Books = append(r.shelf.Books, next)
	}
	return r
}

func TestReviewAcceptanceRestartAndScopedReset(t *testing.T) {
	db := filepath.Join(t.TempDir(), "state.sqlite")
	r := reviewBatch(4)
	providers := clients{source: func() (source, error) { return r, nil }, target: func() (target, error) { return reviewCatalog{}, nil }}
	var out bytes.Buffer
	run := func(args []string, script string) {
		t.Helper()
		out.Reset()
		if err := runWithInput(context.Background(), args, &out, io.Discard, providers, strings.NewReader(script)); err != nil {
			t.Fatal(err)
		}
		reviewState(t, db) // Reopens after every invocation; also verifies lease release.
	}
	run(reviewArgs(db), "match\nwork 77\n\nyes\nskip\nexclude\nnot-in-catalog\n")
	rows, decisions := reviewState(t, db)
	if len(rows) != 1 || rows[0].BookID != "001" || len(decisions) != 3 {
		t.Fatalf("rows=%+v decisions=%+v", rows, decisions)
	}
	run(reviewArgs(db), "skip\n")
	if strings.Count(out.String(), "Book ") != 1 || !strings.Contains(out.String(), "Book 002:") {
		t.Fatalf("restart: %s", &out)
	}
	run([]string{"reset-decision", "--book-id", "003", "--account", "other", "--target-account", "target", "--db", db}, "")
	run(reviewArgs(db), "skip\n")
	if strings.Contains(out.String(), "Book 003:") {
		t.Fatal("other scope reset exclusion")
	}
	run([]string{"reset-decision", "--book-id", "003", "--account", "source", "--target-account", "target", "--db", db}, "")
	run(reviewArgs(db), "skip\nexclude\n")
	if !strings.Contains(out.String(), "Book 003:") || strings.Contains(out.String(), "Book 004:") {
		t.Fatalf("reset: %s", &out)
	}
}

type reviewPendingSource struct {
	*fakeReader
	book      func(context.Context, string) (companion.Book, error)
	shelfCall func(context.Context) (companion.ShelfResult, error)
}

func (r reviewPendingSource) Book(ctx context.Context, id string) (companion.Book, error) {
	if r.book != nil {
		return r.book(ctx, id)
	}
	return r.fakeReader.Book(ctx, id)
}
func (r reviewPendingSource) Shelf(ctx context.Context) (companion.ShelfResult, error) {
	if r.shelfCall != nil {
		return r.shelfCall(ctx)
	}
	return r.fakeReader.Shelf(ctx)
}

func TestReviewAcceptanceOlderPageFailuresAndRetry(t *testing.T) {
	db := filepath.Join(t.TempDir(), "state.sqlite")
	r := reviewBatch(103)
	fail := true
	var attempted []string
	src := reviewPendingSource{fakeReader: r, book: func(ctx context.Context, id string) (companion.Book, error) {
		attempted = append(attempted, id)
		if fail && id == "001" {
			return companion.Book{}, errors.New("private metadata body")
		}
		return r.Book(ctx, id)
	}}
	providers := clients{source: func() (source, error) { return src, nil }, target: func() (target, error) {
		return reviewCatalog{match: func(_ context.Context, b companion.Book) (companion.MatchReport, error) {
			if fail && b.ID == "051" {
				return companion.MatchReport{}, errors.New("private lookup body")
			}
			return companion.MatchReport{}, nil
		}, validate: func(_ context.Context, work, _ int64) error {
			if fail && work == 52 {
				return errors.New("private validation body")
			}
			return nil
		}}, nil
	}}
	// 001 fails metadata, 051 lookup, 052 validation; later rows still succeed.
	script := strings.Repeat("exclude\n", 49) + "match\nmatch\nwork 52\n\nyes\n" + strings.Repeat("exclude\n", 50) + "match\nwork 77\n\nyes\n"
	var out, stderr bytes.Buffer
	err := runWithInput(context.Background(), reviewArgs(db), &out, &stderr, providers, strings.NewReader(script))
	if err == nil {
		t.Fatal("partial failure reported success")
	}
	for _, id := range []string{"001", "051", "052"} {
		if !strings.Contains(err.Error(), id) {
			t.Fatalf("missing failed ID %s: %v", id, err)
		}
	}
	if strings.Contains(err.Error()+stderr.String(), "private") {
		t.Fatalf("private error escaped: %v %s", err, &stderr)
	}
	rows, decisions := reviewState(t, db)
	if len(rows) != 1 || rows[0].BookID != "103" || len(decisions) != 99 {
		t.Fatalf("rows=%+v decisions=%d output=%s err=%v", rows, len(decisions), &out, err)
	}
	if !strings.Contains(out.String(), "Book 103:") {
		t.Fatal("older page not reached")
	}
	fail = false
	attempted = nil
	out.Reset()
	if err := runWithInput(context.Background(), reviewArgs(db), &out, io.Discard, providers, strings.NewReader(strings.Repeat("exclude\n", 3))); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(attempted, []string{"001", "051", "052"}) {
		t.Fatalf("retry attempted %v", attempted)
	}
	rows, decisions = reviewState(t, db)
	if len(rows) != 1 || len(decisions) != 102 {
		t.Fatalf("rows=%+v decisions=%d", rows, len(decisions))
	}
}

func TestReviewAcceptanceNormalPaginationIsNotFailure(t *testing.T) {
	db := filepath.Join(t.TempDir(), "state.sqlite")
	r := reviewBatch(101)
	providers := clients{source: func() (source, error) { return r, nil }}
	if err := runWithInput(context.Background(), reviewArgs(db), io.Discard, io.Discard, providers, strings.NewReader(strings.Repeat("exclude\n", 101))); err != nil {
		t.Fatal(err)
	}
	_, decisions := reviewState(t, db)
	if len(decisions) != 101 {
		t.Fatalf("decisions=%d", len(decisions))
	}
}

func TestReviewAcceptancePageDeadlinePreservesRowsAndContinues(t *testing.T) {
	old := reviewProviderTimeout
	reviewProviderTimeout = 50 * time.Millisecond
	t.Cleanup(func() { reviewProviderTimeout = old })
	db := filepath.Join(t.TempDir(), "state.sqlite")
	r := reviewBatch(3)
	src := reviewPendingSource{fakeReader: r, book: func(ctx context.Context, id string) (companion.Book, error) {
		if id == "002" {
			<-ctx.Done()
			return companion.Book{}, ctx.Err()
		}
		return r.Book(ctx, id)
	}}
	providers := clients{source: func() (source, error) { return src, nil }}
	var out, stderr bytes.Buffer
	err := runWithInput(context.Background(), reviewArgs(db), &out, &stderr, providers, strings.NewReader("exclude\nexclude\n"))
	if err == nil || !strings.Contains(err.Error(), "002") {
		t.Fatalf("missing timed out ID: %v", err)
	}
	_, decisions := reviewState(t, db)
	if len(decisions) != 2 || !strings.Contains(out.String(), "Book 001:") || !strings.Contains(out.String(), "Book 003:") {
		t.Fatalf("decisions=%+v output=%s stderr=%s", decisions, &out, &stderr)
	}
}

func TestReviewAcceptanceCancellationDuringProviderStages(t *testing.T) {
	for _, stage := range []string{"shelf", "metadata", "lookup", "confirmation"} {
		t.Run(stage, func(t *testing.T) {
			db := filepath.Join(t.TempDir(), "state.sqlite")
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			started := make(chan struct{})
			block := func(ctx context.Context) error { close(started); <-ctx.Done(); return ctx.Err() }
			r := reviewBatch(3)
			src := reviewPendingSource{fakeReader: r}
			if stage == "shelf" {
				src.shelfCall = func(ctx context.Context) (companion.ShelfResult, error) { return companion.ShelfResult{}, block(ctx) }
			}
			if stage == "metadata" {
				src.book = func(ctx context.Context, id string) (companion.Book, error) {
					if id == "002" {
						return companion.Book{}, block(ctx)
					}
					return r.Book(ctx, id)
				}
			}
			providers := clients{source: func() (source, error) { return src, nil }, target: func() (target, error) {
				return reviewCatalog{match: func(ctx context.Context, _ companion.Book) (companion.MatchReport, error) {
					if stage == "lookup" {
						return companion.MatchReport{}, block(ctx)
					}
					return companion.MatchReport{}, nil
				}, validate: func(ctx context.Context, _, _ int64) error { return block(ctx) }}, nil
			}}
			done := make(chan error, 1)
			var stderr bytes.Buffer
			go func() {
				done <- runWithInput(ctx, reviewArgs(db), io.Discard, &stderr, providers, strings.NewReader("match\nwork 77\n\nyes\n"))
			}()
			<-started
			cancel()
			select {
			case err := <-done:
				if !errors.Is(err, context.Canceled) {
					t.Fatalf("err=%v", err)
				}
			case <-time.After(time.Second):
				t.Fatal("cancellation stuck")
			}
			rows, decisions := reviewState(t, db)
			if len(rows) != 0 || len(decisions) != 0 {
				t.Fatalf("rows=%+v decisions=%+v", rows, decisions)
			}
			if stage != "shelf" && !strings.Contains(stderr.String(), "003") {
				t.Fatalf("missing unattempted summary: %s", &stderr)
			}
		})
	}
}

func TestReviewAcceptanceInputFailureAndDefaultsReleaseLease(t *testing.T) {
	for _, mode := range []string{"default", "input-failure", "source-failure"} {
		t.Run(mode, func(t *testing.T) {
			db := filepath.Join(t.TempDir(), "state.sqlite")
			r := reviewBatch(2)
			src := reviewPendingSource{fakeReader: r}
			if mode == "source-failure" {
				src.shelfCall = func(context.Context) (companion.ShelfResult, error) {
					return companion.ShelfResult{}, errors.New("private shelf payload")
				}
			}
			providers := clients{source: func() (source, error) { return src, nil }}
			var input io.Reader = strings.NewReader("\n\nquit\n")
			if mode == "input-failure" {
				input = &reviewScript{lines: []string{"unused"}, before: func(int) error { return errors.New("private input payload") }}
			}
			var stderr bytes.Buffer
			err := runWithInput(context.Background(), reviewArgs(db), io.Discard, &stderr, providers, input)
			if (err == nil) != (mode == "default") {
				t.Fatalf("mode=%s err=%v", mode, err)
			}
			if err != nil && strings.Contains(err.Error()+stderr.String(), "private") {
				t.Fatalf("unsafe error: %v %s", err, &stderr)
			}
			rows, decisions := reviewState(t, db)
			if len(rows) != 0 || len(decisions) != 0 {
				t.Fatalf("rows=%+v decisions=%+v", rows, decisions)
			}
		})
	}
}

func TestReviewAcceptancePrivateAndUnknownRowsAreNotEnriched(t *testing.T) {
	for _, include := range []bool{false, true} {
		t.Run(fmt.Sprint(include), func(t *testing.T) {
			db := filepath.Join(t.TempDir(), "state.sqlite")
			r := reviewBatch(3)
			private := 1
			r.shelf.Books[1].Secret = &private
			r.shelf.Books[2].Secret = nil
			providers := clients{source: func() (source, error) { return r, nil }}
			args := reviewArgs(db)
			if include {
				args = append(args, "--include-private")
			}
			var out bytes.Buffer
			if err := runWithInput(context.Background(), args, &out, io.Discard, providers, strings.NewReader("exclude\nexclude\n")); err != nil {
				t.Fatal(err)
			}
			want := []string{"book:001"}
			if include {
				want = append(want, "book:002")
			}
			var bookCalls []string
			for _, call := range r.calls {
				if strings.HasPrefix(call, "book:") {
					bookCalls = append(bookCalls, call)
				}
			}
			if !reflect.DeepEqual(bookCalls, want) || strings.Contains(out.String(), "Book 003:") {
				t.Fatalf("calls=%v output=%s", bookCalls, &out)
			}
			rows, decisions := reviewState(t, db)
			if len(rows) != 0 || len(decisions) != len(want) {
				t.Fatalf("rows=%+v decisions=%+v", rows, decisions)
			}
		})
	}
}

func TestReviewAcceptanceMetadataOmissionsRemainHonest(t *testing.T) {
	for _, issue := range []string{"translator", "activity", "shelf"} {
		t.Run(issue, func(t *testing.T) {
			db := filepath.Join(t.TempDir(), "state.sqlite")
			r := reviewBatch(2)
			src := reviewPendingSource{fakeReader: r}
			if issue == "activity" {
				future := time.Now().Add(time.Hour).Unix()
				r.shelf.Books[0].LastOpened = &future
			}
			if issue == "shelf" {
				r.shelf.Complete = false
			}
			if issue == "translator" {
				src.book = func(ctx context.Context, id string) (companion.Book, error) {
					b, err := r.Book(ctx, id)
					if id == "001" {
						b.TranslatorKnown = false
					}
					return b, err
				}
			}
			providers := clients{source: func() (source, error) { return src, nil }}
			var out, stderr bytes.Buffer
			err := runWithInput(context.Background(), reviewArgs(db), &out, &stderr, providers, strings.NewReader("exclude\nexclude\n"))
			if err == nil {
				t.Fatal("incomplete source reported success")
			}
			if issue != "shelf" && !strings.Contains(err.Error(), "001") {
				t.Fatalf("omitted source ID lost: %v", err)
			}
			_, decisions := reviewState(t, db)
			want := 1
			if issue == "shelf" {
				want = 2
			}
			if len(decisions) != want || !strings.Contains(out.String(), "Book 002:") {
				t.Fatalf("decisions=%+v output=%s err=%v", decisions, &out, err)
			}
		})
	}
}

func TestReviewAcceptanceInputCancellationPreservesPriorChoices(t *testing.T) {
	db := filepath.Join(t.TempDir(), "state.sqlite")
	r := reviewBatch(3)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	providers := clients{source: func() (source, error) { return r, nil }, target: func() (target, error) { return reviewCatalog{}, nil }}
	input := &reviewScript{lines: []string{"exclude\n", "match\n", "work 77\n", "\n", "yes\n"}, before: func(i int) error {
		if i == 4 {
			cancel()
		}
		return nil
	}}
	var stderr bytes.Buffer
	err := runWithInput(ctx, reviewArgs(db), io.Discard, &stderr, providers, input)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err=%v", err)
	}
	rows, decisions := reviewState(t, db)
	if len(rows) != 0 || len(decisions) != 1 || decisions[0].BookID != "001" {
		t.Fatalf("rows=%+v decisions=%+v", rows, decisions)
	}
	for _, text := range []string{"saved choices: 1", "002, 003"} {
		if !strings.Contains(stderr.String(), text) {
			t.Fatalf("missing %q: %s", text, &stderr)
		}
	}
}

func TestReviewAcceptanceDeadlineThenEOFReportsUnattemptedIDs(t *testing.T) {
	old := reviewProviderTimeout
	reviewProviderTimeout = 50 * time.Millisecond
	t.Cleanup(func() { reviewProviderTimeout = old })
	db := filepath.Join(t.TempDir(), "state.sqlite")
	r := reviewBatch(3)
	src := reviewPendingSource{fakeReader: r, book: func(ctx context.Context, id string) (companion.Book, error) {
		if id == "002" {
			<-ctx.Done()
			return companion.Book{}, ctx.Err()
		}
		return r.Book(ctx, id)
	}}
	providers := clients{source: func() (source, error) { return src, nil }}
	var stderr bytes.Buffer
	err := runWithInput(context.Background(), reviewArgs(db), io.Discard, &stderr, providers, strings.NewReader(""))
	if err == nil || !strings.Contains(err.Error(), "002") {
		t.Fatalf("err=%v", err)
	}
	for _, text := range []string{"unfinished/unattempted source books:", "001", "003", "saved choices: 0"} {
		if !strings.Contains(stderr.String(), text) {
			t.Fatalf("missing %q: %s", text, &stderr)
		}
	}
	rows, decisions := reviewState(t, db)
	if len(rows) != 0 || len(decisions) != 0 {
		t.Fatalf("rows=%+v decisions=%+v", rows, decisions)
	}
}
