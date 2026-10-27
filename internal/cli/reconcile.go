package cli

import (
	"fmt"
	"os"
	"time"

	"github.com/azusachino/drifter/internal/companion"
	"github.com/spf13/cobra"
)

func (app *cliApp) reconcileCommand() *cobra.Command {
	var (
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
		Use:   "reconcile",
		Short: "Reconcile reading statuses across all confirmed mappings for an account pair",
	}

	flags := command.Flags()
	flags.StringVar(&account, "source-account", "", "source account namespace (default: default or inferred)")
	flags.StringVar(&account, "account", "", "alias for --source-account")
	flags.StringVar(&targetAccount, "target-account", "", "target account namespace (default: default or inferred)")
	flags.StringVar(&provider, "source", companion.DefaultSourceProvider, "source provider name (default: weread)")
	flags.StringVar(&provider, "provider", companion.DefaultSourceProvider, "alias for --source")
	flags.StringVar(&dbPath, "db", "", "private SQLite path (default: XDG_STATE_HOME/drifter/mappings.sqlite)")
	flags.StringVar(&policyVersion, "policy-version", companion.DefaultPolicyVersion, "policy version for three-way evaluation")
	flags.StringVar(&sourceVID, "source-vid", os.Getenv("WEREAD_VID"), "declared source user VID for account fingerprint binding")
	flags.BoolVar(&allowPrivate, "allow-private", false, "allow synchronizing private reading status")
	flags.BoolVar(&dryRun, "dry-run", false, "preview reconciliation without mutating target")

	return app.command(command, func(cmd *cobra.Command, _ []string) error {
		path, err := decisionPath(dbPath)
		if err != nil {
			return err
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
		target, err := app.getApplyTarget()
		if err != nil {
			return err
		}

		summary, err := companion.ReconcileAccount(
			cmd.Context(),
			store,
			source,
			target,
			companion.ReconcileOptions{
				Provider:          provider,
				SourceAccount:     account,
				TargetAccount:     targetAccount,
				PolicyVersion:     policyVersion,
				AllowPrivateSync:  allowPrivate,
				DeclaredSourceVID: sourceVID,
				DryRun:            dryRun,
				Now:               time.Now().UTC(),
			},
		)
		if err != nil {
			if encodeErr := encodeJSON(cmd, summary); encodeErr != nil {
				return err
			}
			return err
		}

		return encodeJSON(cmd, summary)
	})
}
