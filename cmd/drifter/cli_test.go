package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/azusachino/drifter/internal/companion"
)

type fakeReader struct {
	calls []string
	shelf *companion.ShelfResult
}

func (f *fakeReader) Shelf(_ context.Context) (companion.ShelfResult, error) {
	f.calls = append(f.calls, "shelf")
	if f.shelf != nil {
		return *f.shelf, nil
	}
	return companion.ShelfResult{Books: []companion.ShelfBook{}, Complete: true}, nil
}

func (f *fakeReader) Search(_ context.Context, title string) (companion.SearchResult, error) {
	f.calls = append(f.calls, "search:"+title)
	return companion.SearchResult{Books: []companion.Book{{ID: "123", Title: "Synthetic book"}}, HasMore: true}, nil
}
func (f *fakeReader) Book(_ context.Context, id string) (companion.Book, error) {
	f.calls = append(f.calls, "book:"+id)
	return companion.Book{ID: id, Title: "Synthetic book", ISBN: "9780306406157", TranslatorKnown: true}, nil
}
func (f *fakeReader) Progress(_ context.Context, id string) (companion.Progress, error) {
	f.calls = append(f.calls, "progress:"+id)
	return companion.Progress{Started: true, Status: "reading"}, nil
}

type fakeCatalog struct {
	calls  []string
	reject bool
}

func (f *fakeCatalog) Match(_ context.Context, book companion.Book, query string) (companion.MatchReport, error) {
	f.calls = append(f.calls, "match:"+query)
	return companion.MatchReport{Book: book, SearchQuery: query, RequiresConfirmation: true}, nil
}
func (f *fakeCatalog) ValidateLink(_ context.Context, work, edition int64) error {
	f.calls = append(f.calls, "validate")
	if f.reject {
		return errors.New("edition does not belong to the selected work")
	}
	return nil
}
func fakeClients(reader *fakeReader, catalog *fakeCatalog) clients {
	return clients{
		source: func() (source, error) { return reader, nil },
		target: func() (target, error) { return catalog, nil },
	}
}

func TestCLIReadCommands(t *testing.T) {
	for _, tc := range []struct {
		args, calls []string
		output      string
	}{
		{[]string{"search", "--title", "合成"}, []string{"search:合成"}, `"has_more": true`},
		{[]string{"inspect", "--book-id", "123"}, []string{"book:123"}, `"book"`},
		{[]string{"inspect", "--book-id", "123", "--progress"}, []string{"book:123", "progress:123"}, `"proposed_status": "reading"`},
		{[]string{"match", "--book-id", "123", "--query", "Original title"}, []string{"book:123"}, `"requires_confirmation": true`},
	} {
		t.Run(strings.Join(tc.args, " "), func(t *testing.T) {
			reader, catalog := &fakeReader{}, &fakeCatalog{}
			var out, stderr bytes.Buffer
			if err := run(context.Background(), tc.args, &out, &stderr, fakeClients(reader, catalog)); err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(reader.calls, tc.calls) {
				t.Fatalf("unexpected source calls: %v", reader.calls)
			}
			if !strings.Contains(out.String(), tc.output) || !json.Valid(out.Bytes()) {
				t.Fatalf("invalid output: %s", &out)
			}
			if tc.args[0] == "inspect" && len(tc.calls) == 1 && strings.Contains(out.String(), `"progress"`) {
				t.Fatal("progress fetched/displayed without approval")
			}
			if tc.args[0] == "match" {
				if !reflect.DeepEqual(catalog.calls, []string{"match:Original title"}) {
					t.Fatalf("unexpected target calls: %v", catalog.calls)
				}
				if strings.Contains(out.String(), "catalogs_complete") || strings.Contains(out.String(), "catalog_assertions") {
					t.Fatalf("no-source match changed the legacy report: %s", out.String())
				}
			} else if len(catalog.calls) != 0 {
				t.Fatal("inspection contacted Hardcover")
			}
		})
	}
}

func TestCLIHelpAndInvalidInputsDoNotNeedCredentials(t *testing.T) {
	for _, args := range [][]string{nil, {"help"}, {"--help"}, {"link", "-h"}, {"inspect", "--help"}, {"catalog", "--help"}, {"catalog", "export", "--help"}} {
		var out, stderr bytes.Buffer
		if err := run(context.Background(), args, &out, &stderr, clients{}); err != nil {
			t.Fatal(err)
		}
		if out.Len()+stderr.Len() == 0 {
			t.Fatal("missing help")
		}
	}
	secret := "synthetic-secret-never-echo"
	for _, args := range [][]string{
		{"daemon"}, {"search"}, {"inspect", "--book-id", "0"},
		{"link", "--book-id", "123", "--work-id", "42"},
		{"unlink", "--book-id", "123", "--account", "source"},
		{"link", "--work-id", secret}, {"search", "--api-key", secret},
		{"links", secret},
	} {
		var out, stderr bytes.Buffer
		err := run(context.Background(), args, &out, &stderr, clients{})
		if err == nil {
			t.Fatalf("invalid args accepted: %v", args)
		}
		if strings.Contains(err.Error()+out.String()+stderr.String(), secret) {
			t.Fatal("parse error echoed secret")
		}
	}
}

func TestCLILinkReplacementAndLocalLifecycle(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "mappings.sqlite")
	reader, catalog := &fakeReader{}, &fakeCatalog{}
	providers := fakeClients(reader, catalog)
	linkArgs := []string{"link", "--book-id", "123", "--work-id", "42", "--account", "source", "--target-account", "target", "--db", path}
	var out, stderr bytes.Buffer
	if err := run(ctx, linkArgs, &out, &stderr, providers); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), `"edition_id": 0`) {
		t.Fatal("work-only confirmation assigned an edition")
	}
	changed := append([]string{}, linkArgs...)
	changed[4] = "99"
	if err := run(ctx, changed, &out, &stderr, providers); err == nil {
		t.Fatal("CLI silently replaced mapping")
	}
	if err := run(ctx, append(changed, "--replace"), &out, &stderr, providers); err != nil {
		t.Fatal(err)
	}
	out.Reset()
	// Nil factories prove these commands cannot request credentials or network clients.
	if err := run(ctx, []string{"links", "--db", path}, &out, &stderr, clients{}); err != nil {
		t.Fatal(err)
	}
	var rows []companion.Mapping
	if err := json.Unmarshal(out.Bytes(), &rows); err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0].WorkID != 99 || rows[0].EditionID != 0 {
		t.Fatalf("unexpected mappings: %+v", rows)
	}
	if err := run(ctx, []string{"unlink", "--book-id", "123", "--account", "source", "--target-account", "other", "--db", path}, &out, &stderr, clients{}); err != nil {
		t.Fatal(err)
	}
	out.Reset()
	if err := run(ctx, []string{"links", "--db", path}, &out, &stderr, clients{}); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(out.Bytes(), &rows); err != nil || len(rows) != 1 {
		t.Fatal("unlink affected another account")
	}
	if err := run(ctx, []string{"unlink", "--book-id", "123", "--account", "source", "--target-account", "target", "--db", path}, &out, &stderr, clients{}); err != nil {
		t.Fatal(err)
	}
	out.Reset()
	if err := run(ctx, []string{"links", "--db", path}, &out, &stderr, clients{}); err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(out.String()) != "[]" {
		t.Fatalf("mapping not removed: %s", &out)
	}
}

func TestCLIRejectedEditionDoesNotCreateMapping(t *testing.T) {
	path := filepath.Join(t.TempDir(), "mappings.sqlite")
	var out, stderr bytes.Buffer
	err := run(context.Background(), []string{"link", "--book-id", "123", "--work-id", "42", "--edition-id", "100", "--account", "source", "--target-account", "target", "--db", path}, &out, &stderr, fakeClients(&fakeReader{}, &fakeCatalog{reject: true}))
	if err == nil || out.Len() != 0 {
		t.Fatal("invalid edition confirmed")
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatal("rejected confirmation created local state")
	}
}

func TestCLIValidatesBothCredentialsBeforeFetchingBook(t *testing.T) {
	reader := &fakeReader{}
	providers := clients{source: func() (source, error) { return reader, nil },
		target: func() (target, error) { return nil, errors.New("Hardcover credential missing or invalid") }}
	var out, stderr bytes.Buffer
	if err := run(context.Background(), []string{"match", "--book-id", "123"}, &out, &stderr, providers); err == nil {
		t.Fatal("missing credential ignored")
	}
	if len(reader.calls) != 0 || out.Len() != 0 {
		t.Fatal("partial provider access/output on missing target credential")
	}
}

func TestCLIDefaultStatePath(t *testing.T) {
	base := t.TempDir()
	t.Setenv("XDG_STATE_HOME", base)
	path, err := defaultDB()
	if err != nil || path != filepath.Join(base, "drifter", "mappings.sqlite") {
		t.Fatalf("default path: %q %v", path, err)
	}
	var out, stderr bytes.Buffer
	if err := run(context.Background(), []string{"links"}, &out, &stderr, clients{}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatal(err)
	}
	t.Setenv("XDG_STATE_HOME", "relative")
	if _, err := defaultDB(); err == nil {
		t.Fatal("relative XDG state home accepted")
	}
}

func TestCLIServeProbesWiring(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.toml")
	dbPath := filepath.Join(dir, "mappings.sqlite")
	if err := os.WriteFile(configPath, []byte("[probes]\nenabled = true\naddr = \"127.0.0.1:0\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	var raw bytes.Buffer
	out := &syncWriter{mu: &mu, buf: &raw}
	var stderr bytes.Buffer
	done := make(chan error, 1)
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		done <- run(ctx, []string{"serve-probes", "--config", configPath, "--db", dbPath}, out, &stderr, clients{})
	}()
	var line string
	for i := 0; i < 200; i++ {
		line = out.String()
		if strings.Contains(line, "probe server starting") {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if !strings.Contains(line, `"msg":"probe server starting"`) || !strings.Contains(line, "run_id") {
		t.Fatalf("expected structured startup log with run id, got: %s", line)
	}
	if strings.Contains(line, "synthetic-secret") {
		t.Fatal("log echoed secret")
	}
	cancel()
	if err := <-done; err != nil {
		t.Fatalf("serve-probes failed: %v", err)
	}
}

// syncWriter serializes log writes with test reads under -race.
type syncWriter struct {
	mu  *sync.Mutex
	buf *bytes.Buffer
}

func (w *syncWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.buf.Write(p)
}

func (w *syncWriter) String() string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.buf.String()
}

const writerSubprocessEnv = "DRIFTER_CLI_WRITER_HELPER"

func TestCLIWriterSubprocessHelper(t *testing.T) {
	mode := os.Getenv(writerSubprocessEnv)
	if mode == "" {
		return
	}
	path := os.Getenv("DRIFTER_CLI_WRITER_DB")
	args := []string{"--db", path}
	switch mode {
	case "link":
		args = append([]string{"link", "--book-id", "123", "--work-id", "42", "--account", "source", "--target-account", "target"}, args...)
	case "unlink":
		args = append([]string{"unlink", "--book-id", "123", "--account", "source", "--target-account", "target"}, args...)
	default:
		t.Fatalf("unknown CLI helper mode %q", mode)
	}
	reader := &fakeReader{}
	var out, stderr bytes.Buffer
	err := run(context.Background(), args, &out, &stderr, fakeClients(reader, &fakeCatalog{}))
	if err == nil {
		t.Fatal("CLI mutation unexpectedly succeeded")
	}
	if mode == "link" && len(reader.calls) != 0 {
		t.Fatalf("link contacted provider before ownership refusal: %v", reader.calls)
	}
	_, _ = fmt.Fprintln(os.Stdout, err.Error())
}

func TestCLIWritersRefuseDaemonOwnershipAndReadsRemainAvailable(t *testing.T) {
	path := filepath.Join(t.TempDir(), "mappings.sqlite")
	owner, err := companion.AcquireDatabaseOwnership(path)
	if err != nil {
		t.Fatal(err)
	}
	defer owner.Close()
	store, err := companion.OpenOwnedStore(path, owner)
	if err != nil {
		t.Fatal(err)
	}
	mapping := companion.Mapping{Provider: "weread", SourceAccount: "source", BookID: "123", TargetAccount: "target", WorkID: 42}
	if err := store.Confirm(context.Background(), mapping, false); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	for _, mode := range []string{"link", "unlink"} {
		cmd := exec.Command(os.Args[0], "-test.run=^TestCLIWriterSubprocessHelper$")
		cmd.Env = append(os.Environ(), writerSubprocessEnv+"="+mode, "DRIFTER_CLI_WRITER_DB="+path)
		output, err := cmd.CombinedOutput()
		if err != nil || !strings.Contains(string(output), "mapping database is owned or write-locked") {
			t.Fatalf("%s subprocess was not clearly refused: %q %v", mode, output, err)
		}
	}
	var out, stderr bytes.Buffer
	if err := run(context.Background(), []string{"links", "--db", path}, &out, &stderr, clients{}); err != nil {
		t.Fatalf("read-only links failed during ownership: %v", err)
	}
	var rows []companion.Mapping
	if err := json.Unmarshal(out.Bytes(), &rows); err != nil || len(rows) != 1 || rows[0].WorkID != 42 {
		t.Fatalf("owned-state read: rows=%+v err=%v", rows, err)
	}
}

func TestCLIServeStartsWithoutProviderCredentials(t *testing.T) {
	t.Setenv("WEREAD_API_KEY", "")
	t.Setenv("HARDCOVER_API_KEY", "")
	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.toml")
	if err := os.WriteFile(configPath, []byte("[server]\naddr = \"127.0.0.1:0\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	var raw bytes.Buffer
	out := &syncWriter{mu: &sync.Mutex{}, buf: &raw}
	var stderr bytes.Buffer
	providers := clients{
		source: func() (source, error) { return companion.NewWeRead(os.Getenv("WEREAD_API_KEY")) },
		target: func() (target, error) { return companion.NewHardcover(os.Getenv("HARDCOVER_API_KEY")) },
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- run(ctx, []string{"serve", "--config", configPath, "--db", filepath.Join(dir, "mappings.sqlite")}, out, &stderr, providers)
	}()
	var record struct {
		Addr    string `json:"addr"`
		Message string `json:"msg"`
	}
	for i := 0; i < 200; i++ {
		for _, line := range strings.Split(strings.TrimSpace(out.String()), "\n") {
			_ = json.Unmarshal([]byte(line), &record)
		}
		if record.Message == "HTTP server starting" {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if record.Message != "HTTP server starting" {
		cancel()
		t.Fatalf("HTTP server did not start without provider credentials: %s", out.String())
	}
	for _, path := range []string{"/", "/api/mappings"} {
		response, err := http.Get("http://" + record.Addr + path)
		if err != nil {
			cancel()
			t.Fatal(err)
		}
		response.Body.Close()
		if response.StatusCode != http.StatusOK {
			cancel()
			t.Fatalf("GET %s status=%d without provider credentials", path, response.StatusCode)
		}
	}
	cancel()
	if err := <-done; err != nil {
		t.Fatalf("serve did not stop cleanly without provider credentials: %v", err)
	}
}

func TestCLIServeHTTPWiringAndCancellation(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.toml")
	if err := os.WriteFile(configPath, []byte("[server]\naddr = \"127.0.0.1:0\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	var raw bytes.Buffer
	out := &syncWriter{mu: &sync.Mutex{}, buf: &raw}
	var stderr bytes.Buffer
	reader := &fakeReader{}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- run(ctx, []string{"serve", "--config", configPath, "--db", filepath.Join(dir, "mappings.sqlite")}, out, &stderr, fakeClients(reader, &fakeCatalog{}))
	}()
	var record struct {
		Addr    string `json:"addr"`
		Message string `json:"msg"`
	}
	for i := 0; i < 200; i++ {
		for _, line := range strings.Split(strings.TrimSpace(out.String()), "\n") {
			_ = json.Unmarshal([]byte(line), &record)
		}
		if record.Message == "HTTP server starting" {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if record.Message != "HTTP server starting" || !strings.HasPrefix(record.Addr, "127.0.0.1:") {
		cancel()
		t.Fatalf("HTTP server startup log missing: %s", out.String())
	}
	response, err := http.Get("http://" + record.Addr + "/healthz")
	if err != nil {
		cancel()
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusOK || len(reader.calls) != 0 {
		cancel()
		t.Fatalf("health status=%d provider calls=%v", response.StatusCode, reader.calls)
	}
	cancel()
	if err := <-done; err != nil {
		t.Fatalf("serve did not stop cleanly: %v", err)
	}
}

func TestCLIServeProbesRejectsInvalidConfig(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.toml")
	if err := os.WriteFile(configPath, []byte("werede_api_key = \"synthetic-secret\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	var out, stderr bytes.Buffer
	err := run(context.Background(), []string{"serve-probes", "--config", configPath, "--db", filepath.Join(dir, "m.sqlite")}, &out, &stderr, clients{})
	if err == nil || !strings.Contains(err.Error(), "credential") {
		t.Fatalf("credential-bearing config accepted: %v", err)
	}
	if strings.Contains(err.Error()+out.String(), "synthetic-secret") {
		t.Fatal("error echoed secret")
	}
}
