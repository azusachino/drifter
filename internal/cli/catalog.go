package cli

import (
	"fmt"

	"github.com/azusachino/drifter/internal/catalog"
	"github.com/azusachino/drifter/internal/companion"
	"github.com/spf13/cobra"
)

func (app *cliApp) exportCommand() *cobra.Command {
	var account, targetAccount, catalogID, publisher string
	var provenanceSource, provenanceReference, dbPath string
	command := &cobra.Command{Use: "export", Short: "Export selected confirmed mappings as a public catalog"}
	flags := command.Flags()
	flags.StringVar(&account, "account", "", "selected source account label (required; not exported)")
	flags.StringVar(&targetAccount, "target-account", "", "selected target account label (required; not exported)")
	flags.StringVar(&catalogID, "catalog-id", "", "explicit output catalog ID (required)")
	flags.StringVar(&publisher, "publisher", "", "explicit output publisher (required)")
	flags.StringVar(&provenanceSource, "provenance-source", "", "explicit public provenance source (required)")
	flags.StringVar(&provenanceReference, "provenance-reference", "", "explicit public provenance reference (required)")
	flags.StringVar(&dbPath, "db", "", "private SQLite path (default: XDG_STATE_HOME/drifter/mappings.sqlite)")
	markRequiredFlags(command, "account", "target-account", "catalog-id", "publisher", "provenance-source", "provenance-reference")
	return app.command(command, func(cmd *cobra.Command, _ []string) error {
		if !companion.ValidLabel(account) || !companion.ValidLabel(targetAccount) || provenanceSource == "" || provenanceReference == "" {
			return fmt.Errorf("valid selected accounts and explicit public provenance flags are required")
		}
		if dbPath == "" {
			var err error
			dbPath, err = defaultDB()
			if err != nil {
				return err
			}
		}
		encoded, err := companion.ExportCatalog(cmd.Context(), dbPath, companion.CatalogExportOptions{
			SourceAccount: account, TargetAccount: targetAccount,
			CatalogID: catalogID, Publisher: publisher,
			ProvenanceSource: provenanceSource, ProvenanceReference: provenanceReference,
		})
		if err != nil {
			return err
		}
		_, err = cmd.OutOrStdout().Write(append(encoded, '\n'))
		return err
	})
}

func (app *cliApp) mergeCommand() *cobra.Command {
	var catalogID, publisher string
	var inputs []string
	command := &cobra.Command{Use: "merge", Short: "Merge explicitly named local catalogs"}
	flags := command.Flags()
	flags.StringVar(&catalogID, "catalog-id", "", "explicit output catalog ID (required)")
	flags.StringVar(&publisher, "publisher", "", "explicit output publisher (required)")
	flags.StringArrayVar(&inputs, "input", nil, "explicit local JSON catalog path; repeat at least twice (required)")
	markRequiredFlags(command, "catalog-id", "publisher", "input")
	return app.command(command, func(cmd *cobra.Command, _ []string) error {
		if len(inputs) < 2 {
			return fmt.Errorf("catalog merge requires at least two --input files")
		}
		encoded, err := catalog.MergeLocal(cmd.Context(), inputs, catalogID, publisher)
		if err != nil {
			return err
		}
		_, err = cmd.OutOrStdout().Write(append(encoded, '\n'))
		return err
	})
}
