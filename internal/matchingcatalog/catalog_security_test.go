package matchingcatalog_test

import (
	"bytes"
	"compress/gzip"
	"context"
	"crypto/rand"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/azusachino/drifter/internal/matchingcatalog"
)

func TestLoaderRejectsSpecialAndNonPublicDNSAddresses(t *testing.T) {
	for _, address := range []string{
		"0.1.2.3", "10.1.2.3", "100.64.0.1", "127.0.0.1", "169.254.169.254",
		"172.16.0.1", "192.0.2.1", "192.168.1.1", "198.18.0.1", "203.0.113.1",
		"224.0.0.1", "240.0.0.1", "::", "::1", "::ffff:192.168.1.1",
		"64:ff9b::a00:1", "100::1", "2001:db8::1", "2002::1", "fc00::1", "fe80::1", "ff02::1",
	} {
		t.Run(address, func(t *testing.T) {
			calls := 0
			loader := newLoader(t, matchingcatalog.Subscription{ID: "safe", URL: "https://catalog.example.org/catalog.json"}, matchingcatalog.LoaderOptions{
				Resolver: resolverFunc(func(context.Context, string, string) ([]netip.Addr, error) {
					return []netip.Addr{netip.MustParseAddr(address)}, nil
				}),
				RoundTripper: roundTripperFunc(func(*http.Request) (*http.Response, error) {
					calls++
					return nil, fmt.Errorf("unsafe address reached transport")
				}),
			})
			snapshot := loader.Load(context.Background())
			if calls != 0 || snapshot.Complete || len(snapshot.Issues) != 1 || snapshot.Issues[0].Code != matchingcatalog.IssueUnsafeDestination {
				t.Fatalf("special address was used: %s calls=%d snapshot=%+v", address, calls, snapshot)
			}
		})
	}
}

func TestLoaderFetchesPublicHTTPSWithoutForwardingCredentials(t *testing.T) {
	var seen *http.Request
	loader := newLoader(t, matchingcatalog.Subscription{ID: "public", URL: "https://catalog.example.org/catalog.json"}, matchingcatalog.LoaderOptions{
		Resolver: resolverFunc(func(_ context.Context, network, host string) ([]netip.Addr, error) {
			if network != "ip" || host != "catalog.example.org" {
				t.Fatalf("unexpected DNS lookup: network=%q host=%q", network, host)
			}
			return []netip.Addr{netip.MustParseAddr("93.184.216.34")}, nil
		}),
		RoundTripper: roundTripperFunc(func(request *http.Request) (*http.Response, error) {
			seen = request
			return response(request, http.StatusOK, make(http.Header), io.NopCloser(strings.NewReader(validCatalog))), nil
		}),
	})
	snapshot := loader.Load(context.Background())
	if !snapshot.Complete || len(snapshot.Catalogs) != 1 || seen == nil {
		t.Fatalf("public source was not loaded: %+v", snapshot)
	}
	if seen.Method != http.MethodGet || seen.URL.Host != "catalog.example.org" || seen.Header.Get("Authorization") != "" || seen.Header.Get("Cookie") != "" || seen.Header.Get("User-Agent") != "drifter-matching-catalog/1" {
		t.Fatalf("unexpected remote request or credential forwarding: %+v", seen)
	}
}

func TestLoaderKeepsValidCatalogWhenAnotherSourceFails(t *testing.T) {
	validPath := writeCatalog(t, validCatalog)
	invalidPath := writeCatalog(t, `{"catalog_schema_version":1,"unexpected":true}`)
	loader, err := matchingcatalog.NewLoader([]matchingcatalog.Subscription{
		{ID: "valid", File: mustAbs(t, validPath)},
		{ID: "invalid", File: mustAbs(t, invalidPath)},
	}, matchingcatalog.LoaderOptions{})
	if err != nil {
		t.Fatal(err)
	}
	snapshot := loader.Load(context.Background())
	if snapshot.Complete || len(snapshot.Catalogs) != 1 || snapshot.Catalogs[0].SubscriptionID != "valid" || len(snapshot.Issues) != 1 || snapshot.Issues[0].SubscriptionID != "invalid" {
		t.Fatalf("one failed source discarded valid evidence or escaped reporting: %+v", snapshot)
	}
}

func TestLoaderEnforcesCombinedBudgetsAcrossFailedDocuments(t *testing.T) {
	first := `{"bad":"` + strings.Repeat("a", 900<<10) + `"}`
	second := `{"bad":"` + strings.Repeat("b", 900<<10) + `"}`
	third := `{"bad":"` + strings.Repeat("c", 900<<10) + `"}`
	fourth := `{"bad":"` + strings.Repeat("d", 900<<10) + `"}`
	fifth := `{"bad":"` + strings.Repeat("e", 500<<10) + `"}`
	contents := []string{first, second, third, fourth, fifth}
	subscriptions := make([]matchingcatalog.Subscription, len(contents))
	for i, content := range contents {
		subscriptions[i] = matchingcatalog.Subscription{ID: fmt.Sprintf("local-%d", i), File: mustAbs(t, writeCatalog(t, content))}
	}
	loader, err := matchingcatalog.NewLoader(subscriptions, matchingcatalog.LoaderOptions{})
	if err != nil {
		t.Fatal(err)
	}
	snapshot := loader.Load(context.Background())
	if snapshot.Complete || len(snapshot.Catalogs) != 0 || len(snapshot.Issues) != len(contents) || snapshot.Issues[4].Code != matchingcatalog.IssueLimitExceeded {
		t.Fatalf("failed source bytes were omitted from the combined budget: %+v", snapshot)
	}
}

func TestLoaderEnforcesCombinedRecordBudgetAcrossInvalidDocuments(t *testing.T) {
	record := `{"record_id":"row","source":{"provider":"weread","id":"42"},"target":{"provider":"hardcover","work_id":1},"provenance":[{"source":"fixture","reference":"row"}]}`
	large := func(start, count int) string {
		var builder strings.Builder
		builder.WriteString(`{"catalog_schema_version":1,"catalog_id":"invalid.catalog","publisher":"fixture","assertions":[`)
		for i := 0; i < count; i++ {
			if i != 0 {
				builder.WriteByte(',')
			}
			row := strings.Replace(record, `"row"`, fmt.Sprintf(`"row-%d"`, start+i), 1)
			builder.WriteString(row)
		}
		builder.WriteString(`],"hints":[]}`)
		return builder.String()
	}
	first := strings.Replace(large(0, 5000), `"catalog_schema_version":1`, `"catalog_schema_version":9`, 1)
	second := strings.Replace(large(5000, 5000), `"catalog_schema_version":1`, `"catalog_schema_version":9`, 1)
	third := `{"catalog_schema_version":1,"catalog_id":"invalid.catalog","publisher":"fixture","assertions":[` + record + `],"hints":[]}`
	contents := []string{first, second, third}
	subscriptions := make([]matchingcatalog.Subscription, len(contents))
	for i, content := range contents {
		subscriptions[i] = matchingcatalog.Subscription{ID: fmt.Sprintf("rows-%d", i), File: mustAbs(t, writeCatalog(t, content))}
	}
	loader, err := matchingcatalog.NewLoader(subscriptions, matchingcatalog.LoaderOptions{})
	if err != nil {
		t.Fatal(err)
	}
	snapshot := loader.Load(context.Background())
	if snapshot.Complete || len(snapshot.Catalogs) != 0 || len(snapshot.Issues) != 3 || snapshot.Issues[2].Code != matchingcatalog.IssueLimitExceeded {
		t.Fatalf("failed source records were omitted from the combined budget: %+v", snapshot)
	}
}

func TestLoaderRejectsNonRegularLocalFilesWithoutReading(t *testing.T) {
	path := filepath.Join(t.TempDir(), "catalog.fifo")
	if err := syscall.Mkfifo(path, 0o600); err != nil {
		t.Skipf("named pipes unavailable: %v", err)
	}
	loader := newLoader(t, matchingcatalog.Subscription{ID: "fifo", File: mustAbs(t, path)}, matchingcatalog.LoaderOptions{})
	snapshot := loader.Load(context.Background())
	if snapshot.Complete || len(snapshot.Issues) != 1 || snapshot.Issues[0].Code != matchingcatalog.IssueUnavailable {
		t.Fatalf("non-regular file was accepted: %+v", snapshot)
	}
}

func TestLoaderRejectsSymlinkedLocalCatalog(t *testing.T) {
	directory := t.TempDir()
	target := filepath.Join(directory, "target.json")
	if err := os.WriteFile(target, []byte(validCatalog), 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(directory, "catalog.json")
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	loader := newLoader(t, matchingcatalog.Subscription{ID: "symlink", File: mustAbs(t, link)}, matchingcatalog.LoaderOptions{})
	snapshot := loader.Load(context.Background())
	if snapshot.Complete || len(snapshot.Issues) != 1 || snapshot.Issues[0].Code != matchingcatalog.IssueUnavailable {
		t.Fatalf("symlinked catalog was accepted: %+v", snapshot)
	}
}

func TestLoaderCancellationInterruptsDNSConnectAndRead(t *testing.T) {
	t.Run("DNS", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		started := make(chan struct{})
		loader := newLoader(t, matchingcatalog.Subscription{ID: "cancel-dns", URL: "https://catalog.example.org/catalog.json"}, matchingcatalog.LoaderOptions{
			Resolver: resolverFunc(func(ctx context.Context, _, _ string) ([]netip.Addr, error) {
				close(started)
				<-ctx.Done()
				return nil, ctx.Err()
			}),
		})
		result := make(chan matchingcatalog.Snapshot, 1)
		go func() { result <- loader.Load(ctx) }()
		<-started
		cancel()
		assertCancelledSnapshot(t, result)
	})
	t.Run("connect", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		started := make(chan struct{})
		loader := newLoader(t, matchingcatalog.Subscription{ID: "cancel-connect", URL: "https://catalog.example.org/catalog.json"}, matchingcatalog.LoaderOptions{
			Resolver: resolverFunc(func(context.Context, string, string) ([]netip.Addr, error) {
				return []netip.Addr{netip.MustParseAddr("93.184.216.34")}, nil
			}),
			DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
				close(started)
				<-ctx.Done()
				return nil, ctx.Err()
			},
		})
		result := make(chan matchingcatalog.Snapshot, 1)
		go func() { result <- loader.Load(ctx) }()
		<-started
		cancel()
		assertCancelledSnapshot(t, result)
	})
	t.Run("read", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		started := make(chan struct{})
		loader := newLoader(t, matchingcatalog.Subscription{ID: "cancel-read", URL: "https://catalog.example.org/catalog.json"}, matchingcatalog.LoaderOptions{
			Resolver: resolverFunc(func(context.Context, string, string) ([]netip.Addr, error) {
				return []netip.Addr{netip.MustParseAddr("93.184.216.34")}, nil
			}),
			RoundTripper: roundTripperFunc(func(request *http.Request) (*http.Response, error) {
				close(started)
				return response(request, http.StatusOK, make(http.Header), &blockingBody{ctx: request.Context()}), nil
			}),
		})
		result := make(chan matchingcatalog.Snapshot, 1)
		go func() { result <- loader.Load(ctx) }()
		<-started
		cancel()
		assertCancelledSnapshot(t, result)
	})
}

func assertCancelledSnapshot(t *testing.T, result <-chan matchingcatalog.Snapshot) {
	t.Helper()
	select {
	case snapshot := <-result:
		if snapshot.Complete || len(snapshot.Issues) != 1 || snapshot.Issues[0].Code != matchingcatalog.IssueCancelled {
			t.Fatalf("canceled operation was not reported: %+v", snapshot)
		}
	case <-time.After(time.Second):
		t.Fatal("loader did not stop after context cancellation")
	}
}

func TestLoaderAppliesFetchTimeoutToRemoteBodyRead(t *testing.T) {
	loader := newLoader(t, matchingcatalog.Subscription{ID: "timeout", URL: "https://catalog.example.org/catalog.json"}, matchingcatalog.LoaderOptions{
		Resolver: resolverFunc(func(context.Context, string, string) ([]netip.Addr, error) {
			return []netip.Addr{netip.MustParseAddr("93.184.216.34")}, nil
		}),
		RoundTripper: roundTripperFunc(func(request *http.Request) (*http.Response, error) {
			body := &blockingBody{ctx: request.Context()}
			return response(request, http.StatusOK, make(http.Header), body), nil
		}),
	})
	started := time.Now()
	snapshot := loader.Load(context.Background())
	if elapsed := time.Since(started); elapsed > 7*time.Second {
		t.Fatalf("fetch timeout exceeded bound: %s", elapsed)
	}
	if snapshot.Complete || len(snapshot.Issues) != 1 || snapshot.Issues[0].Code != matchingcatalog.IssueTimeout {
		t.Fatalf("remote timeout was not reported: %+v", snapshot)
	}
}

type blockingBody struct {
	ctx context.Context
}

func (body *blockingBody) Read([]byte) (int, error) {
	<-body.ctx.Done()
	return 0, body.ctx.Err()
}

func (body *blockingBody) Close() error { return nil }

func TestLoaderCapsCompressedWireAndLocalFileBytes(t *testing.T) {
	uncompressed := make([]byte, 1<<20)
	if _, err := rand.Read(uncompressed); err != nil {
		t.Fatal(err)
	}
	var compressed bytes.Buffer
	writer := gzip.NewWriter(&compressed)
	if _, err := writer.Write(uncompressed); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	if compressed.Len() <= 1<<20 {
		t.Fatal("incompressible test body did not exceed raw transfer limit")
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
		t.Fatalf("compressed wire limit was not enforced: %+v", snapshot)
	}
	tooLarge := writeCatalog(t, strings.Repeat("x", 1<<20+1))
	loader = newLoader(t, matchingcatalog.Subscription{ID: "oversized", File: mustAbs(t, tooLarge)}, matchingcatalog.LoaderOptions{})
	snapshot = loader.Load(context.Background())
	if snapshot.Complete || len(snapshot.Issues) != 1 || snapshot.Issues[0].Code != matchingcatalog.IssueLimitExceeded {
		t.Fatalf("oversized local file was accepted: %+v", snapshot)
	}
}
