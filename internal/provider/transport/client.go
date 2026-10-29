// Package transport provides authenticated, rate-limited HTTP transport
// for provider clients (WeRead and Hardcover).
package transport

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// ResponseLimit is the maximum body size read from a provider response (2 MiB).
const ResponseLimit = 2 << 20

// Client manages rate-limited, token-authenticated requests to a fixed provider endpoint.
type Client struct {
	name, url, token string
	httpClient       *http.Client
	interval         time.Duration
	gate             chan struct{}
	next             time.Time
}

// NewClient creates an authenticated rate-limited client for endpoint url.
func NewClient(name, url, token string, interval time.Duration) (*Client, error) {
	return NewClientWithHTTP(name, url, token, interval, &http.Client{
		Timeout:       30 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	})
}

// NewClientWithHTTP creates a client with a custom underlying http.Client (useful for testing).
func NewClientWithHTTP(name, url, token string, interval time.Duration, httpClient *http.Client) (*Client, error) {
	token = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(token), "Bearer "))
	if token == "" || token == "Bearer" || strings.ContainsAny(token, "\r\n") {
		return nil, fmt.Errorf("%s credential missing or invalid", name)
	}
	gate := make(chan struct{}, 1)
	gate <- struct{}{}
	return &Client{
		name:       name,
		url:        url,
		token:      token,
		interval:   interval,
		gate:       gate,
		httpClient: httpClient,
	}, nil
}

// Post marshals payload as JSON, applies pacing, and unmarshals the response into target.
func (c *Client) Post(ctx context.Context, payload any, target any) error {
	body, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("%s request encoding failed", c.name)
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-c.gate:
	}
	defer func() { c.gate <- struct{}{} }()

	if wait := time.Until(c.next); wait > 0 {
		timer := time.NewTimer(wait)
		defer timer.Stop()
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-timer.C:
		}
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	c.next = time.Now().Add(c.interval)

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.url, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("%s request creation failed", c.name)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+c.token)

	response, err := c.httpClient.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return fmt.Errorf("%s transport failed", c.name)
	}
	defer response.Body.Close()

	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("%s HTTP %d (no automatic retry)", c.name, response.StatusCode)
	}

	data, err := io.ReadAll(io.LimitReader(response.Body, ResponseLimit+1))
	if err != nil || len(data) > ResponseLimit {
		return fmt.Errorf("%s response unreadable or too large", c.name)
	}
	if err := json.Unmarshal(data, target); err != nil {
		return fmt.Errorf("%s response is not valid JSON for this operation", c.name)
	}
	return nil
}
