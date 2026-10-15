package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/azusachino/drifter/internal/companion"
)

func TestCLIMatchLoadsExplicitLocalCatalogThroughCore(t *testing.T) {
	catalogPath, err := filepath.Abs(filepath.Join("..", "..", "examples", "matching-catalog.json"))
	if err != nil {
		t.Fatal(err)
	}
	configPath := filepath.Join(t.TempDir(), "config.toml")
	configBody := "[[matching.catalogs]]\nid = \"synthetic\"\nfile = " + strconv.Quote(catalogPath) + "\n"
	if err := os.WriteFile(configPath, []byte(configBody), 0o600); err != nil {
		t.Fatal(err)
	}
	reader, target := &fakeReader{}, &fakeCatalog{}
	var out, stderr bytes.Buffer
	err = run(context.Background(), []string{"match", "--book-id", "42", "--config", configPath}, &out, &stderr, fakeClients(reader, target))
	if err != nil {
		t.Fatal(err)
	}
	var report map[string]any
	if err := json.Unmarshal(out.Bytes(), &report); err != nil {
		t.Fatal(err)
	}
	assertions, ok := report["catalog_assertions"].([]any)
	hints, hintsOK := report["catalog_hints"].([]any)
	if report["catalogs_complete"] != true || !ok || len(assertions) != 1 || !hintsOK || len(hints) != 1 || len(target.calls) != 1 {
		t.Fatalf("CLI did not return core catalog evidence: %s target=%v", out.String(), target.calls)
	}
	if strings.Contains(out.String(), catalogPath) || strings.Contains(stderr.String(), catalogPath) {
		t.Fatal("CLI exposed local catalog path")
	}
}

func TestCLIMatchReportsIncompleteCatalogAndPreservesProviderResult(t *testing.T) {
	configDir := t.TempDir()
	catalogPath := filepath.Join(configDir, "invalid.json")
	if err := os.WriteFile(catalogPath, []byte(`{"catalog_schema_version":1,"private":"catalog-content-private-marker"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	configPath := filepath.Join(configDir, "config.toml")
	configBody := "[[matching.catalogs]]\nid = \"community-example\"\nfile = " + strconv.Quote(catalogPath) + "\n"
	if err := os.WriteFile(configPath, []byte(configBody), 0o600); err != nil {
		t.Fatal(err)
	}
	reader, target := &fakeReader{}, &fakeCatalog{}
	var out, stderr bytes.Buffer
	err := run(context.Background(), []string{"match", "--book-id", "42", "--config", configPath}, &out, &stderr, fakeClients(reader, target))
	if err == nil || !strings.Contains(err.Error(), "incomplete") {
		t.Fatalf("incomplete catalog did not produce a nonzero command result: %v", err)
	}
	var report map[string]any
	if decodeErr := json.Unmarshal(out.Bytes(), &report); decodeErr != nil {
		t.Fatalf("provider result was not printed as JSON: %v %s", decodeErr, out.String())
	}
	issues, ok := report["catalog_issues"].([]any)
	if report["catalogs_complete"] != false || report["source_book"] == nil || !ok || len(issues) != 1 || len(target.calls) != 1 {
		t.Fatalf("provider result or incomplete issue missing: %s target=%v", out.String(), target.calls)
	}
	encodedIssue, _ := json.Marshal(issues[0])
	if strings.Contains(out.String()+stderr.String()+err.Error()+string(encodedIssue), catalogPath) || strings.Contains(out.String()+stderr.String()+err.Error(), "catalog-content-private-marker") {
		t.Fatal("catalog error exposed source path or document contents")
	}
}

func TestCLIInvalidCatalogConfigurationPrecedesProviderConstruction(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(configPath, []byte("[[matching.catalogs]]\nid = \"remote\"\nurl = \"https://catalog.example.org/catalog.json?token=private\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	providerCalls := 0
	providers := clients{
		source: func() (source, error) { providerCalls++; return &fakeReader{}, nil },
		target: func() (target, error) { providerCalls++; return &fakeCatalog{}, nil },
	}
	var out, stderr bytes.Buffer
	err := run(context.Background(), []string{"match", "--book-id", "42", "--config", configPath}, &out, &stderr, providers)
	if err == nil || providerCalls != 0 || strings.Contains(err.Error()+out.String()+stderr.String(), "private") {
		t.Fatalf("invalid catalog configuration reached providers or leaked config: err=%v calls=%d", err, providerCalls)
	}
}

func TestCLICatalogExportIsAccountScopedAndDoesNotConstructProviders(t *testing.T) {
	path := filepath.Join(t.TempDir(), "mappings.sqlite")
	store, err := companion.OpenStore(path)
	if err != nil {
		t.Fatal(err)
	}
	mapping := companion.Mapping{Provider: "weread", SourceAccount: "source-label", BookID: "123", TargetAccount: "target-label", WorkID: 55, EditionID: 66, SourceTitle: "private-title-marker", SourceISBN: "private-isbn-marker"}
	if err := store.Confirm(context.Background(), mapping, false); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	var out, stderr bytes.Buffer
	err = run(context.Background(), []string{"export", "--account", mapping.SourceAccount, "--target-account", mapping.TargetAccount,
		"--catalog-id", "shared", "--publisher", "public publisher", "--provenance-source", "public source", "--provenance-reference", "public ref", "--db", path}, &out, &stderr, clients{})
	if err != nil || !json.Valid(out.Bytes()) {
		t.Fatalf("catalog export failed: %v %s", err, out.String())
	}
	for _, private := range []string{mapping.SourceAccount, mapping.TargetAccount, mapping.SourceTitle, mapping.SourceISBN} {
		if strings.Contains(out.String(), private) {
			t.Fatalf("export disclosed private value %q", private)
		}
	}
	out.Reset()
	err = run(context.Background(), []string{"export", "--catalog-id", "shared", "--publisher", "publisher", "--provenance-source", "source", "--provenance-reference", "ref", "--db", filepath.Join(t.TempDir(), "absent.sqlite")}, &out, &stderr, clients{})
	if err == nil || out.Len() != 0 {
		t.Fatalf("missing state exported partial data: err=%v stdout=%q", err, out.String())
	}
}

func TestCLICatalogCommandsRequireExplicitSelections(t *testing.T) {
	for _, args := range [][]string{
		{"export", "--catalog-id", "shared", "--publisher", "publisher", "--provenance-source", "source", "--provenance-reference", "ref"},
		{"merge", "--catalog-id", "merged", "--publisher", "publisher", "--input", "one.json"},
	} {
		var out, stderr bytes.Buffer
		if err := run(context.Background(), args, &out, &stderr, clients{}); err == nil || out.Len() != 0 {
			t.Fatalf("catalog command accepted incomplete explicit selection %v: err=%v output=%q", args, err, out.String())
		}
	}
}

func TestCLICatalogMergeWritesOnlyCompleteValidatedOutput(t *testing.T) {
	dir := t.TempDir()
	input := `{"catalog_schema_version":1,"catalog_id":"one","publisher":"p","assertions":[],"hints":[]}`
	first, second := filepath.Join(dir, "one.json"), filepath.Join(dir, "two.json")
	if err := os.WriteFile(first, []byte(input), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(second, []byte(strings.Replace(input, `"one"`, `"two"`, 1)), 0o600); err != nil {
		t.Fatal(err)
	}
	var out, stderr bytes.Buffer
	err := run(context.Background(), []string{"merge", "--catalog-id", "merged", "--publisher", "chosen", "--input", first, "--input", second}, &out, &stderr, clients{})
	if err != nil || !json.Valid(out.Bytes()) {
		t.Fatalf("valid merge failed: %v %s", err, out.String())
	}
	out.Reset()
	bad := filepath.Join(dir, "invalid-private-path.json")
	if err := os.WriteFile(bad, []byte(`{"broken":true}`), 0o600); err != nil {
		t.Fatal(err)
	}
	err = run(context.Background(), []string{"merge", "--catalog-id", "merged", "--publisher", "chosen", "--input", first, "--input", bad}, &out, &stderr, clients{})
	if err == nil || out.Len() != 0 || strings.Contains(err.Error()+stderr.String(), bad) {
		t.Fatalf("invalid merge emitted output or exposed local path: err=%v stdout=%q stderr=%q", err, out.String(), stderr.String())
	}
}
