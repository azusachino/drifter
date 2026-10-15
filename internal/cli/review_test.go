package cli

import (
	"bufio"
	"bytes"
	"context"
	"database/sql"
	"errors"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"
	"unicode"

	"github.com/azusachino/drifter/internal/companion"
)

func reviewFixture() *fakeReader {
	last := time.Now().Unix()
	secret := 0
	return &fakeReader{shelf: &companion.ShelfResult{Complete: true, Books: []companion.ShelfBook{{ID: "42", LastOpened: &last, Secret: &secret, Title: "Title\x1b[31m", Author: "Author"}}}}
}

func TestReviewSavesDecisionAndSkipsItAfterRestart(t *testing.T) {
	db := filepath.Join(t.TempDir(), "state.sqlite")
	reader := reviewFixture()
	var out, stderr bytes.Buffer
	providers := clients{source: func() (source, error) { return reader, nil }}
	args := []string{"review", "--account", "source", "--target-account", "target", "--db", db}
	if err := runWithInput(context.Background(), args, &out, &stderr, providers, strings.NewReader("\nexclude\n")); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out.String(), "\x1b[31m") {
		t.Fatalf("terminal escape not sanitized: %q", out.String())
	}
	out.Reset()
	if err := runWithInput(context.Background(), args, &out, &stderr, providers, strings.NewReader("")); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out.String(), "Book 42") {
		t.Fatalf("saved exclusion returned on restart: %s", out.String())
	}
}

func TestReviewExplicitlyConfirmsWorkOnlySuggestion(t *testing.T) {
	db := filepath.Join(t.TempDir(), "state.sqlite")
	reader := reviewFixture()
	catalog := &fakeCatalog{report: companion.MatchReport{Book: companion.Book{ID: "42"}, Candidates: []companion.Candidate{{ID: 77, Title: "Suggested", Slug: "suggested"}}, RequiresConfirmation: true}}
	providers := fakeClients(reader, catalog)
	var out bytes.Buffer
	input := strings.NewReader("match\n1\n\nyes\nquit\n")
	if err := runWithInput(context.Background(), []string{"review", "--account", "source", "--target-account", "target", "--db", db}, &out, io.Discard, providers, input); err != nil {
		t.Fatal(err)
	}
	store, err := companion.OpenStoreForRead(db)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	rows, err := store.List(context.Background())
	if err != nil || len(rows) != 1 || rows[0].WorkID != 77 || rows[0].EditionID != 0 {
		t.Fatalf("rows=%+v err=%v", rows, err)
	}
	if !strings.Contains(out.String(), "Suggested") || !strings.Contains(out.String(), "Confirm work 77 edition 0") {
		t.Fatalf("missing displayed selection: %s", out.String())
	}
}

func TestTerminalTextRemovesC1Controls(t *testing.T) {
	got := terminalText("a\u009bb\u009dc")
	if got != "abc" {
		t.Fatalf("terminalText=%q", got)
	}
}

func TestReviewManualWorkIDCannotCollideWithSuggestionNumber(t *testing.T) {
	db := filepath.Join(t.TempDir(), "state.sqlite")
	reader := reviewFixture()
	catalog := &fakeCatalog{report: companion.MatchReport{Book: companion.Book{ID: "42"}, Candidates: []companion.Candidate{{ID: 77, Title: "Suggested"}}, RequiresConfirmation: true}}
	providers := fakeClients(reader, catalog)
	var out bytes.Buffer
	input := strings.NewReader("match\nwork 1\n\nyes\nquit\n")
	if err := runWithInput(context.Background(), []string{"review", "--account", "source", "--target-account", "target", "--db", db}, &out, io.Discard, providers, input); err != nil {
		t.Fatal(err)
	}
	store, err := companion.OpenStoreForRead(db)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	rows, err := store.List(context.Background())
	if err != nil || len(rows) != 1 || rows[0].WorkID != 1 {
		t.Fatalf("rows=%+v err=%v", rows, err)
	}
}

func TestReviewConfigDatabasePathOverridesFlag(t *testing.T) {
	dir := t.TempDir()
	configured := filepath.Join(dir, "configured.sqlite")
	flagged := filepath.Join(dir, "flagged.sqlite")
	configPath := filepath.Join(dir, "config.toml")
	if err := os.WriteFile(configPath, []byte("[database]\npath = "+strconv.Quote(configured)+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	providers := clients{source: func() (source, error) { return reviewFixture(), nil }}
	args := []string{"review", "--account", "source", "--target-account", "target", "--db", flagged, "--config", configPath}
	if err := runWithInput(context.Background(), args, io.Discard, io.Discard, providers, strings.NewReader("")); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(configured); err != nil {
		t.Fatalf("configured DB was not used: %v", err)
	}
	if _, err := os.Stat(flagged); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("--db unexpectedly used: %v", err)
	}
}

func TestReviewAcquiresOwnershipBeforeProviderFactory(t *testing.T) {
	db := filepath.Join(t.TempDir(), "state.sqlite")
	owner, err := companion.AcquireDatabaseOwnership(db)
	if err != nil {
		t.Fatal(err)
	}
	defer owner.Close()
	called := false
	providers := clients{source: func() (source, error) { called = true; return nil, errors.New("unexpected") }}
	err = runWithInput(context.Background(), []string{"review", "--account", "source", "--target-account", "target", "--db", db}, io.Discard, io.Discard, providers, strings.NewReader(""))
	if err == nil || called {
		t.Fatalf("err=%v factory called=%v", err, called)
	}
}

func TestReviewEOFDoesNotSaveDecisionAndReleasesLease(t *testing.T) {
	db := filepath.Join(t.TempDir(), "state.sqlite")
	providers := clients{source: func() (source, error) { return reviewFixture(), nil }}
	args := []string{"review", "--account", "source", "--target-account", "target", "--db", db}
	if err := runWithInput(context.Background(), args, io.Discard, io.Discard, providers, strings.NewReader("")); err != nil {
		t.Fatal(err)
	}
	assertReviewNoDecisionAndUnlocked(t, db)
}

type blockedInput struct {
	started chan struct{}
	release chan struct{}
}

func (r blockedInput) Read([]byte) (int, error) {
	close(r.started)
	<-r.release
	return 0, io.EOF
}

func TestReviewCancellationWhileInputBlockedReleasesLease(t *testing.T) {
	db := filepath.Join(t.TempDir(), "state.sqlite")
	ctx, cancel := context.WithCancel(context.Background())
	input := blockedInput{started: make(chan struct{}), release: make(chan struct{})}
	providers := clients{source: func() (source, error) { return reviewFixture(), nil }}
	done := make(chan error, 1)
	go func() {
		done <- runWithInput(ctx, []string{"review", "--account", "source", "--target-account", "target", "--db", db}, io.Discard, io.Discard, providers, input)
	}()
	<-input.started
	cancel()
	close(input.release)
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("got %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("review did not cancel while input blocked")
	}
	assertReviewNoDecisionAndUnlocked(t, db)
}

func assertReviewNoDecisionAndUnlocked(t *testing.T, db string) {
	t.Helper()
	store, err := companion.OpenStoreForRead(db)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	rows, err := store.ListDecisions(context.Background(), "weread", "source", "target")
	if err != nil || len(rows) != 0 {
		t.Fatalf("rows=%v err=%v", rows, err)
	}
	if err := companion.CheckDatabaseWriteAvailable(db); err != nil {
		t.Fatalf("lease not released: %v", err)
	}
}

// reviewCatalog keeps scripted provider behavior local to CLI acceptance tests.
type reviewCatalog struct {
	match    func(context.Context, companion.Book) (companion.MatchReport, error)
	validate func(context.Context, int64, int64) error
}

func (f reviewCatalog) Match(ctx context.Context, book companion.Book, _ string) (companion.MatchReport, error) {
	if f.match != nil {
		return f.match(ctx, book)
	}
	return companion.MatchReport{Candidates: []companion.Candidate{{ID: 77, Title: "Suggested\x00\x1b\u009b", Contributions: []struct {
		Author struct {
			Name string `json:"name"`
		} `json:"author"`
	}{{Author: struct {
		Name string `json:"name"`
	}{Name: "Author\u009d"}}}}}, ExactEditions: []companion.Edition{{ID: 88, WorkID: 77, Title: "Exact\x7f\u009d"}}}, nil
}
func (f reviewCatalog) ValidateLink(ctx context.Context, work, edition int64) error {
	if f.validate != nil {
		return f.validate(ctx, work, edition)
	}
	if edition != 0 && (work != 77 || edition != 88) {
		return errors.New("synthetic membership rejection")
	}
	return nil
}

func reviewArgs(db string) []string {
	return []string{"review", "--account", "source", "--target-account", "target", "--db", db}
}

func reviewState(t *testing.T, db string) ([]companion.Mapping, []companion.Decision) {
	t.Helper()
	store, err := companion.OpenStoreForRead(db)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	mappings, err := store.List(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	decisions, err := store.ListDecisions(context.Background(), "weread", "source", "target")
	if err != nil {
		t.Fatal(err)
	}
	if err := companion.CheckDatabaseWriteAvailable(db); err != nil {
		t.Fatal(err)
	}
	return mappings, decisions
}

func TestReviewManualEditionAndWorkOnlyDespiteExactEdition(t *testing.T) {
	for _, edition := range []int64{0, 88} {
		t.Run(strconv.FormatInt(edition, 10), func(t *testing.T) {
			db := filepath.Join(t.TempDir(), "state.sqlite")
			var out bytes.Buffer
			editionInput := ""
			if edition != 0 {
				editionInput = strconv.FormatInt(edition, 10)
			}
			providers := clients{source: func() (source, error) { return reviewFixture(), nil }, target: func() (target, error) { return reviewCatalog{}, nil }}
			if err := runWithInput(context.Background(), reviewArgs(db), &out, io.Discard, providers, strings.NewReader("match\nwork 77\n"+editionInput+"\nyes\n")); err != nil {
				t.Fatal(err)
			}
			rows, decisions := reviewState(t, db)
			if len(rows) != 1 || rows[0].WorkID != 77 || rows[0].EditionID != edition || len(decisions) != 0 {
				t.Fatalf("mappings=%+v decisions=%+v", rows, decisions)
			}
			if !strings.Contains(out.String(), "Exact edition:") {
				t.Fatal("exact edition not displayed")
			}
			for _, r := range out.String() {
				if unicode.IsControl(r) && r != '\n' {
					t.Fatalf("terminal control %U in %q", r, out.String())
				}
			}
		})
	}
}

func TestReviewWrongEditionPreservesMappingsAndDecisions(t *testing.T) {
	db := filepath.Join(t.TempDir(), "state.sqlite")
	store, err := companion.OpenStore(db)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.SaveDecision(context.Background(), companion.Decision{Provider: "weread", SourceAccount: "source", BookID: "42", TargetAccount: "target", Kind: "skip"}); err != nil {
		t.Fatal(err)
	}
	if err := store.Confirm(context.Background(), companion.Mapping{Provider: "weread", SourceAccount: "other", BookID: "42", TargetAccount: "target", WorkID: 5}, false); err != nil {
		t.Fatal(err)
	}
	store.Close()
	beforeMappings, beforeDecisions := reviewState(t, db)
	providers := clients{source: func() (source, error) { return reviewFixture(), nil }, target: func() (target, error) { return reviewCatalog{}, nil }}
	var stderr bytes.Buffer
	err = runWithInput(context.Background(), reviewArgs(db), io.Discard, &stderr, providers, strings.NewReader("match\nwork 77\n89\nyes\n"))
	if err == nil || !strings.Contains(err.Error(), "42") {
		t.Fatalf("missing failed source ID/nonzero: %v", err)
	}
	afterMappings, afterDecisions := reviewState(t, db)
	if !reflect.DeepEqual(beforeMappings, afterMappings) || !reflect.DeepEqual(beforeDecisions, afterDecisions) {
		t.Fatalf("changed state: %+v %+v", afterMappings, afterDecisions)
	}
	if strings.Contains(stderr.String()+err.Error(), "synthetic membership") {
		t.Fatal("raw provider error escaped")
	}
}

// Each Read supplies one owner line, allowing state changes and think time at
// actual prompt boundaries rather than relying on bufio prefetch timing.
type reviewScript struct {
	lines  []string
	at     int
	before func(int) error
}

func (s *reviewScript) Read(p []byte) (int, error) {
	if s.at == len(s.lines) {
		return 0, io.EOF
	}
	if s.before != nil {
		if err := s.before(s.at); err != nil {
			return 0, err
		}
	}
	line := s.lines[s.at]
	s.at++
	return copy(p, line), nil
}

func TestReviewGuardedReplacementAndStaleExpectedTarget(t *testing.T) {
	for _, mode := range []string{"refuse", "replace", "stale"} {
		t.Run(mode, func(t *testing.T) {
			db := filepath.Join(t.TempDir(), "state.sqlite")
			// Isolated SQL simulates another state change after pending selection;
			// production ownership and both confirmation guards remain unchanged.
			mutate := func(work int) error {
				conn, err := sql.Open("sqlite3", db)
				if err != nil {
					return err
				}
				defer conn.Close()
				_, err = conn.Exec(`INSERT INTO mappings VALUES ('weread','source','42','target',?,0,'','') ON CONFLICT(provider,source_account,book_id,target_account) DO UPDATE SET work_id=excluded.work_id`, work)
				return err
			}
			answer := "yes\n"
			if mode == "refuse" {
				answer = "no\n"
			}
			input := &reviewScript{lines: []string{"match\n", "work 77\n", "\n", "yes\n", answer}, before: func(i int) error {
				if i == 0 {
					return mutate(5)
				}
				if i == 4 && mode == "stale" {
					return mutate(6)
				}
				return nil
			}}
			providers := clients{source: func() (source, error) { return reviewFixture(), nil }, target: func() (target, error) { return reviewCatalog{}, nil }}
			var out bytes.Buffer
			err := runWithInput(context.Background(), reviewArgs(db), &out, io.Discard, providers, input)
			if mode == "stale" {
				if err == nil || !strings.Contains(err.Error(), "42") {
					t.Fatalf("stale replacement reported success: %v", err)
				}
			} else if err != nil {
				t.Fatal(err)
			}
			rows, decisions := reviewState(t, db)
			want := int64(5)
			if mode == "replace" {
				want = 77
			}
			if mode == "stale" {
				want = 6
			}
			if len(rows) != 1 || rows[0].WorkID != want || len(decisions) != 0 {
				t.Fatalf("mappings=%+v decisions=%+v", rows, decisions)
			}
			if !strings.Contains(out.String(), "Existing mapping: work 5 edition 0. Replace?") {
				t.Fatal("missing explicit replacement prompt")
			}
		})
	}
}

func TestReviewFailedLookupThenDurableSuccessReusesFactories(t *testing.T) {
	db := filepath.Join(t.TempDir(), "state.sqlite")
	reader := reviewFixture()
	second := reader.shelf.Books[0]
	second.ID = "43"
	reader.shelf.Books = append(reader.shelf.Books, second)
	sourceCalls, targetCalls, matches, validations := 0, 0, 0, 0
	providers := clients{source: func() (source, error) { sourceCalls++; return reader, nil }, target: func() (target, error) {
		targetCalls++
		return reviewCatalog{match: func(_ context.Context, book companion.Book) (companion.MatchReport, error) {
			matches++
			if book.ID == "42" {
				return companion.MatchReport{}, errors.New("secret raw body")
			}
			return companion.MatchReport{}, nil
		}, validate: func(context.Context, int64, int64) error { validations++; return nil }}, nil
	}}
	var stderr bytes.Buffer
	err := runWithInput(context.Background(), reviewArgs(db), io.Discard, &stderr, providers, strings.NewReader("match\nmatch\nwork 99\n\nyes\n"))
	if err == nil || !strings.Contains(err.Error(), "42") || strings.Contains(err.Error()+stderr.String(), "secret raw body") {
		t.Fatalf("unsafe success/error: %v %s", err, &stderr)
	}
	if sourceCalls != 1 || targetCalls != 1 || matches != 2 || validations != 1 {
		t.Fatalf("source=%d target=%d match=%d validate=%d", sourceCalls, targetCalls, matches, validations)
	}
	rows, decisions := reviewState(t, db)
	if len(rows) != 1 || rows[0].BookID != "43" || rows[0].WorkID != 99 || len(decisions) != 0 {
		t.Fatalf("mappings=%+v decisions=%+v", rows, decisions)
	}
}

func TestReviewTargetFactoryIsLazyAndFailedCreationIsMemoized(t *testing.T) {
	for _, script := range []string{"", "skip\n", "exclude\n", "not-in-catalog\n", "match\nmatch\n"} {
		t.Run(strconv.Quote(script), func(t *testing.T) {
			db := filepath.Join(t.TempDir(), "state.sqlite")
			reader := reviewFixture()
			second := reader.shelf.Books[0]
			second.ID = "43"
			reader.shelf.Books = append(reader.shelf.Books, second)
			sources, targets := 0, 0
			providers := clients{source: func() (source, error) { sources++; return reader, nil }, target: func() (target, error) { targets++; return nil, errors.New("factory secret") }}
			err := runWithInput(context.Background(), reviewArgs(db), io.Discard, io.Discard, providers, strings.NewReader(script))
			want := 0
			if strings.Contains(script, "match") {
				want = 1
				if err == nil {
					t.Fatal("factory failure reported success")
				}
			} else if err != nil {
				t.Fatal(err)
			}
			if sources != 1 || targets != want {
				t.Fatalf("sources=%d targets=%d want targets=%d", sources, targets, want)
			}
		})
	}
}

func TestReviewProviderStageDeadlinesAndOwnerThinkTime(t *testing.T) {
	old := reviewProviderTimeout
	reviewProviderTimeout = 100 * time.Millisecond
	t.Cleanup(func() { reviewProviderTimeout = old })
	for _, stage := range []string{"lookup", "confirmation", "owner"} {
		t.Run(stage, func(t *testing.T) {
			db := filepath.Join(t.TempDir(), "state.sqlite")
			lookups, confirmations := 0, 0
			providers := clients{source: func() (source, error) { return reviewFixture(), nil }, target: func() (target, error) {
				return reviewCatalog{match: func(ctx context.Context, _ companion.Book) (companion.MatchReport, error) {
					lookups++
					if stage == "lookup" {
						<-ctx.Done()
						return companion.MatchReport{}, ctx.Err()
					}
					return companion.MatchReport{}, ctx.Err()
				}, validate: func(ctx context.Context, _, _ int64) error {
					confirmations++
					if stage == "confirmation" {
						<-ctx.Done()
					}
					return ctx.Err()
				}}, nil
			}}
			input := &reviewScript{lines: []string{"match\n", "work 77\n", "\n", "yes\n"}, before: func(i int) error {
				if stage == "owner" && (i == 0 || i == 3) {
					time.Sleep(3 * reviewProviderTimeout)
				}
				return nil
			}}
			err := runWithInput(context.Background(), reviewArgs(db), io.Discard, io.Discard, providers, input)
			if stage == "owner" {
				if err != nil {
					t.Fatal(err)
				}
			} else if err == nil || !strings.Contains(err.Error(), "42") {
				t.Fatalf("timeout reported success: %v", err)
			}
			rows, decisions := reviewState(t, db)
			wantRows, wantConfirmations := 0, 1
			if stage == "owner" {
				wantRows = 1
			}
			if stage == "lookup" {
				wantConfirmations = 0
			}
			if len(rows) != wantRows || len(decisions) != 0 || lookups != 1 || confirmations != wantConfirmations {
				t.Fatalf("rows=%+v decisions=%+v lookup=%d confirmation=%d", rows, decisions, lookups, confirmations)
			}
		})
	}
}

func TestReviewImportedRowsNeverLink(t *testing.T) {
	db := filepath.Join(t.TempDir(), "state.sqlite")
	reader := reviewFixture()
	reader.shelf.Books[0].ID = "CB_import"
	targets := 0
	providers := clients{source: func() (source, error) { return reader, nil }, target: func() (target, error) { targets++; return reviewCatalog{}, nil }}
	args := append(reviewArgs(db), "--include-imported")
	var out bytes.Buffer
	if err := runWithInput(context.Background(), args, &out, io.Discard, providers, strings.NewReader("match\nwork 77\nyes\nquit\n")); err != nil {
		t.Fatal(err)
	}
	rows, decisions := reviewState(t, db)
	if len(rows) != 0 || len(decisions) != 0 || targets != 0 || !strings.Contains(out.String(), "inspection-only") {
		t.Fatalf("rows=%+v decisions=%+v targets=%d output=%s", rows, decisions, targets, &out)
	}
}

type cancelReviewInput struct {
	cancel context.CancelFunc
	text   string
}

func (r cancelReviewInput) Read(p []byte) (int, error) {
	r.cancel()
	if r.text == "" {
		return 0, io.EOF
	}
	return copy(p, r.text), nil
}

func TestReadReviewLineCancellationWinsReadyEOFAndConsent(t *testing.T) {
	for _, text := range []string{"", "yes\n"} {
		for i := 0; i < 100; i++ {
			ctx, cancel := context.WithCancel(context.Background())
			line, err := readReviewLine(ctx, bufio.NewReader(cancelReviewInput{cancel: cancel, text: text}))
			cancel()
			if !errors.Is(err, context.Canceled) || line != "" {
				t.Fatalf("text=%q line=%q err=%v", text, line, err)
			}
		}
	}
}

func TestReviewFailuresRemainNonzeroAtEOFAndQuit(t *testing.T) {
	for _, ending := range []string{"", "quit\n"} {
		t.Run(strconv.Quote(ending), func(t *testing.T) {
			db := filepath.Join(t.TempDir(), "state.sqlite")
			reader := reviewFixture()
			second := reader.shelf.Books[0]
			second.ID = "43"
			reader.shelf.Books = append(reader.shelf.Books, second)
			providers := clients{source: func() (source, error) { return reader, nil }, target: func() (target, error) {
				return reviewCatalog{match: func(context.Context, companion.Book) (companion.MatchReport, error) {
					return companion.MatchReport{}, errors.New("synthetic failure")
				}}, nil
			}}
			err := runWithInput(context.Background(), reviewArgs(db), io.Discard, io.Discard, providers, strings.NewReader("match\n"+ending))
			if err == nil || !strings.Contains(err.Error(), "42") {
				t.Fatalf("failure lost at EOF/quit: %v", err)
			}
			rows, decisions := reviewState(t, db)
			if len(rows) != 0 || len(decisions) != 0 {
				t.Fatalf("mappings=%+v decisions=%+v", rows, decisions)
			}
		})
	}
}

func TestReviewIncompleteSelectionsNeverMeanConsent(t *testing.T) {
	for _, script := range []string{"match\n", "match\nwork 77\n", "match\nwork 77\n\n", "match\nwork 77\n\nno\n", "match\nwork 77\n\n\n", "match\nnone\n", "match\nwork 0\n", "match\nwork 77\ninvalid\n"} {
		t.Run(strconv.Quote(script), func(t *testing.T) {
			db := filepath.Join(t.TempDir(), "state.sqlite")
			validations := 0
			providers := clients{source: func() (source, error) { return reviewFixture(), nil }, target: func() (target, error) {
				return reviewCatalog{validate: func(context.Context, int64, int64) error { validations++; return nil }}, nil
			}}
			if err := runWithInput(context.Background(), reviewArgs(db), io.Discard, io.Discard, providers, strings.NewReader(script)); err != nil {
				t.Fatal(err)
			}
			rows, decisions := reviewState(t, db)
			if len(rows) != 0 || len(decisions) != 0 || validations != 0 {
				t.Fatalf("rows=%+v decisions=%+v validations=%d", rows, decisions, validations)
			}
		})
	}
}

type reviewControlSource struct{ *fakeReader }

func (s reviewControlSource) Book(ctx context.Context, id string) (companion.Book, error) {
	book, err := s.fakeReader.Book(ctx, id)
	book.Title = "Source\x00\x1b\u009b"
	book.Author = "Writer\x7f\u009d"
	return book, err
}

func TestReviewSourceMetadataIsTerminalSafe(t *testing.T) {
	db := filepath.Join(t.TempDir(), "state.sqlite")
	providers := clients{source: func() (source, error) { return reviewControlSource{reviewFixture()}, nil }}
	var out bytes.Buffer
	if err := runWithInput(context.Background(), reviewArgs(db), &out, io.Discard, providers, strings.NewReader("quit\n")); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "Book 42: Source — Writer") {
		t.Fatalf("missing safe source metadata: %q", out.String())
	}
	for _, r := range out.String() {
		if unicode.IsControl(r) && r != '\n' {
			t.Fatalf("terminal control %U in %q", r, out.String())
		}
	}
}
