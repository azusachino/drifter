package httpserver

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/azusachino/drifter/internal/companion"
)

func TestAdminStatusDashboardAndAPIs(t *testing.T) {
	now := time.Date(2026, 10, 27, 12, 0, 0, 0, time.UTC)
	dbPath := filepath.Join(t.TempDir(), "admin_test.sqlite")
	store, err := companion.OpenStore(dbPath)
	if err != nil {
		t.Fatal(err)
	}

	ctx := context.Background()
	// Seed 1 mapping
	m := companion.Mapping{
		Provider:      "weread",
		SourceAccount: "mine",
		BookID:        "12345",
		TargetAccount: "default",
		WorkID:        5001,
		EditionID:     6001,
		SourceTitle:   "Admin Test Book",
	}
	if err := store.Confirm(ctx, m, false); err != nil {
		t.Fatal(err)
	}

	// Seed 1 fingerprint
	if _, err := store.VerifyOrBindAccountFingerprint(ctx, "weread", "mine", "weread:99999", now); err != nil {
		t.Fatal(err)
	}

	// Seed 1 sync run
	runID, err := store.CreateSyncRun(ctx, "mine", "default", "v1", now)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.UpdateSyncRun(ctx, runID, "partial", 1, 0, 0, 1, 1, 0, now.Add(2*time.Second)); err != nil {
		t.Fatal(err)
	}

	// Seed 1 sync hold
	hold := companion.SyncHold{
		RunID:         runID,
		Provider:      "weread",
		SourceAccount: "mine",
		BookID:        "12345",
		TargetAccount: "default",
		WorkID:        5001,
		Reason:        "conflict",
		Message:       "target status mismatch",
		CreatedAt:     now,
	}
	if _, err := store.RecordSyncHold(ctx, hold); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	server, base, _, _ := newTestServer(t, &sourceStub{now: now}, dbPath, now)

	// 1. GET / renders the full dashboard HTML
	res := request(t, base, http.MethodGet, "/", nil, nil)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 from /, got %d", res.StatusCode)
	}
	body, err := io.ReadAll(res.Body)
	if err != nil {
		t.Fatal(err)
	}
	pageHTML := string(body)
	if !strings.Contains(pageHTML, "Latest sync run") {
		t.Errorf("missing Latest sync run section")
	}
	if !strings.Contains(pageHTML, "Run #1") {
		t.Errorf("missing Run #1")
	}
	if !strings.Contains(pageHTML, "Active holds and conflicts") {
		t.Errorf("missing Active holds section")
	}
	if !strings.Contains(pageHTML, "target status mismatch") {
		t.Errorf("missing hold message")
	}
	if !strings.Contains(pageHTML, "Bound account identities") {
		t.Errorf("missing Bound account identities section")
	}
	if !strings.Contains(pageHTML, "weread:99999") {
		t.Errorf("missing fingerprint in HTML")
	}
	if !strings.Contains(pageHTML, "Export State JSON") {
		t.Errorf("missing export link in HTML")
	}

	// 2. GET /api/status returns JSON summary
	statusRes := request(t, base, http.MethodGet, "/api/status", nil, nil)
	if statusRes.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 from /api/status, got %d", statusRes.StatusCode)
	}
	var statusData daemonStatusResponse
	if err := json.NewDecoder(statusRes.Body).Decode(&statusData); err != nil {
		t.Fatalf("failed to decode /api/status: %v", err)
	}
	if !statusData.Ready || statusData.MappingsCount != 1 || statusData.ActiveHoldsCount != 1 {
		t.Fatalf("unexpected status response: %+v", statusData)
	}
	if statusData.LatestRun == nil || statusData.LatestRun.ID != runID || statusData.LatestRun.Status != "partial" {
		t.Fatalf("unexpected latest run in status: %+v", statusData.LatestRun)
	}
	if len(statusData.Fingerprints) != 1 || statusData.Fingerprints[0].Fingerprint != "weread:99999" {
		t.Fatalf("unexpected fingerprints in status: %+v", statusData.Fingerprints)
	}

	// 3. GET /api/runs returns run list
	runsRes := request(t, base, http.MethodGet, "/api/runs", nil, nil)
	if runsRes.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 from /api/runs, got %d", runsRes.StatusCode)
	}
	var runs []companion.SyncRun
	if err := json.NewDecoder(runsRes.Body).Decode(&runs); err != nil {
		t.Fatalf("failed to decode runs: %v", err)
	}
	if len(runs) != 1 || runs[0].ID != runID {
		t.Fatalf("unexpected runs: %+v", runs)
	}

	// Query runs with filter
	runsFiltered := request(t, base, http.MethodGet, "/api/runs?account=mine&target_account=default", nil, nil)
	if runsFiltered.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 with filter, got %d", runsFiltered.StatusCode)
	}
	var filteredRuns []companion.SyncRun
	_ = json.NewDecoder(runsFiltered.Body).Decode(&filteredRuns)
	if len(filteredRuns) != 1 {
		t.Fatalf("expected 1 run for mine/default, got %d", len(filteredRuns))
	}

	// Query runs with other account
	runsOther := request(t, base, http.MethodGet, "/api/runs?account=other&target_account=default", nil, nil)
	var otherRuns []companion.SyncRun
	_ = json.NewDecoder(runsOther.Body).Decode(&otherRuns)
	if len(otherRuns) != 0 {
		t.Fatalf("expected 0 runs for other, got %d", len(otherRuns))
	}

	// 4. GET /api/holds returns holds list
	holdsRes := request(t, base, http.MethodGet, "/api/holds", nil, nil)
	if holdsRes.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 from /api/holds, got %d", holdsRes.StatusCode)
	}
	var holds []companion.SyncHold
	if err := json.NewDecoder(holdsRes.Body).Decode(&holds); err != nil {
		t.Fatalf("failed to decode holds: %v", err)
	}
	if len(holds) != 1 || holds[0].Reason != "conflict" {
		t.Fatalf("unexpected holds: %+v", holds)
	}

	// 5. GET /api/export returns export format
	exportRes := request(t, base, http.MethodGet, "/api/export", nil, nil)
	if exportRes.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 from /api/export, got %d", exportRes.StatusCode)
	}
	if cd := exportRes.Header.Get("Content-Disposition"); !strings.Contains(cd, "drifter-export.json") {
		t.Errorf("missing or unexpected Content-Disposition: %q", cd)
	}
	var exportData companion.DrifterStateExport
	if err := json.NewDecoder(exportRes.Body).Decode(&exportData); err != nil {
		t.Fatalf("failed to decode export: %v", err)
	}
	if exportData.FormatVersion != companion.StateExportFormatVersion || len(exportData.Mappings) != 1 || len(exportData.AccountFingerprints) != 1 {
		t.Fatalf("unexpected export data: %+v", exportData)
	}

	// 6. POST /api/import security tests
	exportBytes, _ := json.Marshal(exportData)
	csrfToken := server.token

	// Missing CSRF -> 403
	noCSRF := request(t, base, http.MethodPost, "/api/import", map[string]string{
		"Origin":       base,
		"Content-Type": "application/json",
	}, bytes.NewReader(exportBytes))
	if noCSRF.StatusCode != http.StatusForbidden {
		t.Fatalf("expected 403 without CSRF token, got %d", noCSRF.StatusCode)
	}

	// Missing Origin -> 403
	noOrigin := request(t, base, http.MethodPost, "/api/import", map[string]string{
		"X-Drifter-CSRF": csrfToken,
		"Content-Type":   "application/json",
	}, bytes.NewReader(exportBytes))
	if noOrigin.StatusCode != http.StatusForbidden {
		t.Fatalf("expected 403 without Origin, got %d", noOrigin.StatusCode)
	}

	// Wrong Content-Type -> 415
	wrongCT := request(t, base, http.MethodPost, "/api/import", map[string]string{
		"Origin":         base,
		"X-Drifter-CSRF": csrfToken,
		"Content-Type":   "text/plain",
	}, bytes.NewReader(exportBytes))
	if wrongCT.StatusCode != http.StatusUnsupportedMediaType {
		t.Fatalf("expected 415 without application/json, got %d", wrongCT.StatusCode)
	}

	// 7. POST /api/import into a fresh server instance
	dbPath2 := filepath.Join(t.TempDir(), "import_target.sqlite")
	server2, base2, _, _ := newTestServer(t, &sourceStub{now: now}, dbPath2, now)

	goodImport := request(t, base2, http.MethodPost, "/api/import", map[string]string{
		"Origin":         base2,
		"X-Drifter-CSRF": server2.token,
		"Content-Type":   "application/json",
	}, bytes.NewReader(exportBytes))
	if goodImport.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(goodImport.Body)
		t.Fatalf("expected 200 from import, got %d: %s", goodImport.StatusCode, string(body))
	}
	var report companion.StateImportReport
	if err := json.NewDecoder(goodImport.Body).Decode(&report); err != nil {
		t.Fatalf("failed to decode import report: %v", err)
	}
	if report.MappingsImported != 1 || report.FingerprintsBound != 1 {
		t.Fatalf("unexpected import report: %+v", report)
	}

	// 8. Conflicting import rejection test
	conflictingData := exportData
	conflictingData.Mappings = []companion.Mapping{
		{
			Provider:      "weread",
			SourceAccount: "mine",
			BookID:        "12345",
			TargetAccount: "default",
			WorkID:        9999, // different work ID!
			EditionID:     0,
			SourceTitle:   "Conflicting Book",
		},
	}
	conflictingBytes, _ := json.Marshal(conflictingData)

	// With reject_on_conflict=true -> 409 Conflict
	conflictRes := request(t, base2, http.MethodPost, "/api/import?reject_on_conflict=true", map[string]string{
		"Origin":         base2,
		"X-Drifter-CSRF": server2.token,
		"Content-Type":   "application/json",
	}, bytes.NewReader(conflictingBytes))
	if conflictRes.StatusCode != http.StatusConflict {
		t.Fatalf("expected 409 for conflicting import with reject_on_conflict=true, got %d", conflictRes.StatusCode)
	}

	// With default (skip) -> 200 OK with report.Conflicts and MappingsSkipped
	skipRes := request(t, base2, http.MethodPost, "/api/import", map[string]string{
		"Origin":         base2,
		"X-Drifter-CSRF": server2.token,
		"Content-Type":   "application/json",
	}, bytes.NewReader(conflictingBytes))
	if skipRes.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 for skipped conflict import, got %d", skipRes.StatusCode)
	}
	var skipReport companion.StateImportReport
	_ = json.NewDecoder(skipRes.Body).Decode(&skipReport)
	if skipReport.MappingsSkipped != 1 || len(skipReport.Conflicts) != 1 {
		t.Fatalf("expected 1 conflict skipped, got: %+v", skipReport)
	}
}
