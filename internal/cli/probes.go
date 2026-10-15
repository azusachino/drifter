package cli

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os"

	"github.com/azusachino/drifter/internal/companion"
	"github.com/azusachino/drifter/internal/config"
	"github.com/azusachino/drifter/internal/logging"
	"github.com/azusachino/drifter/internal/probes"
)

// serveProbes runs the read-only health endpoint. It performs no
// synchronization and makes no provider network calls; readiness reflects
// local state only.
func serveProbes(ctx context.Context, configPath, dbPath string, requireProviders bool, out io.Writer) error {
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
	ready := []probes.Readiness{{
		Name: "mapping_database",
		Check: func(*http.Request) error {
			store, err := companion.OpenStoreForRead(dbPath)
			if err != nil {
				return err
			}
			defer store.Close()
			if _, err := store.List(ctx); err != nil {
				return err
			}
			return nil
		},
	}}
	if requireProviders {
		ready = append(ready, probes.Readiness{
			Name: "provider_credentials",
			Check: func(*http.Request) error {
				for name, key := range map[string]string{
					"WEREAD_API_KEY":    os.Getenv("WEREAD_API_KEY"),
					"HARDCOVER_API_KEY": os.Getenv("HARDCOVER_API_KEY"),
				} {
					if key == "" {
						return fmt.Errorf("%s missing", name)
					}
				}
				return nil
			},
		})
	}
	server, err := probes.New(cfg.Probes.Addr, ready, logger)
	if err != nil {
		return err
	}
	listener, err := server.Listen()
	if err != nil {
		return err
	}
	logger.InfoContext(ctx, "probe server starting",
		"run_id", runID, "addr", listener.Addr().String(),
		"require_providers", requireProviders)
	return server.Serve(ctx, listener)
}
