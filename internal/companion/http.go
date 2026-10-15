package companion

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

const responseLimit = 2 << 20

type api struct {
	name, url, token string
	client           *http.Client
	interval         time.Duration
	gate             chan struct{}
	next             time.Time
}

func newAPI(name, url, token string, interval time.Duration) (*api, error) {
	token = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(token), "Bearer "))
	if token == "" || token == "Bearer" || strings.ContainsAny(token, "\r\n") {
		return nil, fmt.Errorf("%s credential missing or invalid", name)
	}
	gate := make(chan struct{}, 1)
	gate <- struct{}{}
	return &api{name: name, url: url, token: token, interval: interval, gate: gate,
		client: &http.Client{Timeout: 30 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}, nil
}

func (a *api) post(ctx context.Context, payload any, target any) error {
	body, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("%s request encoding failed", a.name)
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-a.gate:
	}
	defer func() { a.gate <- struct{}{} }()
	if wait := time.Until(a.next); wait > 0 {
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
	a.next = time.Now().Add(a.interval)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, a.url, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("%s request creation failed", a.name)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+a.token)
	response, err := a.client.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return fmt.Errorf("%s transport failed", a.name)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("%s HTTP %d (no automatic retry)", a.name, response.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(response.Body, responseLimit+1))
	if err != nil || len(data) > responseLimit {
		return fmt.Errorf("%s response unreadable or too large", a.name)
	}
	if err := json.Unmarshal(data, target); err != nil {
		return fmt.Errorf("%s response is not valid JSON for this operation", a.name)
	}
	return nil
}
