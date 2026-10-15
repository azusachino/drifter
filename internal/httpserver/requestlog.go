package httpserver

import (
	"context"
	"log/slog"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/httplog/v3"
)

const unmatchedRoute = "unmatched"

var safeRoutes = map[string]struct{}{
	"/": {}, "/healthz": {}, "/readyz": {},
	"/api/csrf": {}, "/api/mappings": {}, "/api/pending": {},
	"/api/mappings/confirm": {}, "/api/mappings/replace": {}, "/api/mappings/unlink": {},
	"/mappings/confirm": {}, "/mappings/replace": {}, "/mappings/unlink": {},
}

func requestRouteLogAttribute(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		next.ServeHTTP(w, r)
		route := chi.RouteContext(r.Context()).RoutePattern()
		if _, ok := safeRoutes[route]; !ok {
			route = unmatchedRoute
		}
		httplog.SetAttrs(r.Context(), slog.String("http.route", route))
	})
}

// requestLoggingMiddleware keeps request timing and panic recovery while
// restricting emitted records to operationally useful, non-sensitive fields.
func requestLoggingMiddleware(logger *slog.Logger) func(http.Handler) http.Handler {
	schema := *httplog.SchemaECS
	schema.RequestURL = ""
	schema.RequestPath = ""
	schema.RequestRemoteIP = ""
	schema.RequestHost = ""
	schema.RequestScheme = ""
	schema.RequestProto = ""
	schema.RequestHeaders = ""
	schema.RequestBody = ""
	schema.RequestBytes = ""
	schema.RequestBytesUnread = ""
	schema.RequestUserAgent = ""
	schema.RequestReferer = ""
	schema.ResponseHeaders = ""
	schema.ResponseBody = ""
	schema.ResponseBytes = ""
	schema.ErrorMessage = ""
	schema.ErrorType = ""
	schema.ErrorStackTrace = ""

	requestLogger := slog.New(requestLogHandler{next: logger.Handler()})
	return httplog.RequestLogger(requestLogger, &httplog.Options{
		Level:              slog.LevelInfo,
		Schema:             &schema,
		RecoverPanics:      true,
		LogRequestHeaders:  []string{},
		LogResponseHeaders: []string{},
	})
}

// requestLogHandler replaces httplog's message and filters attributes before
// the request record reaches the configured process logger.
type requestLogHandler struct {
	next slog.Handler
}

func (handler requestLogHandler) Enabled(ctx context.Context, level slog.Level) bool {
	return handler.next.Enabled(ctx, level)
}

func (handler requestLogHandler) Handle(ctx context.Context, record slog.Record) error {
	filtered := make([]slog.Attr, 0, record.NumAttrs())
	record.Attrs(func(attr slog.Attr) bool {
		if safe, ok := safeRequestLogAttribute(attr); ok {
			filtered = append(filtered, safe)
		}
		return true
	})
	clean := slog.NewRecord(record.Time, record.Level, "HTTP request", 0)
	clean.AddAttrs(filtered...)
	return handler.next.Handle(ctx, clean)
}

func (handler requestLogHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	filtered := make([]slog.Attr, 0, len(attrs))
	for _, attr := range attrs {
		if safe, ok := safeRequestLogAttribute(attr); ok {
			filtered = append(filtered, safe)
		}
	}
	return requestLogHandler{next: handler.next.WithAttrs(filtered)}
}

func (handler requestLogHandler) WithGroup(name string) slog.Handler {
	return requestLogHandler{next: handler.next.WithGroup(name)}
}

func safeRequestLogAttribute(attr slog.Attr) (slog.Attr, bool) {
	switch attr.Key {
	case "http.request.method":
		if attr.Value.Kind() != slog.KindString {
			return slog.Attr{}, false
		}
		switch attr.Value.String() {
		case http.MethodGet, http.MethodHead, http.MethodPost, http.MethodDelete:
			return attr, true
		default:
			return slog.String(attr.Key, "OTHER"), true
		}
	case "http.route":
		if attr.Value.Kind() != slog.KindString {
			return slog.Attr{}, false
		}
		route := attr.Value.String()
		if _, ok := safeRoutes[route]; !ok {
			route = unmatchedRoute
		}
		return slog.String(attr.Key, route), true
	case "http.response.status_code":
		if attr.Value.Kind() != slog.KindInt64 {
			return slog.Attr{}, false
		}
		status := attr.Value.Int64()
		if status < 100 || status > 599 {
			return slog.Attr{}, false
		}
		return attr, true
	case "event.duration":
		if attr.Value.Kind() != slog.KindInt64 || attr.Value.Int64() < 0 {
			return slog.Attr{}, false
		}
		return slog.Int64(attr.Key, attr.Value.Int64()), true
	default:
		return slog.Attr{}, false
	}
}

var _ slog.Handler = requestLogHandler{}
