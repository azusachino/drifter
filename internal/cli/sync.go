package cli

import (
	"fmt"
	"os"
	"time"

	"github.com/azusachino/drifter/internal/companion"
	"github.com/spf13/cobra"
)

func (app *cliApp) getApplyTarget() (companion.ApplyTarget, error) {
	if app.providers.applyTarget != nil {
		return app.providers.applyTarget()
	}
	if app.providers.target != nil {
		t, err := app.providers.target()
		if err != nil {
			return nil, err
		}
		if at, ok := t.(companion.ApplyTarget); ok {
			return at, nil
		}
		return nil, fmt.Errorf("target provider does not support status mutation")
	}
	return nil, fmt.Errorf("target provider not configured")
}

func (app *cliApp) syncCommand() *cobra.Command {
	var (
		bookID        string
		account       string
		targetAccount string
		provider      string
		dbPath        string
		policyVersion string
		sourceVID     string
		allowPrivate  bool
		dryRun        bool
	)

	command := &cobra.Command{
		Use:   "sync",
		Short: "Synchronize reading status for a confirmed book safely (persists intent, applies approved fields, writes baseline)",
	}

	flags := command.Flags()
	flags.StringVar(&bookID, "book-id", "", "source book ID (required)")
	flags.StringVar(&account, "source-account", "", "source account namespace (default: default or inferred)")
	flags.StringVar(&account, "account", "", "alias for --source-account")
	flags.StringVar(&targetAccount, "target-account", "", "target account namespace (default: default or inferred)")
	flags.StringVar(&provider, "source", companion.DefaultSourceProvider, "source provider name (default: weread)")
	flags.StringVar(&provider, "provider", companion.DefaultSourceProvider, "alias for --source")
	flags.StringVar(&dbPath, "db", "", "private SQLite path (default: XDG_STATE_HOME/drifter/mappings.sqlite)")
	flags.StringVar(&policyVersion, "policy-version", companion.DefaultPolicyVersion, "policy version for three-way evaluation")
	flags.StringVar(&sourceVID, "source-vid", os.Getenv("WEREAD_VID"), "declared source user VID for account fingerprint binding")
	flags.BoolVar(&allowPrivate, "allow-private", false, "allow synchronizing private reading status")
	flags.BoolVar(&dryRun, "dry-run", false, "evaluate and preview status sync proposal without mutating target")

	markRequiredFlags(command, "book-id")

	return app.command(command, func(cmd *cobra.Command, _ []string) error {
		path, err := decisionPath(dbPath)
		if err != nil {
			return err
		}
		if !companion.ValidBookID(bookID) {
			return fmt.Errorf("valid book ID required")
		}
		if provider == "" {
			provider = companion.DefaultSourceProvider
		}
		if !companion.ValidLabel(provider) {
			return fmt.Errorf("valid provider label required")
		}

		if err := companion.CheckDatabaseWriteAvailable(path); err != nil {
			return err
		}
		store, err := companion.OpenStore(path)
		if err != nil {
			return err
		}
		defer store.Close()

		account, targetAccount, err = store.ResolveAccounts(cmd.Context(), account, targetAccount)
		if err != nil {
			return err
		}

		source, err := app.getPreviewSource()
		if err != nil {
			return err
		}

		now := time.Now().UTC()

		if dryRun {
			target, err := app.getPreviewTarget()
			if err != nil {
				return err
			}
			proposal, err := companion.PreviewSync(
				cmd.Context(),
				store,
				source,
				target,
				provider,
				account,
				bookID,
				targetAccount,
				companion.PreviewSyncOptions{
					PolicyVersion:     policyVersion,
					AllowPrivateSync:  allowPrivate,
					DeclaredSourceVID: sourceVID,
					Now:               now,
				},
			)
			if err != nil {
				return err
			}
			return encodeJSON(cmd, proposal)
		}

		target, err := app.getApplyTarget()
		if err != nil {
			return err
		}

		result, err := companion.ApplySync(
			cmd.Context(),
			store,
			source,
			target,
			provider,
			account,
			bookID,
			targetAccount,
			companion.ApplySyncOptions{
				PolicyVersion:     policyVersion,
				AllowPrivateSync:  allowPrivate,
				DeclaredSourceVID: sourceVID,
				Now:               now,
			},
		)
		if err != nil {
			if encodeErr := encodeJSON(cmd, result); encodeErr != nil {
				return err
			}
			return err
		}

		return encodeJSON(cmd, result)
	})
}
