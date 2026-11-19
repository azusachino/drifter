package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/azusachino/drifter/internal/companion"
	"github.com/azusachino/drifter/internal/config"
	"github.com/azusachino/drifter/internal/provider/hardcover"
	"github.com/azusachino/drifter/internal/provider/weread"
	"github.com/azusachino/drifter/internal/server"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	providers := server.ProviderFactories{
		Source: func() (companion.Source, error) { return weread.New(os.Getenv("WEREAD_API_KEY")) },
		Target: func() (companion.Target, error) { return hardcover.New(os.Getenv("HARDCOVER_API_KEY")) },
		SourceWithConfig: func(p config.ProvidersConfig) (companion.Source, error) {
			client, err := weread.NewWithOptions(
				os.Getenv("WEREAD_API_KEY"),
				config.ParseDurationOr(p.WeReadInterval, 0),
				config.ParseDurationOr(p.Timeout, 0),
			)
			if err != nil {
				return nil, err
			}
			client.SetSkillVersion(p.WeReadSkillVersion)
			return client, nil
		},
		TargetWithConfig: func(p config.ProvidersConfig) (companion.Target, error) {
			return hardcover.NewWithOptions(
				os.Getenv("HARDCOVER_API_KEY"),
				config.ParseDurationOr(p.HardcoverInterval, 0),
				config.ParseDurationOr(p.Timeout, 0),
			)
		},
	}
	if err := server.Run(ctx, os.Args[1:], os.Stdout, os.Stderr, providers); err != nil {
		fmt.Fprintln(os.Stderr, "drifter-server:", err)
		os.Exit(1)
	}
}
