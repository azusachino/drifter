package logging

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/azusachino/drifter/internal/config"
)

func TestNewEmitsJSONAtConfiguredLevel(t *testing.T) {
	var buf bytes.Buffer
	logger, err := New(config.LevelDebug, &buf)
	if err != nil {
		t.Fatal(err)
	}
	logger.Info("hello", "run", "x")
	logger.Debug("detail")
	lines := strings.Split(strings.TrimSpace(buf.String()), "\n")
	if len(lines) != 2 {
		t.Fatalf("expected two JSON records: %s", &buf)
	}
	var record map[string]any
	if err := json.Unmarshal([]byte(lines[0]), &record); err != nil {
		t.Fatalf("log line is not JSON: %v", err)
	}
	if record["msg"] != "hello" {
		t.Fatalf("unexpected record: %s", lines[0])
	}

	buf.Reset()
	logger, err = New(config.LevelError, &buf)
	if err != nil {
		t.Fatal(err)
	}
	logger.Info("dropped")
	if buf.Len() != 0 {
		t.Fatalf("info recorded at error level: %s", &buf)
	}
}

func TestNewRejectsUnsupportedLevel(t *testing.T) {
	if _, err := New(config.Level("loud"), nil); err == nil {
		t.Fatal("unsupported level accepted")
	}
}

func TestRunIDIsUniqueHex(t *testing.T) {
	first, err := NewRunID()
	if err != nil {
		t.Fatal(err)
	}
	second, err := NewRunID()
	if err != nil {
		t.Fatal(err)
	}
	if first == second || len(first) != 16 {
		t.Fatalf("weak run identifiers: %q %q", first, second)
	}
}
