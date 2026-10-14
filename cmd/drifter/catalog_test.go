package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
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
