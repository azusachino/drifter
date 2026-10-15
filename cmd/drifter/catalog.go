package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"

	"github.com/azusachino/drifter/internal/companion"
	"github.com/azusachino/drifter/internal/matchingcatalog"
)

func runCatalog(ctx context.Context, args []string, out, errOut io.Writer) error {
	if len(args) == 0 || args[0] == "help" || args[0] == "-h" || args[0] == "--help" {
		_, err := io.WriteString(out, "Usage: drifter catalog COMMAND [flags]\n\nCommands:\n  export   Export selected confirmed local mappings to a v1 catalog on stdout\n  merge    Merge explicitly named local v1 catalogs to stdout\n")
		return err
	}
	command := args[0]
	if command != "export" && command != "merge" {
		return fmt.Errorf("unknown catalog command; use drifter catalog -h")
	}
	fs := flag.NewFlagSet("catalog "+command, flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	fs.Usage = func() {
		fmt.Fprintf(errOut, "Usage: drifter catalog %s [flags]\n", command)
		fs.SetOutput(errOut)
		fs.PrintDefaults()
		fs.SetOutput(io.Discard)
	}
	var account, targetAccount, catalogID, publisher, provenanceSource, provenanceReference, dbPath string
	var inputs stringListFlag
	fs.StringVar(&catalogID, "catalog-id", "", "explicit output catalog ID (required)")
	fs.StringVar(&publisher, "publisher", "", "explicit output publisher (required)")
	if command == "export" {
		fs.StringVar(&account, "account", "", "selected source account label (required; not exported)")
		fs.StringVar(&targetAccount, "target-account", "", "selected target account label (required; not exported)")
		fs.StringVar(&provenanceSource, "provenance-source", "", "explicit public provenance source (required)")
		fs.StringVar(&provenanceReference, "provenance-reference", "", "explicit public provenance reference (required)")
		fs.StringVar(&dbPath, "db", "", "private SQLite path (default: XDG_STATE_HOME/drifter/mappings.sqlite)")
	}
	if command == "merge" {
		fs.Var(&inputs, "input", "explicit local JSON catalog path; repeat at least twice (required)")
	}
	if err := fs.Parse(args[1:]); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return fmt.Errorf("invalid catalog flags; use drifter catalog %s -h", command)
	}
	if fs.NArg() != 0 {
		return fmt.Errorf("unexpected positional arguments; use named catalog flags")
	}
	if catalogID == "" || publisher == "" {
		return fmt.Errorf("--catalog-id and --publisher are required")
	}
	var encoded []byte
	var err error
	switch command {
	case "export":
		if !companion.ValidLabel(account) || !companion.ValidLabel(targetAccount) || provenanceSource == "" || provenanceReference == "" {
			return fmt.Errorf("valid selected accounts and explicit public provenance flags are required")
		}
		if dbPath == "" {
			dbPath, err = defaultDB()
			if err != nil {
				return err
			}
		}
		encoded, err = companion.ExportCatalog(ctx, dbPath, companion.CatalogExportOptions{
			SourceAccount: account, TargetAccount: targetAccount,
			CatalogID: catalogID, Publisher: publisher,
			ProvenanceSource: provenanceSource, ProvenanceReference: provenanceReference,
		})
	case "merge":
		encoded, err = matchingcatalog.MergeLocal(ctx, inputs, catalogID, publisher)
	}
	if err != nil {
		return err
	}
	_, err = out.Write(append(encoded, '\n'))
	return err
}

type stringListFlag []string

func (values *stringListFlag) String() string {
	return "local catalog input paths"
}

func (values *stringListFlag) Set(value string) error {
	*values = append(*values, value)
	return nil
}
