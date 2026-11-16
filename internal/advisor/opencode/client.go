// Package opencode implements the advisor.Advisor interface against an
// OpenAI-compatible chat completions endpoint.
package opencode

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/azusachino/drifter/internal/advisor"
	"github.com/azusachino/drifter/internal/config"
	"github.com/openai/openai-go"
	"github.com/openai/openai-go/option"
	"github.com/openai/openai-go/shared"
)

const (
	// DefaultBaseURL is the standard fallback OpenAI-compatible gateway endpoint.
	DefaultBaseURL = "https://api.openai.com/v1"
	// DefaultModel is the default model identifier used for advisor calls.
	DefaultModel = "opencode-go/beam-free"
	// DefaultTimeout is the default per-request timeout.
	DefaultTimeout = 30 * time.Second
	// DefaultTimeoutEnv is consulted when no timeout is configured explicitly.
	DefaultTimeoutEnv = "DRIFTER_ADVISOR_TIMEOUT"
	// ResponseLimit caps the response body read from the completions endpoint (8 MiB).
	ResponseLimit = 8 << 20
	// maxErrorDetail caps how much upstream error text is echoed back.
	maxErrorDetail = 512
	// maxRetryAfter caps parsed Retry-After durations from HTTP 429 responses.
	maxRetryAfter = 5 * time.Hour
	// defaultModelCooldown is the initial per-model cooldown applied when no Retry-After header is present.
	defaultModelCooldown = 60 * time.Second
)

// modelCooldownSchedule defines the escalating per-model cooldown durations for
// consecutive 429/overload responses when no Retry-After header is provided.
var modelCooldownSchedule = []time.Duration{
	1 * time.Minute,
	5 * time.Minute,
	15 * time.Minute,
	1 * time.Hour,
	5 * time.Hour,
}

// Configuration errors. Callers should match these with errors.Is.
var (
	// ErrNotConfigured indicates no advisor API key was found in the environment.
	ErrNotConfigured = errors.New("advisor API key not configured")
	// ErrRateLimited indicates the advisor endpoint returned HTTP 429.
	ErrRateLimited = advisor.ErrRateLimited
	// ErrInvalidResponse indicates the advisor returned a body that is not valid JSON.
	ErrInvalidResponse = errors.New("advisor response is not valid JSON")
)

// Config holds the resolved advisor configuration.
type Config struct {
	BaseURL    string
	APIKey     string
	Model      string
	Models     []string
	Timeout    time.Duration
	HTTPClient *http.Client
}

// Client is an OpenAI-compatible advisor client.
type Client struct {
	baseURL         string
	apiKey          string
	model           string
	models          []string
	httpClient      *http.Client
	sdk             openai.Client
	mu              sync.Mutex
	cooldownUntil   map[string]time.Time
	cooldownStrikes map[string]int
	now             func() time.Time
}

var _ advisor.Advisor = (*Client)(nil)

// Settings holds optional advisor settings from a local configuration file.
// Credentials (API keys) never belong here.
type Settings struct {
	BaseURL string
	Model   string
	Models  []string
	Timeout time.Duration
}

// LoadConfig resolves advisor configuration from the environment and settings file.
//
// Endpoint precedence: DRIFTER_ADVISOR_URL, MAGPIE_BASE_URL, MAGPIE_URL, OPENCODE_BASE_URL, config advisor.base_url, then DefaultBaseURL.
// API key precedence: MAGPIE_GATEWAY_KEY, OPENCODE_API_KEY, DRIFTER_ADVISOR_KEY (strictly environment).
// Model precedence: DRIFTER_ADVISOR_MODELS, DRIFTER_ADVISOR_MODEL, config advisor.model, then DefaultModel.
// Timeout precedence: DRIFTER_ADVISOR_TIMEOUT (Go duration; a bare integer means seconds), config advisor.timeout, then DefaultTimeout.
func LoadConfig() (Config, error) {
	return LoadConfigPath(config.DefaultPath())
}

// LoadConfigPath resolves advisor configuration using settings loaded from path.
func LoadConfigPath(path string) (Config, error) {
	var s Settings
	if strings.TrimSpace(path) != "" {
		if fileCfg, err := config.Load(path); err == nil {
			s.BaseURL = fileCfg.Advisor.BaseURL
			s.Model = fileCfg.Advisor.Model
			if strings.TrimSpace(fileCfg.Advisor.Timeout) != "" {
				if d, err := time.ParseDuration(strings.TrimSpace(fileCfg.Advisor.Timeout)); err == nil {
					s.Timeout = d
				}
			}
		}
	}
	return LoadConfigWithSettings(s)
}

// LoadConfigWithSettings resolves advisor configuration using explicit settings fallback.
func LoadConfigWithSettings(s Settings) (Config, error) {
	baseURL := firstEnv("DRIFTER_ADVISOR_URL", "MAGPIE_BASE_URL", "MAGPIE_URL", "OPENCODE_BASE_URL")
	if baseURL == "" {
		baseURL = strings.TrimSpace(s.BaseURL)
	}
	if baseURL == "" {
		baseURL = DefaultBaseURL
	}
	apiKey := firstEnv("MAGPIE_GATEWAY_KEY", "OPENCODE_API_KEY", "DRIFTER_ADVISOR_KEY")
	if apiKey == "" {
		return Config{}, ErrNotConfigured
	}
	models := parseModelsList(
		firstEnv("DRIFTER_ADVISOR_MODELS"),
		firstEnv("DRIFTER_ADVISOR_MODEL"),
	)
	if len(models) == 0 {
		models = parseModelsList(strings.Join(s.Models, ","), s.Model)
	}
	if len(models) == 0 {
		models = []string{DefaultModel}
	}
	model := models[0]
	timeout := DefaultTimeout
	if raw := strings.TrimSpace(os.Getenv(DefaultTimeoutEnv)); raw != "" {
		d, err := parseTimeout(raw)
		if err != nil {
			return Config{}, fmt.Errorf("invalid %s value %q: want a Go duration such as 45s, or whole seconds", DefaultTimeoutEnv, raw)
		}
		timeout = d
	} else if s.Timeout > 0 {
		timeout = s.Timeout
	}
	return Config{
		BaseURL:    baseURL,
		APIKey:     apiKey,
		Model:      model,
		Models:     models,
		Timeout:    timeout,
		HTTPClient: &http.Client{Timeout: timeout},
	}, nil
}

// New constructs an advisor client from the environment. It returns
// ErrNotConfigured when no API key is available.
func New() (*Client, error) {
	cfg, err := LoadConfig()
	if err != nil {
		return nil, err
	}
	return NewWithConfig(cfg)
}

// NewWithConfig constructs an advisor client with an explicit configuration.
func NewWithConfig(cfg Config) (*Client, error) {
	apiKey := strings.TrimSpace(cfg.APIKey)
	if apiKey == "" {
		return nil, ErrNotConfigured
	}
	baseURL := strings.TrimRight(strings.TrimSpace(cfg.BaseURL), "/")
	if baseURL == "" {
		baseURL = DefaultBaseURL
	}
	models := parseModelsList(strings.Join(cfg.Models, ","), cfg.Model)
	if len(models) == 0 {
		models = []string{DefaultModel}
	}
	model := models[0]
	httpClient := cfg.HTTPClient
	if httpClient == nil {
		timeout := cfg.Timeout
		if timeout <= 0 {
			timeout = DefaultTimeout
		}
		httpClient = &http.Client{Timeout: timeout}
	}
	sdk := openai.NewClient(
		option.WithBaseURL(baseURL),
		option.WithAPIKey(apiKey),
		option.WithHTTPClient(httpClient),
		option.WithMaxRetries(0),
		option.WithMiddleware(responseMiddleware),
	)
	return &Client{
		baseURL:         baseURL,
		apiKey:          apiKey,
		model:           model,
		models:          models,
		httpClient:      httpClient,
		sdk:             sdk,
		cooldownUntil:   make(map[string]time.Time),
		cooldownStrikes: make(map[string]int),
		now:             time.Now,
	}, nil
}

// NewWithEndpoint constructs an advisor client pointed at a specific endpoint,
// useful for tests and private deployments.
func NewWithEndpoint(endpoint, apiKey string) (*Client, error) {
	return NewWithConfig(Config{BaseURL: endpoint, APIKey: apiKey, Model: DefaultModel, Timeout: DefaultTimeout})
}

// Endpoint returns the configured completions endpoint URL.
func (c *Client) Endpoint() string {
	return c.baseURL + "/chat/completions"
}

// Model returns the configured model identifier.
func (c *Client) Model() string {
	return c.model
}

// Models returns a copy of the configured model fallback chain.
func (c *Client) Models() []string {
	if c == nil {
		return nil
	}
	out := make([]string, len(c.models))
	copy(out, c.models)
	return out
}

func (c *Client) nowTime() time.Time {
	if c != nil && c.now != nil {
		return c.now()
	}
	return time.Now()
}

func (c *Client) modelCooldownRemaining(model string) (time.Duration, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.cooldownUntil == nil {
		return 0, false
	}
	until, ok := c.cooldownUntil[model]
	if !ok {
		return 0, false
	}
	now := c.nowTime()
	if now.Before(until) {
		return until.Sub(now), true
	}
	delete(c.cooldownUntil, model)
	return 0, false
}

func (c *Client) recordModelCooldown(model string, retryAfter time.Duration) time.Duration {
	now := c.nowTime()
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.cooldownUntil == nil {
		c.cooldownUntil = make(map[string]time.Time)
	}
	if c.cooldownStrikes == nil {
		c.cooldownStrikes = make(map[string]int)
	}
	strike := c.cooldownStrikes[model]
	c.cooldownStrikes[model] = strike + 1

	d := retryAfter
	if d <= 0 {
		if strike >= len(modelCooldownSchedule) {
			strike = len(modelCooldownSchedule) - 1
		}
		d = modelCooldownSchedule[strike]
	}
	c.cooldownUntil[model] = now.Add(d)
	return d
}

func (c *Client) resetModelCooldown(model string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.cooldownUntil, model)
	delete(c.cooldownStrikes, model)
}

// parseModelsList splits comma-separated model strings in priority order,
// deduplicating while preserving order.
func parseModelsList(rawList ...string) []string {
	var models []string
	seen := make(map[string]struct{})
	for _, raw := range rawList {
		for _, part := range strings.Split(raw, ",") {
			m := strings.TrimSpace(part)
			if m == "" {
				continue
			}
			if _, exists := seen[m]; exists {
				continue
			}
			seen[m] = struct{}{}
			models = append(models, m)
		}
	}
	return models
}

// ExplainConflict asks the advisor to explain a reading status divergence and
// recommend a safe resolution.
func (c *Client) ExplainConflict(ctx context.Context, req advisor.ConflictExplanationRequest) (advisor.ConflictExplanationResponse, error) {
	var response advisor.ConflictExplanationResponse
	err := c.complete(ctx, conflictSystemPrompt, conflictPrompt(req), &response)
	if err != nil {
		return advisor.ConflictExplanationResponse{}, err
	}
	if response.RecommendedAction == "" {
		return advisor.ConflictExplanationResponse{}, fmt.Errorf("%w: missing recommended_action", ErrInvalidResponse)
	}
	return response, nil
}

// ResolveOriginalWork asks the advisor to resolve the foreign original of a
// localized edition, abstaining when the work is native or evidence is thin.
func (c *Client) ResolveOriginalWork(ctx context.Context, req advisor.OriginalWorkRequest) (advisor.OriginalWorkResponse, error) {
	var response advisor.OriginalWorkResponse
	err := c.complete(ctx, originalWorkSystemPrompt, originalWorkPrompt(req), &response)
	if err != nil {
		return advisor.OriginalWorkResponse{}, err
	}
	return response, nil
}

var errResponseTooLarge = fmt.Errorf("advisor response exceeds %d bytes", ResponseLimit)

type limitedBody struct {
	rc    io.ReadCloser
	read  int64
	limit int64
}

func (b *limitedBody) Read(p []byte) (int, error) {
	n, err := b.rc.Read(p)
	b.read += int64(n)
	if b.read > b.limit {
		return n, errResponseTooLarge
	}
	return n, err
}

func (b *limitedBody) Close() error {
	return b.rc.Close()
}

func responseMiddleware(req *http.Request, next option.MiddlewareNext) (*http.Response, error) {
	res, err := next(req)
	if err != nil || res == nil {
		return res, err
	}
	if res.StatusCode < 400 && res.Header != nil {
		ct := res.Header.Get("Content-Type")
		if ct == "" || strings.HasPrefix(strings.ToLower(ct), "text/plain") {
			res.Header.Set("Content-Type", "application/json")
		}
	}
	if res.Body != nil {
		res.Body = &limitedBody{
			rc:    res.Body,
			limit: ResponseLimit,
		}
	}
	return res, nil
}

// complete sends a chat completion request and decodes the assistant message
// content into target as JSON, falling back across c.models when a model is
// rate-limited or transiently overloaded (HTTP 429, 502, 503, 529).
func (c *Client) complete(ctx context.Context, systemPrompt, userPrompt string, target any) error {
	if c == nil {
		return errors.New("advisor client is nil")
	}
	if strings.TrimSpace(c.apiKey) == "" {
		return ErrNotConfigured
	}
	if target == nil {
		return errors.New("advisor response target is nil")
	}

	models := c.models
	if len(models) == 0 {
		if strings.TrimSpace(c.model) != "" {
			models = []string{c.model}
		} else {
			models = []string{DefaultModel}
		}
	}

	var earliestRemaining time.Duration
	var lastDetail string

	updateEarliest := func(rem time.Duration) {
		if rem <= 0 {
			return
		}
		if earliestRemaining == 0 || rem < earliestRemaining {
			earliestRemaining = rem
		}
	}

	for _, model := range models {
		if rem, cooling := c.modelCooldownRemaining(model); cooling {
			updateEarliest(rem)
			continue
		}

		retryAfter, detail, rateLimited, err := c.completeWithModel(ctx, model, systemPrompt, userPrompt, target)
		if err == nil {
			return nil
		}
		if rateLimited {
			rem := c.recordModelCooldown(model, retryAfter)
			updateEarliest(rem)
			if detail != "" {
				lastDetail = detail
			}
			continue
		}
		return err
	}

	return &advisor.RateLimitError{
		RetryAfter: earliestRemaining,
		Detail:     lastDetail,
	}
}

func (c *Client) completeWithModel(ctx context.Context, model, systemPrompt, userPrompt string, target any) (retryAfter time.Duration, detail string, rateLimited bool, err error) {
	jsonFmt := shared.NewResponseFormatJSONObjectParam()
	params := openai.ChatCompletionNewParams{
		Model: shared.ChatModel(model),
		Messages: []openai.ChatCompletionMessageParamUnion{
			openai.SystemMessage(systemPrompt),
			openai.UserMessage(userPrompt),
		},
		ResponseFormat: openai.ChatCompletionNewParamsResponseFormatUnion{
			OfJSONObject: &jsonFmt,
		},
	}

	completion, err := c.sdk.Chat.Completions.New(ctx, params)
	if err != nil {
		var apiErr *openai.Error
		if errors.As(err, &apiErr) {
			msg := apiErrorDetail(apiErr)
			switch apiErr.StatusCode {
			case http.StatusTooManyRequests, http.StatusBadGateway, http.StatusServiceUnavailable, 529:
				var retryHeader string
				if apiErr.Response != nil {
					retryHeader = apiErr.Response.Header.Get("Retry-After")
				}
				parsed := parseRetryAfter(retryHeader, c.nowTime())
				return parsed, msg, true, ErrRateLimited
			default:
				if msg != "" {
					return 0, "", false, fmt.Errorf("advisor request failed: HTTP %d: %s", apiErr.StatusCode, msg)
				}
				return 0, "", false, fmt.Errorf("advisor request failed: HTTP %d", apiErr.StatusCode)
			}
		}
		if ctx.Err() != nil {
			return 0, "", false, ctx.Err()
		}
		if errors.Is(err, errResponseTooLarge) {
			return 0, "", false, errResponseTooLarge
		}
		if strings.Contains(err.Error(), "error parsing response json") {
			return 0, "", false, fmt.Errorf("%w: %v", ErrInvalidResponse, err)
		}
		return 0, "", false, fmt.Errorf("advisor transport failed: %w", err)
	}

	if rawJSON := completion.RawJSON(); rawJSON != "" {
		var envelope struct {
			Error *struct {
				Message string `json:"message"`
			} `json:"error"`
		}
		if err := json.Unmarshal([]byte(rawJSON), &envelope); err == nil && envelope.Error != nil {
			if message := sanitizeDetail([]byte(envelope.Error.Message)); message != "" {
				return 0, "", false, fmt.Errorf("advisor request rejected: %s", message)
			}
			return 0, "", false, errors.New("advisor request rejected")
		}
	}

	c.resetModelCooldown(model)

	if len(completion.Choices) == 0 {
		return 0, "", false, fmt.Errorf("%w: no choices returned", ErrInvalidResponse)
	}
	rawContent := strings.TrimSpace(completion.Choices[0].Message.Content)
	if rawContent == "" {
		return 0, "", false, fmt.Errorf("%w: empty message content", ErrInvalidResponse)
	}
	content := stripMarkdownFence([]byte(rawContent))
	if err := json.Unmarshal(content, target); err != nil {
		return 0, "", false, fmt.Errorf("%w: %v", ErrInvalidResponse, err)
	}
	return 0, "", false, nil
}

func apiErrorDetail(apiErr *openai.Error) string {
	if apiErr == nil {
		return ""
	}
	if msg := sanitizeDetail([]byte(apiErr.Message)); msg != "" {
		return msg
	}
	if apiErr.Response != nil && apiErr.Response.Body != nil {
		rawDetail, _ := io.ReadAll(io.LimitReader(apiErr.Response.Body, maxErrorDetail))
		return sanitizeDetail(rawDetail)
	}
	return ""
}

// stripMarkdownFence removes a leading and trailing markdown code fence that
// some models wrap around JSON output (```json ... ``` or ``` ... ```), as
// well as trailing chatml stop tokens like <|im_end|>.
func stripMarkdownFence(content []byte) []byte {
	const fence = "```"
	trimmed := bytes.TrimSpace(content)
	if bytes.HasPrefix(trimmed, []byte(fence)) {
		rest := trimmed[len(fence):]
		// Drop the opening fence line, including an optional language tag.
		if newline := bytes.IndexByte(rest, '\n'); newline >= 0 {
			rest = rest[newline+1:]
		} else {
			// A single-line fence keeps the language tag on the same line:
			// ```json {"a":1}```. Drop the tag before the JSON payload.
			rest = bytes.TrimLeft(rest, " \t")
			if space := bytes.IndexAny(rest, " \t"); space >= 0 && !bytes.HasPrefix(rest, []byte("{")) {
				rest = bytes.TrimLeft(rest[space:], " \t")
			}
		}
		// Drop the closing fence if present.
		if idx := bytes.LastIndex(rest, []byte(fence)); idx >= 0 {
			rest = rest[:idx]
		}
		trimmed = bytes.TrimSpace(rest)
	}
	if start := bytes.IndexByte(trimmed, '{'); start >= 0 {
		if end := bytes.LastIndexByte(trimmed, '}'); end > start {
			if suffix := bytes.TrimSpace(trimmed[end+1:]); bytes.HasPrefix(suffix, []byte("<|")) {
				trimmed = bytes.TrimSpace(trimmed[start : end+1])
			}
		}
	}
	return trimmed
}

// sanitizeDetail trims an upstream error detail and removes control characters
// so it cannot corrupt logs or headers.
func sanitizeDetail(detail []byte) string {
	s := strings.TrimSpace(string(detail))
	s = strings.Map(func(r rune) rune {
		if r == '\n' || r == '\r' || r == '\t' {
			return ' '
		}
		if r < 0x20 || r == 0x7f {
			return -1
		}
		return r
	}, s)
	if len(s) > maxErrorDetail {
		cut := maxErrorDetail
		for cut > 0 && !utf8.RuneStart(s[cut]) {
			cut--
		}
		s = s[:cut] + "…"
	}
	return s
}

// firstEnv returns the first non-empty, non-whitespace environment value.
func firstEnv(keys ...string) string {
	for _, key := range keys {
		if value := strings.TrimSpace(os.Getenv(key)); value != "" {
			return value
		}
	}
	return ""
}

// parseTimeout accepts a Go duration ("45s", "1m"). A bare integer is read as
// seconds, which is what the variable accepted before durations were allowed.
func parseTimeout(raw string) (time.Duration, error) {
	d, err := time.ParseDuration(raw)
	if err != nil {
		seconds, convErr := strconv.Atoi(raw)
		if convErr != nil {
			return 0, err
		}
		d = time.Duration(seconds) * time.Second
	}
	if d <= 0 {
		return 0, errors.New("timeout must be positive")
	}
	return d, nil
}

// parseRetryAfter parses an HTTP Retry-After header (either integer seconds or
// an HTTP-date) and caps the returned duration to maxRetryAfter.
func parseRetryAfter(raw string, now time.Time) time.Duration {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return 0
	}
	if secs, err := strconv.ParseInt(trimmed, 10, 64); err == nil {
		if secs <= 0 {
			return 0
		}
		d := time.Duration(secs) * time.Second
		if d > maxRetryAfter {
			return maxRetryAfter
		}
		return d
	}
	if retryAt, err := http.ParseTime(trimmed); err == nil {
		d := retryAt.Sub(now)
		if d <= 0 {
			return 0
		}
		if d > maxRetryAfter {
			return maxRetryAfter
		}
		return d
	}
	return 0
}
