package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/azusachino/drifter/internal/companion"
	"github.com/azusachino/drifter/internal/httpserver"
)

type source struct{}

func (source) Shelf(context.Context) (companion.ShelfResult, error) {
	return companion.ShelfResult{Complete: true}, nil
}
func (source) Search(context.Context, string) (companion.SearchResult, error) {
	return companion.SearchResult{}, nil
}
func (source) Book(_ context.Context, id string) (companion.Book, error) {
	return companion.Book{ID: id, Title: "Synthetic source title", TranslatorKnown: true}, nil
}
func (source) Progress(context.Context, string) (companion.Progress, error) {
	return companion.Progress{}, errors.New("synthetic progress is not configured")
}

type target struct{}

func (target) Match(context.Context, companion.Book, string) (companion.MatchReport, error) {
	return companion.MatchReport{}, nil
}
func (target) ValidateLink(_ context.Context, workID, editionID int64) error {
	if workID == 999 || editionID == 999 {
		return errors.New("synthetic target mismatch")
	}
	return nil
}

func main() {
	address := flag.String("addr", "127.0.0.1:0", "synthetic server listener")
	database := flag.String("db", "", "temporary test database")
	flag.Parse()
	if *database == "" {
		fmt.Fprintln(os.Stderr, "database path required")
		os.Exit(2)
	}
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	server, err := httpserver.New(httpserver.Options{Address: *address, Database: *database,
		Source: source{}, Target: target{}, Logger: logger})
	if err != nil {
		fmt.Fprintln(os.Stderr, "synthetic HTTP server unavailable")
		os.Exit(1)
	}
	listener, err := server.Listen()
	if err != nil {
		fmt.Fprintln(os.Stderr, "synthetic HTTP listener unavailable")
		os.Exit(1)
	}
	logger.Info("HTTP server starting", slog.String("addr", listener.Addr().String()))
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	if err := server.Serve(ctx, listener); err != nil {
		fmt.Fprintln(os.Stderr, "synthetic HTTP server stopped unexpectedly")
		os.Exit(1)
	}
}
