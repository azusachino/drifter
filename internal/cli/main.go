package cli

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/azusachino/drifter/internal/advisor"
	"github.com/azusachino/drifter/internal/advisor/opencode"
	"github.com/azusachino/drifter/internal/companion"
	"github.com/azusachino/drifter/internal/provider/hardcover"
	"github.com/azusachino/drifter/internal/provider/weread"
)

// Main runs the interactive command-line application.
func Main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	providers := clients{
		source: func() (source, error) { return weread.New(os.Getenv("WEREAD_API_KEY")) },
		target: func() (target, error) { return hardcover.New(os.Getenv("HARDCOVER_API_KEY")) },
		advisor: func(store *companion.Store) (advisor.Advisor, error) {
			cfg, err := opencode.LoadConfig()
			if err != nil {
				return nil, err
			}
			client, err := opencode.NewWithConfig(cfg)
			if err != nil {
				return nil, err
			}
			if store != nil {
				return advisor.NewCachedAdvisor(client, store, cfg.Model, opencode.PromptHash()), nil
			}
			return client, nil
		},
	}
	if err := run(ctx, os.Args[1:], os.Stdout, os.Stderr, providers); err != nil {
		fmt.Fprintln(os.Stderr, "drifter:", err)
		os.Exit(1)
	}
}
