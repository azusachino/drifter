// Package httpserver provides Drifter's local HTTP mapping server and mapping interface.
package httpserver

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/subtle"
	"embed"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"html/template"
	"io"
	"log/slog"
	"mime"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/azusachino/drifter/internal/companion"
	"github.com/azusachino/drifter/internal/probes"
	"github.com/go-chi/chi/v5"
)

const maxRequestBody = 8 << 10
const defaultOperationTimeout = 90 * time.Second

var errProviderUnavailable = errors.New("provider unavailable")

//go:embed templates/*.html
var htmlTemplates embed.FS

//go:embed assets/favicon.svg
var favicon []byte

type pageData struct {
	Mappings []companion.Mapping
	Token    string
	Notice   string
}

var page = template.Must(template.ParseFS(htmlTemplates, "templates/index.html"))

// Server owns an HTTP listener and the shared core dependencies used by routes.
type Server struct {
	http             *http.Server
	address          string
	database         string
	ownership        *companion.DatabaseOwnership
	token            string
	logger           *slog.Logger
	now              func() time.Time
	source           companion.Source
	sourceFactory    func() (companion.Source, error)
	sourceOnce       sync.Once
	sourceErr        error
	target           companion.Target
	targetFactory    func() (companion.Target, error)
	targetOnce       sync.Once
	targetErr        error
	operationSlot    chan struct{}
	operationTimeout time.Duration
}

type Options struct {
	Address          string
	Database         string
	Source           companion.Source
	SourceFactory    func() (companion.Source, error)
	Target           companion.Target
	TargetFactory    func() (companion.Target, error)
	Logger           *slog.Logger
	Now              func() time.Time
	operationTimeout time.Duration
}

func New(options Options) (*Server, error) {
	if options.Address == "" || options.Database == "" || options.Logger == nil {
		return nil, fmt.Errorf("server address, database and logger required")
	}
	if options.Source == nil && options.SourceFactory == nil {
		return nil, fmt.Errorf("source provider factory required")
	}
	if options.Target == nil && options.TargetFactory == nil {
		return nil, fmt.Errorf("target provider factory required")
	}
	if options.Now == nil {
		options.Now = time.Now
	}
	if options.operationTimeout <= 0 {
		options.operationTimeout = defaultOperationTimeout
	}
	tokenBytes := make([]byte, 32)
	if _, err := rand.Read(tokenBytes); err != nil {
		return nil, fmt.Errorf("cannot initialize request protection")
	}
	s := &Server{address: options.Address, database: options.Database, token: base64.RawURLEncoding.EncodeToString(tokenBytes),
		logger: options.Logger, now: options.Now, source: options.Source, sourceFactory: options.SourceFactory,
		target: options.Target, targetFactory: options.TargetFactory,
		operationSlot: make(chan struct{}, 1), operationTimeout: options.operationTimeout}
	router := chi.NewRouter()
	router.Use(requestRouteLogAttribute)
	ready := []probes.Readiness{{Name: "mapping_database", Check: func(r *http.Request) error {
		store, err := companion.OpenOwnedStore(s.database, s.ownership)
		if err != nil {
			return err
		}
		defer store.Close()
		_, err = store.List(r.Context())
		return err
	}}}
	router.Get("/", s.handleRoot)
	router.Head("/", s.handleRoot)
	router.Get("/favicon.svg", s.handleFavicon)
	router.Head("/favicon.svg", s.handleFavicon)
	router.Method(http.MethodGet, "/healthz", probesHandler("/healthz", ready, options.Logger))
	router.Method(http.MethodHead, "/healthz", probesHandler("/healthz", ready, options.Logger))
	router.Method(http.MethodGet, "/readyz", probesHandler("/readyz", ready, options.Logger))
	router.Method(http.MethodHead, "/readyz", probesHandler("/readyz", ready, options.Logger))
	router.Route("/api", func(api chi.Router) {
		api.Get("/csrf", s.handleCSRF)
		api.Get("/mappings", s.handleMappings)
		api.Head("/mappings", s.handleMappings)
		api.Get("/pending", s.handlePending)
		api.Post("/mappings/confirm", func(w http.ResponseWriter, r *http.Request) { s.handleJSONMapping(w, r, false) })
		api.Post("/mappings/replace", func(w http.ResponseWriter, r *http.Request) { s.handleJSONMapping(w, r, true) })
		api.Delete("/mappings/unlink", s.handleJSONUnlink)
	})
	router.Route("/mappings", func(mappings chi.Router) {
		mappings.Post("/confirm", func(w http.ResponseWriter, r *http.Request) { s.handleFormMapping(w, r, false) })
		mappings.Post("/replace", func(w http.ResponseWriter, r *http.Request) { s.handleFormMapping(w, r, true) })
		mappings.Post("/unlink", s.handleFormUnlink)
	})
	s.http = &http.Server{
		Addr: options.Address, Handler: router, ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout: 15 * time.Second, WriteTimeout: 2 * time.Minute, IdleTimeout: 60 * time.Second,
	}
	return s, nil
}

func (s *Server) admit(parent context.Context) (context.Context, func(), bool) {
	select {
	case s.operationSlot <- struct{}{}:
	default:
		return nil, nil, false
	}
	ctx, cancel := context.WithTimeout(parent, s.operationTimeout)
	return ctx, func() { cancel(); <-s.operationSlot }, true
}

func (s *Server) pendingSource() (companion.Source, error) {
	s.sourceOnce.Do(func() {
		if s.source == nil {
			s.source, s.sourceErr = s.sourceFactory()
		}
	})
	return s.source, s.sourceErr
}

func (s *Server) confirmationTarget() (companion.Target, error) {
	s.targetOnce.Do(func() {
		if s.target == nil {
			s.target, s.targetErr = s.targetFactory()
		}
	})
	return s.target, s.targetErr
}

func (s *Server) handleFavicon(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		methodNotAllowed(w, "GET, HEAD")
		return
	}
	if r.URL.RawQuery != "" || !noBody(w, r) {
		if r.URL.RawQuery != "" {
			http.Error(w, "unexpected query", http.StatusBadRequest)
		}
		return
	}
	w.Header().Set("Content-Type", "image/svg+xml")
	w.Header().Set("Cache-Control", "public, max-age=86400")
	w.Header().Set("Content-Length", strconv.Itoa(len(favicon)))
	if r.Method == http.MethodGet {
		_, _ = w.Write(favicon)
	}
}

func (s *Server) handleRoot(w http.ResponseWriter, r *http.Request) {
	if !readRequestAllowed(w, r) {
		return
	}
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		methodNotAllowed(w, "GET, HEAD")
		return
	}
	if len(r.URL.Query()) > 1 || (len(r.URL.Query()) == 1 && (len(r.URL.Query()["notice"]) != 1 || r.URL.Query().Get("notice") == "")) {
		http.Error(w, "invalid page query", http.StatusBadRequest)
		return
	}
	if !noBody(w, r) {
		return
	}
	notices := map[string]string{"confirmed": "Mapping confirmed.", "replaced": "Mapping replaced.", "unlinked": "Mapping unlinked."}
	notice := notices[r.URL.Query().Get("notice")]
	if r.URL.Query().Get("notice") != "" && notice == "" {
		http.Error(w, "invalid page query", http.StatusBadRequest)
		return
	}
	store, err := companion.OpenOwnedStore(s.database, s.ownership)
	if err != nil {
		http.Error(w, "mapping database unavailable", http.StatusServiceUnavailable)
		return
	}
	defer store.Close()
	mappings, err := store.List(r.Context())
	if err != nil {
		http.Error(w, "mapping database unavailable", http.StatusServiceUnavailable)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	if r.Method == http.MethodGet {
		_ = page.Execute(w, pageData{Mappings: mappings, Token: s.token, Notice: notice})
	}
}

func (s *Server) handleCSRF(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		methodNotAllowed(w, "GET")
		return
	}
	if r.URL.RawQuery != "" || !noBody(w, r) {
		if r.URL.RawQuery != "" {
			http.Error(w, "unexpected query", http.StatusBadRequest)
		}
		return
	}
	writeJSON(w, r, map[string]string{"csrf_token": s.token})
}

func (s *Server) handleMappings(w http.ResponseWriter, r *http.Request) {
	if !readRequestAllowed(w, r) {
		return
	}
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		methodNotAllowed(w, "GET, HEAD")
		return
	}
	if r.URL.RawQuery != "" || !noBody(w, r) {
		if r.URL.RawQuery != "" {
			http.Error(w, "unexpected query", http.StatusBadRequest)
		}
		return
	}
	store, err := companion.OpenOwnedStore(s.database, s.ownership)
	if err != nil {
		http.Error(w, "mapping database unavailable", http.StatusServiceUnavailable)
		return
	}
	defer store.Close()
	rows, err := store.List(r.Context())
	if err != nil {
		http.Error(w, "mapping database unavailable", http.StatusServiceUnavailable)
		return
	}
	writeJSON(w, r, rows)
}

func (s *Server) handlePending(w http.ResponseWriter, r *http.Request) {
	if !readRequestAllowed(w, r) {
		return
	}
	if r.Method != http.MethodGet {
		methodNotAllowed(w, "GET")
		return
	}
	noBody(w, r, func() {
		opts, err := pendingOptions(r.URL.Query(), s.now())
		if err != nil {
			http.Error(w, "invalid pending query", http.StatusBadRequest)
			return
		}
		ctx, release, ok := s.admit(r.Context())
		if !ok {
			http.Error(w, "provider operation busy", http.StatusServiceUnavailable)
			return
		}
		defer release()
		source, err := s.pendingSource()
		if err != nil {
			http.Error(w, "provider unavailable", http.StatusServiceUnavailable)
			return
		}
		report, err := companion.PendingWorklistOwned(ctx, source, s.database, opts, s.ownership)
		if report.Books == nil {
			report.Books = []companion.PendingBook{}
		}
		if report.Issues == nil {
			report.Issues = []companion.PendingIssue{}
		}
		if err != nil {
			if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) || errors.Is(ctx.Err(), context.Canceled) || errors.Is(ctx.Err(), context.DeadlineExceeded) {
				report.Complete = false
				code := "operation_canceled"
				status := http.StatusGatewayTimeout
				if errors.Is(err, context.DeadlineExceeded) || errors.Is(ctx.Err(), context.DeadlineExceeded) {
					code = "operation_timeout"
				}
				report.Issues = append(report.Issues, companion.PendingIssue{Code: code})
				writeJSONStatus(w, r, status, report)
				return
			}
			http.Error(w, "pending request failed", http.StatusBadGateway)
			return
		}
		writeJSON(w, r, report)
	})
}

type explicitEditionID struct {
	Value   int64
	Present bool
}

func (id *explicitEditionID) UnmarshalJSON(data []byte) error {
	if bytes.Equal(bytes.TrimSpace(data), []byte("null")) {
		return fmt.Errorf("edition_id must be an explicit integer")
	}
	id.Present = true
	return json.Unmarshal(data, &id.Value)
}

type mappingMutation struct {
	SourceAccount     string            `json:"source_account"`
	BookID            string            `json:"book_id"`
	TargetAccount     string            `json:"target_account"`
	WorkID            int64             `json:"work_id"`
	EditionID         explicitEditionID `json:"edition_id"`
	ExpectedWorkID    explicitEditionID `json:"expected_work_id"`
	ExpectedEditionID explicitEditionID `json:"expected_edition_id"`
}

type unlinkMutation struct {
	SourceAccount     string            `json:"source_account"`
	BookID            string            `json:"book_id"`
	TargetAccount     string            `json:"target_account"`
	ExpectedWorkID    explicitEditionID `json:"expected_work_id"`
	ExpectedEditionID explicitEditionID `json:"expected_edition_id"`
}

func (s *Server) handleJSONMapping(w http.ResponseWriter, r *http.Request, replace bool) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", "POST")
		apiError(w, http.StatusMethodNotAllowed, "method_not_allowed")
		return
	}
	if !mutationOriginAllowed(w, r) {
		return
	}
	if r.URL.RawQuery != "" {
		apiError(w, http.StatusBadRequest, "unexpected_query")
		return
	}
	if !validHeaderToken(r, s.token) {
		apiError(w, http.StatusForbidden, "request_rejected")
		return
	}
	if !hasContentType(r, "application/json") {
		apiError(w, http.StatusUnsupportedMediaType, "application_json_required")
		return
	}
	body, ok := readBoundedBody(w, r)
	if !ok {
		return
	}
	var request mappingMutation
	if err := decodeStrictJSON(body, &request); err != nil || !request.EditionID.Present || (replace && (!request.ExpectedWorkID.Present || !request.ExpectedEditionID.Present || request.ExpectedWorkID.Value <= 0 || request.ExpectedEditionID.Value < 0)) || (!replace && (request.ExpectedWorkID.Present || request.ExpectedEditionID.Present)) {
		apiError(w, http.StatusBadRequest, "invalid_mapping_request")
		return
	}
	selection := companion.Mapping{Provider: "weread", SourceAccount: request.SourceAccount, BookID: request.BookID,
		TargetAccount: request.TargetAccount, WorkID: request.WorkID, EditionID: request.EditionID.Value}
	ctx, release, admitted := s.admit(r.Context())
	if !admitted {
		apiError(w, http.StatusServiceUnavailable, "provider_operation_busy")
		return
	}
	defer release()
	var err error
	if replace {
		err = s.confirmMappingExpected(ctx, selection, companion.MappingTarget{WorkID: request.ExpectedWorkID.Value, EditionID: request.ExpectedEditionID.Value})
	} else {
		err = s.confirmMapping(ctx, selection, false)
	}
	if err != nil {
		status, code := mutationError(err, true)
		apiError(w, status, code)
		return
	}
	writeJSONStatus(w, r, http.StatusOK, map[string]bool{"confirmed": true, "replaced": replace})
}

func (s *Server) handleJSONUnlink(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodDelete {
		w.Header().Set("Allow", "DELETE")
		apiError(w, http.StatusMethodNotAllowed, "method_not_allowed")
		return
	}
	if !mutationOriginAllowed(w, r) {
		return
	}
	if r.URL.RawQuery != "" {
		apiError(w, http.StatusBadRequest, "unexpected_query")
		return
	}
	if !validHeaderToken(r, s.token) {
		apiError(w, http.StatusForbidden, "request_rejected")
		return
	}
	if !hasContentType(r, "application/json") {
		apiError(w, http.StatusUnsupportedMediaType, "application_json_required")
		return
	}
	body, ok := readBoundedBody(w, r)
	if !ok {
		return
	}
	var request unlinkMutation
	if err := decodeStrictJSON(body, &request); err != nil || !request.ExpectedWorkID.Present || !request.ExpectedEditionID.Present || request.ExpectedWorkID.Value <= 0 || request.ExpectedEditionID.Value < 0 {
		apiError(w, http.StatusBadRequest, "invalid_unlink_request")
		return
	}
	if err := s.unlinkMappingExpected(r.Context(), request); err != nil {
		status, code := mutationError(err, false)
		apiError(w, status, code)
		return
	}
	writeJSONStatus(w, r, http.StatusOK, map[string]bool{"unlinked": true})
}

func (s *Server) handleFormMapping(w http.ResponseWriter, r *http.Request, replace bool) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w, "POST")
		return
	}
	if !mutationOriginAllowed(w, r) {
		return
	}
	allowed := []string{"csrf_token", "source_account", "book_id", "target_account", "work_id", "edition_mode", "edition_id"}
	if replace {
		allowed = append(allowed, "expected_work_id", "expected_edition_id")
	}
	values, ok := parseStrictForm(w, r, allowed)
	if !ok {
		return
	}
	if !validCSRFToken(values.Get("csrf_token"), s.token) {
		http.Error(w, "request rejected", http.StatusForbidden)
		return
	}
	if replace && !validExpectedForm(values) {
		http.Error(w, "invalid form", http.StatusBadRequest)
		return
	}
	selection, err := mappingFromForm(values)
	if err != nil {
		s.renderFormFailure(w, r, http.StatusBadRequest, "The mapping request was invalid.")
		return
	}
	ctx, release, admitted := s.admit(r.Context())
	if !admitted {
		s.renderFormFailure(w, r, http.StatusServiceUnavailable, "Another provider operation is in progress.")
		return
	}
	defer release()
	var confirmErr error
	if replace {
		confirmErr = s.confirmMappingExpected(ctx, selection, companion.MappingTarget{WorkID: parseFormInt(values.Get("expected_work_id")), EditionID: parseFormInt(values.Get("expected_edition_id"))})
	} else {
		confirmErr = s.confirmMapping(ctx, selection, false)
	}
	if err := confirmErr; err != nil {
		status, _ := mutationError(err, true)
		message := "The source or target selection could not be validated."
		if errors.Is(err, context.DeadlineExceeded) {
			message = "Provider validation timed out. Reload mappings before retrying."
		}
		if errors.Is(err, companion.ErrMappingConflict) {
			message = "The mapping changed since this page was loaded. Reload mappings and review the current target before trying again."
		}
		s.renderFormFailure(w, r, status, message)
		return
	}
	notice := "confirmed"
	if replace {
		notice = "replaced"
	}
	http.Redirect(w, r, "/?notice="+notice, http.StatusSeeOther)
}

func (s *Server) handleFormUnlink(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w, "POST")
		return
	}
	if !mutationOriginAllowed(w, r) {
		return
	}
	values, ok := parseStrictForm(w, r, []string{"csrf_token", "source_account", "book_id", "target_account", "expected_work_id", "expected_edition_id"})
	if !ok {
		return
	}
	if !validCSRFToken(values.Get("csrf_token"), s.token) {
		http.Error(w, "request rejected", http.StatusForbidden)
		return
	}
	if !validExpectedForm(values) {
		http.Error(w, "invalid form", http.StatusBadRequest)
		return
	}
	request := unlinkMutation{SourceAccount: values.Get("source_account"), BookID: values.Get("book_id"), TargetAccount: values.Get("target_account"), ExpectedWorkID: explicitEditionID{Value: parseFormInt(values.Get("expected_work_id")), Present: true}, ExpectedEditionID: explicitEditionID{Value: parseFormInt(values.Get("expected_edition_id")), Present: true}}
	if err := s.unlinkMappingExpected(r.Context(), request); err != nil {
		status, _ := mutationError(err, false)
		message := "The mapping could not be unlinked."
		if errors.Is(err, companion.ErrMappingConflict) {
			message = "The mapping changed since this page was loaded. Reload mappings and review the current target before trying again."
		}
		s.renderFormFailure(w, r, status, message)
		return
	}
	http.Redirect(w, r, "/?notice=unlinked", http.StatusSeeOther)
}

func (s *Server) confirmMapping(ctx context.Context, selection companion.Mapping, replace bool) error {
	source, err := s.pendingSource()
	if err != nil {
		return errProviderUnavailable
	}
	target, err := s.confirmationTarget()
	if err != nil {
		return errProviderUnavailable
	}
	return companion.ConfirmLinkOwned(ctx, source, target, s.database, selection, replace, s.ownership)
}

func (s *Server) confirmMappingExpected(ctx context.Context, selection companion.Mapping, expected companion.MappingTarget) error {
	source, err := s.pendingSource()
	if err != nil {
		return errProviderUnavailable
	}
	target, err := s.confirmationTarget()
	if err != nil {
		return errProviderUnavailable
	}
	return companion.ConfirmLinkExpectedOwned(ctx, source, target, s.database, selection, expected, s.ownership)
}

func (s *Server) unlinkMappingExpected(ctx context.Context, request unlinkMutation) error {
	store, err := companion.OpenOwnedStore(s.database, s.ownership)
	if err != nil {
		return err
	}
	defer store.Close()
	return store.UnlinkExpected(ctx, "weread", request.SourceAccount, request.BookID, request.TargetAccount,
		companion.MappingTarget{WorkID: request.ExpectedWorkID.Value, EditionID: request.ExpectedEditionID.Value})
}

func mutationError(err error, confirming bool) (int, string) {
	switch {
	case errors.Is(err, companion.ErrInvalidMapping), errors.Is(err, companion.ErrInvalidMappingKey):
		return http.StatusBadRequest, "invalid_mapping"
	case errors.Is(err, companion.ErrMappingConflict):
		return http.StatusConflict, "mapping_conflict"
	case errors.Is(err, context.DeadlineExceeded):
		return http.StatusGatewayTimeout, "provider_operation_timeout"
	case errors.Is(err, errProviderUnavailable):
		return http.StatusServiceUnavailable, "provider_unavailable"
	case errors.Is(err, companion.ErrDatabaseOwned), errors.Is(err, companion.ErrOwnershipUnsupported):
		return http.StatusServiceUnavailable, "mapping_database_unavailable"
	default:
		if confirming {
			return http.StatusUnprocessableEntity, "mapping_validation_failed"
		}
		return http.StatusServiceUnavailable, "mapping_database_unavailable"
	}
}

func (s *Server) renderFormFailure(w http.ResponseWriter, r *http.Request, status int, notice string) {
	store, err := companion.OpenOwnedStore(s.database, s.ownership)
	if err != nil {
		http.Error(w, "mapping database unavailable", http.StatusServiceUnavailable)
		return
	}
	defer store.Close()
	mappings, err := store.List(r.Context())
	if err != nil {
		http.Error(w, "mapping database unavailable", http.StatusServiceUnavailable)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	_ = page.Execute(w, pageData{Mappings: mappings, Token: s.token, Notice: notice})
}

func writeJSONStatus(w http.ResponseWriter, r *http.Request, status int, value any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(status)
	if r.Method != http.MethodHead {
		_ = json.NewEncoder(w).Encode(value)
	}
}

func apiError(w http.ResponseWriter, status int, code string) {
	body := map[string]string{"error": code}
	if code == "mapping_conflict" {
		body["message"] = "Mapping changed since it was displayed; reload mappings and review the current target."
	}
	writeJSONStatus(w, &http.Request{Method: http.MethodPost}, status, body)
}

func mutationOriginAllowed(w http.ResponseWriter, r *http.Request) bool {
	origins := r.Header.Values("Origin")
	if len(origins) != 1 || !validOrigin(origins[0], r.Host) {
		http.Error(w, "request rejected", http.StatusForbidden)
		return false
	}
	if site := strings.ToLower(strings.TrimSpace(r.Header.Get("Sec-Fetch-Site"))); site != "" && site != "same-origin" {
		http.Error(w, "request rejected", http.StatusForbidden)
		return false
	}
	return true
}

func validOrigin(origin, host string) bool {
	return origin == "http://"+host
}

func validHeaderToken(r *http.Request, expected string) bool {
	values := r.Header.Values("X-Drifter-CSRF")
	return len(values) == 1 && validCSRFToken(values[0], expected)
}

func validCSRFToken(given, expected string) bool {
	return subtle.ConstantTimeCompare([]byte(given), []byte(expected)) == 1
}

func hasContentType(r *http.Request, want string) bool {
	values := r.Header.Values("Content-Type")
	if len(values) != 1 {
		return false
	}
	mediaType, params, err := mime.ParseMediaType(values[0])
	if err != nil || mediaType != want {
		return false
	}
	if want == "application/json" {
		for key, value := range params {
			if key != "charset" || !strings.EqualFold(value, "utf-8") {
				return false
			}
		}
		return true
	}
	return len(params) == 0
}

func readBoundedBody(w http.ResponseWriter, r *http.Request) ([]byte, bool) {
	if !readRequestAllowed(w, r) {
		return nil, false
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxRequestBody))
	if err != nil {
		var limitErr *http.MaxBytesError
		if errors.As(err, &limitErr) {
			http.Error(w, "request body too large", http.StatusRequestEntityTooLarge)
		} else {
			http.Error(w, "invalid request body", http.StatusBadRequest)
		}
		return nil, false
	}
	return body, true
}

func decodeStrictJSON(body []byte, target any) error {
	if !utf8.Valid(body) {
		return fmt.Errorf("JSON body is not UTF-8")
	}
	check := json.NewDecoder(bytes.NewReader(body))
	if err := consumeJSONValue(check); err != nil {
		return err
	}
	if _, err := check.Token(); err != io.EOF {
		return fmt.Errorf("trailing JSON value")
	}
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		return fmt.Errorf("trailing JSON value")
	}
	return nil
}

func consumeJSONValue(decoder *json.Decoder) error {
	token, err := decoder.Token()
	if err != nil {
		return err
	}
	delimiter, ok := token.(json.Delim)
	if !ok {
		return nil
	}
	switch delimiter {
	case '{':
		seen := make(map[string]struct{})
		for decoder.More() {
			keyToken, err := decoder.Token()
			if err != nil {
				return err
			}
			key, ok := keyToken.(string)
			if !ok {
				return fmt.Errorf("invalid JSON object key")
			}
			normalizedKey := strings.ToLower(key)
			if key != normalizedKey {
				return fmt.Errorf("noncanonical JSON object key")
			}
			if _, duplicate := seen[normalizedKey]; duplicate {
				return fmt.Errorf("duplicate JSON object key")
			}
			seen[normalizedKey] = struct{}{}
			if err := consumeJSONValue(decoder); err != nil {
				return err
			}
		}
		closing, err := decoder.Token()
		if err != nil || closing != json.Delim('}') {
			return fmt.Errorf("invalid JSON object")
		}
	case '[':
		for decoder.More() {
			if err := consumeJSONValue(decoder); err != nil {
				return err
			}
		}
		closing, err := decoder.Token()
		if err != nil || closing != json.Delim(']') {
			return fmt.Errorf("invalid JSON array")
		}
	default:
		return fmt.Errorf("invalid JSON delimiter")
	}
	return nil
}

func validExpectedForm(values url.Values) bool {
	work, workOK := values["expected_work_id"]
	edition, editionOK := values["expected_edition_id"]
	if !workOK || !editionOK || len(work) != 1 || len(edition) != 1 {
		return false
	}
	workID, workErr := strconv.ParseInt(work[0], 10, 64)
	editionID, editionErr := strconv.ParseInt(edition[0], 10, 64)
	return workErr == nil && editionErr == nil && workID > 0 && editionID >= 0
}

func parseFormInt(value string) int64 { id, _ := strconv.ParseInt(value, 10, 64); return id }

func parseStrictForm(w http.ResponseWriter, r *http.Request, allowed []string) (url.Values, bool) {
	if r.URL.RawQuery != "" {
		http.Error(w, "unexpected query", http.StatusBadRequest)
		return nil, false
	}
	if !hasContentType(r, "application/x-www-form-urlencoded") {
		http.Error(w, "form content type required", http.StatusUnsupportedMediaType)
		return nil, false
	}
	if !readRequestAllowed(w, r) {
		return nil, false
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxRequestBody)
	if err := r.ParseForm(); err != nil {
		var limitErr *http.MaxBytesError
		if errors.As(err, &limitErr) {
			http.Error(w, "request body too large", http.StatusRequestEntityTooLarge)
		} else {
			http.Error(w, "invalid form", http.StatusBadRequest)
		}
		return nil, false
	}
	allow := make(map[string]struct{}, len(allowed))
	for _, key := range allowed {
		allow[key] = struct{}{}
	}
	for key, values := range r.PostForm {
		if _, ok := allow[key]; !ok || len(values) != 1 || !utf8.ValidString(values[0]) {
			http.Error(w, "invalid form", http.StatusBadRequest)
			return nil, false
		}
	}
	return r.PostForm, true
}

func mappingFromForm(values url.Values) (companion.Mapping, error) {
	for _, required := range []string{"csrf_token", "source_account", "book_id", "target_account", "work_id", "edition_mode", "edition_id"} {
		if _, ok := values[required]; !ok {
			return companion.Mapping{}, fmt.Errorf("missing form field")
		}
	}
	workID, err := strconv.ParseInt(values.Get("work_id"), 10, 64)
	if err != nil || workID < 1 {
		return companion.Mapping{}, fmt.Errorf("invalid work ID")
	}
	editionID := int64(0)
	switch values.Get("edition_mode") {
	case "work_only":
		if values.Get("edition_id") != "" {
			return companion.Mapping{}, fmt.Errorf("ambiguous edition choice")
		}
	case "edition":
		editionID, err = strconv.ParseInt(values.Get("edition_id"), 10, 64)
		if err != nil || editionID < 1 {
			return companion.Mapping{}, fmt.Errorf("invalid edition ID")
		}
	default:
		return companion.Mapping{}, fmt.Errorf("edition choice required")
	}
	return companion.Mapping{Provider: "weread", SourceAccount: values.Get("source_account"), BookID: values.Get("book_id"),
		TargetAccount: values.Get("target_account"), WorkID: workID, EditionID: editionID}, nil
}

func noBody(w http.ResponseWriter, r *http.Request, serve ...func()) bool {
	if r.Body != nil {
		limited := http.MaxBytesReader(w, r.Body, maxRequestBody)
		body, err := io.ReadAll(limited)
		if err != nil || len(body) != 0 {
			http.Error(w, "request body not allowed", http.StatusBadRequest)
			return false
		}
	}
	if len(serve) != 0 {
		serve[0]()
	}
	return true
}

func probesHandler(path string, readiness []probes.Readiness, logger *slog.Logger) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !readRequestAllowed(w, r) {
			return
		}
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			methodNotAllowed(w, "GET, HEAD")
			return
		}
		noBody(w, r, func() {
			w.Header().Set("Content-Type", "text/plain; charset=utf-8")
			if path == "/healthz" {
				w.WriteHeader(http.StatusOK)
				if r.Method == http.MethodGet {
					_, _ = io.WriteString(w, "ok\n")
				}
				return
			}
			for _, dependency := range readiness {
				if err := dependency.Check(r); err != nil {
					logger.WarnContext(r.Context(), "readiness check failed", slog.String("check", dependency.Name))
					http.Error(w, "not ready: "+dependency.Name, http.StatusServiceUnavailable)
					return
				}
			}
			w.WriteHeader(http.StatusOK)
			if r.Method == http.MethodGet {
				_, _ = io.WriteString(w, "ready\n")
			}
		})
	})
}

func hostAndBrowserBoundary(want string, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		setSecurityHeaders(w)
		if r.Host != want {
			http.Error(w, "request rejected", http.StatusMisdirectedRequest)
			return
		}
		if site := strings.ToLower(strings.TrimSpace(r.Header.Get("Sec-Fetch-Site"))); site != "" && site != "same-origin" && site != "none" {
			http.Error(w, "request rejected", http.StatusForbidden)
			return
		}
		origins := r.Header.Values("Origin")
		if len(origins) > 1 || (len(origins) == 1 && !validOrigin(origins[0], want)) {
			http.Error(w, "request rejected", http.StatusForbidden)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func readRequestAllowed(w http.ResponseWriter, r *http.Request) bool {
	if len(r.TransferEncoding) != 0 || r.ContentLength > maxRequestBody {
		http.Error(w, "request body not allowed", http.StatusRequestEntityTooLarge)
		return false
	}
	return true
}

func setSecurityHeaders(w http.ResponseWriter) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("X-Frame-Options", "DENY")
	w.Header().Set("Referrer-Policy", "strict-origin-when-cross-origin")
	w.Header().Set("Content-Security-Policy", "default-src 'none'; img-src 'self'; style-src 'unsafe-inline'; form-action 'self'; base-uri 'none'; frame-ancestors 'none'")
}

func pendingOptions(query url.Values, now time.Time) (companion.PendingOptions, error) {
	for key, values := range query {
		if key != "account" && key != "target_account" && key != "days" && key != "after" || len(values) != 1 {
			return companion.PendingOptions{}, fmt.Errorf("invalid query")
		}
	}
	account, target := query.Get("account"), query.Get("target_account")
	days := 90
	if raw := query.Get("days"); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil {
			return companion.PendingOptions{}, fmt.Errorf("invalid days")
		}
		days = parsed
	}
	if !companion.ValidLabel(account) || !companion.ValidLabel(target) || days < 1 || days > 36500 {
		return companion.PendingOptions{}, fmt.Errorf("invalid options")
	}
	options := companion.PendingOptions{SourceAccount: account, TargetAccount: target, Days: days, After: query.Get("after"), Now: now}
	if err := companion.ValidatePendingCursor(options); err != nil {
		return companion.PendingOptions{}, err
	}
	return options, nil
}

func writeJSON(w http.ResponseWriter, r *http.Request, value any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	if r.Method == http.MethodHead {
		w.WriteHeader(http.StatusOK)
		return
	}
	if err := json.NewEncoder(w).Encode(value); err != nil {
		return
	}
}

func methodNotAllowed(w http.ResponseWriter, allowed string) {
	w.Header().Set("Allow", allowed)
	http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
}

func (s *Server) Listen() (net.Listener, error) {
	listener, err := net.Listen("tcp", s.address)
	if err != nil {
		return nil, fmt.Errorf("server address unavailable")
	}
	return listener, nil
}

// Serve runs until the context is canceled and then gracefully drains requests.
func (s *Server) Serve(ctx context.Context, listener net.Listener) error {
	defer listener.Close()
	owner, err := companion.AcquireDatabaseOwnership(s.database)
	if err != nil {
		return err
	}
	s.ownership = owner
	defer owner.Close()
	store, err := companion.OpenOwnedStore(s.database, owner)
	if err != nil {
		return err
	}
	if err := store.Close(); err != nil {
		return fmt.Errorf("mapping database initialization failed")
	}
	host, port, err := net.SplitHostPort(s.address)
	if err != nil {
		return fmt.Errorf("invalid HTTP server address")
	}
	if port == "0" {
		if host == "" {
			host, _, _ = net.SplitHostPort(listener.Addr().String())
		}
		_, actualPort, _ := net.SplitHostPort(listener.Addr().String())
		port = actualPort
	}
	s.http.Handler = requestLoggingMiddleware(s.logger)(hostAndBrowserBoundary(net.JoinHostPort(host, port), s.http.Handler))
	s.http.BaseContext = func(net.Listener) context.Context { return ctx }
	errCh := make(chan error, 1)
	go func() { errCh <- s.http.Serve(listener) }()
	select {
	case err := <-errCh:
		if err == http.ErrServerClosed {
			return nil
		}
		return fmt.Errorf("HTTP server failed")
	case <-ctx.Done():
		shutdown, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := s.http.Shutdown(shutdown); err != nil {
			_ = s.http.Close()
			return fmt.Errorf("HTTP server shutdown failed")
		}
		return nil
	}
}
