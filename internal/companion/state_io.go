package companion

import (
	"context"
	"fmt"
	"time"
)

const StateExportFormatVersion = 1

// DrifterStateExport represents the exact portable versioned export format for Drifter state.
type DrifterStateExport struct {
	FormatVersion       int                  `json:"format_version"`
	ExportedAt          time.Time            `json:"exported_at"`
	AccountFingerprints []AccountFingerprint `json:"account_fingerprints,omitempty"`
	Mappings            []Mapping            `json:"mappings"`
	StatusBaselines     []StatusBaseline     `json:"status_baselines,omitempty"`
}

// StateImportReport describes the outcome of importing a state export file.
type StateImportReport struct {
	MappingsImported  int      `json:"mappings_imported"`
	MappingsSkipped   int      `json:"mappings_skipped"`
	FingerprintsBound int      `json:"fingerprints_bound"`
	BaselinesImported int      `json:"baselines_imported"`
	Conflicts         []string `json:"conflicts,omitempty"`
}

// ExportState dumps the database state (mappings, fingerprints, baselines) into a portable export struct.
func ExportState(ctx context.Context, store *Store) (DrifterStateExport, error) {
	mappings, err := store.List(ctx)
	if err != nil {
		return DrifterStateExport{}, fmt.Errorf("failed to list mappings: %w", err)
	}
	fingerprints, err := store.ListAccountFingerprints(ctx)
	if err != nil {
		return DrifterStateExport{}, fmt.Errorf("failed to list account fingerprints: %w", err)
	}
	baselines, err := store.ListStatusBaselines(ctx)
	if err != nil {
		return DrifterStateExport{}, fmt.Errorf("failed to list status baselines: %w", err)
	}

	return DrifterStateExport{
		FormatVersion:       StateExportFormatVersion,
		ExportedAt:          time.Now().UTC(),
		AccountFingerprints: fingerprints,
		Mappings:            mappings,
		StatusBaselines:     baselines,
	}, nil
}

// ImportState imports portable Drifter state, rejecting tampering and refusing implicit overwrites of differing mappings.
func ImportState(ctx context.Context, store *Store, data DrifterStateExport, rejectOnConflict bool) (StateImportReport, error) {
	if data.FormatVersion != StateExportFormatVersion {
		return StateImportReport{}, fmt.Errorf("unsupported export format version %d (expected %d)", data.FormatVersion, StateExportFormatVersion)
	}

	report := StateImportReport{
		Conflicts: []string{},
	}
	now := time.Now().UTC()

	// 1. Import fingerprints
	for _, fp := range data.AccountFingerprints {
		existing, err := store.GetAccountFingerprint(ctx, fp.Provider, fp.Account)
		if err != nil {
			return report, err
		}
		if existing != nil {
			if existing.Fingerprint != fp.Fingerprint {
				conflictMsg := fmt.Sprintf("fingerprint mismatch for %s:%s (existing: %s, imported: %s)",
					fp.Provider, fp.Account, existing.Fingerprint, fp.Fingerprint)
				report.Conflicts = append(report.Conflicts, conflictMsg)
				if rejectOnConflict {
					return report, fmt.Errorf("%s", conflictMsg)
				}
				continue
			}
		} else {
			if _, err := store.VerifyOrBindAccountFingerprint(ctx, fp.Provider, fp.Account, fp.Fingerprint, now); err != nil {
				return report, fmt.Errorf("failed to bind imported fingerprint %s:%s: %w", fp.Provider, fp.Account, err)
			}
			report.FingerprintsBound++
		}
	}

	// 2. Import mappings
	for _, m := range data.Mappings {
		if !m.valid() {
			return report, fmt.Errorf("invalid mapping record in import for %s:%s:%s", m.Provider, m.SourceAccount, m.BookID)
		}
		existing, err := store.GetMapping(ctx, m.Provider, m.SourceAccount, m.BookID, m.TargetAccount)
		if err != nil {
			return report, err
		}
		if existing != nil {
			if existing.WorkID != m.WorkID || existing.EditionID != m.EditionID {
				conflictMsg := fmt.Sprintf("conflicting mapping for %s:%s:%s -> existing work %d / edition %d vs imported work %d / edition %d",
					m.Provider, m.SourceAccount, m.BookID, existing.WorkID, existing.EditionID, m.WorkID, m.EditionID)
				report.Conflicts = append(report.Conflicts, conflictMsg)
				if rejectOnConflict {
					return report, fmt.Errorf("%s", conflictMsg)
				}
				report.MappingsSkipped++
				continue
			}
			report.MappingsSkipped++
			continue
		}
		if err := store.Confirm(ctx, m, false); err != nil {
			return report, fmt.Errorf("failed to confirm imported mapping: %w", err)
		}
		report.MappingsImported++
	}

	// 3. Import baselines
	for _, b := range data.StatusBaselines {
		if !b.valid() {
			continue
		}
		existing, err := store.GetStatusBaseline(ctx, b.Provider, b.SourceAccount, b.BookID, b.TargetAccount)
		if err != nil {
			return report, err
		}
		if existing == nil {
			if err := store.SaveStatusBaseline(ctx, b); err != nil {
				return report, fmt.Errorf("failed to save imported baseline: %w", err)
			}
			report.BaselinesImported++
		}
	}

	return report, nil
}
