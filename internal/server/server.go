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

	"github.com/azusachino/drifter/internal/advisor/opencode"
	"github.com/azusachino/drifter/internal/companion"
	"github.com/azusachino/drifter/internal/config"
	"github.com/azusachino/drifter/internal/httpserver"
	"github.com/azusachino/drifter/internal/logging"
)

// ProviderFactories supplies lazy provider construction to the HTTP server.
// When SourceWithConfig or TargetWithConfig is set, it is preferred over
// Source or Target so provider timeouts and pacing come from config.toml.
type ProviderFactories struct {
	Source           func() (companion.Source, error)
	Target           func() (companion.Target, error)
	SourceWithConfig func(config.ProvidersConfig) (companion.Source, error)
	TargetWithConfig func(config.ProvidersConfig) (companion.Target, error)
}

// Run parses the server flags, loads settings and serves until ctx is canceled.
// Flag diagnostics are suppressed to avoid echoing arguments; errors are returned
// for the executable to report once.
func Run(ctx context.Context, args []string, out, _ io.Writer, providers ProviderFactories) error {
	flags := flag.NewFlagSet("drifter-server", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	configPath := flags.String("config", config.DefaultPath(), "settings-only TOML path (missing file uses defaults; never credentials)")
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
	sourceFactory := providers.Source
	if providers.SourceWithConfig != nil {
		provCfg := cfg.Providers
		sourceFactory = func() (companion.Source, error) { return providers.SourceWithConfig(provCfg) }
	}
	targetFactory := providers.Target
	if providers.TargetWithConfig != nil {
		provCfg := cfg.Providers
		targetFactory = func() (companion.Target, error) { return providers.TargetWithConfig(provCfg) }
	}
	paceDelay := config.ParseDurationOr(cfg.Jobs.AdvisorPaceDelay, 0)
	advisorOpts := &httpserver.AdvisorOptions{
		Settings: opencode.Settings{
			BaseURL: cfg.Advisor.BaseURL,
			Model:   cfg.Advisor.Model,
			Models:  cfg.Advisor.Models,
			Timeout: config.ParseDurationOr(cfg.Advisor.Timeout, 0),
		},
		CacheTTL: config.ParseDurationOr(cfg.Advisor.CacheTTL, 0),
	}
	server, err := httpserver.New(httpserver.Options{
		Address:           cfg.Server.Addr,
		AllowedHosts:      allowedHosts,
		Database:          *dbPath,
		SourceFactory:     sourceFactory,
		TargetFactory:     targetFactory,
		Logger:            logger,
		OperationTimeout:  config.ParseDurationOr(cfg.Server.OperationTimeout, 0),
		ReadHeaderTimeout: config.ParseDurationOr(cfg.Server.ReadHeaderTimeout, 0),
		ReadTimeout:       config.ParseDurationOr(cfg.Server.ReadTimeout, 0),
		WriteTimeout:      config.ParseDurationOr(cfg.Server.WriteTimeout, 0),
		IdleTimeout:       config.ParseDurationOr(cfg.Server.IdleTimeout, 0),
		ShutdownTimeout:   config.ParseDurationOr(cfg.Server.ShutdownTimeout, 0),
		Jobs: httpserver.JobWorkerOptions{
			PollInterval:       config.ParseDurationOr(cfg.Jobs.PollInterval, 0),
			BatchSize:          cfg.Jobs.BatchSize,
			BookStepTimeout:    config.ParseDurationOr(cfg.Jobs.BookStepTimeout, 0),
			ReconcileTimeout:   config.ParseDurationOr(cfg.Jobs.ReconcileTimeout, 0),
			CooldownBase:       config.ParseDurationOr(cfg.Jobs.CooldownBase, 0),
			CooldownMax:        config.ParseDurationOr(cfg.Jobs.CooldownMax, 0),
			MaxCooldownWait:    config.ParseDurationOr(cfg.Jobs.MaxCooldownWait, 0),
			MaxRateLimitRetry:  cfg.Jobs.MaxRateLimitRetry,
			AdvisorPaceDelay:   paceDelay,
			DisableAdvisorPace: paceDelay == 0,
		},
		Cache: httpserver.CacheOptions{
			MaxEntries: cfg.Cache.MaxEntries,
			ShelfTTL:   config.ParseDurationOr(cfg.Cache.ShelfTTL, 0),
			BookTTL:    config.ParseDurationOr(cfg.Cache.BookTTL, 0),
			SearchTTL:  config.ParseDurationOr(cfg.Cache.SearchTTL, 0),
			TargetTTL:  config.ParseDurationOr(cfg.Cache.TargetTTL, 0),
		},
		Advisor: advisorOpts,
	})
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
