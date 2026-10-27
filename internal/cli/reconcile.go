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
	flags.StringVar(&account, "account", "", "source account namespace (required; not an authenticated ID)")
	flags.StringVar(&targetAccount, "target-account", "", "target account namespace (required; not an authenticated ID)")
	flags.StringVar(&provider, "provider", "weread", "source provider name (default: weread)")
	flags.StringVar(&dbPath, "db", "", "private SQLite path (default: XDG_STATE_HOME/drifter/mappings.sqlite)")
	flags.StringVar(&policyVersion, "policy-version", companion.DefaultPolicyVersion, "policy version for three-way evaluation")
	flags.StringVar(&sourceVID, "source-vid", os.Getenv("WEREAD_VID"), "declared source user VID for account fingerprint binding")
	flags.BoolVar(&allowPrivate, "allow-private", false, "allow synchronizing private reading status")
	flags.BoolVar(&dryRun, "dry-run", false, "preview reconciliation without mutating target")

	markRequiredFlags(command, "account", "target-account")

	return app.command(command, func(cmd *cobra.Command, _ []string) error {
		path, err := decisionPath(dbPath)
		if err != nil {
			return err
		}
		if !companion.ValidLabel(account) || !companion.ValidLabel(targetAccount) || !companion.ValidLabel(provider) {
			return fmt.Errorf("valid account labels required")
		}

		if err := companion.CheckDatabaseWriteAvailable(path); err != nil {
			return err
		}
		store, err := companion.OpenStore(path)
		if err != nil {
			return err
		}
		defer store.Close()

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
