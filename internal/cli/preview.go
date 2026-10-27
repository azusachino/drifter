package cli

import (
	"fmt"
	"time"

	"github.com/azusachino/drifter/internal/companion"
	"github.com/spf13/cobra"
)

func (app *cliApp) getPreviewSource() (companion.PreviewSource, error) {
	if app.providers.previewSource != nil {
		return app.providers.previewSource()
	}
	if app.providers.source != nil {
		s, err := app.providers.source()
		if err != nil {
			return nil, err
		}
		if ps, ok := s.(companion.PreviewSource); ok {
			return ps, nil
		}
		return nil, fmt.Errorf("source provider does not support preview")
	}
	return nil, fmt.Errorf("source provider not configured")
}

func (app *cliApp) getPreviewTarget() (companion.PreviewTarget, error) {
	if app.providers.previewTarget != nil {
		return app.providers.previewTarget()
	}
	if app.providers.target != nil {
		t, err := app.providers.target()
		if err != nil {
			return nil, err
		}
		if pt, ok := t.(companion.PreviewTarget); ok {
			return pt, nil
		}
		return nil, fmt.Errorf("target provider does not support preview")
	}
	return nil, fmt.Errorf("target provider not configured")
}

func (app *cliApp) previewCommand() *cobra.Command {
	var (
		bookID        string
		account       string
		targetAccount string
		provider      string
		dbPath        string
		policyVersion string
		allowPrivate  bool
	)

	command := &cobra.Command{
		Use:   "preview",
		Short: "Preview reading status synchronization proposal for a confirmed book (read-only)",
	}

	flags := command.Flags()
	flags.StringVar(&bookID, "book-id", "", "source book ID (required)")
	flags.StringVar(&account, "account", "", "source account namespace (required; not an authenticated ID)")
	flags.StringVar(&targetAccount, "target-account", "", "target account namespace (required; not an authenticated ID)")
	flags.StringVar(&provider, "provider", "weread", "source provider name (default: weread)")
	flags.StringVar(&dbPath, "db", "", "private SQLite path (default: XDG_STATE_HOME/drifter/mappings.sqlite)")
	flags.StringVar(&policyVersion, "policy-version", companion.DefaultPolicyVersion, "policy version for three-way evaluation")
	flags.BoolVar(&allowPrivate, "allow-private", false, "allow previewing private book synchronization")

	markRequiredFlags(command, "book-id", "account", "target-account")

	return app.command(command, func(cmd *cobra.Command, _ []string) error {
		path, err := decisionPath(dbPath)
		if err != nil {
			return err
		}
		if !companion.ValidBookID(bookID) || !companion.ValidLabel(account) || !companion.ValidLabel(targetAccount) || !companion.ValidLabel(provider) {
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

		source, err := app.getPreviewSource()
		if err != nil {
			return err
		}
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
				PolicyVersion:    policyVersion,
				AllowPrivateSync: allowPrivate,
				Now:              time.Now().UTC(),
			},
		)
		if err != nil {
			return err
		}

		return encodeJSON(cmd, proposal)
	})
}
