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
	providers := clients{
		sourceWithConfig: func(p config.ProvidersConfig) (source, error) {
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
		targetWithConfig: func(p config.ProvidersConfig) (target, error) {
			return hardcover.NewWithOptions(
				os.Getenv("HARDCOVER_API_KEY"),
				config.ParseDurationOr(p.HardcoverInterval, 0),
				config.ParseDurationOr(p.Timeout, 0),
			)
		},
		advisorWithConfig: func(advCfg config.AdvisorConfig, store *companion.Store) (advisor.Advisor, error) {
			cfg, err := opencode.LoadConfigWithSettings(opencode.Settings{
				BaseURL: advCfg.BaseURL,
				Model:   advCfg.Model,
				Models:  advCfg.Models,
				Timeout: config.ParseDurationOr(advCfg.Timeout, 0),
			})
			if err != nil {
				return nil, err
			}
			client, err := opencode.NewWithConfig(cfg)
			if err != nil {
				return nil, err
			}
			if store != nil {
				ttl := config.ParseDurationOr(advCfg.CacheTTL, 0)
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
