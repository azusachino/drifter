package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/azusachino/drifter/internal/catalog"
	"github.com/azusachino/drifter/internal/companion"
	"github.com/azusachino/drifter/internal/config"
	"github.com/spf13/cobra"
)

// Aliases keep credential factories local while core owns provider contracts.
type source = companion.Source
type target = companion.Target

type clients struct {
	source func() (source, error)
	target func() (target, error)
}

type cliApp struct {
	providers    clients
	commandError *error
	flagError    *error
	input        io.Reader
}

func defaultDB() (string, error) {
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

func run(ctx context.Context, args []string, out, errOut io.Writer, providers clients) error {
	return runWithInput(ctx, args, out, errOut, providers, os.Stdin)
}

func runWithInput(ctx context.Context, args []string, out, errOut io.Writer, providers clients, input io.Reader) error {
	var commandError, flagError error
	app := &cliApp{providers: providers, commandError: &commandError, flagError: &flagError, input: input}
	root := app.rootCommand(out, errOut)
	if args == nil {
		args = []string{}
	}
	root.SetArgs(args)
	if err := root.ExecuteContext(ctx); err != nil {
		if commandError != nil {
			return commandError
		}
		if flagError != nil {
			return flagError
		}
		return fmt.Errorf("invalid command or flags; use drifter help")
	}
	return nil
}

func (app *cliApp) rootCommand(out, errOut io.Writer) *cobra.Command {
	root := app.command(&cobra.Command{
		Use:   "drifter",
		Short: "Inspect WeRead books and maintain explicit local Hardcover mappings",
		Long: `Drifter provides read-only provider inspection, matching suggestions and
explicit local identity mappings. It never writes provider status. Credentials
come only from WEREAD_API_KEY and HARDCOVER_API_KEY in the process environment.`,
	}, func(cmd *cobra.Command, _ []string) error {
		return cmd.Help()
	})
	root.SilenceErrors = true
	root.SilenceUsage = true
	root.CompletionOptions.DisableDefaultCmd = true
	root.SetOut(out)
	root.SetErr(errOut)
	root.SetFlagErrorFunc(func(cmd *cobra.Command, _ error) error {
		err := fmt.Errorf("invalid flags; use %s -h", cmd.CommandPath())
		*app.flagError = err
		return err
	})

	root.AddCommand(
		app.searchCommand(),
		app.inspectCommand(),
		app.matchCommand(),
		app.pendingCommand(),
		app.reviewCommand(),
		app.decideCommand(),
		app.decisionsCommand(),
		app.resetDecisionCommand(),
		app.linkCommand(),
		app.linksCommand(),
		app.unlinkCommand(),
		app.exportCommand(),
		app.mergeCommand(),
		app.serveProbesCommand(),
	)
	return root
}

func (app *cliApp) command(command *cobra.Command, runE func(*cobra.Command, []string) error) *cobra.Command {
	command.Args = cobra.NoArgs
	command.RunE = func(cmd *cobra.Command, args []string) error {
		err := runE(cmd, args)
		if err != nil {
			*app.commandError = err
		}
		return err
	}
	return command
}

func markRequiredFlags(command *cobra.Command, names ...string) {
	for _, name := range names {
		if err := command.MarkFlagRequired(name); err != nil {
			panic(err)
		}
	}
}

func encodeJSON(cmd *cobra.Command, value any) error {
	encoder := json.NewEncoder(cmd.OutOrStdout())
	encoder.SetIndent("", "  ")
	return encoder.Encode(value)
}

func (app *cliApp) searchCommand() *cobra.Command {
	var title string
	command := &cobra.Command{Use: "search", Short: "Search WeRead books by title"}
	command.Flags().StringVar(&title, "title", "", "WeRead search title (required)")
	markRequiredFlags(command, "title")
	return app.command(command, func(cmd *cobra.Command, _ []string) error {
		if title == "" {
			return fmt.Errorf("--title required")
		}
		reader, err := app.providers.source()
		if err != nil {
			return err
		}
		result, err := reader.Search(cmd.Context(), title)
		if err != nil {
			return err
		}
		return encodeJSON(cmd, result)
	})
}

func (app *cliApp) inspectCommand() *cobra.Command {
	var bookID string
	var progress bool
	command := &cobra.Command{Use: "inspect", Short: "Inspect a WeRead book"}
	command.Flags().StringVar(&bookID, "book-id", "", "WeRead book ID (required)")
	command.Flags().BoolVar(&progress, "progress", false, "explicitly fetch personal reading progress")
	markRequiredFlags(command, "book-id")
	return app.command(command, func(cmd *cobra.Command, _ []string) error {
		if !companion.ValidBookID(bookID) {
			return fmt.Errorf("valid --book-id required")
		}
		reader, err := app.providers.source()
		if err != nil {
			return err
		}
		result, err := companion.Inspect(cmd.Context(), reader, bookID, progress)
		if err != nil {
			return err
		}
		return encodeJSON(cmd, result)
	})
}

func (app *cliApp) matchCommand() *cobra.Command {
	var bookID, query, configPath string
	command := &cobra.Command{Use: "match", Short: "Suggest Hardcover matches for a WeRead book"}
	command.Flags().StringVar(&bookID, "book-id", "", "WeRead book ID (required)")
	command.Flags().StringVar(&query, "query", "", "original-language or alternate title search")
	command.Flags().StringVar(&configPath, "config", "config.toml", "settings-only TOML path (missing file uses defaults; never credentials)")
	markRequiredFlags(command, "book-id")
	return app.command(command, func(cmd *cobra.Command, _ []string) error {
		if !companion.ValidBookID(bookID) {
			return fmt.Errorf("valid --book-id required")
		}
		cfg, err := config.Load(configPath)
		if err != nil {
			return err
		}
		loader, err := catalog.NewLoader(cfg.Matching.Catalogs, catalog.LoaderOptions{})
		if err != nil {
			return err
		}
		reader, err := app.providers.source()
		if err != nil {
			return err
		}
		hardcover, err := app.providers.target()
		if err != nil {
			return err
		}
		report, err := companion.MatchWithOptions(cmd.Context(), reader, hardcover, bookID, query, companion.MatchOptions{CatalogLoader: loader})
		if err != nil {
			return err
		}
		if err := encodeJSON(cmd, report); err != nil {
			return err
		}
		if report.CatalogsComplete != nil && !*report.CatalogsComplete {
			return fmt.Errorf("matching catalogs incomplete; see JSON issue codes")
		}
		return nil
	})
}

func (app *cliApp) pendingCommand() *cobra.Command {
	var account, targetAccount, dbPath, after string
	var days int
	var includePrivate, includeImported bool
	command := &cobra.Command{Use: "pending", Short: "List recently opened eligible WeRead books"}
	flags := command.Flags()
	flags.StringVar(&account, "account", "", "source account namespace (required; not an authenticated ID)")
	flags.StringVar(&targetAccount, "target-account", "", "target account namespace (required; not an authenticated ID)")
	flags.IntVar(&days, "days", 90, "recently opened window in days (1–36500; browsing counts)")
	flags.BoolVar(&includePrivate, "include-private", false, "explicitly include privacy-marked books (unknown privacy remains excluded)")
	flags.BoolVar(&includeImported, "include-imported", false, "include imported books for inspection only; match/link remain unsupported")
	flags.StringVar(&after, "after", "", "continue a pending worklist using its next_cursor")
	flags.StringVar(&dbPath, "db", "", "private SQLite path (default: XDG_STATE_HOME/drifter/mappings.sqlite)")
	markRequiredFlags(command, "account", "target-account")
	return app.command(command, func(cmd *cobra.Command, _ []string) error {
		if !companion.ValidLabel(account) || !companion.ValidLabel(targetAccount) {
			return fmt.Errorf("valid --account and --target-account required")
		}
		if days < 1 || days > 36500 {
			return fmt.Errorf("--days must be between 1 and 36500")
		}
		options := companion.PendingOptions{SourceAccount: account, TargetAccount: targetAccount, Days: days,
			IncludePrivate: includePrivate, IncludeImported: includeImported, After: after, Now: time.Now()}
		if err := companion.ValidatePendingCursor(options); err != nil {
			return err
		}
		if dbPath == "" {
			var err error
			dbPath, err = defaultDB()
			if err != nil {
				return err
			}
		}
		reader, err := app.providers.source()
		if err != nil {
			return err
		}
		report, err := companion.PendingWorklist(cmd.Context(), reader, dbPath, companion.PendingOptions{
			SourceAccount: account, TargetAccount: targetAccount, Days: days,
			IncludePrivate: includePrivate, IncludeImported: includeImported, After: after, Now: time.Now(),
		})
		if err != nil {
			return err
		}
		if err := encodeJSON(cmd, report); err != nil {
			return err
		}
		if !report.Complete {
			return fmt.Errorf("pending worklist incomplete; see JSON issue codes")
		}
		return nil
	})
}

func decisionPath(dbPath string) (string, error) {
	if dbPath != "" {
		return dbPath, nil
	}
	return defaultDB()
}

func (app *cliApp) decisionKeyFlags(command *cobra.Command, bookID, account, targetAccount, dbPath *string, includeBook bool) {
	flags := command.Flags()
	if includeBook {
		flags.StringVar(bookID, "book-id", "", "WeRead book ID (required)")
		markRequiredFlags(command, "book-id")
	}
	flags.StringVar(account, "account", "", "source account namespace (required; not an authenticated ID)")
	flags.StringVar(targetAccount, "target-account", "", "target account namespace (required; not an authenticated ID)")
	flags.StringVar(dbPath, "db", "", "private SQLite path (default: XDG_STATE_HOME/drifter/mappings.sqlite)")
	markRequiredFlags(command, "account", "target-account")
}

func (app *cliApp) decideCommand() *cobra.Command {
	var bookID, account, targetAccount, dbPath, kind string
	command := &cobra.Command{Use: "decide", Short: "Record a local pending-review decision"}
	app.decisionKeyFlags(command, &bookID, &account, &targetAccount, &dbPath, true)
	command.Flags().StringVar(&kind, "decision", "", "skip, exclude, or not-in-catalog (required)")
	markRequiredFlags(command, "decision")
	return app.command(command, func(cmd *cobra.Command, _ []string) error {
		path, err := decisionPath(dbPath)
		if err != nil {
			return err
		}
		if !companion.ValidBookID(bookID) || !companion.ValidLabel(account) || !companion.ValidLabel(targetAccount) {
			return fmt.Errorf("valid book ID and account labels required")
		}
		if kind != "skip" && kind != "exclude" && kind != "not-in-catalog" {
			return fmt.Errorf("--decision must be skip, exclude, or not-in-catalog")
		}
		if err := companion.CheckDatabaseWriteAvailable(path); err != nil {
			return err
		}
		store, err := companion.OpenStore(path)
		if err != nil {
			return err
		}
		defer store.Close()
		if err := store.SaveDecision(cmd.Context(), companion.Decision{Provider: "weread", SourceAccount: account, BookID: bookID, TargetAccount: targetAccount, Kind: kind}); err != nil {
			return err
		}
		return encodeJSON(cmd, map[string]any{"saved": true, "book_id": bookID, "decision": kind})
	})
}

func (app *cliApp) decisionsCommand() *cobra.Command {
	var bookID, account, targetAccount, dbPath string
	command := &cobra.Command{Use: "decisions", Short: "List local decisions for an account pair"}
	app.decisionKeyFlags(command, &bookID, &account, &targetAccount, &dbPath, false)
	return app.command(command, func(cmd *cobra.Command, _ []string) error {
		path, err := decisionPath(dbPath)
		if err != nil {
			return err
		}
		if !companion.ValidLabel(account) || !companion.ValidLabel(targetAccount) {
			return fmt.Errorf("valid account labels required")
		}
		store, err := companion.OpenStoreForRead(path)
		if err != nil {
			return err
		}
		defer store.Close()
		rows, err := store.ListDecisions(cmd.Context(), "weread", account, targetAccount)
		if err != nil {
			return err
		}
		return encodeJSON(cmd, rows)
	})
}

func (app *cliApp) resetDecisionCommand() *cobra.Command {
	var bookID, account, targetAccount, dbPath string
	command := &cobra.Command{Use: "reset-decision", Short: "Reset one local review decision"}
	app.decisionKeyFlags(command, &bookID, &account, &targetAccount, &dbPath, true)
	return app.command(command, func(cmd *cobra.Command, _ []string) error {
		path, err := decisionPath(dbPath)
		if err != nil {
			return err
		}
		if !companion.ValidBookID(bookID) || !companion.ValidLabel(account) || !companion.ValidLabel(targetAccount) {
			return fmt.Errorf("valid book ID and account labels required")
		}
		if err := companion.CheckDatabaseWriteAvailable(path); err != nil {
			return err
		}
		store, err := companion.OpenStore(path)
		if err != nil {
			return err
		}
		defer store.Close()
		if err := store.ResetDecision(cmd.Context(), "weread", account, bookID, targetAccount); err != nil {
			return err
		}
		return encodeJSON(cmd, map[string]any{"reset": true, "book_id": bookID})
	})
}

func (app *cliApp) linkCommand() *cobra.Command {
	var bookID, account, targetAccount, dbPath string
	var workID, editionID int64
	var replace bool
	command := &cobra.Command{Use: "link", Short: "Confirm a local WeRead-to-Hardcover mapping"}
	flags := command.Flags()
	flags.StringVar(&bookID, "book-id", "", "WeRead book ID (required)")
	flags.Int64Var(&workID, "work-id", 0, "explicitly selected Hardcover work ID (required)")
	flags.Int64Var(&editionID, "edition-id", 0, "optional confirmed edition; omitted means work-only")
	flags.StringVar(&account, "account", "", "source account namespace (required; not an authenticated ID)")
	flags.StringVar(&targetAccount, "target-account", "", "target account namespace (required; not an authenticated ID)")
	flags.BoolVar(&replace, "replace", false, "explicitly replace an existing local mapping")
	flags.StringVar(&dbPath, "db", "", "private SQLite path (default: XDG_STATE_HOME/drifter/mappings.sqlite)")
	markRequiredFlags(command, "book-id", "work-id", "account", "target-account")
	return app.command(command, func(cmd *cobra.Command, _ []string) error {
		if !companion.ValidBookID(bookID) {
			return fmt.Errorf("valid --book-id required")
		}
		if !companion.ValidLabel(account) || !companion.ValidLabel(targetAccount) {
			return fmt.Errorf("valid --account and --target-account required")
		}
		if workID <= 0 || editionID < 0 {
			return fmt.Errorf("positive --work-id and nonnegative --edition-id required")
		}
		if dbPath == "" {
			var err error
			dbPath, err = defaultDB()
			if err != nil {
				return err
			}
		}
		if err := companion.CheckDatabaseWriteAvailable(dbPath); err != nil {
			return err
		}
		reader, err := app.providers.source()
		if err != nil {
			return err
		}
		hardcover, err := app.providers.target()
		if err != nil {
			return err
		}
		mapping := companion.Mapping{Provider: "weread", SourceAccount: account,
			BookID: bookID, TargetAccount: targetAccount, WorkID: workID,
			EditionID: editionID}
		if err := companion.ConfirmLink(cmd.Context(), reader, hardcover, dbPath, mapping, replace); err != nil {
			return err
		}
		return encodeJSON(cmd, map[string]any{"confirmed_local_mapping": true,
			"book_id": bookID, "work_id": workID, "edition_id": editionID})
	})
}

func (app *cliApp) linksCommand() *cobra.Command {
	var dbPath string
	command := &cobra.Command{Use: "links", Short: "List saved local mappings"}
	command.Flags().StringVar(&dbPath, "db", "", "private SQLite path (default: XDG_STATE_HOME/drifter/mappings.sqlite)")
	return app.command(command, func(cmd *cobra.Command, _ []string) error {
		if dbPath == "" {
			var err error
			dbPath, err = defaultDB()
			if err != nil {
				return err
			}
		}
		store, err := companion.OpenStoreForRead(dbPath)
		if err != nil {
			return err
		}
		defer store.Close()
		rows, err := store.List(cmd.Context())
		if err != nil {
			return err
		}
		return encodeJSON(cmd, rows)
	})
}

func (app *cliApp) unlinkCommand() *cobra.Command {
	var bookID, account, targetAccount, dbPath string
	command := &cobra.Command{Use: "unlink", Short: "Remove one local mapping"}
	flags := command.Flags()
	flags.StringVar(&bookID, "book-id", "", "WeRead book ID (required)")
	flags.StringVar(&account, "account", "", "source account namespace (required; not an authenticated ID)")
	flags.StringVar(&targetAccount, "target-account", "", "target account namespace (required; not an authenticated ID)")
	flags.StringVar(&dbPath, "db", "", "private SQLite path (default: XDG_STATE_HOME/drifter/mappings.sqlite)")
	markRequiredFlags(command, "book-id", "account", "target-account")
	return app.command(command, func(cmd *cobra.Command, _ []string) error {
		if !companion.ValidBookID(bookID) {
			return fmt.Errorf("valid --book-id required")
		}
		if !companion.ValidLabel(account) || !companion.ValidLabel(targetAccount) {
			return fmt.Errorf("valid --account and --target-account required")
		}
		if dbPath == "" {
			var err error
			dbPath, err = defaultDB()
			if err != nil {
				return err
			}
		}
		store, err := companion.OpenStore(dbPath)
		if err != nil {
			return err
		}
		defer store.Close()
		if err := store.Unlink(cmd.Context(), "weread", account, bookID, targetAccount); err != nil {
			return err
		}
		return encodeJSON(cmd, map[string]any{"local_mapping_absent": true, "book_id": bookID})
	})
}

func (app *cliApp) serveProbesCommand() *cobra.Command {
	var configPath, dbPath string
	var requireProviders bool
	command := &cobra.Command{Use: "serve-probes", Short: "Serve standalone local health probes"}
	flags := command.Flags()
	flags.StringVar(&configPath, "config", "", "settings-only TOML path (missing file uses defaults; never credentials)")
	flags.StringVar(&dbPath, "db", "", "private SQLite path (default: XDG_STATE_HOME/drifter/mappings.sqlite)")
	flags.BoolVar(&requireProviders, "require-providers", false, "readiness reports unready when provider credentials are absent (no network calls)")
	return app.command(command, func(cmd *cobra.Command, _ []string) error {
		if dbPath == "" {
			var err error
			dbPath, err = defaultDB()
			if err != nil {
				return err
			}
		}
		return serveProbes(cmd.Context(), configPath, dbPath, requireProviders, cmd.OutOrStdout())
	})
}
