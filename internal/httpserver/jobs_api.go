package httpserver

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/azusachino/drifter/internal/companion"
	"github.com/go-chi/chi/v5"
)

// syncJobPayload represents the JSON body or query parameters accepted by POST /api/v1/jobs/sync.
type syncJobPayload struct {
	Scope         string   `json:"scope"`
	Days          int      `json:"days"`
	Mode          string   `json:"mode"`
	Finished      *bool    `json:"finished"`
	FinishedOnly  *bool    `json:"finished_only"`
	MinConfidence *float64 `json:"min_confidence"`
	Force         *bool    `json:"force"`
	DryRun        *bool    `json:"dry_run"`
	Provider      string   `json:"provider"`
	SourceAccount string   `json:"source_account"`
	TargetAccount string   `json:"target_account"`
}

func parseBoolParam(val string) (bool, bool) {
	val = strings.TrimSpace(strings.ToLower(val))
	switch val {
	case "1", "t", "true", "yes", "y", "on":
		return true, true
	case "0", "f", "false", "no", "n", "off":
		return false, true
	default:
		return false, false
	}
}

// parseSyncJobInput parses parameters from query parameters and optional JSON body.
func parseSyncJobInput(r *http.Request) (companion.SyncJob, error) {
	var payload syncJobPayload

	// Check if there is a JSON body
	if r.Body != nil && r.ContentLength != 0 {
		ct := r.Header.Get("Content-Type")
		// If Content-Type is provided, it must be application/json
		if ct != "" && !hasContentType(r, "application/json") {
			return companion.SyncJob{}, errors.New("unsupported_media_type")
		}
		// Decode JSON body with size limit
		r.Body = http.MaxBytesReader(nil, r.Body, maxRequestBody)
		dec := json.NewDecoder(r.Body)
		if err := dec.Decode(&payload); err != nil {
			return companion.SyncJob{}, errors.New("invalid_json")
		}
	}

	// Query parameters override / supply defaults
	q := r.URL.Query()
	if val := q.Get("scope"); val != "" {
		payload.Scope = val
	}
	if val := q.Get("mode"); val != "" {
		payload.Mode = val
	}
	if val := q.Get("provider"); val != "" {
		payload.Provider = val
	}
	if val := q.Get("source_account"); val != "" {
		payload.SourceAccount = val
	}
	if val := q.Get("target_account"); val != "" {
		payload.TargetAccount = val
	}
	if val := q.Get("days"); val != "" {
		d, err := strconv.Atoi(val)
		if err != nil || d < 1 || d > companion.MaxLookbackDays {
			return companion.SyncJob{}, errors.New("invalid_days")
		}
		payload.Days = d
	}
	if val := q.Get("min_confidence"); val != "" {
		c, err := strconv.ParseFloat(val, 64)
		if err != nil || c <= 0 || c > 1.0 {
			return companion.SyncJob{}, errors.New("invalid_min_confidence")
		}
		payload.MinConfidence = &c
	}
	if val := q.Get("finished"); val != "" {
		if b, ok := parseBoolParam(val); ok {
			payload.Finished = &b
		} else {
			return companion.SyncJob{}, errors.New("invalid_finished")
		}
	}
	if val := q.Get("finished_only"); val != "" {
		if b, ok := parseBoolParam(val); ok {
			payload.FinishedOnly = &b
		} else {
			return companion.SyncJob{}, errors.New("invalid_finished")
		}
	}
	if val := q.Get("force"); val != "" {
		if b, ok := parseBoolParam(val); ok {
			payload.Force = &b
		} else {
			return companion.SyncJob{}, errors.New("invalid_force")
		}
	}
	if val := q.Get("dry_run"); val != "" {
		if b, ok := parseBoolParam(val); ok {
			payload.DryRun = &b
		} else {
			return companion.SyncJob{}, errors.New("invalid_dry_run")
		}
	}

	job := companion.SyncJob{
		Provider:      payload.Provider,
		SourceAccount: payload.SourceAccount,
		TargetAccount: payload.TargetAccount,
		Scope:         payload.Scope,
		Mode:          payload.Mode,
		Days:          payload.Days,
	}
	if payload.Finished != nil {
		job.FinishedOnly = *payload.Finished
	} else if payload.FinishedOnly != nil {
		job.FinishedOnly = *payload.FinishedOnly
	}
	if payload.MinConfidence != nil {
		job.MinConfidence = *payload.MinConfidence
	}
	if payload.Force != nil {
		job.Force = *payload.Force
	}
	if payload.DryRun != nil {
		job.DryRun = *payload.DryRun
	}

	return job, nil
}

// handleAPISyncJobCreate creates or coalesces a sync job.
// POST /api/v1/jobs/sync and POST /api/jobs/sync
func (s *Server) handleAPISyncJobCreate(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w, "POST")
		return
	}
	if !s.bearerAuthorized(r) {
		w.Header().Set("WWW-Authenticate", "Bearer")
		apiError(w, http.StatusUnauthorized, "unauthorized")
		return
	}

	jobInput, err := parseSyncJobInput(r)
	if err != nil {
		if err.Error() == "unsupported_media_type" {
			apiError(w, http.StatusUnsupportedMediaType, "application_json_required")
			return
		}
		apiError(w, http.StatusBadRequest, err.Error())
		return
	}

	store, err := companion.OpenOwnedStore(s.database, s.ownership)
	if err != nil {
		apiError(w, http.StatusServiceUnavailable, "mapping_database_unavailable")
		return
	}
	defer store.Close()

	job, coalesced, err := store.CoalesceOrEnqueueSyncJob(r.Context(), jobInput)
	if err != nil {
		apiError(w, http.StatusBadRequest, "invalid_sync_job")
		return
	}

	if !coalesced {
		s.notifyJobWorker()
		writeJSONStatus(w, r, http.StatusAccepted, job)
	} else {
		writeJSONStatus(w, r, http.StatusOK, job)
	}
}

// handleAPISyncJobList lists recent sync jobs.
// GET /api/v1/jobs and GET /api/jobs
func (s *Server) handleAPISyncJobList(w http.ResponseWriter, r *http.Request) {
	if !readRequestAllowed(w, r) {
		return
	}
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		methodNotAllowed(w, "GET, HEAD")
		return
	}
	if !s.bearerAuthorized(r) {
		w.Header().Set("WWW-Authenticate", "Bearer")
		apiError(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	if !noBody(w, r) {
		return
	}

	limit := 20
	if rawLimit := r.URL.Query().Get("limit"); rawLimit != "" {
		if parsed, err := strconv.Atoi(rawLimit); err == nil && parsed > 0 && parsed <= 100 {
			limit = parsed
		} else {
			apiError(w, http.StatusBadRequest, "invalid_limit")
			return
		}
	}

	store, err := companion.OpenOwnedStore(s.database, s.ownership)
	if err != nil {
		apiError(w, http.StatusServiceUnavailable, "mapping_database_unavailable")
		return
	}
	defer store.Close()

	jobs, err := store.ListSyncJobs(r.Context(), limit)
	if err != nil {
		apiError(w, http.StatusServiceUnavailable, "mapping_database_unavailable")
		return
	}
	if jobs == nil {
		jobs = []companion.SyncJob{}
	}
	writeJSON(w, r, jobs)
}

// handleAPISyncJobGet fetches a single sync job by ID.
// GET /api/v1/jobs/{id} and GET /api/jobs/{id}
func (s *Server) handleAPISyncJobGet(w http.ResponseWriter, r *http.Request) {
	if !readRequestAllowed(w, r) {
		return
	}
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		methodNotAllowed(w, "GET, HEAD")
		return
	}
	if !s.bearerAuthorized(r) {
		w.Header().Set("WWW-Authenticate", "Bearer")
		apiError(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	if !noBody(w, r) {
		return
	}

	rawID := chi.URLParam(r, "id")
	id, err := strconv.ParseInt(rawID, 10, 64)
	if err != nil || id <= 0 {
		apiError(w, http.StatusBadRequest, "invalid_job_id")
		return
	}

	store, err := companion.OpenOwnedStore(s.database, s.ownership)
	if err != nil {
		apiError(w, http.StatusServiceUnavailable, "mapping_database_unavailable")
		return
	}
	defer store.Close()

	job, err := store.GetSyncJob(r.Context(), id)
	if err != nil {
		apiError(w, http.StatusServiceUnavailable, "mapping_database_unavailable")
		return
	}
	if job == nil {
		apiError(w, http.StatusNotFound, "job_not_found")
		return
	}
	writeJSON(w, r, job)
}

// handleAPISyncJobCancel requests cancellation of a sync job.
// POST /api/v1/jobs/{id}/cancel and POST /api/jobs/{id}/cancel
func (s *Server) handleAPISyncJobCancel(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w, "POST")
		return
	}
	if !s.bearerAuthorized(r) {
		w.Header().Set("WWW-Authenticate", "Bearer")
		apiError(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	if !noBody(w, r) {
		return
	}

	rawID := chi.URLParam(r, "id")
	id, err := strconv.ParseInt(rawID, 10, 64)
	if err != nil || id <= 0 {
		apiError(w, http.StatusBadRequest, "invalid_job_id")
		return
	}

	store, err := companion.OpenOwnedStore(s.database, s.ownership)
	if err != nil {
		apiError(w, http.StatusServiceUnavailable, "mapping_database_unavailable")
		return
	}
	defer store.Close()

	job, active, err := store.RequestCancelSyncJob(r.Context(), id, s.now())
	if err != nil {
		apiError(w, http.StatusServiceUnavailable, "mapping_database_unavailable")
		return
	}
	if job == nil {
		apiError(w, http.StatusNotFound, "job_not_found")
		return
	}

	// Cancel in-memory worker context if it's currently actively running
	if active {
		s.cancelActiveJob(id)
	}

	writeJSON(w, r, job)
}

// handleFormJobSync handles browser form submission for starting a sync job.
// POST /jobs/sync
func (s *Server) handleFormJobSync(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w, "POST")
		return
	}
	if !mutationOriginAllowed(w, r) {
		return
	}

	values, ok := parseStrictForm(w, r, []string{
		"csrf_token", "scope", "mode", "days", "finished", "finished_only", "force", "dry_run",
	})
	if !ok {
		return
	}

	if !validCSRFToken(values.Get("csrf_token"), s.token) {
		http.Error(w, "request rejected", http.StatusForbidden)
		return
	}

	scope := values.Get("scope")
	if scope == "" {
		scope = companion.SyncJobScopeRecent
	}
	mode := values.Get("mode")
	if mode == "" {
		mode = companion.SyncJobModeMatchAndReconcile
	}

	days := 0
	if rawDays := values.Get("days"); rawDays != "" {
		d, err := strconv.Atoi(rawDays)
		if err != nil || d < 1 || d > companion.MaxLookbackDays {
			http.Error(w, "invalid form", http.StatusBadRequest)
			return
		}
		days = d
	}

	var finishedOnly bool
	if val := values.Get("finished"); val != "" {
		if b, ok := parseBoolParam(val); ok {
			finishedOnly = b
		} else {
			http.Error(w, "invalid form", http.StatusBadRequest)
			return
		}
	} else if val := values.Get("finished_only"); val != "" {
		if b, ok := parseBoolParam(val); ok {
			finishedOnly = b
		} else {
			http.Error(w, "invalid form", http.StatusBadRequest)
			return
		}
	}

	var force bool
	if val := values.Get("force"); val != "" {
		if b, ok := parseBoolParam(val); ok {
			force = b
		} else {
			http.Error(w, "invalid form", http.StatusBadRequest)
			return
		}
	}

	var dryRun bool
	if val := values.Get("dry_run"); val != "" {
		if b, ok := parseBoolParam(val); ok {
			dryRun = b
		} else {
			http.Error(w, "invalid form", http.StatusBadRequest)
			return
		}
	}

	jobInput := companion.SyncJob{
		Scope:        scope,
		Mode:         mode,
		Days:         days,
		FinishedOnly: finishedOnly,
		Force:        force,
		DryRun:       dryRun,
	}

	store, err := companion.OpenOwnedStore(s.database, s.ownership)
	if err != nil {
		s.renderFormFailure(w, r, http.StatusServiceUnavailable, "Mapping database unavailable.")
		return
	}
	defer store.Close()

	_, coalesced, err := store.CoalesceOrEnqueueSyncJob(r.Context(), jobInput)
	if err != nil {
		s.renderFormFailure(w, r, http.StatusBadRequest, "Invalid sync job parameters.")
		return
	}

	notice := "job_started"
	if coalesced {
		notice = "job_coalesced"
	} else {
		s.notifyJobWorker()
	}

	http.Redirect(w, r, "/?notice="+notice, http.StatusSeeOther)
}

// handleFormJobCancel handles browser form submission for cancelling a sync job.
// POST /jobs/cancel
func (s *Server) handleFormJobCancel(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w, "POST")
		return
	}
	if !mutationOriginAllowed(w, r) {
		return
	}

	values, ok := parseStrictForm(w, r, []string{"csrf_token", "job_id"})
	if !ok {
		return
	}

	if !validCSRFToken(values.Get("csrf_token"), s.token) {
		http.Error(w, "request rejected", http.StatusForbidden)
		return
	}

	jobID := parseFormInt(values.Get("job_id"))
	if jobID <= 0 {
		http.Error(w, "invalid form", http.StatusBadRequest)
		return
	}

	store, err := companion.OpenOwnedStore(s.database, s.ownership)
	if err != nil {
		s.renderFormFailure(w, r, http.StatusServiceUnavailable, "Mapping database unavailable.")
		return
	}
	defer store.Close()

	job, active, err := store.RequestCancelSyncJob(r.Context(), jobID, s.now())
	if err != nil {
		s.renderFormFailure(w, r, http.StatusServiceUnavailable, "Mapping database unavailable.")
		return
	}
	if job == nil {
		s.renderFormFailure(w, r, http.StatusNotFound, "Job not found.")
		return
	}

	if active {
		s.cancelActiveJob(jobID)
	}

	http.Redirect(w, r, "/?notice=job_cancelled", http.StatusSeeOther)
}
