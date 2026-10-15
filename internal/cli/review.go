package cli

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/azusachino/drifter/internal/catalog"
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
	return app.command(cmd, func(c *cobra.Command, _ []string) (reviewErr error) {
		if !companion.ValidLabel(account) || !companion.ValidLabel(targetAccount) || days < 1 || days > 36500 {
			return fmt.Errorf("valid account labels and --days between 1 and 36500 required")
		}
		cfg, err := config.Load(configPath)
		if err != nil {
			return err
		}
		loader, err := catalog.NewLoader(cfg.Matching.Catalogs, catalog.LoaderOptions{})
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
		var failedBooks []string
		var unfinished []string
		var morePending bool
		pendingIncomplete := false
		savedChoices := 0
		catalogsIncomplete := false
		defer func() {
			fmt.Fprintf(c.ErrOrStderr(), "Review saved choices: %d.\n", savedChoices)
			if len(unfinished) > 0 {
				fmt.Fprintf(c.ErrOrStderr(), "Review unfinished/unattempted source books: %s.\n", strings.Join(unfinished, ", "))
			}
			if morePending {
				fmt.Fprintln(c.ErrOrStderr(), "Later pending rows may remain unattempted; start a fresh review to resume.")
			}
			if pendingIncomplete {
				reviewErr = errors.Join(reviewErr, fmt.Errorf("pending source/metadata incomplete; see issue codes"))
			}
			if catalogsIncomplete {
				reviewErr = errors.Join(reviewErr, fmt.Errorf("matching catalogs incomplete; see issue codes"))
			}
			if len(failedBooks) > 0 {
				reviewErr = errors.Join(reviewErr, fmt.Errorf("review failed for source books: %s; no decisions saved for failures", strings.Join(failedBooks, ", ")))
			}
		}()
		var reviewTarget target
		var reviewTargetErr error
		targetLoaded := false
		loadTarget := func() (target, error) {
			if !targetLoaded {
				reviewTarget, reviewTargetErr = app.providers.target()
				targetLoaded = true
			}
			return reviewTarget, reviewTargetErr
		}
		after := ""
		for {
			if err := c.Context().Err(); err != nil {
				return err
			}
			pageCtx, pageCancel := context.WithTimeout(c.Context(), reviewProviderTimeout)
			report, pageErr := companion.PendingWorklistOwned(pageCtx, source, dbPath, companion.PendingOptions{SourceAccount: account, TargetAccount: targetAccount, Days: days, IncludePrivate: includePrivate, IncludeImported: includeImported, After: after, Now: now}, owner)
			pageCancel()
			unfinished = nil
			var pageUnattempted []string
			morePending = report.NextCursor != ""
			for _, book := range report.Books {
				unfinished = append(unfinished, book.ID)
			}
			for _, issue := range report.Issues {
				if issue.Code == "worklist_limit" {
					continue // Normal pagination is resolved by consuming NextCursor.
				}
				if issue.Code == "unattempted" {
					pageUnattempted = append(pageUnattempted, issue.BookID)
					unfinished = append(unfinished, issue.BookID)
					continue // A local page deadline continues from the last attempted row.
				}
				pendingIncomplete = true
				fmt.Fprintf(c.ErrOrStderr(), "Pending issue: %s %s\n", terminalText(issue.BookID), terminalText(issue.Code))
				if issue.BookID != "" && !slices.Contains(failedBooks, issue.BookID) {
					failedBooks = append(failedBooks, issue.BookID)
				}
			}
			if err := c.Context().Err(); err != nil {
				if pageErr != nil {
					fmt.Fprintln(c.ErrOrStderr(), "Pending acquisition interrupted; remaining eligibility is unknown beyond reported source IDs.")
				}
				return err
			}
			if pageErr != nil {
				pendingIncomplete = true
				fmt.Fprintln(c.ErrOrStderr(), "Pending acquisition failed; provider details omitted.")
				if !errors.Is(pageErr, context.DeadlineExceeded) || report.NextCursor == "" {
					return fmt.Errorf("pending acquisition failed; no decision saved for unfinished rows")
				}
			}
			for i, book := range report.Books {
				unfinished = append([]string(nil), pageUnattempted...)
				for _, remaining := range report.Books[i:] {
					unfinished = append(unfinished, remaining.ID)
				}
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
						query, expectedAuthor := "", ""
						for {
							lookupCtx, cancel := context.WithTimeout(c.Context(), reviewProviderTimeout)
							targetClient, sourceErr := loadTarget()
							var report companion.MatchReport
							if sourceErr == nil {
								report, sourceErr = companion.MatchWithOptions(lookupCtx, source, targetClient, book.ID, query, companion.MatchOptions{CatalogLoader: loader, ExpectedAuthor: expectedAuthor})
							}
							cancel()
							if sourceErr != nil {
								failedBooks = append(failedBooks, book.ID)
								fmt.Fprintf(c.ErrOrStderr(), "Book %s lookup failed; no decision saved.\n", book.ID)
								if err := c.Context().Err(); err != nil {
									return err
								}
								break
							}
							if report.CatalogsComplete != nil && !*report.CatalogsComplete {
								catalogsIncomplete = true
							}
							showReviewCatalogs(c.OutOrStdout(), report)
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
							fmt.Fprint(c.OutOrStdout(), "Choose suggestion number, manual work ID (work ID), none, query TEXT, title N, or author N: ")
							selection, inputErr := readReviewLine(c.Context(), terminal)
							if inputErr != nil {
								if errors.Is(inputErr, io.EOF) {
									return nil
								}
								return inputErr
							}
							selection = strings.TrimSpace(selection)
							if strings.HasPrefix(selection, "query ") {
								query = strings.TrimSpace(strings.TrimPrefix(selection, "query "))
								if query == "" || len(query) > 1024 {
									fmt.Fprintln(c.OutOrStdout(), "Query must contain 1–1024 bytes; no search issued.")
									break
								}
								expectedAuthor = ""
								continue
							}
							if strings.HasPrefix(selection, "title ") || strings.HasPrefix(selection, "author ") {
								parts := strings.Fields(selection)
								n := 0
								if len(parts) == 2 {
									n, _ = strconv.Atoi(parts[1])
								}
								if n < 1 || n > len(report.CatalogHints) {
									fmt.Fprintln(c.OutOrStdout(), "No such hint; no search issued.")
									break
								}
								hint := report.CatalogHints[n-1]
								if parts[0] == "title" && hint.Title != "" && len(hint.Title) <= 1024 {
									query = hint.Title
									expectedAuthor = ""
								} else if parts[0] == "author" && strings.TrimSpace(hint.OriginalAuthor) != "" {
									expectedAuthor = hint.OriginalAuthor
								} else {
									fmt.Fprintln(c.OutOrStdout(), "Hint field unavailable; no search issued.")
									break
								}
								continue
							}
							if selection == "none" || selection == "" {
								break
							}
							var chosen int64
							manual := strings.HasPrefix(selection, "work ")
							if manual {
								chosen, _ = strconv.ParseInt(strings.TrimSpace(strings.TrimPrefix(selection, "work ")), 10, 64)
							} else if n, parseErr := strconv.Atoi(selection); parseErr == nil && n > 0 && n <= len(candidates) {
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
							mapping := companion.Mapping{Provider: "weread", SourceAccount: account, BookID: book.ID, TargetAccount: targetAccount, WorkID: chosen, EditionID: editionID}
							var finalErr error
							if expected != nil {
								finalErr = companion.ConfirmLinkExpectedOwned(finalCtx, source, targetClient, dbPath, mapping, *expected, owner)
							} else {
								finalErr = companion.ConfirmLinkOwned(finalCtx, source, targetClient, dbPath, mapping, false, owner)
							}
							finalCancel()
							if finalErr != nil {
								failedBooks = append(failedBooks, book.ID)
								fmt.Fprintf(c.ErrOrStderr(), "Book %s link failed; no decision saved.\n", book.ID)
								if err := c.Context().Err(); err != nil {
									return err
								}
							} else {
								savedChoices++
							}
							break
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
					savedChoices++
					break
				}
			}
			unfinished = pageUnattempted
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
		if !unicode.IsControl(r) {
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
	if err := ctx.Err(); err != nil {
		return "", err
	}
	select {
	case <-ctx.Done():
		return "", ctx.Err()
	case r := <-result:
		if err := ctx.Err(); err != nil {
			return "", err
		}
		if r.err != nil && !errors.Is(r.err, io.EOF) {
			return "", fmt.Errorf("review input failed")
		}
		return r.line, r.err
	}
}

// Catalog targets are evidence only; they never enter the candidate selection list.
func showReviewCatalogs(out io.Writer, report companion.MatchReport) {
	if report.CatalogsComplete == nil {
		return
	}
	fmt.Fprintln(out, "Unverified catalog evidence (not mapping selections):")
	if !*report.CatalogsComplete {
		fmt.Fprintln(out, "Catalogs incomplete; missing evidence does not establish absence.")
		for _, issue := range report.CatalogIssues {
			fmt.Fprintf(out, "Catalog issue: %s %s\n", terminalText(issue.SubscriptionID), terminalText(string(issue.Code)))
		}
	}
	if len(report.CatalogConflicts) > 0 {
		fmt.Fprintln(out, "Conflicting assertions; all alternatives remain unverified.")
	}
	provenance := func(subscription, catalogID, publisher, record string, entries []catalog.Provenance) {
		fmt.Fprintf(out, "  Subscription %s; catalog %s; publisher %s; record %s\n", terminalText(subscription), terminalText(catalogID), terminalText(publisher), terminalText(record))
		for _, entry := range entries {
			fmt.Fprintf(out, "  Provenance: %s — %s\n", terminalText(entry.Source), terminalText(entry.Reference))
		}
	}
	for _, assertion := range report.CatalogAssertions {
		edition := int64(0)
		if assertion.Target.EditionID != nil {
			edition = *assertion.Target.EditionID
		}
		fmt.Fprintf(out, "Assertion: work %d edition %d\n", assertion.Target.WorkID, edition)
		provenance(assertion.SubscriptionID, assertion.CatalogID, assertion.Publisher, assertion.RecordID, assertion.Provenance)
	}
	for i, hint := range report.CatalogHints {
		fmt.Fprintf(out, "Hint %d: title %s; author %s; ISBN %s\n", i+1, terminalText(hint.Title), terminalText(hint.OriginalAuthor), terminalText(hint.ISBN))
		provenance(hint.SubscriptionID, hint.CatalogID, hint.Publisher, hint.RecordID, hint.Provenance)
	}
}
