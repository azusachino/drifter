// Package server runs the standalone foreground HTTP server command.
package server

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/azusachino/drifter/internal/companion"
	"github.com/azusachino/drifter/internal/config"
	"github.com/azusachino/drifter/internal/httpserver"
	"github.com/azusachino/drifter/internal/logging"
)

// ProviderFactories supplies lazy provider construction to the HTTP server.
type ProviderFactories struct {
	Source func() (companion.Source, error)
	Target func() (companion.Target, error)
}

// Run parses the server flags, loads settings and serves until ctx is canceled.
// Flag diagnostics are suppressed to avoid echoing arguments; errors are returned
// for the executable to report once.
func Run(ctx context.Context, args []string, out, _ io.Writer, providers ProviderFactories) error {
	flags := flag.NewFlagSet("drifter-server", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	configPath := flags.String("config", "config.toml", "settings-only TOML path (missing file uses defaults; never credentials)")
	dbPath := flags.String("db", "", "private SQLite path (default: XDG_STATE_HOME/drifter/mappings.sqlite)")
	allowedHostsFlag := flags.String("allowed-hosts", "", "comma-separated additional allowed Host headers for reverse proxy/tailnet preview")
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			flags.SetOutput(out)
			flags.Usage()
			return nil
		}
		return fmt.Errorf("invalid flags; use drifter-server --help")
	}
	if flags.NArg() != 0 {
		return fmt.Errorf("unexpected arguments; use drifter-server --help")
	}
	cfg, err := config.Load(*configPath)
	if err != nil {
		return err
	}
	logger, err := logging.New(cfg.Log.Level, out)
	if err != nil {
		return err
	}
	runID, err := logging.NewRunID()
	if err != nil {
		return err
	}
	if cfg.Database.Path != "" {
		*dbPath = cfg.Database.Path
	}
	if *dbPath == "" {
		*dbPath, err = defaultDatabasePath()
		if err != nil {
			return err
		}
	}
	if err := companion.CheckDatabaseWriteAvailable(*dbPath); err != nil {
		return err
	}
	allowedHosts := cfg.Server.AllowedHosts
	if *allowedHostsFlag != "" {
		for _, h := range strings.Split(*allowedHostsFlag, ",") {
			if trimmed := strings.TrimSpace(h); trimmed != "" {
				allowedHosts = append(allowedHosts, trimmed)
			}
		}
	}
	server, err := httpserver.New(httpserver.Options{Address: cfg.Server.Addr, AllowedHosts: allowedHosts, Database: *dbPath,
		SourceFactory: providers.Source, TargetFactory: providers.Target, Logger: logger})
	if err != nil {
		return err
	}
	listener, err := server.Listen()
	if err != nil {
		return err
	}
	logger.InfoContext(ctx, "HTTP server starting", "run_id", runID, "addr", listener.Addr().String())
	if err := server.Serve(ctx, listener); err != nil && err != http.ErrServerClosed {
		return fmt.Errorf("HTTP server failed")
	}
	return nil
}

func defaultDatabasePath() (string, error) {
	base := os.Getenv("XDG_STATE_HOME")
	if base == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("state directory unavailable; use --db")
		}
		base = filepath.Join(home, ".local", "state")
	}
	if !filepath.IsAbs(base) {
		return "", fmt.Errorf("XDG_STATE_HOME must be absolute; use --db")
	}
	return filepath.Join(base, "drifter", "mappings.sqlite"), nil
}
