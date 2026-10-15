package cli

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/azusachino/drifter/internal/companion"
	"github.com/azusachino/drifter/internal/config"
	"github.com/spf13/cobra"
)

func (app *cliApp) reviewCommand() *cobra.Command {
	var account, targetAccount, dbPath, configPath string
	var days int
	var includePrivate, includeImported bool
	cmd := &cobra.Command{Use: "review", Short: "Review pending books and save local decisions"}
	f := cmd.Flags()
	f.StringVar(&account, "account", "", "source account namespace (required; not an authenticated ID)")
	f.StringVar(&targetAccount, "target-account", "", "target account namespace (required; not an authenticated ID)")
	f.IntVar(&days, "days", 90, "recently opened window in days (1–36500)")
	f.StringVar(&dbPath, "db", "", "private SQLite path (config database.path overrides this; otherwise default)")
	f.StringVar(&configPath, "config", "config.toml", "settings-only TOML path (missing file uses defaults)")
	f.BoolVar(&includePrivate, "include-private", false, "explicitly include privacy-marked books")
	f.BoolVar(&includeImported, "include-imported", false, "inspect imported books (linking unsupported)")
	markRequiredFlags(cmd, "account", "target-account")
	return app.command(cmd, func(c *cobra.Command, _ []string) error {
		if !companion.ValidLabel(account) || !companion.ValidLabel(targetAccount) || days < 1 || days > 36500 {
			return fmt.Errorf("valid account labels and --days between 1 and 36500 required")
		}
		cfg, err := config.Load(configPath)
		if err != nil {
			return err
		}
		if strings.TrimSpace(cfg.Database.Path) != "" {
			dbPath = cfg.Database.Path
		}
		if dbPath == "" {
			dbPath, err = defaultDB()
			if err != nil {
				return err
			}
		}
		// Ownership is acquired before provider construction or terminal input.
		owner, err := companion.AcquireDatabaseOwnership(dbPath)
		if err != nil {
			return fmt.Errorf("cannot start review: database is in use; stop the server or other review first")
		}
		defer owner.Close()
		source, err := app.providers.source()
		if err != nil {
			return err
		}
		now := time.Now()
		store, err := companion.OpenOwnedStore(dbPath, owner)
		if err != nil {
			return err
		}
		defer store.Close()
		terminal := bufio.NewReader(app.input)
		after := ""
		for {
			report, err := companion.PendingWorklistOwned(c.Context(), source, dbPath, companion.PendingOptions{SourceAccount: account, TargetAccount: targetAccount, Days: days, IncludePrivate: includePrivate, IncludeImported: includeImported, After: after, Now: now}, owner)
			if err != nil {
				return err
			}
			if !report.Complete {
				return fmt.Errorf("pending worklist incomplete; see issue codes")
			}
			for _, book := range report.Books {
				if err := c.Context().Err(); err != nil {
					return err
				}
				fmt.Fprintf(c.OutOrStdout(), "Book %s: %s — %s\n", book.ID, terminalText(book.Title), terminalText(book.Author))
				if strings.HasPrefix(book.ID, "CB_") {
					fmt.Fprintln(c.OutOrStdout(), "Imported source books are inspection-only; no link is available.")
				}
				for {
					fmt.Fprint(c.OutOrStdout(), "Decision [skip/exclude/not-in-catalog/quit]: ")
					line, err := readReviewLine(c.Context(), terminal)
					if err != nil {
						if errors.Is(err, io.EOF) {
							return nil
						}
						return err
					}
					choice := strings.TrimSpace(line)
					if choice == "" {
						continue
					}
					if choice == "quit" {
						return nil
					}
					if choice != "skip" && choice != "exclude" && choice != "not-in-catalog" {
						fmt.Fprintln(c.OutOrStdout(), "Choose skip, exclude, not-in-catalog, or quit.")
						continue
					}
					err = store.SaveDecision(c.Context(), companion.Decision{Provider: "weread", SourceAccount: account, BookID: book.ID, TargetAccount: targetAccount, Kind: choice})
					if err != nil {
						return err
					}
					break
				}
			}
			if report.NextCursor == "" {
				return nil
			}
			after = report.NextCursor
		}
	})
}

func terminalText(s string) string {
	var b strings.Builder
	for _, r := range s {
		if r >= ' ' && r != 0x7f && r != 0x1b {
			b.WriteRune(r)
		}
	}
	return b.String()
}

// readReviewLine isolates blocking terminal reads so cancellation can win without replacing process stdin.
func readReviewLine(ctx context.Context, input *bufio.Reader) (string, error) {
	result := make(chan struct {
		line string
		err  error
	}, 1)
	go func() {
		line, err := input.ReadString('\n')
		result <- struct {
			line string
			err  error
		}{strings.TrimRight(line, "\r\n"), err}
	}()
	select {
	case <-ctx.Done():
		return "", ctx.Err()
	case r := <-result:
		return r.line, r.err
	}
}
