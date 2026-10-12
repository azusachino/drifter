package companion

import (
	"context"
	"io"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestSearchRequiresResultsAndPagination(t *testing.T) {
	for _, body := range []string{`{}`, `{"results":null,"hasMore":0}`, `{"results":[]}`, `{"results":[],"hasMore":2}`, `{"results":[{"books":[{"bookInfo":{"bookId":"0","title":"invalid"}}]}],"hasMore":0}`} {
		w := &WeRead{testAPI(t, func(rw http.ResponseWriter, _ *http.Request) { io.WriteString(rw, body) })}
		if _, err := w.Search(context.Background(), "synthetic"); err == nil {
			t.Fatalf("malformed search accepted: %s", body)
		}
	}
	w := &WeRead{testAPI(t, func(rw http.ResponseWriter, _ *http.Request) { io.WriteString(rw, `{"results":[],"hasMore":0}`) })}
	result, err := w.Search(context.Background(), "synthetic")
	if err != nil || result.Books == nil || len(result.Books) != 0 || result.HasMore {
		t.Fatalf("valid empty search rejected: %+v %v", result, err)
	}
}

func TestProgressRejectsMissingOrInvalidFacts(t *testing.T) {
	for _, body := range []string{
		`{}`, `{"bookId":"123","book":null}`, `{"bookId":"123","book":{"progress":0}}`,
		`{"bookId":"123","book":{"isStartReading":1}}`,
		`{"bookId":"999","book":{"progress":0,"isStartReading":1}}`,
		`{"bookId":"123","book":{"progress":101,"isStartReading":1}}`,
		`{"bookId":"123","book":{"progress":0,"isStartReading":2}}`,
		`{"bookId":"123","book":{"progress":100,"isStartReading":1,"finishTime":-1}}`,
	} {
		w := &WeRead{testAPI(t, func(rw http.ResponseWriter, _ *http.Request) { io.WriteString(rw, body) })}
		if _, err := w.Progress(context.Background(), "123"); err == nil {
			t.Fatalf("invalid progress accepted: %s", body)
		}
	}
}

func TestHardcoverRejectsMissingEditionList(t *testing.T) {
	for _, body := range []string{`{"data":{}}`, `{"data":{"editions":null}}`, `{"data":{"editions":[{"id":0,"book_id":2}]}}`} {
		h := &Hardcover{testAPI(t, func(rw http.ResponseWriter, _ *http.Request) { io.WriteString(rw, body) })}
		if _, err := h.Match(context.Background(), Book{Title: "synthetic", ISBN: "9780306406157"}, ""); err == nil {
			t.Fatalf("invalid edition response accepted: %s", body)
		}
	}
}

func TestHardcoverRejectsWrongReturnedMembership(t *testing.T) {
	h := &Hardcover{testAPI(t, func(rw http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		if strings.Contains(string(body), "query Edition") {
			io.WriteString(rw, `{"data":{"editions":[{"id":99,"book_id":3}]}}`)
		} else {
			io.WriteString(rw, `{"data":{"books":[{"id":2}]}}`)
		}
	})}
	if err := h.ValidateLink(context.Background(), 2, 99); err == nil {
		t.Fatal("edition from another work accepted")
	}
}

func TestWeReadOperationAllowlist(t *testing.T) {
	w := &WeRead{}
	for _, operation := range []string{"/shelf/delete", "/book/update", "", "mutation"} {
		if err := w.call(context.Background(), operation, nil, nil); err == nil {
			t.Fatal("unsupported operation accepted")
		}
	}
}

func TestCredentialAndTransportErrorsAreSanitized(t *testing.T) {
	for _, token := range []string{"", "Bearer ", "synthetic\nsecret", "synthetic\rsecret"} {
		if _, err := newAPI("test", "https://example.com", token, 0); err == nil || strings.Contains(err.Error(), "synthetic") {
			t.Fatal("unsafe credential validation")
		}
	}
	a := testAPI(t, func(rw http.ResponseWriter, _ *http.Request) { io.WriteString(rw, "synthetic-secret-not-json") })
	var result any
	if err := a.post(context.Background(), nil, &result); err == nil || strings.Contains(err.Error(), "synthetic-secret") {
		t.Fatalf("unsafe JSON error: %v", err)
	}
	a.url = "http://127.0.0.1:0/synthetic-secret"
	if err := a.post(context.Background(), nil, &result); err == nil || strings.Contains(err.Error(), "synthetic-secret") {
		t.Fatalf("unsafe transport error: %v", err)
	}
}

func TestCanceledSpacingDoesNotSendRequest(t *testing.T) {
	var requests atomic.Int32
	a := testAPI(t, func(rw http.ResponseWriter, _ *http.Request) { requests.Add(1); io.WriteString(rw, `{}`) })
	a.next = time.Now().Add(time.Minute)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	var result any
	if err := a.post(ctx, nil, &result); err == nil {
		t.Fatal("canceled pacing wait ignored")
	}
	if requests.Load() != 0 {
		t.Fatal("request sent despite cancellation")
	}
}
