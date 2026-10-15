package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"unicode"

	"github.com/azusachino/drifter/internal/companion"
)

// Record the actual shared-matcher input, including the native ISBN.
type hintTarget struct {
	queries     []string
	books       []companion.Book
	validations int
}

func (h *hintTarget) Match(_ context.Context, book companion.Book, query string) (companion.MatchReport, error) {
	h.queries = append(h.queries, query)
	h.books = append(h.books, book)
	var report companion.MatchReport
	err := json.Unmarshal([]byte(`{"work_candidates":[{"id":11,"title":"First","contributions":[{"author":{"name":"Other"}}]},{"id":22,"title":"Second","contributions":[{"author":{"name":"Chosen"}}]}],"exact_editions":[{"id":88,"book_id":11,"title":"Exact"}]}`), &report)
	return report, err
}
func (h *hintTarget) ValidateLink(context.Context, int64, int64) error { h.validations++; return nil }

func TestReviewHintQueryActions(t *testing.T) {
	for _, tc := range []struct {
		name, script    string
		queries         []string
		absent, partial bool
		first, second   int
	}{
		{name: "title only does not select author", script: "title 2\nnone\n", queries: []string{"", "Chosen title"}, first: 2},
		{name: "explicit author", script: "title 2\nauthor 2\nnone\n", queries: []string{"", "Chosen title", "Chosen title"}, first: 2, second: 1},
		{name: "manual query clears author", script: "author 2\nquery Manual title\nnone\n", queries: []string{"", "", "Manual title"}, first: 2, second: 1},
		{name: "wrong hint no automatic link", script: "title 1\nnone\n", queries: []string{"", "Wrong title"}, first: 2},
		{name: "conflicting hints no search", script: "none\n", queries: []string{""}, first: 1},
		{name: "absent hints ordinary path", script: "none\n", queries: []string{""}, absent: true, first: 1},
		{name: "manual query without hints", script: "query Manual title\nnone\n", queries: []string{"", "Manual title"}, absent: true, first: 2},
		{name: "oversized manual query rejected", script: "query " + strings.Repeat("x", 1025) + "\n", queries: []string{""}, absent: true, first: 1},
		{name: "absent hint selection", script: "title 1\n", queries: []string{""}, absent: true, first: 1},
		{name: "partial catalogs retain good hints", script: "title 2\nnone\n", queries: []string{"", "Chosen title"}, partial: true, first: 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			body := `{"catalog_schema_version":1,"catalog_id":"synthetic","publisher":"Untrusted\u001b\u009d","assertions":[{"record_id":"a","source":{"provider":"weread","id":"42"},"target":{"provider":"hardcover","work_id":900},"provenance":[{"source":"origin\u001b","reference":"ref\u009b"}]},{"record_id":"b","source":{"provider":"weread","id":"42"},"target":{"provider":"hardcover","work_id":901,"edition_id":902},"provenance":[{"source":"other","reference":"alternative"}]}],"hints":[{"record_id":"h1","source":{"provider":"weread","id":"42"},"title":"Wrong title","original_author":"Wrong","confidence":1,"provenance":[{"source":"hint origin","reference":"wrong"}]},{"record_id":"h2","source":{"provider":"weread","id":"42"},"title":"Chosen title","original_author":"Chosen","isbn":"9780306406157","confidence":0.01,"provenance":[{"source":"hint origin","reference":"chosen"}]}]}`
			path := filepath.Join(dir, "catalog.json")
			if err := os.WriteFile(path, []byte(body), 0600); err != nil {
				t.Fatal(err)
			}
			cfg := ""
			if !tc.absent {
				cfg = "[[matching.catalogs]]\nid = \"good\"\nfile = " + strconv.Quote(path) + "\n"
			}
			if tc.partial {
				cfg += "[[matching.catalogs]]\nid = \"bad\"\nfile = " + strconv.Quote(filepath.Join(dir, "missing.json")) + "\n"
			}
			configPath := filepath.Join(dir, "config.toml")
			if err := os.WriteFile(configPath, []byte(cfg), 0600); err != nil {
				t.Fatal(err)
			}
			spy := &hintTarget{}
			providers := clients{source: func() (source, error) { return reviewFixture(), nil }, target: func() (target, error) { return spy, nil }}
			db := filepath.Join(dir, "state.sqlite")
			var out, stderr bytes.Buffer
			err := runWithInput(context.Background(), []string{"review", "--account", "source", "--target-account", "target", "--db", db, "--config", configPath}, &out, &stderr, providers, strings.NewReader("match\n"+tc.script))
			if (err != nil) != tc.partial {
				t.Fatalf("err=%v output=%s stderr=%s", err, &out, &stderr)
			}
			if !reflect.DeepEqual(spy.queries, tc.queries) {
				t.Fatalf("queries=%q want=%q", spy.queries, tc.queries)
			}
			for _, book := range spy.books {
				if book.ISBN != "9780306406157" || book.Title != "Synthetic book" {
					t.Fatalf("native identity changed: %+v", book)
				}
			}
			if spy.validations != 0 {
				t.Fatal("hint caused validation/link")
			}
			store, err := companion.OpenStoreForRead(db)
			if err != nil {
				t.Fatal(err)
			}
			rows, err := store.List(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			decisions, err := store.ListDecisions(context.Background(), "weread", "source", "target")
			if err != nil {
				t.Fatal(err)
			}
			store.Close()
			if len(rows) != 0 || len(decisions) != 0 {
				t.Fatalf("hint wrote state: %+v %+v", rows, decisions)
			}
			output := out.String()
			if strings.Count(output, "1) First") != tc.first || strings.Count(output, "1) Second") != tc.second {
				t.Fatalf("unexpected ranking: %s", output)
			}
			for _, r := range output {
				if unicode.IsControl(r) && r != '\n' {
					t.Fatalf("terminal control %U", r)
				}
			}
			if !tc.absent {
				for _, text := range []string{"Unverified catalog evidence", "Conflicting assertions", "work 900 edition 0", "work 901 edition 902", "Wrong title", "Chosen title", "ISBN 9780306406157", "Provenance: origin — ref", "hint origin", "publisher Untrusted"} {
					if !strings.Contains(output, text) {
						t.Fatalf("missing %q in %s", text, output)
					}
				}
			}
			if tc.partial && (!strings.Contains(output, "Catalogs incomplete") || !strings.Contains(output, "Catalog issue: bad")) {
				t.Fatalf("partial failure hidden: %s", output)
			}
			if strings.Contains(output+stderr.String(), dir) {
				t.Fatal("catalog path leaked")
			}
			// The selected title/manual query is passed exactly as standalone match --query.
			if len(tc.queries) > 1 {
				explicit := &hintTarget{}
				if err := run(context.Background(), []string{"match", "--book-id", "42", "--query", tc.queries[len(tc.queries)-1]}, io.Discard, io.Discard, clients{source: func() (source, error) { return &fakeReader{}, nil }, target: func() (target, error) { return explicit, nil }}); err != nil {
					t.Fatal(err)
				}
				if explicit.queries[0] != spy.queries[len(spy.queries)-1] {
					t.Fatal("query differs from standalone")
				}
			}
		})
	}
}
