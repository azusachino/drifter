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
	"time"
	"unicode/utf8"

	"github.com/azusachino/drifter/internal/advisor"
)

const (
	// DefaultBaseURL is the production OpenAI-compatible gateway endpoint.
	DefaultBaseURL = "https://magpie.h.azusachino.com/v1"
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
)

// Configuration errors. Callers should match these with errors.Is.
var (
	// ErrNotConfigured indicates no advisor API key was found in the environment.
	ErrNotConfigured = errors.New("advisor API key not configured")
	// ErrRateLimited indicates the advisor endpoint returned HTTP 429.
	ErrRateLimited = errors.New("advisor endpoint rate limited")
	// ErrInvalidResponse indicates the advisor returned a body that is not valid JSON.
	ErrInvalidResponse = errors.New("advisor response is not valid JSON")
)

// Config holds the resolved advisor configuration.
type Config struct {
	BaseURL    string
	APIKey     string
	Model      string
	Timeout    time.Duration
	HTTPClient *http.Client
}

// Client is an OpenAI-compatible advisor client.
type Client struct {
	baseURL    string
	apiKey     string
	model      string
	httpClient *http.Client
}

var _ advisor.Advisor = (*Client)(nil)

// LoadConfig resolves advisor configuration from the environment.
//
// Endpoint precedence: DRIFTER_ADVISOR_URL, MAGPIE_BASE_URL, MAGPIE_URL, OPENCODE_BASE_URL, then DefaultBaseURL.
// API key precedence: MAGPIE_GATEWAY_KEY, OPENCODE_API_KEY, DRIFTER_ADVISOR_KEY.
// Model precedence: DRIFTER_ADVISOR_MODEL, then DefaultModel.
// Timeout precedence: DRIFTER_ADVISOR_TIMEOUT, then DefaultTimeout.
func LoadConfig() (Config, error) {
	baseURL := firstEnv("DRIFTER_ADVISOR_URL", "MAGPIE_BASE_URL", "MAGPIE_URL", "OPENCODE_BASE_URL")
	if baseURL == "" {
		baseURL = DefaultBaseURL
	}
	apiKey := firstEnv("MAGPIE_GATEWAY_KEY", "OPENCODE_API_KEY", "DRIFTER_ADVISOR_KEY")
	if apiKey == "" {
		return Config{}, ErrNotConfigured
	}
	model := firstEnv("DRIFTER_ADVISOR_MODEL")
	if model == "" {
		model = DefaultModel
	}
	timeout := DefaultTimeout
	if raw := strings.TrimSpace(os.Getenv(DefaultTimeoutEnv)); raw != "" {
		seconds, err := strconv.Atoi(raw)
		if err != nil || seconds <= 0 {
			return Config{}, fmt.Errorf("invalid %s value %q", DefaultTimeoutEnv, raw)
		}
		timeout = time.Duration(seconds) * time.Second
	}
	return Config{
		BaseURL:    baseURL,
		APIKey:     apiKey,
		Model:      model,
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
	model := strings.TrimSpace(cfg.Model)
	if model == "" {
		model = DefaultModel
	}
	httpClient := cfg.HTTPClient
	if httpClient == nil {
		timeout := cfg.Timeout
		if timeout <= 0 {
			timeout = DefaultTimeout
		}
		httpClient = &http.Client{Timeout: timeout}
	}
	return &Client{
		baseURL:    baseURL,
		apiKey:     apiKey,
		model:      model,
		httpClient: httpClient,
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

// ExplainConflict asks the advisor to explain a reading status divergence and
// recommend a safe resolution.
func (c *Client) ExplainConflict(ctx context.Context, req advisor.ConflictExplanationRequest) (advisor.ConflictExplanationResponse, error) {
	var response advisor.ConflictExplanationResponse
	err := c.complete(ctx, conflictSystemPrompt, conflictPrompt(req), &response)
	if err != nil {
		return advisor.ConflictExplanationResponse{}, err
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

// chatMessage is one message in a chat completion request.
type chatMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

// responseFormat requests a specific assistant output shape.
type responseFormat struct {
	Type string `json:"type"`
}

// chatRequest is the OpenAI-compatible chat completions request body.
type chatRequest struct {
	Model          string         `json:"model"`
	Messages       []chatMessage  `json:"messages"`
	ResponseFormat responseFormat `json:"response_format"`
}

// chatResponse is the OpenAI-compatible chat completions response body.
type chatResponse struct {
	Choices []struct {
		Message struct {
			Content string `json:"content"`
		} `json:"message"`
	} `json:"choices"`
	Error *struct {
		Message string `json:"message"`
		Type    string `json:"type"`
	} `json:"error"`
}

// complete sends a chat completion request and decodes the assistant message
// content into target as JSON.
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

	payload := chatRequest{
		Model: c.model,
		Messages: []chatMessage{
			{Role: "system", Content: systemPrompt},
			{Role: "user", Content: userPrompt},
		},
		ResponseFormat: responseFormat{Type: "json_object"},
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("advisor request encoding failed: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.Endpoint(), bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("advisor request creation failed: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Authorization", "Bearer "+c.apiKey)

	response, err := c.httpClient.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return fmt.Errorf("advisor transport failed: %w", err)
	}
	defer response.Body.Close()

	switch response.StatusCode {
	case http.StatusTooManyRequests:
		// Read a bounded slice so the failure message can be useful without
		// reflecting an unbounded upstream body.
		detail, _ := io.ReadAll(io.LimitReader(response.Body, maxErrorDetail))
		if message := sanitizeDetail(detail); message != "" {
			return fmt.Errorf("%w: HTTP 429: %s", ErrRateLimited, message)
		}
		return fmt.Errorf("%w: HTTP 429", ErrRateLimited)
	case http.StatusOK:
	default:
		detail, _ := io.ReadAll(io.LimitReader(response.Body, maxErrorDetail))
		if message := sanitizeDetail(detail); message != "" {
			return fmt.Errorf("advisor request failed: HTTP %d: %s", response.StatusCode, message)
		}
		return fmt.Errorf("advisor request failed: HTTP %d", response.StatusCode)
	}

	data, err := io.ReadAll(io.LimitReader(response.Body, ResponseLimit+1))
	if err != nil {
		return fmt.Errorf("advisor response unreadable: %w", err)
	}
	if len(data) > ResponseLimit {
		return fmt.Errorf("advisor response exceeds %d bytes", ResponseLimit)
	}

	var envelope chatResponse
	if err := json.Unmarshal(data, &envelope); err != nil {
		return fmt.Errorf("%w: %v", ErrInvalidResponse, err)
	}
	if envelope.Error != nil {
		if message := sanitizeDetail([]byte(envelope.Error.Message)); message != "" {
			return fmt.Errorf("advisor request rejected: %s", message)
		}
		return errors.New("advisor request rejected")
	}
	if len(envelope.Choices) == 0 {
		return fmt.Errorf("%w: no choices returned", ErrInvalidResponse)
	}
	rawContent := strings.TrimSpace(envelope.Choices[0].Message.Content)
	if rawContent == "" {
		return fmt.Errorf("%w: empty message content", ErrInvalidResponse)
	}
	content := stripMarkdownFence([]byte(rawContent))
	if err := json.Unmarshal(content, target); err != nil {
		return fmt.Errorf("%w: %v", ErrInvalidResponse, err)
	}
	return nil
}

// stripMarkdownFence removes a leading and trailing markdown code fence that
// some models wrap around JSON output (```json ... ``` or ``` ... ```).
func stripMarkdownFence(content []byte) []byte {
	const fence = "```"
	trimmed := bytes.TrimSpace(content)
	if !bytes.HasPrefix(trimmed, []byte(fence)) {
		return trimmed
	}
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
	return bytes.TrimSpace(rest)
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
