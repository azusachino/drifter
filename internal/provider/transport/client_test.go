package transport

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func testClient(t *testing.T, handler http.HandlerFunc) *Client {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	c, err := NewClientWithHTTP("TestProvider", server.URL, "synthetic-token", 5*time.Millisecond, server.Client())
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func TestCanceledSerializationWaitDoesNotSendAndGateCanBeReused(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	var requests atomic.Int32
	c := testClient(t, func(rw http.ResponseWriter, _ *http.Request) {
		if requests.Add(1) == 1 {
			close(started)
			<-release
		}
		io.WriteString(rw, `{}`)
	})

	var releaseOnce sync.Once
	releaseProvider := func() { releaseOnce.Do(func() { close(release) }) }
	firstDone := make(chan error, 1)
	go func() {
		defer close(firstDone)
		var result any
		firstDone <- c.Post(context.Background(), nil, &result)
	}()
	t.Cleanup(func() {
		releaseProvider()
		select {
		case <-firstDone:
		case <-time.After(time.Second):
			t.Error("first provider request did not finish during cleanup")
		}
	})
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("first request did not enter provider")
	}

	ctx, cancel := context.WithCancel(context.Background())
	secondDone := make(chan error, 1)
	go func() {
		var result any
		secondDone <- c.Post(ctx, nil, &result)
	}()
	cancel()
	waiterReturned := false
	select {
	case err := <-secondDone:
		waiterReturned = true
		if err != context.Canceled {
			t.Fatalf("canceled waiter error = %v, want context.Canceled", err)
		}
	case <-time.After(time.Second):
	}
	if got := requests.Load(); got != 1 {
		releaseProvider()
		<-firstDone
		t.Fatalf("canceled waiter sent provider request; requests=%d", got)
	}

	releaseProvider()
	select {
	case err := <-firstDone:
		if err != nil {
			t.Fatalf("first request failed: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("first request did not finish")
	}

	if !waiterReturned {
		t.Fatal("canceled waiter remained blocked behind in-flight request")
	}
	var result any
	if err := c.Post(context.Background(), nil, &result); err != nil {
		t.Fatalf("serialization gate was not reusable: %v", err)
	}
	if got := requests.Load(); got != 2 {
		t.Fatalf("unexpected provider request count after reuse: %d", got)
	}
}

func TestNewClientCredentialValidation(t *testing.T) {
	for _, token := range []string{"", "   ", "Bearer", "Bearer   ", "line1\nline2"} {
		if _, err := NewClient("WeRead", "http://example.com", token, time.Second); err == nil {
			t.Errorf("expected invalid token %q to be rejected", token)
		}
	}
}

func TestHTTPRejectsRedirectsErrorsAndOversizedBodies(t *testing.T) {
	for _, status := range []int{http.StatusFound, http.StatusUnauthorized, http.StatusTooManyRequests} {
		c := testClient(t, func(rw http.ResponseWriter, _ *http.Request) {
			rw.Header().Set("Location", "https://example.com")
			rw.WriteHeader(status)
			io.WriteString(rw, "private-test-token")
		})
		var got any
		if err := c.Post(context.Background(), map[string]string{}, &got); err == nil || strings.Contains(err.Error(), "private-test-token") {
			t.Fatal(err)
		}
	}
	c := testClient(t, func(rw http.ResponseWriter, _ *http.Request) {
		io.WriteString(rw, strings.Repeat("x", ResponseLimit+1))
	})
	var got any
	if err := c.Post(context.Background(), nil, &got); err == nil {
		t.Fatal("oversized response accepted")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := c.Post(ctx, nil, &got); err == nil {
		t.Fatal("cancellation ignored")
	}
}

func TestCredentialAndTransportErrorsAreSanitized(t *testing.T) {
	for _, token := range []string{"", "Bearer ", "synthetic\nsecret", "synthetic\rsecret"} {
		if _, err := NewClient("test", "https://example.com", token, 0); err == nil || strings.Contains(err.Error(), "synthetic") {
			t.Fatal("unsafe credential validation")
		}
	}
	c := testClient(t, func(rw http.ResponseWriter, _ *http.Request) { io.WriteString(rw, "synthetic-secret-not-json") })
	var result any
	if err := c.Post(context.Background(), nil, &result); err == nil || strings.Contains(err.Error(), "synthetic-secret") {
		t.Fatalf("unsafe JSON error: %v", err)
	}
	c.url = "http://127.0.0.1:0/synthetic-secret"
	if err := c.Post(context.Background(), nil, &result); err == nil || strings.Contains(err.Error(), "synthetic-secret") {
		t.Fatalf("unsafe transport error: %v", err)
	}
}
