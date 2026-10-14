package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/azusachino/drifter/internal/companion"
	"github.com/azusachino/drifter/internal/config"
	"github.com/azusachino/drifter/internal/matchingcatalog"
)

// Aliases keep credential factories local while core owns provider contracts.
type source = companion.Source
type target = companion.Target

type clients struct {
	source func() (source, error)
	target func() (target, error)
}

const usage = `Usage: drifter COMMAND [flags]

Read-only provider operations:
  search   --title TITLE
  inspect  --book-id ID [--progress]
  match    --book-id ID [--query ORIGINAL_TITLE] [--config PATH]
  pending  --account LABEL --target-account LABEL [--days 90] [--db PATH]
           [--include-private] [--include-imported]

Explicit local mappings (never remote status writes):
  link     --book-id ID --work-id ID --account LABEL --target-account LABEL
           [--edition-id ID] [--replace] [--db PATH]
  links    [--db PATH]
  unlink   --book-id ID --account LABEL --target-account LABEL [--db PATH]
  serve    [--config PATH] [--db PATH]

Use COMMAND -h for flags. Credentials come from WEREAD_API_KEY and
HARDCOVER_API_KEY; local links/unlink need neither. serve-probes remains a
standalone health endpoint. serve starts the local HTTP server and its mapping
interface; synchronization and provider-status writes are not implemented.
`

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
	if len(args) == 0 || args[0] == "help" || args[0] == "-h" || args[0] == "--help" {
		_, err := io.WriteString(out, usage)
		return err
	}
	command := args[0]
	fs := flag.NewFlagSet(command, flag.ContinueOnError)
	// Parse errors must not echo arbitrary values, including misrouted secrets.
	fs.SetOutput(io.Discard)
	fs.Usage = func() {
		fmt.Fprintf(errOut, "Usage: drifter %s [flags]\n", command)
		fs.SetOutput(errOut)
		fs.PrintDefaults()
		fs.SetOutput(io.Discard)
	}
	var title, bookID, query, account, targetAccount, dbPath, configPath string
	var progress, replace, requireProviders, includePrivate, includeImported bool
	var days int
	var workID, editionID int64
	switch command {
	case "search":
		fs.StringVar(&title, "title", "", "WeRead search title (required)")
	case "inspect", "match", "link", "unlink":
		fs.StringVar(&bookID, "book-id", "", "WeRead book ID (required)")
	case "links", "serve-probes", "serve", "pending":
	default:
		return fmt.Errorf("unknown command; use drifter help")
	}
	if command == "inspect" {
		fs.BoolVar(&progress, "progress", false, "explicitly fetch personal reading progress")
	}
	if command == "match" {
		fs.StringVar(&query, "query", "", "original-language or alternate title search")
	}
	if command == "pending" {
		fs.IntVar(&days, "days", 90, "recently opened window in days (1–36500; browsing counts)")
		fs.BoolVar(&includePrivate, "include-private", false, "explicitly include privacy-marked books (unknown privacy remains excluded)")
		fs.BoolVar(&includeImported, "include-imported", false, "include imported books for inspection only; match/link remain unsupported")
	}
	if command == "link" || command == "unlink" || command == "pending" {
		fs.StringVar(&account, "account", "", "source account namespace (required; not an authenticated ID)")
		fs.StringVar(&targetAccount, "target-account", "", "target account namespace (required; not an authenticated ID)")
	}
	if command == "link" {
		fs.Int64Var(&workID, "work-id", 0, "explicitly selected Hardcover work ID (required)")
		fs.Int64Var(&editionID, "edition-id", 0, "optional confirmed edition; omitted means work-only")
		fs.BoolVar(&replace, "replace", false, "explicitly replace an existing local mapping")
	}
	if command == "link" || command == "links" || command == "unlink" || command == "serve-probes" || command == "serve" || command == "pending" {
		fs.StringVar(&dbPath, "db", "", "private SQLite path (default: XDG_STATE_HOME/drifter/mappings.sqlite)")
	}
	if command == "serve-probes" || command == "serve" || command == "match" {
		defaultConfig := ""
		if command == "serve" || command == "match" {
			defaultConfig = "config.toml"
		}
		fs.StringVar(&configPath, "config", defaultConfig, "settings-only TOML path (missing file uses defaults; never credentials)")
	}
	if command == "serve-probes" {
		fs.BoolVar(&requireProviders, "require-providers", false, "readiness reports unready when provider credentials are absent (no network calls)")
	}
	if err := fs.Parse(args[1:]); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return fmt.Errorf("invalid flags; use drifter %s -h", command)
	}
	if fs.NArg() != 0 {
		return fmt.Errorf("unexpected positional arguments; use named flags")
	}
	if command == "search" && title == "" {
		return fmt.Errorf("--title required")
	}
	if command != "search" && command != "links" && command != "serve-probes" && command != "serve" && command != "pending" && !companion.ValidBookID(bookID) {
		return fmt.Errorf("valid --book-id required")
	}
	if command == "pending" && (days < 1 || days > 36500) {
		return fmt.Errorf("--days must be between 1 and 36500")
	}
	if command == "link" || command == "unlink" || command == "pending" {
		if !companion.ValidLabel(account) || !companion.ValidLabel(targetAccount) {
			return fmt.Errorf("valid --account and --target-account required")
		}
	}
	if command == "link" && (workID <= 0 || editionID < 0) {
		return fmt.Errorf("positive --work-id and nonnegative --edition-id required")
	}
	if command == "link" || command == "links" || command == "unlink" || command == "serve-probes" || command == "serve" || command == "pending" {
		if dbPath == "" {
			var err error
			dbPath, err = defaultDB()
			if err != nil {
				return err
			}
		}
	}
	encoder := json.NewEncoder(out)
	encoder.SetIndent("", "  ")
	if command == "serve-probes" {
		return serveProbes(ctx, configPath, dbPath, requireProviders, out)
	}
	if command == "serve" {
		return serveHTTPServer(ctx, configPath, dbPath, out, providers)
	}
	if command == "link" {
		if err := companion.CheckDatabaseWriteAvailable(dbPath); err != nil {
			return err
		}
	}
	if command == "links" || command == "unlink" {
		var store *companion.Store
		var err error
		if command == "links" {
			store, err = companion.OpenStoreForRead(dbPath)
		} else {
			store, err = companion.OpenStore(dbPath)
		}
		if err != nil {
			return err
		}
		defer store.Close()
		if command == "links" {
			rows, err := store.List(ctx)
			if err != nil {
				return err
			}
			return encoder.Encode(rows)
		}
		if err := store.Unlink(ctx, "weread", account, bookID, targetAccount); err != nil {
			return err
		}
		return encoder.Encode(map[string]any{"local_mapping_absent": true, "book_id": bookID})
	}
	var matchOptions companion.MatchOptions
	if command == "match" {
		cfg, err := config.Load(configPath)
		if err != nil {
			return err
		}
		loader, err := matchingcatalog.NewLoader(cfg.Matching.Catalogs, matchingcatalog.LoaderOptions{})
		if err != nil {
			return err
		}
		matchOptions.CatalogLoader = loader
	}
	reader, err := providers.source()
	if err != nil {
		return err
	}
	if command == "pending" {
		report, err := companion.PendingWorklist(ctx, reader, dbPath, companion.PendingOptions{
			SourceAccount: account, TargetAccount: targetAccount, Days: days,
			IncludePrivate: includePrivate, IncludeImported: includeImported, Now: time.Now(),
		})
		if err != nil {
			return err
		}
		if err := encoder.Encode(report); err != nil {
			return err
		}
		if !report.Complete {
			return fmt.Errorf("pending worklist incomplete; see JSON issue codes")
		}
		return nil
	}
	if command == "search" {
		result, err := reader.Search(ctx, title)
		if err != nil {
			return err
		}
		return encoder.Encode(result)
	}
	var catalog target
	if command == "match" || command == "link" {
		catalog, err = providers.target()
		if err != nil {
			return err
		}
	}
	switch command {
	case "inspect":
		result, err := companion.Inspect(ctx, reader, bookID, progress)
		if err != nil {
			return err
		}
		return encoder.Encode(result)
	case "match":
		report, err := companion.MatchWithOptions(ctx, reader, catalog, bookID, query, matchOptions)
		if err != nil {
			return err
		}
		if err := encoder.Encode(report); err != nil {
			return err
		}
		if report.CatalogsComplete != nil && !*report.CatalogsComplete {
			return fmt.Errorf("matching catalogs incomplete; see JSON issue codes")
		}
		return nil
	case "link":
		mapping := companion.Mapping{Provider: "weread", SourceAccount: account,
			BookID: bookID, TargetAccount: targetAccount, WorkID: workID,
			EditionID: editionID}
		if err := companion.ConfirmLink(ctx, reader, catalog, dbPath, mapping, replace); err != nil {
			return err
		}
		return encoder.Encode(map[string]any{"confirmed_local_mapping": true,
			"book_id": bookID, "work_id": workID, "edition_id": editionID})
	}
	return fmt.Errorf("unsupported command")
}
