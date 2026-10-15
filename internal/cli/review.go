package cli

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"

	"github.com/azusachino/drifter/internal/companion"
	"github.com/azusachino/drifter/internal/config"
	"github.com/spf13/cobra"
)

var reviewProviderTimeout = 60 * time.Second

func (app *cliApp) reviewCommand() *cobra.Command {
	var account, targetAccount, dbPath, configPath string
	var days int
	var includePrivate, includeImported bool
	cmd := &cobra.Command{Use: "review", Short: "Review pending books, save decisions, and confirm local links"}
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
				if strings.HasPrefix(book.ID, "CB_") {
					fmt.Fprint(c.OutOrStdout(), "Decision [skip/exclude/not-in-catalog/quit]: ")
				} else {
					fmt.Fprintln(c.OutOrStdout(), "Suggestions require an explicit match lookup; enter match or a review decision.")
				}
				for {
					fmt.Fprint(c.OutOrStdout(), "Decision [match/skip/exclude/not-in-catalog/quit]: ")
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
					if choice == "match" && !strings.HasPrefix(book.ID, "CB_") {
						lookupCtx, cancel := context.WithTimeout(c.Context(), reviewProviderTimeout)
						candidateSource, sourceErr := app.providers.source()
						var report companion.MatchReport
						if sourceErr == nil {
							targetClient, targetErr := app.providers.target()
							if targetErr != nil {
								sourceErr = targetErr
							} else {
								report, sourceErr = companion.Match(lookupCtx, candidateSource, targetClient, book.ID, "")
							}
						}
						cancel()
						if sourceErr != nil {
							fmt.Fprintln(c.ErrOrStderr(), "Book lookup failed; no decision saved.")
							break
						}
						candidates := report.Candidates
						if len(candidates) > 3 {
							candidates = candidates[:3]
						}
						for i, candidate := range candidates {
							author := ""
							if len(candidate.Contributions) > 0 {
								author = terminalText(candidate.Contributions[0].Author.Name)
							}
							fmt.Fprintf(c.OutOrStdout(), "%d) %s — %s [work %d]\n", i+1, terminalText(candidate.Title), author, candidate.ID)
						}
						shownEditions := 0
						for _, edition := range report.ExactEditions {
							if shownEditions >= 3 {
								break
							}
							fmt.Fprintf(c.OutOrStdout(), "Exact edition: %s [edition %d, work %d]\n", terminalText(edition.Title), edition.ID, edition.WorkID)
							shownEditions++
						}
						fmt.Fprint(c.OutOrStdout(), "Choose suggestion number, manual work ID, or none: ")
						selection, inputErr := readReviewLine(c.Context(), terminal)
						if inputErr != nil {
							if errors.Is(inputErr, io.EOF) {
								return nil
							}
							return inputErr
						}
						selection = strings.TrimSpace(selection)
						if selection == "none" || selection == "" {
							break
						}
						var chosen int64
						if n, parseErr := strconv.Atoi(selection); parseErr == nil && n > 0 && n <= len(candidates) {
							chosen = candidates[n-1].ID
						} else {
							chosen, _ = strconv.ParseInt(selection, 10, 64)
						}
						if chosen <= 0 {
							fmt.Fprintln(c.OutOrStdout(), "No valid target selected.")
							break
						}
						fmt.Fprint(c.OutOrStdout(), "Edition ID (blank for work only): ")
						editionText, inputErr := readReviewLine(c.Context(), terminal)
						if inputErr != nil {
							if errors.Is(inputErr, io.EOF) {
								return nil
							}
							return inputErr
						}
						editionID := int64(0)
						if strings.TrimSpace(editionText) != "" {
							editionID, _ = strconv.ParseInt(strings.TrimSpace(editionText), 10, 64)
							if editionID <= 0 {
								fmt.Fprintln(c.OutOrStdout(), "Invalid edition ID.")
								break
							}
						}
						fmt.Fprintf(c.OutOrStdout(), "Confirm work %d edition %d? [yes/no]: ", chosen, editionID)
						confirm, inputErr := readReviewLine(c.Context(), terminal)
						if inputErr != nil {
							if errors.Is(inputErr, io.EOF) {
								return nil
							}
							return inputErr
						}
						if strings.TrimSpace(confirm) != "yes" {
							break
						}
						var expected *companion.MappingTarget
						replaceAllowed := true
						mappings, listErr := store.List(c.Context())
						if listErr != nil {
							return listErr
						}
						for _, existing := range mappings {
							if existing.Provider == "weread" && existing.SourceAccount == account && existing.BookID == book.ID && existing.TargetAccount == targetAccount {
								fmt.Fprintf(c.OutOrStdout(), "Existing mapping: work %d edition %d. Replace? [yes/no]: ", existing.WorkID, existing.EditionID)
								answer, readErr := readReviewLine(c.Context(), terminal)
								if readErr != nil {
									if errors.Is(readErr, io.EOF) {
										return nil
									}
									return readErr
								}
								if strings.TrimSpace(answer) != "yes" {
									replaceAllowed = false
									break
								}
								expected = &companion.MappingTarget{WorkID: existing.WorkID, EditionID: existing.EditionID}
								break
							}
						}
						if !replaceAllowed {
							break
						}
						finalCtx, finalCancel := context.WithTimeout(c.Context(), reviewProviderTimeout)
						finalSource, finalErr := app.providers.source()
						if finalErr == nil {
							finalTarget, targetErr := app.providers.target()
							if targetErr != nil {
								finalErr = targetErr
							} else {
								mapping := companion.Mapping{Provider: "weread", SourceAccount: account, BookID: book.ID, TargetAccount: targetAccount, WorkID: chosen, EditionID: editionID}
								if expected != nil {
									finalErr = companion.ConfirmLinkExpectedOwned(finalCtx, finalSource, finalTarget, dbPath, mapping, *expected, owner)
								} else if replaceAllowed {
									finalErr = companion.ConfirmLinkOwned(finalCtx, finalSource, finalTarget, dbPath, mapping, false, owner)
								}
							}
						}
						finalCancel()
						if finalErr != nil {
							fmt.Fprintln(c.ErrOrStderr(), "Link failed; no decision saved.")
						}
						break
					}
					if choice != "skip" && choice != "exclude" && choice != "not-in-catalog" {
						fmt.Fprintln(c.OutOrStdout(), "Choose match, skip, exclude, not-in-catalog, or quit.")
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
