package opencode

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/azusachino/drifter/internal/advisor"
)

// testServer spins up an httptest server that responds with the given handler
// and returns a client pointed at it.
func testServer(t *testing.T, handler http.HandlerFunc) (*Client, *httptest.Server) {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	client, err := NewWithEndpoint(server.URL+"/v1", "test-api-key")
	if err != nil {
		t.Fatal(err)
	}
	return client, server
}

func TestExplainConflictSuccess(t *testing.T) {
	client, server := testServer(t, func(rw http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/chat/completions" {
			t.Errorf("unexpected path %s", r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer test-api-key" {
			t.Errorf("Authorization = %q", got)
		}
		var body chatRequest
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("decode: %v", err)
		}
		if body.Model != DefaultModel {
			t.Errorf("model = %q, want %q", body.Model, DefaultModel)
		}
		if body.ResponseFormat == nil || body.ResponseFormat.Type != "json_object" {
			t.Errorf("response_format = %+v, want json_object", body.ResponseFormat)
		}
		if len(body.Messages) != 2 || body.Messages[0].Role != "system" || body.Messages[1].Role != "user" {
			t.Errorf("messages = %+v", body.Messages)
		}
		io.WriteString(rw, `{"choices":[{"message":{"content":"{\"summary\":\"Source finished the book after target marked it read.\",\"recommended_action\":\"adopt_source\",\"rationale\":\"The source recorded a later finished timestamp.\"}"}}]}`)
	})

	resp, err := client.ExplainConflict(context.Background(), advisor.ConflictExplanationRequest{
		SourceTitle:    "克拉拉与太阳",
		SourceAuthor:   "石黑一雄",
		SourceStatus:   "read",
		TargetStatus:   "reading",
		BaselineStatus: "reading",
		Reason:         "source reported finished",
		Message:        "progress 100%",
		ObservedAt:     time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC),
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.Summary == "" || resp.RecommendedAction != "adopt_source" || resp.Rationale == "" {
		t.Fatalf("unexpected response: %+v", resp)
	}
	_ = server
}

func TestExplainConflictMalformedJSON(t *testing.T) {
	client, _ := testServer(t, func(rw http.ResponseWriter, r *http.Request) {
		io.WriteString(rw, `{"choices":[{"message":{"content":"not json at all"}}]}`)
	})
	_, err := client.ExplainConflict(context.Background(), advisor.ConflictExplanationRequest{SourceStatus: "read", TargetStatus: "reading"})
	if err == nil {
		t.Fatal("expected error for malformed JSON")
	}
	if !errors.Is(err, ErrInvalidResponse) {
		t.Fatalf("error = %v, want ErrInvalidResponse", err)
	}
}

func TestExplainConflictMarkdownWrappedJSON(t *testing.T) {
	client, _ := testServer(t, func(rw http.ResponseWriter, r *http.Request) {
		payload := map[string]any{
			"choices": []map[string]any{
				{
					"message": map[string]any{
						"content": "```json\n{\"summary\":\"target is ahead\",\"recommended_action\":\"adopt_target\",\"rationale\":\"target has a later read timestamp\"}\n```",
					},
				},
			},
		}
		_ = json.NewEncoder(rw).Encode(payload)
	})
	resp, err := client.ExplainConflict(context.Background(), advisor.ConflictExplanationRequest{SourceStatus: "reading", TargetStatus: "read"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.RecommendedAction != "adopt_target" {
		t.Fatalf("recommended_action = %q", resp.RecommendedAction)
	}
}

func TestExplainConflictRateLimited(t *testing.T) {
	client, _ := testServer(t, func(rw http.ResponseWriter, r *http.Request) {
		rw.WriteHeader(http.StatusTooManyRequests)
		io.WriteString(rw, "slow down")
	})
	_, err := client.ExplainConflict(context.Background(), advisor.ConflictExplanationRequest{SourceStatus: "read", TargetStatus: "reading"})
	if !errors.Is(err, ErrRateLimited) {
		t.Fatalf("error = %v, want ErrRateLimited", err)
	}
}

func TestExplainConflictServerError(t *testing.T) {
	client, _ := testServer(t, func(rw http.ResponseWriter, r *http.Request) {
		rw.WriteHeader(http.StatusInternalServerError)
		io.WriteString(rw, "internal error")
	})
	_, err := client.ExplainConflict(context.Background(), advisor.ConflictExplanationRequest{SourceStatus: "read", TargetStatus: "reading"})
	if err == nil {
		t.Fatal("expected error for HTTP 500")
	}
	if errors.Is(err, ErrRateLimited) {
		t.Fatalf("500 should not be ErrRateLimited: %v", err)
	}
}

func TestExplainConflictMissingKey(t *testing.T) {
	t.Setenv("MAGPIE_GATEWAY_KEY", "")
	t.Setenv("OPENCODE_API_KEY", "")
	t.Setenv("DRIFTER_ADVISOR_KEY", "")

	_, err := New()
	if !errors.Is(err, ErrNotConfigured) {
		t.Fatalf("error = %v, want ErrNotConfigured", err)
	}

	client, err := NewWithConfig(Config{BaseURL: "http://127.0.0.1:1/v1", APIKey: " "})
	if !errors.Is(err, ErrNotConfigured) {
		t.Fatalf("error = %v, want ErrNotConfigured for blank key", err)
	}
	if client != nil {
		t.Fatal("expected nil client for blank key")
	}
}

func TestResolveOriginalWorkTranslation(t *testing.T) {
	client, _ := testServer(t, func(rw http.ResponseWriter, r *http.Request) {
		io.WriteString(rw, `{"choices":[{"message":{"content":"{\"original_title\":\"Klara and the Sun\",\"original_author\":\"Kazuo Ishiguro\",\"language\":\"English\",\"confidence\":0.95,\"abstained\":false,\"rationale\":\"Translator and catalog field agree on the English original.\"}"}}]}`)
	})
	resp, err := client.ResolveOriginalWork(context.Background(), advisor.OriginalWorkRequest{
		Title:               "克拉拉与太阳",
		Author:              "石黑一雄",
		Translator:          "宋佥",
		DoubanOriginalTitle: "Klara and the Sun",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.OriginalTitle != "Klara and the Sun" || resp.OriginalAuthor != "Kazuo Ishiguro" {
		t.Fatalf("unexpected response: %+v", resp)
	}
	if resp.Abstained {
		t.Fatalf("should not abstain: %+v", resp)
	}
	if resp.Confidence < 0 || resp.Confidence > 1 {
		t.Fatalf("confidence out of range: %v", resp.Confidence)
	}
}

func TestResolveOriginalWorkAbstainsOnDomesticBook(t *testing.T) {
	client, _ := testServer(t, func(rw http.ResponseWriter, r *http.Request) {
		io.WriteString(rw, `{"choices":[{"message":{"content":"{\"confidence\":0.0,\"abstained\":true,\"rationale\":\"Domestic technical manual with no translator or foreign original.\"}"}}]}`)
	})
	resp, err := client.ResolveOriginalWork(context.Background(), advisor.OriginalWorkRequest{
		Title:  "Go 程序设计语言",
		Author: "张三",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !resp.Abstained {
		t.Fatalf("expected abstention: %+v", resp)
	}
	if resp.Confidence != 0 {
		t.Fatalf("confidence = %v, want 0", resp.Confidence)
	}
}

func TestResolveOriginalWorkMalformedJSON(t *testing.T) {
	client, _ := testServer(t, func(rw http.ResponseWriter, r *http.Request) {
		io.WriteString(rw, `{"choices":[{"message":{"content":"{bad json"}}]}`)
	})
	_, err := client.ResolveOriginalWork(context.Background(), advisor.OriginalWorkRequest{Title: "x", Author: "y"})
	if !errors.Is(err, ErrInvalidResponse) {
		t.Fatalf("error = %v, want ErrInvalidResponse", err)
	}
}

func TestResolveOriginalWorkEmptyChoices(t *testing.T) {
	client, _ := testServer(t, func(rw http.ResponseWriter, r *http.Request) {
		io.WriteString(rw, `{"choices":[]}`)
	})
	_, err := client.ResolveOriginalWork(context.Background(), advisor.OriginalWorkRequest{Title: "x", Author: "y"})
	if err == nil {
		t.Fatal("expected error for empty choices")
	}
}

func TestResolveOriginalWorkUpstreamError(t *testing.T) {
	client, _ := testServer(t, func(rw http.ResponseWriter, r *http.Request) {
		io.WriteString(rw, `{"error":{"message":"model not found","type":"invalid_request_error"}}`)
	})
	_, err := client.ResolveOriginalWork(context.Background(), advisor.OriginalWorkRequest{Title: "x", Author: "y"})
	if err == nil || !strings.Contains(err.Error(), "model not found") {
		t.Fatalf("error = %v, want upstream error detail", err)
	}
}

func TestLoadConfigPrecedence(t *testing.T) {
	t.Run("defaults", func(t *testing.T) {
		for _, key := range []string{"DRIFTER_ADVISOR_URL", "OPENCODE_BASE_URL", "MAGPIE_GATEWAY_KEY", "OPENCODE_API_KEY", "DRIFTER_ADVISOR_KEY", "DRIFTER_ADVISOR_MODEL", DefaultTimeoutEnv} {
			t.Setenv(key, "")
		}
		t.Setenv("MAGPIE_GATEWAY_KEY", "k")
		cfg, err := LoadConfig()
		if err != nil {
			t.Fatal(err)
		}
		if cfg.BaseURL != DefaultBaseURL {
			t.Errorf("baseURL = %q, want %q", cfg.BaseURL, DefaultBaseURL)
		}
		if cfg.Model != DefaultModel {
			t.Errorf("model = %q, want %q", cfg.Model, DefaultModel)
		}
		if cfg.Timeout != DefaultTimeout {
			t.Errorf("timeout = %v, want %v", cfg.Timeout, DefaultTimeout)
		}
	})

	t.Run("drifter url wins", func(t *testing.T) {
		t.Setenv("DRIFTER_ADVISOR_URL", "https://a.example/v1")
		t.Setenv("OPENCODE_BASE_URL", "https://b.example/v1")
		t.Setenv("MAGPIE_GATEWAY_KEY", "k")
		cfg, err := LoadConfig()
		if err != nil {
			t.Fatal(err)
		}
		if cfg.BaseURL != "https://a.example/v1" {
			t.Errorf("baseURL = %q", cfg.BaseURL)
		}
	})

	t.Run("api key precedence", func(t *testing.T) {
		t.Setenv("MAGPIE_GATEWAY_KEY", "")
		t.Setenv("OPENCODE_API_KEY", "oc-key")
		t.Setenv("DRIFTER_ADVISOR_KEY", "dr-key")
		cfg, err := LoadConfig()
		if err != nil {
			t.Fatal(err)
		}
		if cfg.APIKey != "oc-key" {
			t.Errorf("apiKey = %q", cfg.APIKey)
		}
	})

	t.Run("model override", func(t *testing.T) {
		t.Setenv("MAGPIE_GATEWAY_KEY", "k")
		t.Setenv("DRIFTER_ADVISOR_MODEL", "custom/model")
		cfg, err := LoadConfig()
		if err != nil {
			t.Fatal(err)
		}
		if cfg.Model != "custom/model" {
			t.Errorf("model = %q", cfg.Model)
		}
	})

	t.Run("timeout override", func(t *testing.T) {
		t.Setenv("MAGPIE_GATEWAY_KEY", "k")
		t.Setenv(DefaultTimeoutEnv, "5")
		cfg, err := LoadConfig()
		if err != nil {
			t.Fatal(err)
		}
		if cfg.Timeout != 5*time.Second {
			t.Errorf("timeout = %v", cfg.Timeout)
		}
	})

	t.Run("invalid timeout", func(t *testing.T) {
		t.Setenv("MAGPIE_GATEWAY_KEY", "k")
		t.Setenv(DefaultTimeoutEnv, "not-a-number")
		if _, err := LoadConfig(); err == nil {
			t.Fatal("expected invalid timeout error")
		}
	})
}

func TestClientImplementsAdvisor(t *testing.T) {
	var _ advisor.Advisor = (*Client)(nil)
}

func TestStripMarkdownFence(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"plain", `{"a":1}`, `{"a":1}`},
		{"json fence", "```json\n{\"a\":1}\n```", `{"a":1}`},
		{"plain fence", "```\n{\"a\":1}\n```", `{"a":1}`},
		{"no closing fence", "```json\n{\"a\":1}", `{"a":1}`},
		{"empty", "", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := string(stripMarkdownFence([]byte(tc.in)))
			if got != tc.want {
				t.Errorf("stripMarkdownFence(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

func TestContextCancellation(t *testing.T) {
	started := make(chan struct{})
	client, _ := testServer(t, func(rw http.ResponseWriter, r *http.Request) {
		close(started)
		select {
		case <-r.Context().Done():
		case <-time.After(500 * time.Millisecond):
		}
	})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		_, err := client.ExplainConflict(ctx, advisor.ConflictExplanationRequest{SourceStatus: "read", TargetStatus: "reading"})
		done <- err
	}()
	<-started
	cancel()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("expected cancellation error")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("request did not honor cancellation")
	}
}

func TestRequestCount(t *testing.T) {
	var count atomic.Int32
	client, _ := testServer(t, func(rw http.ResponseWriter, r *http.Request) {
		count.Add(1)
		io.WriteString(rw, `{"choices":[{"message":{"content":"{\"recommended_action\":\"keep_separate\"}"}}]}`)
	})
	for i := 0; i < 3; i++ {
		_, _ = client.ExplainConflict(context.Background(), advisor.ConflictExplanationRequest{SourceStatus: "read", TargetStatus: "reading"})
	}
	if got := count.Load(); got != 3 {
		t.Fatalf("request count = %d, want 3", got)
	}
}

func TestLiveAdvisorFreeModel(t *testing.T) {
	if os.Getenv("DRIFTER_LIVE_ADVISOR") == "" {
		t.Skip("skipping live advisor test: DRIFTER_LIVE_ADVISOR not set")
	}
	client, err := New()
	if err != nil {
		t.Fatalf("failed to create client: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	resp, err := client.ExplainConflict(ctx, advisor.ConflictExplanationRequest{
		SourceTitle:  "Klara and the Sun",
		SourceAuthor: "Kazuo Ishiguro",
		SourceStatus: "read",
		TargetStatus: "reading",
		Reason:       "divergent_status_changes",
	})
	if err != nil {
		t.Fatalf("live ExplainConflict failed: %v", err)
	}
	if resp.Summary == "" || resp.RecommendedAction == "" {
		t.Fatalf("invalid live response: %+v", resp)
	}
	t.Logf("Live Advisor Result: summary=%q, action=%q", resp.Summary, resp.RecommendedAction)

	// Test domestic book abstention live
	origResp, err := client.ResolveOriginalWork(ctx, advisor.OriginalWorkRequest{
		Title:     "Kubernetes权威指南：企业级容器云实战",
		Author:    "龚正",
		Publisher: "电子工业出版社",
	})
	if err != nil {
		t.Fatalf("live ResolveOriginalWork failed: %v", err)
	}
	t.Logf("Live Original Work Result: abstained=%v, confidence=%v, rationale=%q", origResp.Abstained, origResp.Confidence, origResp.Rationale)
	if !origResp.Abstained {
		t.Errorf("expected abstention for domestic book, got: %+v", origResp)
	}
}
