package companion

import (
	"context"
	"io"
	"net/http"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestCanceledSerializationWaitDoesNotSendAndGateCanBeReused(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	var requests atomic.Int32
	a := testAPI(t, func(rw http.ResponseWriter, _ *http.Request) {
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
		firstDone <- a.post(context.Background(), nil, &result)
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
		secondDone <- a.post(ctx, nil, &result)
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
	if err := a.post(context.Background(), nil, &result); err != nil {
		t.Fatalf("serialization gate was not reusable: %v", err)
	}
	if got := requests.Load(); got != 2 {
		t.Fatalf("unexpected provider request count after reuse: %d", got)
	}
}
