package server_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/azusachino/drifter/internal/companion"
	"github.com/azusachino/drifter/internal/provider/hardcover"
	"github.com/azusachino/drifter/internal/provider/weread"
	"github.com/azusachino/drifter/internal/server"
)

type synchronizedBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *synchronizedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *synchronizedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

func TestRunHelpIsUsefulWithoutCredentials(t *testing.T) {
	var out, errOut bytes.Buffer
	if err := server.Run(context.Background(), []string{"--help"}, &out, &errOut, server.ProviderFactories{}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "-config") || !strings.Contains(out.String(), "-db") || errOut.Len() != 0 {
		t.Fatalf("server help output: stdout=%q stderr=%q", out.String(), errOut.String())
	}
}

func TestRunSanitizesBadArgumentsBeforeInitializingStateOrProviders(t *testing.T) {
	secret := "synthetic-cli-secret-7f93"
	stateDir := filepath.Join(t.TempDir(), "not-created")
	dbPath := filepath.Join(stateDir, "mappings.sqlite")
	var sourceCalls, targetCalls int
	providers := server.ProviderFactories{
		Source: func() (companion.Source, error) { sourceCalls++; return nil, nil },
		Target: func() (companion.Target, error) { targetCalls++; return nil, nil },
	}
	for _, args := range [][]string{{"--api-key", secret}, {"--db", dbPath, secret}} {
		var out, errOut bytes.Buffer
		err := server.Run(context.Background(), args, &out, &errOut, providers)
		if err == nil {
			t.Fatalf("invalid arguments accepted: %v", args)
		}
		if strings.Contains(err.Error()+out.String()+errOut.String(), secret) {
			t.Fatalf("parse failure exposed argument: err=%q stdout=%q stderr=%q", err, out.String(), errOut.String())
		}
		if out.Len() != 0 || errOut.Len() != 0 {
			t.Fatalf("parse failure emitted output: stdout=%q stderr=%q", out.String(), errOut.String())
		}
		if _, err := os.Stat(stateDir); !os.IsNotExist(err) {
			t.Fatalf("invalid arguments initialized state directory: %v", err)
		}
		if sourceCalls != 0 || targetCalls != 0 {
			t.Fatalf("invalid arguments initialized providers: source=%d target=%d", sourceCalls, targetCalls)
		}
	}
}

func TestRunStartsWithoutProviderCredentialsAndStops(t *testing.T) {
	t.Setenv("WEREAD_API_KEY", "")
	t.Setenv("HARDCOVER_API_KEY", "")
	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.toml")
	configuredDB := filepath.Join(dir, "configured.sqlite")
	cliDB := filepath.Join(dir, "cli.sqlite")
	configBody := fmt.Sprintf("[server]\naddr = \"127.0.0.1:0\"\n[database]\npath = %q\n", configuredDB)
	if err := os.WriteFile(configPath, []byte(configBody), 0o600); err != nil {
		t.Fatal(err)
	}
	var out synchronizedBuffer
	var errOut bytes.Buffer
	var sourceCalls, targetCalls int
	providers := server.ProviderFactories{
		Source: func() (companion.Source, error) {
			sourceCalls++
			return weread.New(os.Getenv("WEREAD_API_KEY"))
		},
		Target: func() (companion.Target, error) {
			targetCalls++
			return hardcover.New(os.Getenv("HARDCOVER_API_KEY"))
		},
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- server.Run(ctx, []string{"--config", configPath, "--db", cliDB}, &out, &errOut, providers)
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
		t.Fatalf("server failed to start without provider credentials: %s", out.String())
	}
	response, err := http.Get("http://" + record.Addr + "/healthz")
	if err != nil {
		cancel()
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusOK {
		cancel()
		t.Fatalf("health status=%d", response.StatusCode)
	}
	if sourceCalls != 0 || targetCalls != 0 {
		cancel()
		t.Fatalf("startup initialized providers: source=%d target=%d", sourceCalls, targetCalls)
	}
	cancel()
	if err := <-done; err != nil {
		t.Fatalf("server did not stop cleanly: %v", err)
	}
	if _, err := os.Stat(configuredDB); err != nil {
		t.Fatalf("settings database.path did not override --db: %v", err)
	}
	if _, err := os.Stat(cliDB); !os.IsNotExist(err) {
		t.Fatalf("CLI --db unexpectedly overrode settings database.path: %v", err)
	}
}
