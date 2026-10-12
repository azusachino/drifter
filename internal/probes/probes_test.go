package probes

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"testing"
)

func testLogger() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

func get(t *testing.T, s *Server, path string) (*http.Response, string) {
	t.Helper()
	listener, err := s.Listen()
	if err != nil {
		t.Fatal(err)
	}
	go func() { _ = s.Serve(context.Background(), listener) }()
	t.Cleanup(func() { listener.Close() })
	response, err := http.Get("http://" + listener.Addr().String() + path)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	body, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatal(err)
	}
	return response, string(body)
}

func TestHealthzIsAlwaysLive(t *testing.T) {
	s, err := New("127.0.0.1:0", nil, testLogger())
	if err != nil {
		t.Fatal(err)
	}
	if response, _ := get(t, s, "/healthz"); response.StatusCode != http.StatusOK {
		t.Fatalf("healthz status: %d", response.StatusCode)
	}
}

func TestReadyzReflectsDependencies(t *testing.T) {
	healthy := Readiness{Name: "database", Check: func(*http.Request) error { return nil }}
	s, err := New("127.0.0.1:0", []Readiness{healthy}, testLogger())
	if err != nil {
		t.Fatal(err)
	}
	if response, _ := get(t, s, "/readyz"); response.StatusCode != http.StatusOK {
		t.Fatalf("readyz status with healthy dependency: %d", response.StatusCode)
	}

	failing := Readiness{Name: "database", Check: func(*http.Request) error { return errors.New("synthetic") }}
	s, err = New("127.0.0.1:0", []Readiness{failing}, testLogger())
	if err != nil {
		t.Fatal(err)
	}
	response, body := get(t, s, "/readyz")
	if response.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("readyz status with failed dependency: %d", response.StatusCode)
	}
	if strings.Contains(body, "synthetic") {
		t.Fatal("readyz body leaked internal error text")
	}
	if !strings.Contains(body, "database") {
		t.Fatal("readyz body omitted failing check name")
	}
}

func TestProbeServerRequiresLoggerAndAddress(t *testing.T) {
	if _, err := New("", nil, testLogger()); err == nil {
		t.Fatal("empty address accepted")
	}
	if _, err := New("127.0.0.1:0", nil, nil); err == nil {
		t.Fatal("missing logger accepted")
	}
}

func TestServeStopsOnContextCancel(t *testing.T) {
	s, err := New("127.0.0.1:0", nil, testLogger())
	if err != nil {
		t.Fatal(err)
	}
	listener, err := s.Listen()
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- s.Serve(ctx, listener) }()
	cancel()
	if err := <-done; err != nil {
		t.Fatalf("cancel produced error: %v", err)
	}
}

func TestListenReportsBusyAddress(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	s, err := New(listener.Addr().String(), nil, testLogger())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Listen(); err == nil {
		t.Fatal("busy address accepted")
	}
}
