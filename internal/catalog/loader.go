package catalog

import (
	"bytes"
	"compress/gzip"
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/azusachino/drifter/schemas"
	"github.com/santhosh-tekuri/jsonschema/v6"
)

const (
	maxCatalogs          = 8
	maxDocumentBytes     = int64(1 << 20)
	maxTotalCatalogBytes = int64(4 << 20)
	// MaxRecordsPerCatalog is the published v1 per-document record limit.
	MaxRecordsPerCatalog = 5000
	maxRecordsTotal      = 10000
	maxJSONDepth         = 64
	perFetchTimeout      = 5 * time.Second
	totalLoadTimeout     = 15 * time.Second
	maxDNSAnswers        = 32
	catalogSchemaID      = "https://github.com/azusachino/drifter/blob/main/schemas/matching-catalog-v1.schema.json"
)

var (
	compileSchemaOnce sync.Once
	compiledSchema    *jsonschema.Schema
	compileSchemaErr  error
	publicOnly        = errors.New("catalog request rejected")
	// These are IANA special-purpose ranges; reject complete ranges rather
	// than trying to infer whether an individual address is publicly routed.
	globalAddressDeny = []netip.Prefix{
		netip.MustParsePrefix("0.0.0.0/8"),
		netip.MustParsePrefix("10.0.0.0/8"),
		netip.MustParsePrefix("100.64.0.0/10"),
		netip.MustParsePrefix("127.0.0.0/8"),
		netip.MustParsePrefix("169.254.0.0/16"),
		netip.MustParsePrefix("172.16.0.0/12"),
		netip.MustParsePrefix("192.0.0.0/24"),
		netip.MustParsePrefix("192.0.2.0/24"),
		netip.MustParsePrefix("192.31.196.0/24"),
		netip.MustParsePrefix("192.52.193.0/24"),
		netip.MustParsePrefix("192.88.99.0/24"),
		netip.MustParsePrefix("192.168.0.0/16"),
		netip.MustParsePrefix("192.175.48.0/24"),
		netip.MustParsePrefix("198.18.0.0/15"),
		netip.MustParsePrefix("198.51.100.0/24"),
		netip.MustParsePrefix("203.0.113.0/24"),
		netip.MustParsePrefix("224.0.0.0/4"),
		netip.MustParsePrefix("240.0.0.0/4"),
		netip.MustParsePrefix("::/96"),
		netip.MustParsePrefix("64:ff9b::/96"),
		netip.MustParsePrefix("64:ff9b:1::/48"),
		netip.MustParsePrefix("100::/64"),
		netip.MustParsePrefix("2001::/23"),
		netip.MustParsePrefix("2001:db8::/32"),
		netip.MustParsePrefix("2002::/16"),
		netip.MustParsePrefix("3fff::/20"),
		netip.MustParsePrefix("5f00::/16"),
	}
)

// Subscription names one literal local JSON file or HTTPS URL.
type Subscription struct {
	ID   string `toml:"id"`
	File string `toml:"file"`
	URL  string `toml:"url"`
}

type SourceRef struct {
	Provider string `json:"provider"`
	ID       string `json:"id"`
}

type TargetRef struct {
	Provider  string `json:"provider"`
	WorkID    int64  `json:"work_id"`
	EditionID *int64 `json:"edition_id,omitempty"`
}

type Provenance struct {
	Source    string `json:"source"`
	Reference string `json:"reference"`
}

type Assertion struct {
	RecordID   string       `json:"record_id"`
	Source     SourceRef    `json:"source"`
	Target     TargetRef    `json:"target"`
	Provenance []Provenance `json:"provenance"`
}

type Hint struct {
	RecordID       string       `json:"record_id"`
	Source         SourceRef    `json:"source"`
	Title          string       `json:"title,omitempty"`
	OriginalAuthor string       `json:"original_author,omitempty"`
	ISBN           string       `json:"isbn,omitempty"`
	Confidence     float64      `json:"confidence"`
	Provenance     []Provenance `json:"provenance"`
}

type Catalog struct {
	SubscriptionID string
	CatalogID      string
	Publisher      string
	Assertions     []Assertion
	Hints          []Hint
}

type IssueCode string

const (
	IssueUnavailable       IssueCode = "unavailable"
	IssueUnsafeDestination IssueCode = "unsafe_destination"
	IssueRedirectRejected  IssueCode = "redirect_rejected"
	IssueInvalidDocument   IssueCode = "invalid_document"
	IssueUnsupportedSchema IssueCode = "unsupported_schema"
	IssueLimitExceeded     IssueCode = "limit_exceeded"
	IssueCancelled         IssueCode = "cancelled"
	IssueTimeout           IssueCode = "timeout"
)

type Issue struct {
	SubscriptionID string    `json:"subscription_id"`
	Code           IssueCode `json:"code"`
}

type Snapshot struct {
	Configured bool
	Complete   bool
	Catalogs   []Catalog
	Issues     []Issue
}

type Resolver interface {
	LookupNetIP(context.Context, string, string) ([]netip.Addr, error)
}

type DialContextFunc func(context.Context, string, string) (net.Conn, error)

// LoaderOptions injects DNS and transport seams for deterministic callers and
// tests. Production callers leave these fields nil to use the pinned transport.
type LoaderOptions struct {
	Resolver     Resolver
	RoundTripper http.RoundTripper
	DialContext  DialContextFunc
}

type Loader struct {
	subscriptions []Subscription
	options       LoaderOptions
}

type document struct {
	SchemaVersion int         `json:"catalog_schema_version"`
	CatalogID     string      `json:"catalog_id"`
	Publisher     string      `json:"publisher"`
	Assertions    []Assertion `json:"assertions"`
	Hints         []Hint      `json:"hints"`
}

// ValidateSubscriptions validates literal source configuration without loading
// files or making network requests.
func ValidateSubscriptions(subscriptions []Subscription) error {
	if len(subscriptions) > maxCatalogs {
		return fmt.Errorf("matching.catalogs exceeds the configured source limit")
	}
	seen := make(map[string]struct{}, len(subscriptions))
	for _, subscription := range subscriptions {
		if !validAlias(subscription.ID) {
			return fmt.Errorf("matching.catalogs requires a safe unique id")
		}
		if _, exists := seen[subscription.ID]; exists {
			return fmt.Errorf("matching.catalogs requires a safe unique id")
		}
		seen[subscription.ID] = struct{}{}
		if (subscription.File == "") == (subscription.URL == "") {
			return fmt.Errorf("matching.catalogs entry requires exactly one of file or url")
		}
		if subscription.File != "" {
			if !filepath.IsAbs(subscription.File) || strings.TrimSpace(subscription.File) != subscription.File {
				return fmt.Errorf("matching.catalogs file must be an absolute literal path")
			}
			continue
		}
		if _, err := parseCatalogURL(subscription.URL); err != nil {
			return fmt.Errorf("matching.catalogs url is not an allowed HTTPS catalog URL")
		}
	}
	return nil
}

// NewLoader creates the shared catalog loader used by the core match operation.
func NewLoader(subscriptions []Subscription, options LoaderOptions) (*Loader, error) {
	if err := ValidateSubscriptions(subscriptions); err != nil {
		return nil, err
	}
	if len(subscriptions) > 0 {
		if _, err := schemaV1(); err != nil {
			return nil, fmt.Errorf("matching catalog schema unavailable")
		}
	}
	copyOf := append([]Subscription(nil), subscriptions...)
	return &Loader{subscriptions: copyOf, options: options}, nil
}

// Load reads each configured source in order, atomically accepting only fully
// validated documents. Source failures remain safe report issues.
func (loader *Loader) Load(ctx context.Context) Snapshot {
	if len(loader.subscriptions) == 0 {
		return Snapshot{Complete: true}
	}
	loadCtx, cancel := context.WithTimeout(ctx, totalLoadTimeout)
	defer cancel()
	snapshot := Snapshot{Configured: true, Complete: true, Catalogs: []Catalog{}, Issues: []Issue{}}
	budget := byteBudget{}
	totalRecords := 0
	for _, subscription := range loader.subscriptions {
		if err := loadCtx.Err(); err != nil {
			addIssue(&snapshot, subscription.ID, contextIssue(ctx, loadCtx, err))
			continue
		}
		var doc Catalog
		var issue IssueCode
		if subscription.File != "" {
			doc, issue = loader.loadFile(loadCtx, subscription, &budget, &totalRecords)
		} else {
			doc, issue = loader.loadRemote(loadCtx, subscription, &budget, &totalRecords)
		}
		if issue != "" {
			addIssue(&snapshot, subscription.ID, issue)
			continue
		}
		doc.SubscriptionID = subscription.ID
		snapshot.Catalogs = append(snapshot.Catalogs, doc)
	}
	if len(snapshot.Issues) != 0 {
		snapshot.Complete = false
	}
	return snapshot
}

type byteBudget struct {
	wire    int64
	decoded int64
}

func (budget *byteBudget) remainingWire() int64 {
	return maxTotalCatalogBytes - budget.wire
}

func (budget *byteBudget) remainingDecoded() int64 {
	return maxTotalCatalogBytes - budget.decoded
}

func (loader *Loader) loadFile(ctx context.Context, subscription Subscription, budget *byteBudget, totalRecords *int) (Catalog, IssueCode) {
	file, err := openRegularFile(subscription.File)
	if err != nil {
		return Catalog{}, IssueUnavailable
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() {
		return Catalog{}, IssueUnavailable
	}
	if info.Size() > maxDocumentBytes {
		return Catalog{}, IssueLimitExceeded
	}
	if budget.remainingWire() <= 0 || budget.remainingDecoded() <= 0 {
		return Catalog{}, IssueLimitExceeded
	}
	limit := min(maxDocumentBytes, budget.remainingWire(), budget.remainingDecoded())
	data, read, tooLarge, err := readBounded(ctx, file, limit)
	budget.wire += read
	budget.decoded += read
	if tooLarge {
		return Catalog{}, IssueLimitExceeded
	}
	if err != nil {
		return Catalog{}, contextIssue(ctx, ctx, err)
	}
	doc, code := decodeDocument(data, totalRecords)
	return doc, code
}

func (loader *Loader) loadRemote(ctx context.Context, subscription Subscription, budget *byteBudget, totalRecords *int) (Catalog, IssueCode) {
	catalogURL, err := parseCatalogURL(subscription.URL)
	if err != nil {
		return Catalog{}, IssueUnsafeDestination
	}
	wireLimit := min(maxDocumentBytes, budget.remainingWire())
	decodedLimit := min(maxDocumentBytes, budget.remainingDecoded())
	if wireLimit <= 0 || decodedLimit <= 0 {
		return Catalog{}, IssueLimitExceeded
	}
	fetchCtx, cancel := context.WithTimeout(ctx, perFetchTimeout)
	defer cancel()
	resolver := loader.options.Resolver
	if resolver == nil {
		resolver = net.DefaultResolver
	}
	addresses, err := resolver.LookupNetIP(fetchCtx, "ip", catalogURL.Hostname())
	if err != nil {
		return Catalog{}, contextIssue(ctx, fetchCtx, err)
	}
	approved, ok := publicAddresses(addresses)
	if !ok {
		return Catalog{}, IssueUnsafeDestination
	}
	transport := loader.options.RoundTripper
	if transport == nil {
		transport = pinnedTransport(catalogURL.Hostname(), approved, loader.options.DialContext)
	}
	client := &http.Client{
		Transport: transport,
		Timeout:   perFetchTimeout,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	request, err := http.NewRequestWithContext(fetchCtx, http.MethodGet, catalogURL.String(), nil)
	if err != nil {
		return Catalog{}, IssueUnsafeDestination
	}
	request.Header.Set("Accept", "application/json")
	request.Header.Set("Accept-Encoding", "gzip")
	request.Header.Set("User-Agent", "drifter-matching-catalog/1")
	response, err := client.Do(request)
	if err != nil {
		return Catalog{}, contextIssue(ctx, fetchCtx, err)
	}
	if response == nil || response.Body == nil {
		return Catalog{}, IssueUnavailable
	}
	defer response.Body.Close()
	if response.StatusCode >= 300 && response.StatusCode < 400 {
		return Catalog{}, IssueRedirectRejected
	}
	if response.StatusCode != http.StatusOK {
		return Catalog{}, IssueUnavailable
	}
	data, wireRead, decodedRead, tooLarge, readErr := readResponse(fetchCtx, response, wireLimit, decodedLimit)
	budget.wire += wireRead
	budget.decoded += decodedRead
	if tooLarge {
		return Catalog{}, IssueLimitExceeded
	}
	if readErr != nil {
		if errors.Is(readErr, context.Canceled) || errors.Is(readErr, context.DeadlineExceeded) {
			return Catalog{}, contextIssue(ctx, fetchCtx, readErr)
		}
		return Catalog{}, IssueUnavailable
	}
	doc, code := decodeDocument(data, totalRecords)
	return doc, code
}

func readResponse(ctx context.Context, response *http.Response, wireLimit, decodedLimit int64) ([]byte, int64, int64, bool, error) {
	wire := &contextLimitedReader{ctx: ctx, r: response.Body, max: wireLimit}
	var decodedSource io.Reader = wire
	var gzipReader *gzip.Reader
	encoding := strings.TrimSpace(strings.ToLower(response.Header.Get("Content-Encoding")))
	switch encoding {
	case "", "identity":
	case "gzip":
		var err error
		gzipReader, err = gzip.NewReader(wire)
		if err != nil {
			return nil, wire.n, 0, wire.n > wire.max, err
		}
		decodedSource = gzipReader
	default:
		return nil, wire.n, 0, false, fmt.Errorf("unsupported content encoding")
	}
	if gzipReader != nil {
		defer gzipReader.Close()
	}
	data, decodedRead, tooLarge, err := readBounded(ctx, decodedSource, decodedLimit)
	if wire.n > wire.max {
		tooLarge = true
	}
	return data, wire.n, decodedRead, tooLarge, err
}

type contextLimitedReader struct {
	ctx context.Context
	r   io.Reader
	n   int64
	max int64
}

func (reader *contextLimitedReader) Read(buffer []byte) (int, error) {
	if err := reader.ctx.Err(); err != nil {
		return 0, err
	}
	if reader.n > reader.max {
		return 0, io.EOF
	}
	remaining := reader.max + 1 - reader.n
	if int64(len(buffer)) > remaining {
		buffer = buffer[:remaining]
	}
	read, err := reader.r.Read(buffer)
	reader.n += int64(read)
	return read, err
}

func readBounded(ctx context.Context, source io.Reader, limit int64) ([]byte, int64, bool, error) {
	var contents bytes.Buffer
	buffer := make([]byte, 32<<10)
	var read int64
	for {
		if err := ctx.Err(); err != nil {
			return contents.Bytes(), read, false, err
		}
		remaining := limit + 1 - read
		if remaining <= 0 {
			return contents.Bytes(), read, true, nil
		}
		chunk := buffer
		if int64(len(chunk)) > remaining {
			chunk = chunk[:remaining]
		}
		n, err := source.Read(chunk)
		if n > 0 {
			read += int64(n)
			_, _ = contents.Write(chunk[:n])
			if read > limit {
				return contents.Bytes(), read, true, nil
			}
		}
		if err != nil {
			if errors.Is(err, io.EOF) {
				return contents.Bytes(), read, false, nil
			}
			return contents.Bytes(), read, false, err
		}
		if n == 0 {
			return contents.Bytes(), read, false, io.ErrNoProgress
		}
	}
}

func decodeDocument(raw []byte, totalRecords *int) (Catalog, IssueCode) {
	if !utf8.Valid(raw) {
		return Catalog{}, IssueInvalidDocument
	}
	perCatalogRecords := 0
	instance, err := parseStrictJSON(raw, &perCatalogRecords, totalRecords)
	if err != nil {
		if errors.Is(err, errRecordLimit) {
			return Catalog{}, IssueLimitExceeded
		}
		return Catalog{}, IssueInvalidDocument
	}
	root, ok := instance.(map[string]any)
	if !ok {
		return Catalog{}, IssueInvalidDocument
	}
	version, ok := root["catalog_schema_version"].(json.Number)
	if ok && version.String() != "1" {
		return Catalog{}, IssueUnsupportedSchema
	}
	schema, err := schemaV1()
	if err != nil || schema.Validate(instance) != nil {
		return Catalog{}, IssueInvalidDocument
	}
	var doc document
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&doc); err != nil {
		return Catalog{}, IssueInvalidDocument
	}
	if doc.SchemaVersion != 1 {
		return Catalog{}, IssueUnsupportedSchema
	}
	seen := make(map[string]struct{}, len(doc.Assertions)+len(doc.Hints))
	for _, assertion := range doc.Assertions {
		if !validSourceID(assertion.Source.ID) || duplicateRecord(seen, assertion.RecordID) {
			return Catalog{}, IssueInvalidDocument
		}
	}
	for _, hint := range doc.Hints {
		if !validSourceID(hint.Source.ID) || duplicateRecord(seen, hint.RecordID) {
			return Catalog{}, IssueInvalidDocument
		}
		if hint.ISBN != "" {
			if _, err := NormalizeISBN(hint.ISBN); err != nil {
				return Catalog{}, IssueInvalidDocument
			}
		}
	}
	return Catalog{CatalogID: doc.CatalogID, Publisher: doc.Publisher, Assertions: doc.Assertions, Hints: doc.Hints}, ""
}

func duplicateRecord(seen map[string]struct{}, recordID string) bool {
	if _, exists := seen[recordID]; exists {
		return true
	}
	seen[recordID] = struct{}{}
	return false
}

func validSourceID(id string) bool {
	if len(id) == 0 || len(id) > 32 || strings.Trim(id, "0") == "" {
		return false
	}
	for _, digit := range id {
		if digit < '0' || digit > '9' {
			return false
		}
	}
	return true
}

func parseStrictJSON(raw []byte, perCatalogRecords, totalRecords *int) (any, error) {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	value, err := decodeJSONValue(decoder, 0, true, false, perCatalogRecords, totalRecords)
	if err != nil {
		return nil, err
	}
	if _, err := decoder.Token(); !errors.Is(err, io.EOF) {
		return nil, fmt.Errorf("trailing JSON value")
	}
	return value, nil
}

func decodeJSONValue(decoder *json.Decoder, depth int, root, recordArray bool, perCatalogRecords, totalRecords *int) (any, error) {
	if depth > maxJSONDepth {
		return nil, fmt.Errorf("JSON nesting limit exceeded")
	}
	token, err := decoder.Token()
	if err != nil {
		return nil, err
	}
	delim, isDelim := token.(json.Delim)
	if !isDelim {
		return token, nil
	}
	switch delim {
	case '{':
		object := make(map[string]any)
		for decoder.More() {
			keyToken, err := decoder.Token()
			if err != nil {
				return nil, err
			}
			key, ok := keyToken.(string)
			if !ok {
				return nil, fmt.Errorf("invalid object key")
			}
			if _, exists := object[key]; exists {
				return nil, fmt.Errorf("duplicate object key")
			}
			countRecords := root && (key == "assertions" || key == "hints")
			child, err := decodeJSONValue(decoder, depth+1, false, countRecords, perCatalogRecords, totalRecords)
			if err != nil {
				return nil, err
			}
			object[key] = child
		}
		if _, err := decoder.Token(); err != nil {
			return nil, err
		}
		return object, nil
	case '[':
		array := make([]any, 0)
		for decoder.More() {
			if recordArray {
				(*perCatalogRecords)++
				(*totalRecords)++
				if *perCatalogRecords > MaxRecordsPerCatalog || *totalRecords > maxRecordsTotal {
					return nil, errRecordLimit
				}
			}
			child, err := decodeJSONValue(decoder, depth+1, false, false, perCatalogRecords, totalRecords)
			if err != nil {
				return nil, err
			}
			array = append(array, child)
		}
		if _, err := decoder.Token(); err != nil {
			return nil, err
		}
		return array, nil
	default:
		return nil, fmt.Errorf("invalid JSON delimiter")
	}
}

var errRecordLimit = errors.New("catalog record limit exceeded")

func schemaV1() (*jsonschema.Schema, error) {
	compileSchemaOnce.Do(func() {
		compiler := jsonschema.NewCompiler()
		compiler.DefaultDraft(jsonschema.Draft2020)
		compiler.UseLoader(denySchemaLoader{})
		var schema any
		decoder := json.NewDecoder(bytes.NewReader(schemas.MatchingCatalogV1))
		decoder.UseNumber()
		if err := decoder.Decode(&schema); err != nil {
			compileSchemaErr = err
			return
		}
		if err := compiler.AddResource(catalogSchemaID, schema); err != nil {
			compileSchemaErr = err
			return
		}
		compiledSchema, compileSchemaErr = compiler.Compile(catalogSchemaID)
	})
	return compiledSchema, compileSchemaErr
}

type denySchemaLoader struct{}

func (denySchemaLoader) Load(string) (any, error) {
	return nil, fmt.Errorf("external schema references are disabled")
}

func addIssue(snapshot *Snapshot, alias string, code IssueCode) {
	snapshot.Complete = false
	snapshot.Issues = append(snapshot.Issues, Issue{SubscriptionID: alias, Code: code})
}

func contextIssue(parent, operation context.Context, err error) IssueCode {
	if errors.Is(parent.Err(), context.Canceled) || errors.Is(err, context.Canceled) {
		return IssueCancelled
	}
	if errors.Is(parent.Err(), context.DeadlineExceeded) || errors.Is(operation.Err(), context.DeadlineExceeded) || errors.Is(err, context.DeadlineExceeded) {
		return IssueTimeout
	}
	return IssueUnavailable
}

func publicAddresses(addresses []netip.Addr) ([]netip.Addr, bool) {
	if len(addresses) == 0 || len(addresses) > maxDNSAnswers {
		return nil, false
	}
	approved := make([]netip.Addr, 0, len(addresses))
	seen := make(map[netip.Addr]struct{}, len(addresses))
	for _, address := range addresses {
		if !globallyRoutable(address) {
			return nil, false
		}
		if _, exists := seen[address]; !exists {
			seen[address] = struct{}{}
			approved = append(approved, address)
		}
	}
	sort.Slice(approved, func(i, j int) bool { return approved[i].Less(approved[j]) })
	return approved, len(approved) > 0
}

func globallyRoutable(address netip.Addr) bool {
	if !address.IsValid() || address.Zone() != "" || address.Is4In6() || !address.IsGlobalUnicast() || address.IsPrivate() || address.IsLoopback() || address.IsLinkLocalUnicast() || address.IsMulticast() || address.IsUnspecified() {
		return false
	}
	for _, prefix := range globalAddressDeny {
		if prefix.Contains(address) {
			return false
		}
	}
	return true
}

func pinnedTransport(host string, addresses []netip.Addr, dialContext DialContextFunc) *http.Transport {
	if dialContext == nil {
		dialer := &net.Dialer{Timeout: perFetchTimeout, KeepAlive: -1}
		dialContext = dialer.DialContext
	}
	pinned := append([]netip.Addr(nil), addresses...)
	return &http.Transport{
		Proxy:               nil,
		DisableCompression:  true,
		DisableKeepAlives:   true,
		TLSClientConfig:     &tls.Config{MinVersion: tls.VersionTLS12, ServerName: host},
		TLSHandshakeTimeout: perFetchTimeout,
		DialContext: func(ctx context.Context, network, address string) (net.Conn, error) {
			if network != "tcp" && network != "tcp4" && network != "tcp6" {
				return nil, publicOnly
			}
			requestHost, requestPort, err := net.SplitHostPort(address)
			if err != nil || !strings.EqualFold(requestHost, host) || requestPort != "443" {
				return nil, publicOnly
			}
			var lastErr error
			for _, ip := range pinned {
				if network == "tcp4" && !ip.Is4() || network == "tcp6" && !ip.Is6() {
					continue
				}
				connection, err := dialContext(ctx, network, net.JoinHostPort(ip.String(), "443"))
				if err == nil {
					return connection, nil
				}
				lastErr = err
			}
			if lastErr == nil {
				lastErr = publicOnly
			}
			return nil, lastErr
		},
	}
}

func validAlias(alias string) bool {
	if len(alias) == 0 || len(alias) > 64 || !isLowerAlphaNumeric(alias[0]) {
		return false
	}
	for _, char := range alias[1:] {
		if !isLowerAlphaNumeric(byte(char)) && char != '.' && char != '_' && char != '-' {
			return false
		}
	}
	return true
}

func isLowerAlphaNumeric(value byte) bool {
	return value >= 'a' && value <= 'z' || value >= '0' && value <= '9'
}

func parseCatalogURL(raw string) (*url.URL, error) {
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Scheme != "https" || parsed.Opaque != "" || parsed.Host == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.ForceQuery || parsed.Fragment != "" || strings.Contains(raw, "#") {
		return nil, publicOnly
	}
	if port := parsed.Port(); port != "" && port != "443" {
		return nil, publicOnly
	}
	host := parsed.Hostname()
	if !validDNSHost(host) {
		return nil, publicOnly
	}
	parsed.Host = strings.ToLower(host)
	if parsed.Path == "" {
		parsed.Path = "/"
	}
	return parsed, nil
}

func validDNSHost(host string) bool {
	if len(host) == 0 || len(host) > 253 || net.ParseIP(host) != nil || strings.HasSuffix(host, ".") {
		return false
	}
	lower := strings.ToLower(host)
	for _, suffix := range []string{".localhost", ".local", ".localdomain", ".internal", ".home", ".home.arpa", ".lan", ".test", ".invalid", ".example", ".onion", ".corp", ".intranet"} {
		if lower == strings.TrimPrefix(suffix, ".") || strings.HasSuffix(lower, suffix) {
			return false
		}
	}
	for _, label := range strings.Split(lower, ".") {
		if len(label) == 0 || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return false
		}
		for _, char := range label {
			if !(char >= 'a' && char <= 'z' || char >= '0' && char <= '9' || char == '-') {
				return false
			}
		}
	}
	return strings.Contains(lower, ".")
}
