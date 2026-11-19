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
	"github.com/azusachino/drifter/internal/config"
	"github.com/azusachino/drifter/internal/provider/hardcover"
	"github.com/azusachino/drifter/internal/provider/weread"
)

// Main runs the interactive command-line application.
func Main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	loadFileCfg := func() config.Config {
		cfg, err := config.Load(config.DefaultPath())
		if err != nil {
			return config.Defaults()
		}
		return cfg
	}
	providers := clients{
		source: func() (source, error) {
			p := loadFileCfg().Providers
			return weread.NewWithOptions(
				os.Getenv("WEREAD_API_KEY"),
				config.ParseDurationOr(p.WeReadInterval, 0),
				config.ParseDurationOr(p.Timeout, 0),
			)
		},
		target: func() (target, error) {
			p := loadFileCfg().Providers
			return hardcover.NewWithOptions(
				os.Getenv("HARDCOVER_API_KEY"),
				config.ParseDurationOr(p.HardcoverInterval, 0),
				config.ParseDurationOr(p.Timeout, 0),
			)
		},
		advisor: func(store *companion.Store) (advisor.Advisor, error) {
			fileCfg := loadFileCfg()
			cfg, err := opencode.LoadConfig()
			if err != nil {
				return nil, err
			}
			client, err := opencode.NewWithConfig(cfg)
			if err != nil {
				return nil, err
			}
			if store != nil {
				ttl := config.ParseDurationOr(fileCfg.Advisor.CacheTTL, 0)
				return advisor.NewCachedAdvisorWithTTL(client, store, cfg.Model, opencode.PromptHash(), ttl), nil
			}
			return client, nil
		},
	}
	if err := run(ctx, os.Args[1:], os.Stdout, os.Stderr, providers); err != nil {
		fmt.Fprintln(os.Stderr, "drifter:", err)
		os.Exit(1)
	}
}
