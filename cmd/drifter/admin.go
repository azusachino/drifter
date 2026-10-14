package main

import (
	"context"
	"fmt"
	"io"
	"net/http"

	"github.com/azusachino/drifter/internal/admin"
	"github.com/azusachino/drifter/internal/companion"
	"github.com/azusachino/drifter/internal/config"
	"github.com/azusachino/drifter/internal/logging"
)

func serveAdmin(ctx context.Context, configPath, dbPath string, out io.Writer, providers clients) error {
	cfg, err := config.Load(configPath)
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
		dbPath = cfg.Database.Path
	}
	if err := companion.CheckDatabaseWriteAvailable(dbPath); err != nil {
		return err
	}
	server, err := admin.New(admin.Options{Address: cfg.Server.Addr, Database: dbPath, SourceFactory: providers.source, TargetFactory: providers.target, Logger: logger})
	if err != nil {
		return err
	}
	listener, err := server.Listen()
	if err != nil {
		return err
	}
	logger.InfoContext(ctx, "admin server starting", "run_id", runID, "addr", listener.Addr().String())
	if err := server.Serve(ctx, listener); err != nil && err != http.ErrServerClosed {
		return fmt.Errorf("admin server failed")
	}
	return nil
}
