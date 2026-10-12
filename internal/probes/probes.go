// Package probes serves minimal Kubernetes-style health endpoints.
//
// This is operational scaffolding, not the daemon: it owns no schedule and
// performs no synchronization. /healthz reports process liveness;
// /readyz reports whether local dependencies (the mapping database) are
// usable. It never exposes business data and binds only the configured
// address.
package probes

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"time"
)

const headerTimeout = 5 * time.Second

// Readiness is a named local dependency check.
type Readiness struct {
	// Name identifies the check in logs and the response body.
	Name string
	// Check returns nil when the dependency is usable.
	Check func(*http.Request) error
}

// Server is a bound, serving probe endpoint.
type Server struct {
	http    *http.Server
	ready   []Readiness
	address string
}

// New constructs a probe server. ready checks are evaluated per /readyz
// request so recovery is observable without restarts.
func New(address string, ready []Readiness, logger *slog.Logger) (*Server, error) {
	if address == "" {
		return nil, fmt.Errorf("probe address required")
	}
	if logger == nil {
		return nil, fmt.Errorf("probe logger required")
	}
	mux := http.NewServeMux()
	s := &Server{ready: ready, address: address}
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		fmt.Fprintln(w, "ok")
	})
	mux.HandleFunc("/readyz", func(w http.ResponseWriter, r *http.Request) {
		for _, dependency := range s.ready {
			if err := dependency.Check(r); err != nil {
				logger.WarnContext(r.Context(), "readiness check failed",
					slog.String("check", dependency.Name))
				w.WriteHeader(http.StatusServiceUnavailable)
				// The error text is a sanitized Drifter message, never a
				// provider response or credential.
				fmt.Fprintf(w, "not ready: %s\n", dependency.Name)
				return
			}
		}
		w.WriteHeader(http.StatusOK)
		fmt.Fprintln(w, "ready")
	})
	s.http = &http.Server{Addr: address, Handler: mux, ReadHeaderTimeout: headerTimeout}
	return s, nil
}

// Listen binds the configured address. Only a loopback default is assumed by
// callers; binding to a wildcard address is an explicit operator choice.
func (s *Server) Listen() (net.Listener, error) {
	listener, err := net.Listen("tcp", s.address)
	if err != nil {
		return nil, fmt.Errorf("probe address unavailable")
	}
	return listener, nil
}

// Serve accepts connections until ctx is canceled or the listener closes.
func (s *Server) Serve(ctx context.Context, listener net.Listener) error {
	errCh := make(chan error, 1)
	go func() { errCh <- s.http.Serve(listener) }()
	select {
	case err := <-errCh:
		if err == http.ErrServerClosed {
			return nil
		}
		return fmt.Errorf("probe server failed")
	case <-ctx.Done():
		return s.http.Shutdown(context.Background())
	}
}
