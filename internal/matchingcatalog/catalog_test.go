package matchingcatalog_test

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/azusachino/drifter/internal/matchingcatalog"
)

const validCatalog = `{
  "catalog_schema_version": 1,
  "catalog_id": "test.catalog",
  "publisher": "Synthetic publisher",
  "assertions": [{
    "record_id": "claim-42",
    "source": {"provider": "weread", "id": "42"},
    "target": {"provider": "hardcover", "work_id": 77},
    "provenance": [{"source": "synthetic fixture", "reference": "row 1"}]
  }],
  "hints": [{
    "record_id": "hint-42",
    "source": {"provider": "weread", "id": "42"},
    "title": "Synthetic title",
    "isbn": "9780306406157",
    "confidence": 0.8,
    "provenance": [{"source": "synthetic fixture", "reference": "hint 1"}]
  }]
}`

type resolverFunc func(context.Context, string, string) ([]netip.Addr, error)

func (resolver resolverFunc) LookupNetIP(ctx context.Context, network, host string) ([]netip.Addr, error) {
	return resolver(ctx, network, host)
}

type roundTripperFunc func(*http.Request) (*http.Response, error)

func (roundTripper roundTripperFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return roundTripper(request)
}

func response(request *http.Request, status int, header http.Header, body io.ReadCloser) *http.Response {
	return &http.Response{StatusCode: status, Header: header, Body: body, Request: request}
}

func writeCatalog(t *testing.T, contents string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "catalog.json")
	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func newLoader(t *testing.T, subscription matchingcatalog.Subscription, options matchingcatalog.LoaderOptions) *matchingcatalog.Loader {
	t.Helper()
	loader, err := matchingcatalog.NewLoader([]matchingcatalog.Subscription{subscription}, options)
	if err != nil {
		t.Fatal(err)
	}
	return loader
}

func TestLoaderLoadsSyntheticLocalCatalog(t *testing.T) {
	path := filepath.Join("..", "..", "examples", "matching-catalog.json")
	loader := newLoader(t, matchingcatalog.Subscription{ID: "synthetic", File: mustAbs(t, path)}, matchingcatalog.LoaderOptions{})
	snapshot := loader.Load(context.Background())
	if !snapshot.Configured || !snapshot.Complete || len(snapshot.Issues) != 0 || len(snapshot.Catalogs) != 1 {
		t.Fatalf("unexpected snapshot: %+v", snapshot)
	}
	catalog := snapshot.Catalogs[0]
	if catalog.SubscriptionID != "synthetic" || catalog.CatalogID != "example.synthetic-crosswalk" || len(catalog.Assertions) != 1 || len(catalog.Hints) != 1 {
		t.Fatalf("catalog metadata or records missing: %+v", catalog)
	}
	claim := catalog.Assertions[0]
	if claim.Source != (matchingcatalog.SourceRef{Provider: "weread", ID: "42"}) || claim.Target.Provider != "hardcover" || claim.Target.WorkID != 77 || claim.Target.EditionID == nil || *claim.Target.EditionID != 88 || claim.Provenance[0].Reference == "" {
		t.Fatalf("assertion was not decoded with provenance: %+v", claim)
	}
	if catalog.Hints[0].ISBN != "9780306406157" || catalog.Hints[0].Confidence != 0.8 || catalog.Hints[0].Provenance[0].Source == "" {
		t.Fatalf("hint was not decoded with provenance: %+v", catalog.Hints[0])
	}
}

func TestLoaderRejectsInvalidDocumentsAtomically(t *testing.T) {
	cases := map[string]string{
		"duplicate-key":           strings.Replace(validCatalog, `"catalog_schema_version": 1,`, `"catalog_schema_version": 1, "catalog_schema_version": 1,`, 1),
		"unknown-property":        strings.Replace(validCatalog, `"catalog_id": "test.catalog",`, `"catalog_id": "test.catalog", "unexpected": true,`, 1),
		"unknown-nested-property": strings.Replace(validCatalog, `"record_id": "claim-42",`, `"record_id": "claim-42", "unexpected": true,`, 1),
		"unsupported-version":     strings.Replace(validCatalog, `"catalog_schema_version": 1`, `"catalog_schema_version": 9`, 1),
		"duplicate-record-id":     strings.Replace(validCatalog, `"record_id": "hint-42"`, `"record_id": "claim-42"`, 1),
		"invalid-isbn-checksum":   strings.Replace(validCatalog, `9780306406157`, `9780306406158`, 1),
		"invalid-confidence":      strings.Replace(validCatalog, `"confidence": 0.8`, `"confidence": 1.1`, 1),
		"zero-target":             strings.Replace(validCatalog, `"work_id": 77`, `"work_id": 0`, 1),
		"zero-source-id":          strings.Replace(validCatalog, `"id": "42"`, `"id": "000"`, 1),
		"trailing-json":           validCatalog + `{}`,
	}
	for name, contents := range cases {
		t.Run(name, func(t *testing.T) {
			loader := newLoader(t, matchingcatalog.Subscription{ID: "safe-alias", File: mustAbs(t, writeCatalog(t, contents))}, matchingcatalog.LoaderOptions{})
			snapshot := loader.Load(context.Background())
			if !snapshot.Configured || snapshot.Complete || len(snapshot.Catalogs) != 0 || len(snapshot.Issues) != 1 || snapshot.Issues[0].SubscriptionID != "safe-alias" || snapshot.Issues[0].Code == "" {
				t.Fatalf("invalid source was not rejected atomically: %+v", snapshot)
			}
			encoded, err := json.Marshal(snapshot.Issues)
			if err != nil || strings.Contains(string(encoded), contents) {
				t.Fatalf("issue leaked document contents: %s", encoded)
			}
		})
	}
}

func TestNewLoaderRejectsUnsafeSubscriptions(t *testing.T) {
	for _, subscription := range []matchingcatalog.Subscription{
		{ID: "../unsafe", File: "/tmp/catalog.json"},
		{ID: "duplicate", File: "/tmp/a.json", URL: "https://catalog.example.org/a.json"},
		{ID: "query", URL: "https://catalog.example.org/a.json?token=hidden"},
		{ID: "fragment", URL: "https://catalog.example.org/a.json#fragment"},
		{ID: "userinfo", URL: "https://user:pass@catalog.example.org/a.json"},
		{ID: "http", URL: "http://catalog.example.org/a.json"},
		{ID: "port", URL: "https://catalog.example.org:8443/a.json"},
		{ID: "literal", URL: "https://127.0.0.1/a.json"},
		{ID: "local", URL: "https://catalog.local/a.json"},
	} {
		if _, err := matchingcatalog.NewLoader([]matchingcatalog.Subscription{subscription}, matchingcatalog.LoaderOptions{}); err == nil {
			t.Fatalf("unsafe subscription accepted: %+v", subscription)
		}
	}
}

func TestLoaderRejectsMixedPublicAndPrivateDNSAnswersBeforeFetch(t *testing.T) {
	calls := 0
	loader := newLoader(t, matchingcatalog.Subscription{ID: "remote", URL: "https://catalog.example.org/catalog.json"}, matchingcatalog.LoaderOptions{
		Resolver: resolverFunc(func(context.Context, string, string) ([]netip.Addr, error) {
			return []netip.Addr{netip.MustParseAddr("93.184.216.34"), netip.MustParseAddr("127.0.0.1")}, nil
		}),
		RoundTripper: roundTripperFunc(func(*http.Request) (*http.Response, error) {
			calls++
			return nil, fmt.Errorf("must not fetch")
		}),
	})
	snapshot := loader.Load(context.Background())
	if snapshot.Complete || calls != 0 || len(snapshot.Issues) != 1 || snapshot.Issues[0].Code != matchingcatalog.IssueUnsafeDestination {
		t.Fatalf("unsafe DNS answer was used: calls=%d snapshot=%+v", calls, snapshot)
	}
}

func TestLoaderRejectsRedirectWithoutFollowingOrForwardingCredentials(t *testing.T) {
	calls := 0
	var headers http.Header
	loader := newLoader(t, matchingcatalog.Subscription{ID: "remote", URL: "https://catalog.example.org/catalog.json"}, matchingcatalog.LoaderOptions{
		Resolver: resolverFunc(func(context.Context, string, string) ([]netip.Addr, error) {
			return []netip.Addr{netip.MustParseAddr("93.184.216.34")}, nil
		}),
		RoundTripper: roundTripperFunc(func(request *http.Request) (*http.Response, error) {
			calls++
			headers = request.Header.Clone()
			return response(request, http.StatusFound, http.Header{"Location": []string{"https://127.0.0.1/private"}}, io.NopCloser(strings.NewReader("private redirect body"))), nil
		}),
	})
	snapshot := loader.Load(context.Background())
	if snapshot.Complete || calls != 1 || len(snapshot.Issues) != 1 || snapshot.Issues[0].Code != matchingcatalog.IssueRedirectRejected {
		t.Fatalf("redirect was followed or accepted: calls=%d snapshot=%+v", calls, snapshot)
	}
	if headers.Get("Authorization") != "" || headers.Get("Cookie") != "" || headers.Get("Accept-Encoding") != "gzip" {
		t.Fatalf("unexpected credential/compression request headers: %v", headers)
	}
}

func TestLoaderLimitsDecompressedGzipBodies(t *testing.T) {
	var compressed bytes.Buffer
	writer := gzip.NewWriter(&compressed)
	if _, err := writer.Write(bytes.Repeat([]byte("x"), 1<<20+1)); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	loader := newLoader(t, matchingcatalog.Subscription{ID: "compressed", URL: "https://catalog.example.org/catalog.json"}, matchingcatalog.LoaderOptions{
		Resolver: resolverFunc(func(context.Context, string, string) ([]netip.Addr, error) {
			return []netip.Addr{netip.MustParseAddr("93.184.216.34")}, nil
		}),
		RoundTripper: roundTripperFunc(func(request *http.Request) (*http.Response, error) {
			return response(request, http.StatusOK, http.Header{"Content-Encoding": []string{"gzip"}}, io.NopCloser(bytes.NewReader(compressed.Bytes()))), nil
		}),
	})
	snapshot := loader.Load(context.Background())
	if snapshot.Complete || len(snapshot.Issues) != 1 || snapshot.Issues[0].Code != matchingcatalog.IssueLimitExceeded {
		t.Fatalf("decompressed body limit was not enforced: %+v", snapshot)
	}
}

func TestLoaderCancellationIsReportedWithSafeIssue(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	loader := newLoader(t, matchingcatalog.Subscription{ID: "cancelled", URL: "https://catalog.example.org/catalog.json"}, matchingcatalog.LoaderOptions{
		Resolver: resolverFunc(func(ctx context.Context, _, _ string) ([]netip.Addr, error) {
			<-ctx.Done()
			return nil, ctx.Err()
		}),
	})
	snapshot := loader.Load(ctx)
	if snapshot.Complete || len(snapshot.Issues) != 1 || snapshot.Issues[0].Code != matchingcatalog.IssueCancelled {
		t.Fatalf("cancellation was not reported safely: %+v", snapshot)
	}
}

func TestLoaderPinsPublicDNSAndSendsTLSForConfiguredHost(t *testing.T) {
	t.Setenv("HTTPS_PROXY", "http://127.0.0.1:1")
	t.Setenv("https_proxy", "http://127.0.0.1:1")
	var dialed string
	var hello []byte
	helloRead := make(chan struct{})
	loader := newLoader(t, matchingcatalog.Subscription{ID: "pinned", URL: "https://catalog.example.org/catalog.json"}, matchingcatalog.LoaderOptions{
		Resolver: resolverFunc(func(context.Context, string, string) ([]netip.Addr, error) {
			return []netip.Addr{netip.MustParseAddr("93.184.216.34")}, nil
		}),
		DialContext: func(_ context.Context, network, address string) (net.Conn, error) {
			dialed = address
			client, server := net.Pipe()
			go func() {
				defer server.Close()
				defer close(helloRead)
				buffer := make([]byte, 4096)
				n, _ := server.Read(buffer)
				hello = append([]byte(nil), buffer[:n]...)
			}()
			return client, nil
		},
	})
	snapshot := loader.Load(context.Background())
	<-helloRead
	if snapshot.Complete || len(snapshot.Issues) != 1 || dialed != "93.184.216.34:443" || !bytes.Contains(hello, []byte("catalog.example.org")) {
		t.Fatalf("dial was not pinned or TLS SNI missing: dialed=%q hello=%x snapshot=%+v", dialed, hello, snapshot)
	}
}

func mustAbs(t *testing.T, path string) string {
	t.Helper()
	absolute, err := filepath.Abs(path)
	if err != nil {
		t.Fatal(err)
	}
	return absolute
}
