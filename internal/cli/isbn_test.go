package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/azusachino/drifter/internal/isbn"
)

func TestISBNCommand(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "9787521768527") {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{
			"isbn": "9787521768527",
			"title": "智人之上",
			"original_title": "Nexus",
			"author": "Yuval Noah Harari",
			"publisher": "中信出版社",
			"year": 2024
		}`))
	}))
	defer server.Close()

	t.Run("positional-argument-custom-provider", func(t *testing.T) {
		var out, stderr bytes.Buffer
		args := []string{
			"isbn", "9787521768527",
			"--provider", "custom",
			"--url", server.URL + "/{isbn}",
		}
		if err := run(context.Background(), args, &out, &stderr, clients{}); err != nil {
			t.Fatalf("unexpected error: %v, stderr: %s", err, stderr.String())
		}
		var meta isbn.Metadata
		if err := json.Unmarshal(out.Bytes(), &meta); err != nil {
			t.Fatalf("failed to decode JSON output: %v, raw: %s", err, out.String())
		}
		if meta.ISBN != "9787521768527" || meta.Title != "智人之上" || meta.OriginalTitle != "Nexus" {
			t.Errorf("unexpected metadata: %+v", meta)
		}
	})

	t.Run("flag-argument", func(t *testing.T) {
		var out, stderr bytes.Buffer
		args := []string{
			"isbn",
			"--isbn", "9787521768527",
			"--provider", "custom",
			"--url", server.URL + "/{isbn}",
		}
		if err := run(context.Background(), args, &out, &stderr, clients{}); err != nil {
			t.Fatalf("unexpected error: %v, stderr: %s", err, stderr.String())
		}
		var meta isbn.Metadata
		if err := json.Unmarshal(out.Bytes(), &meta); err != nil {
			t.Fatalf("failed to decode JSON output: %v", err)
		}
		if meta.Year != 2024 {
			t.Errorf("expected year 2024, got %d", meta.Year)
		}
	})

	t.Run("missing-argument", func(t *testing.T) {
		var out, stderr bytes.Buffer
		args := []string{"isbn"}
		err := run(context.Background(), args, &out, &stderr, clients{})
		if err == nil || !strings.Contains(err.Error(), "ISBN required") {
			t.Fatalf("expected ISBN required error, got %v", err)
		}
	})

	t.Run("invalid-isbn", func(t *testing.T) {
		var out, stderr bytes.Buffer
		args := []string{"isbn", "12345"}
		err := run(context.Background(), args, &out, &stderr, clients{})
		if err == nil || !strings.Contains(err.Error(), "invalid ISBN") {
			t.Fatalf("expected invalid ISBN error, got %v", err)
		}
	})

	t.Run("not-found", func(t *testing.T) {
		var out, stderr bytes.Buffer
		args := []string{
			"isbn", "9780134494166",
			"--provider", "custom",
			"--url", server.URL + "/{isbn}",
		}
		err := run(context.Background(), args, &out, &stderr, clients{})
		if err == nil || !strings.Contains(err.Error(), "not found") {
			t.Fatalf("expected not found error, got %v", err)
		}
	})

	t.Run("disabled-in-config", func(t *testing.T) {
		dir := t.TempDir()
		cfgPath := filepath.Join(dir, "config.toml")
		if err := os.WriteFile(cfgPath, []byte(`
[matching.isbn]
enabled = false
`), 0o600); err != nil {
			t.Fatal(err)
		}

		var out, stderr bytes.Buffer
		args := []string{
			"isbn", "9787521768527",
			"--config", cfgPath,
		}
		err := run(context.Background(), args, &out, &stderr, clients{})
		if err == nil || !strings.Contains(err.Error(), "disabled in configuration") {
			t.Fatalf("expected disabled in configuration error, got %v", err)
		}
	})
}
