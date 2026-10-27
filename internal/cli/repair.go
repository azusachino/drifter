package cli

import (
	"fmt"
	"time"

	"github.com/azusachino/drifter/internal/companion"
	"github.com/spf13/cobra"
)

func (app *cliApp) targetCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "target",
		Short: "Inspect and update target catalog status directly",
	}
	cmd.AddCommand(
		app.targetStatusCommand(),
		app.targetSetStatusCommand(),
	)
	return cmd
}

func (app *cliApp) targetStatusCommand() *cobra.Command {
	var workID int64
	cmd := &cobra.Command{
		Use:   "status",
		Short: "Inspect the live user book status on target catalog",
	}
	flags := cmd.Flags()
	flags.Int64Var(&workID, "work-id", 0, "target work ID (required)")
	markRequiredFlags(cmd, "work-id")

	return app.command(cmd, func(c *cobra.Command, _ []string) error {
		if workID <= 0 {
			return fmt.Errorf("positive --work-id required")
		}
		target, err := app.getPreviewTarget()
		if err != nil {
			return err
		}
		status, err := target.GetUserBookStatus(c.Context(), workID)
		if err != nil {
			return err
		}
		if status == nil {
			return encodeJSON(c, map[string]any{
				"work_id":  workID,
				"on_shelf": false,
				"status":   "none",
			})
		}
		res := map[string]any{
			"work_id":         status.BookID,
			"user_book_id":    status.ID,
			"status_id":       status.StatusID,
			"status":          companion.HardcoverStatusName(status.StatusID),
			"on_shelf":        true,
			"user_book_reads": status.UserBookReads,
		}
		if status.EditionID != nil {
			res["edition_id"] = *status.EditionID
		}
		if status.Rating != nil {
			res["rating"] = *status.Rating
		}
		if status.Review != "" {
			res["review"] = status.Review
		}
		return encodeJSON(c, res)
	})
}

func (app *cliApp) targetSetStatusCommand() *cobra.Command {
	var (
		workID        int64
		editionID     int64
		statusRaw     string
		dbPath        string
		sourceAccount string
		targetAccount string
		provider      string
	)
	cmd := &cobra.Command{
		Use:   "set-status",
		Short: "Directly update reading status on target catalog",
	}
	flags := cmd.Flags()
	flags.Int64Var(&workID, "work-id", 0, "target work ID (required)")
	flags.Int64Var(&editionID, "edition-id", 0, "target edition ID (optional; 0 means work-only)")
	flags.StringVar(&statusRaw, "status", "", "reading status (required: reading, read, want-to-read, paused, dnf, or 1-5)")
	flags.StringVar(&sourceAccount, "source-account", "", "source account namespace (default: default or inferred)")
	flags.StringVar(&sourceAccount, "account", "", "alias for --source-account")
	flags.StringVar(&targetAccount, "target-account", "", "target account namespace (default: default or inferred)")
	flags.StringVar(&provider, "provider", companion.DefaultSourceProvider, "source provider name (default: weread)")
	flags.StringVar(&dbPath, "db", "", "private SQLite path (default: XDG_STATE_HOME/drifter/mappings.sqlite)")
	markRequiredFlags(cmd, "work-id", "status")

	return app.command(cmd, func(c *cobra.Command, _ []string) error {
		if workID <= 0 {
			return fmt.Errorf("positive --work-id required")
		}
		statusID, err := companion.ParseHardcoverStatus(statusRaw)
		if err != nil {
			return err
		}
		target, err := app.getApplyTarget()
		if err != nil {
			return err
		}
		outcome, err := target.ApplyStatusMutation(c.Context(), companion.StatusMutationRequest{
			WorkID:    workID,
			EditionID: editionID,
			StatusID:  statusID,
		})
		if err != nil {
			return err
		}

		// If a local SQLite store exists, keep baseline consistent if mapped
		if dbPath == "" {
			dbPath, _ = defaultDB()
		}
		if dbPath != "" {
			if store, err := companion.OpenStore(dbPath); err == nil {
				sa, ta, _ := store.ResolveAccounts(c.Context(), sourceAccount, targetAccount)
				if baselines, err := store.ListStatusBaselines(c.Context()); err == nil {
					for _, b := range baselines {
						if b.WorkID == workID && (b.SourceAccount == sa && b.TargetAccount == ta) {
							b.TargetStatusID = statusID
							b.LastSyncedAt = time.Now().UTC()
							_ = store.SaveStatusBaseline(c.Context(), b)
						}
					}
				}
				store.Close()
			}
		}

		return encodeJSON(c, map[string]any{
			"work_id":            workID,
			"user_book_id":       outcome.UserBookID,
			"current_status_id":  outcome.CurrentStatus,
			"current_status":     companion.HardcoverStatusName(outcome.CurrentStatus),
			"previous_status_id": outcome.PreviousStatus,
			"skipped":            outcome.Skipped,
		})
	})
}

func (app *cliApp) baselinesCommand() *cobra.Command {
	var dbPath string
	cmd := &cobra.Command{
		Use:   "baselines",
		Short: "List recorded status baselines",
	}
	cmd.Flags().StringVar(&dbPath, "db", "", "private SQLite path (default: XDG_STATE_HOME/drifter/mappings.sqlite)")
	return app.command(cmd, func(c *cobra.Command, _ []string) error {
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
		rows, err := store.ListStatusBaselines(c.Context())
		if err != nil {
			return err
		}
		if rows == nil {
			rows = []companion.StatusBaseline{}
		}
		return encodeJSON(c, rows)
	})
}

func (app *cliApp) resetBaselineCommand() *cobra.Command {
	var (
		bookID        string
		sourceAccount string
		targetAccount string
		provider      string
		dbPath        string
	)
	cmd := &cobra.Command{
		Use:   "reset-baseline",
		Short: "Reset an accepted status baseline for a book to allow fresh re-evaluation",
	}
	flags := cmd.Flags()
	flags.StringVar(&bookID, "book-id", "", "source book ID (required)")
	flags.StringVar(&sourceAccount, "source-account", "", "source account namespace (default: default or inferred)")
	flags.StringVar(&sourceAccount, "account", "", "alias for --source-account")
	flags.StringVar(&targetAccount, "target-account", "", "target account namespace (default: default or inferred)")
	flags.StringVar(&provider, "source", companion.DefaultSourceProvider, "source provider name (default: weread)")
	flags.StringVar(&provider, "provider", companion.DefaultSourceProvider, "alias for --source")
	flags.StringVar(&dbPath, "db", "", "private SQLite path (default: XDG_STATE_HOME/drifter/mappings.sqlite)")
	markRequiredFlags(cmd, "book-id")

	return app.command(cmd, func(c *cobra.Command, _ []string) error {
		if !companion.ValidBookID(bookID) {
			return fmt.Errorf("valid --book-id required")
		}
		path, err := decisionPath(dbPath)
		if err != nil {
			return err
		}
		if err := companion.CheckDatabaseWriteAvailable(path); err != nil {
			return err
		}
		store, err := companion.OpenStore(path)
		if err != nil {
			return err
		}
		defer store.Close()

		sa, ta, err := store.ResolveAccounts(c.Context(), sourceAccount, targetAccount)
		if err != nil {
			return err
		}
		if provider == "" {
			provider = companion.DefaultSourceProvider
		}
		deleted, err := store.DeleteStatusBaseline(c.Context(), provider, sa, bookID, ta)
		if err != nil {
			return err
		}
		return encodeJSON(c, map[string]any{
			"reset":          deleted,
			"book_id":        bookID,
			"source_account": sa,
			"target_account": ta,
			"provider":       provider,
		})
	})
}

func (app *cliApp) holdsCommand() *cobra.Command {
	var (
		sourceAccount string
		targetAccount string
		dbPath        string
	)
	cmd := &cobra.Command{
		Use:   "holds",
		Short: "List active sync holds and conflicts",
	}
	flags := cmd.Flags()
	flags.StringVar(&sourceAccount, "source-account", "", "source account namespace (default: default or inferred)")
	flags.StringVar(&sourceAccount, "account", "", "alias for --source-account")
	flags.StringVar(&targetAccount, "target-account", "", "target account namespace (default: default or inferred)")
	flags.StringVar(&dbPath, "db", "", "private SQLite path (default: XDG_STATE_HOME/drifter/mappings.sqlite)")

	return app.command(cmd, func(c *cobra.Command, _ []string) error {
		path, err := decisionPath(dbPath)
		if err != nil {
			return err
		}
		store, err := companion.OpenStoreForRead(path)
		if err != nil {
			return err
		}
		defer store.Close()
		sa, ta, _ := store.ResolveAccounts(c.Context(), sourceAccount, targetAccount)
		rows, err := store.ListSyncHolds(c.Context(), sa, ta)
		if err != nil {
			return err
		}
		if rows == nil {
			rows = []companion.SyncHold{}
		}
		return encodeJSON(c, rows)
	})
}

func (app *cliApp) clearHoldCommand() *cobra.Command {
	var (
		bookID        string
		sourceAccount string
		targetAccount string
		provider      string
		dbPath        string
	)
	cmd := &cobra.Command{
		Use:   "clear-hold",
		Short: "Clear active sync holds for a book mapping",
	}
	flags := cmd.Flags()
	flags.StringVar(&bookID, "book-id", "", "source book ID (optional; empty clears all holds for account pair)")
	flags.StringVar(&sourceAccount, "source-account", "", "source account namespace (default: default or inferred)")
	flags.StringVar(&sourceAccount, "account", "", "alias for --source-account")
	flags.StringVar(&targetAccount, "target-account", "", "target account namespace (default: default or inferred)")
	flags.StringVar(&provider, "source", companion.DefaultSourceProvider, "source provider name (default: weread)")
	flags.StringVar(&provider, "provider", companion.DefaultSourceProvider, "alias for --source")
	flags.StringVar(&dbPath, "db", "", "private SQLite path (default: XDG_STATE_HOME/drifter/mappings.sqlite)")

	return app.command(cmd, func(c *cobra.Command, _ []string) error {
		path, err := decisionPath(dbPath)
		if err != nil {
			return err
		}
		if err := companion.CheckDatabaseWriteAvailable(path); err != nil {
			return err
		}
		store, err := companion.OpenStore(path)
		if err != nil {
			return err
		}
		defer store.Close()

		sa, ta, err := store.ResolveAccounts(c.Context(), sourceAccount, targetAccount)
		if err != nil {
			return err
		}
		if provider == "" {
			provider = companion.DefaultSourceProvider
		}
		cleared, err := store.ClearSyncHolds(c.Context(), provider, sa, ta, bookID)
		if err != nil {
			return err
		}
		return encodeJSON(c, map[string]any{
			"cleared_count":  cleared,
			"book_id":        bookID,
			"source_account": sa,
			"target_account": ta,
			"provider":       provider,
		})
	})
}
