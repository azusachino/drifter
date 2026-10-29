package cli

import (
	"errors"
	"fmt"

	"github.com/azusachino/drifter/internal/companion"
	"github.com/azusachino/drifter/internal/config"
	"github.com/azusachino/drifter/internal/isbn"
	"github.com/spf13/cobra"
)

func (app *cliApp) isbnCommand() *cobra.Command {
	var isbnFlag, providerName, customURL, configPath string
	command := &cobra.Command{
		Use:   "isbn [isbn]",
		Short: "Query authoritative metadata for an ISBN",
		Args:  cobra.MaximumNArgs(1),
	}
	flags := command.Flags()
	flags.StringVar(&isbnFlag, "isbn", "", "ISBN to query")
	flags.StringVar(&providerName, "provider", "", "Override ISBN provider: default, douban, openlibrary, custom")
	flags.StringVar(&customURL, "url", "", "Custom provider URL template containing {isbn}")
	flags.StringVar(&configPath, "config", "config.toml", "settings-only TOML path (missing file uses defaults; never credentials)")

	return app.command(command, func(cmd *cobra.Command, args []string) error {
		rawISBN := isbnFlag
		if len(args) > 0 {
			rawISBN = args[0]
		}
		if rawISBN == "" {
			return fmt.Errorf("ISBN required (as argument or --isbn)")
		}

		norm, err := companion.NormalizeISBN(rawISBN)
		if err != nil {
			return fmt.Errorf("invalid ISBN %s: %w", rawISBN, err)
		}

		cfg, err := config.Load(configPath)
		if err != nil {
			return err
		}

		isbnCfg := cfg.Matching.ISBN
		if !isbnCfg.Enabled && !cmd.Flags().Changed("config") {
			isbnCfg.Enabled = true
		}
		if providerName != "" {
			isbnCfg.Enabled = true
			isbnCfg.Provider = providerName
		}
		if customURL != "" {
			isbnCfg.URL = customURL
		}
		if isbnCfg.Provider == "" {
			isbnCfg.Provider = "default"
		}
		if !isbnCfg.Enabled {
			return fmt.Errorf("ISBN metadata provider is disabled in configuration")
		}

		provider := isbn.NewProviderFromConfig(isbnCfg)
		if provider == nil {
			return fmt.Errorf("failed to initialize ISBN provider: invalid configuration")
		}

		meta, err := provider.LookupISBN(cmd.Context(), norm)
		if err != nil {
			if errors.Is(err, isbn.ErrNotFound) {
				return fmt.Errorf("ISBN %s not found", norm)
			}
			return fmt.Errorf("ISBN lookup failed: %w", err)
		}

		return encodeJSON(cmd, meta)
	})
}
