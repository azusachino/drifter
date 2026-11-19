// Package config loads Drifter's settings-only TOML configuration.
//
// Configuration never carries credentials: they belong in the environment.
// The file is validated entirely at load time; callers decide when to apply
// it (Drifter's confirmed direction is restart-only, no live reload).
package config

import (
	"errors"
	"fmt"
	"io/fs"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unicode"

	"github.com/BurntSushi/toml"
	"github.com/azusachino/drifter/internal/catalog"
)

// Level is a logging severity threshold.
type Level string

// Supported log levels.
const (
	LevelDebug Level = "debug"
	LevelInfo  Level = "info"
	LevelWarn  Level = "warn"
	LevelError Level = "error"
)

// ISBNConfig configures authoritative ISBN metadata provider lookups.
type ISBNConfig struct {
	Enabled  bool   `toml:"enabled"`
	Provider string `toml:"provider"`
	URL      string `toml:"url"`
}

// AdvisorConfig configures the optional OpenCode LLM advisor.
// Credentials (API keys) never belong in the configuration file;
// they come strictly from the environment.
type AdvisorConfig struct {
	BaseURL  string   `toml:"base_url"`
	Model    string   `toml:"model"`
	Models   []string `toml:"models"`
	Timeout  string   `toml:"timeout"`
	CacheTTL string   `toml:"cache_ttl"`
}

// ServerConfig configures the local HTTP server listener and timeouts.
type ServerConfig struct {
	Addr              string   `toml:"addr"`
	AllowedHosts      []string `toml:"allowed_hosts"`
	OperationTimeout  string   `toml:"operation_timeout"`
	ReadHeaderTimeout string   `toml:"read_header_timeout"`
	ReadTimeout       string   `toml:"read_timeout"`
	WriteTimeout      string   `toml:"write_timeout"`
	IdleTimeout       string   `toml:"idle_timeout"`
	ShutdownTimeout   string   `toml:"shutdown_timeout"`
}

// JobsConfig configures the background sync job worker budgets and pacing.
type JobsConfig struct {
	PollInterval      string `toml:"poll_interval"`
	BatchSize         int    `toml:"batch_size"`
	BookStepTimeout   string `toml:"book_step_timeout"`
	ReconcileTimeout  string `toml:"reconcile_timeout"`
	CooldownBase      string `toml:"cooldown_base"`
	CooldownMax       string `toml:"cooldown_max"`
	MaxCooldownWait   string `toml:"max_cooldown_wait"`
	MaxRateLimitRetry int    `toml:"max_rate_limit_retry"`
	AdvisorPaceDelay  string `toml:"advisor_pace_delay"`
	AttemptCooldown   string `toml:"attempt_cooldown"`
}

// CacheConfig configures the in-memory provider response TTL cache.
type CacheConfig struct {
	MaxEntries int    `toml:"max_entries"`
	ShelfTTL   string `toml:"shelf_ttl"`
	BookTTL    string `toml:"book_ttl"`
	SearchTTL  string `toml:"search_ttl"`
	TargetTTL  string `toml:"target_ttl"`
}

// ProvidersConfig configures HTTP client timeouts and rate-limit pacing for
// upstream providers. Provider URLs remain fixed HTTPS endpoints.
type ProvidersConfig struct {
	Timeout            string `toml:"timeout"`
	WeReadInterval     string `toml:"weread_interval"`
	HardcoverInterval  string `toml:"hardcover_interval"`
	WeReadSkillVersion string `toml:"weread_skill_version"`
}

// Config is the complete validated settings file. Zero values become
// documented defaults in Load.
type Config struct {
	Log struct {
		Level Level `toml:"level"`
	} `toml:"log"`
	Probes struct {
		Enabled bool   `toml:"enabled"`
		Addr    string `toml:"addr"`
	} `toml:"probes"`
	Server   ServerConfig `toml:"server"`
	Database struct {
		// Path overrides the CLI default mapping database location.
		Path string `toml:"path"`
	} `toml:"database"`
	Matching struct {
		Catalogs []catalog.Subscription `toml:"catalogs"`
		ISBN     ISBNConfig             `toml:"isbn"`
	} `toml:"matching"`
	Jobs      JobsConfig      `toml:"jobs"`
	Cache     CacheConfig     `toml:"cache"`
	Providers ProvidersConfig `toml:"providers"`
	Advisor   AdvisorConfig   `toml:"advisor"`
}

// Defaults returns the configuration used when no file is present.
func Defaults() Config {
	var c Config
	c.Log.Level = LevelInfo
	c.Probes.Enabled = false
	c.Probes.Addr = "127.0.0.1:8080"
	c.Server = ServerConfig{
		Addr:              "127.0.0.1:8081",
		OperationTimeout:  "90s",
		ReadHeaderTimeout: "5s",
		ReadTimeout:       "15s",
		WriteTimeout:      "2m",
		IdleTimeout:       "60s",
		ShutdownTimeout:   "10s",
	}
	c.Matching.ISBN.Enabled = false
	c.Matching.ISBN.Provider = "default"
	c.Jobs = JobsConfig{
		PollInterval:      "5s",
		BatchSize:         5,
		BookStepTimeout:   "75s",
		ReconcileTimeout:  "15m",
		CooldownBase:      "15s",
		CooldownMax:       "90s",
		MaxCooldownWait:   "2m",
		MaxRateLimitRetry: 5,
		AdvisorPaceDelay:  "1s",
		AttemptCooldown:   "720h",
	}
	c.Cache = CacheConfig{
		MaxEntries: 10000,
		ShelfTTL:   "5m",
		BookTTL:    "6h",
		SearchTTL:  "1h",
		TargetTTL:  "1h",
	}
	c.Providers = ProvidersConfig{
		Timeout:            "30s",
		WeReadInterval:     "1s",
		HardcoverInterval:  "1100ms",
		WeReadSkillVersion: "1.0.5",
	}
	c.Advisor.CacheTTL = "720h"
	return c
}

// ParseDurationOr parses raw as a Go duration, returning fallback when raw is
// empty or invalid. Callers normally pass values already validated by Load.
func ParseDurationOr(raw string, fallback time.Duration) time.Duration {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return fallback
	}
	d, err := time.ParseDuration(raw)
	if err != nil {
		return fallback
	}
	return d
}

// secretKeyPatterns are key fragments that must never appear in a settings
// file. Matching is deliberately broad: config holds behavior, not secrets.
var secretKeyPatterns = []string{"api_key", "apikey", "token", "secret", "password", "credential"}

func rejectEmbeddedSecrets(raw string) error {
	for lineNo, line := range strings.Split(raw, "\n") {
		name, _, found := strings.Cut(line, "=")
		if !found {
			continue
		}
		key := strings.ToLower(strings.TrimSpace(name))
		for _, pattern := range secretKeyPatterns {
			if strings.Contains(key, pattern) {
				return fmt.Errorf("configuration line %d looks like a credential; keys and tokens belong in the environment, never in config.toml", lineNo+1)
			}
		}
	}
	return nil
}

// Load reads and fully validates the TOML file at path. A missing file is
// allowed and yields Defaults; every other read/parse/validation failure is
// an error. Unknown keys are rejected so typos cannot silently disable
// behavior.
func Load(path string) (Config, error) {
	if strings.TrimSpace(path) == "" {
		return Config{}, fmt.Errorf("configuration path required")
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return Defaults(), nil
		}
		return Config{}, fmt.Errorf("configuration unreadable")
	}
	if err := rejectEmbeddedSecrets(string(raw)); err != nil {
		return Config{}, err
	}
	cfg := Defaults()
	meta, err := toml.Decode(string(raw), &cfg)
	if err != nil {
		return Config{}, fmt.Errorf("configuration is not valid TOML for Drifter")
	}
	if unknown := meta.Undecoded(); len(unknown) > 0 {
		names := make([]string, 0, len(unknown))
		for _, key := range unknown {
			names = append(names, key.String())
		}
		return Config{}, fmt.Errorf("unknown configuration keys: %s", strings.Join(names, ", "))
	}
	if err := cfg.validate(); err != nil {
		return Config{}, err
	}
	return cfg, nil
}

func validateAddress(name, address string) error {
	host, port, err := net.SplitHostPort(address)
	if err != nil || port == "" {
		return fmt.Errorf("%s must be host:port", name)
	}
	if strings.ContainsAny(host, "/\\\\@?#") {
		return fmt.Errorf("%s must be a listener address", name)
	}
	return nil
}

func hasAllowedHost(hosts []string) bool {
	for _, h := range hosts {
		if strings.TrimSpace(h) != "" {
			return true
		}
	}
	return false
}

func validatePositiveDuration(name, raw string) (time.Duration, error) {
	d, err := time.ParseDuration(strings.TrimSpace(raw))
	if err != nil || d <= 0 {
		return 0, fmt.Errorf("%s must be a positive duration (e.g. 30s)", name)
	}
	return d, nil
}

func validateNonNegativeDuration(name, raw string) (time.Duration, error) {
	d, err := time.ParseDuration(strings.TrimSpace(raw))
	if err != nil || d < 0 {
		return 0, fmt.Errorf("%s must be a non-negative duration (e.g. 1s or 0s)", name)
	}
	return d, nil
}

func validateSkillVersion(name, raw string) error {
	if raw == "" || len(raw) > 32 {
		return fmt.Errorf("%s must be a non-empty string of at most 32 characters", name)
	}
	for _, r := range raw {
		if unicode.IsSpace(r) || unicode.IsControl(r) {
			return fmt.Errorf("%s must not contain whitespace or control characters", name)
		}
	}
	return nil
}

func (c Config) validate() error {
	switch c.Log.Level {
	case LevelDebug, LevelInfo, LevelWarn, LevelError:
	default:
		return fmt.Errorf("log.level must be one of debug, info, warn, error")
	}
	if err := validateAddress("probes.addr", c.Probes.Addr); err != nil {
		return err
	}
	if err := validateAddress("server.addr", c.Server.Addr); err != nil {
		return err
	}
	serverHost, _, _ := net.SplitHostPort(c.Server.Addr)
	serverIP := net.ParseIP(serverHost)
	wildcard := serverHost == "" || serverHost == "*" || (serverIP != nil && serverIP.IsUnspecified())
	// A wildcard listener is allowed only with an explicit Host allowlist, which
	// is what Host validation checks; without one there is nothing to validate against.
	if wildcard && !hasAllowedHost(c.Server.AllowedHosts) {
		return fmt.Errorf("server.addr must name a concrete listener host, or set server.allowed_hosts for a wildcard listener")
	}
	for _, h := range c.Server.AllowedHosts {
		if strings.TrimSpace(h) == "" || strings.ContainsAny(h, "/\\@?#") {
			return fmt.Errorf("server.allowed_hosts entries must be valid host[:port] strings")
		}
	}
	for _, field := range []struct{ name, val string }{
		{"server.operation_timeout", c.Server.OperationTimeout},
		{"server.read_header_timeout", c.Server.ReadHeaderTimeout},
		{"server.read_timeout", c.Server.ReadTimeout},
		{"server.write_timeout", c.Server.WriteTimeout},
		{"server.idle_timeout", c.Server.IdleTimeout},
		{"server.shutdown_timeout", c.Server.ShutdownTimeout},
		{"jobs.poll_interval", c.Jobs.PollInterval},
		{"jobs.book_step_timeout", c.Jobs.BookStepTimeout},
		{"jobs.reconcile_timeout", c.Jobs.ReconcileTimeout},
		{"jobs.max_cooldown_wait", c.Jobs.MaxCooldownWait},
		{"jobs.attempt_cooldown", c.Jobs.AttemptCooldown},
		{"cache.shelf_ttl", c.Cache.ShelfTTL},
		{"cache.book_ttl", c.Cache.BookTTL},
		{"cache.search_ttl", c.Cache.SearchTTL},
		{"cache.target_ttl", c.Cache.TargetTTL},
		{"providers.timeout", c.Providers.Timeout},
		{"providers.weread_interval", c.Providers.WeReadInterval},
		{"providers.hardcover_interval", c.Providers.HardcoverInterval},
		{"advisor.cache_ttl", c.Advisor.CacheTTL},
	} {
		if _, err := validatePositiveDuration(field.name, field.val); err != nil {
			return err
		}
	}
	cooldownBase, err := validatePositiveDuration("jobs.cooldown_base", c.Jobs.CooldownBase)
	if err != nil {
		return err
	}
	cooldownMax, err := validatePositiveDuration("jobs.cooldown_max", c.Jobs.CooldownMax)
	if err != nil {
		return err
	}
	if cooldownMax < cooldownBase {
		return fmt.Errorf("jobs.cooldown_max must be greater than or equal to jobs.cooldown_base")
	}
	if _, err := validateNonNegativeDuration("jobs.advisor_pace_delay", c.Jobs.AdvisorPaceDelay); err != nil {
		return err
	}
	if err := validateSkillVersion("providers.weread_skill_version", c.Providers.WeReadSkillVersion); err != nil {
		return err
	}
	if c.Jobs.BatchSize <= 0 {
		return fmt.Errorf("jobs.batch_size must be positive")
	}
	if c.Jobs.MaxRateLimitRetry <= 0 {
		return fmt.Errorf("jobs.max_rate_limit_retry must be positive")
	}
	if c.Cache.MaxEntries <= 0 {
		return fmt.Errorf("cache.max_entries must be positive")
	}
	if strings.TrimSpace(c.Database.Path) != "" && !filepath.IsAbs(c.Database.Path) {
		return fmt.Errorf("database.path must be absolute")
	}
	if err := catalog.ValidateSubscriptions(c.Matching.Catalogs); err != nil {
		return err
	}
	switch strings.ToLower(strings.TrimSpace(c.Matching.ISBN.Provider)) {
	case "", "default", "douban", "openlibrary", "custom":
	default:
		return fmt.Errorf("matching.isbn.provider must be one of default, douban, openlibrary, custom")
	}
	if strings.ToLower(strings.TrimSpace(c.Matching.ISBN.Provider)) == "custom" {
		if strings.TrimSpace(c.Matching.ISBN.URL) == "" || !strings.Contains(c.Matching.ISBN.URL, "{isbn}") {
			return fmt.Errorf("matching.isbn.url must contain {isbn} when provider is custom")
		}
		u, err := url.Parse(c.Matching.ISBN.URL)
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
			return fmt.Errorf("matching.isbn.url must be a valid http or https URL")
		}
	}
	if strings.TrimSpace(c.Advisor.BaseURL) != "" {
		u, err := url.Parse(strings.TrimSpace(c.Advisor.BaseURL))
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
			return fmt.Errorf("advisor.base_url must be a valid http or https URL")
		}
	}
	if strings.TrimSpace(c.Advisor.Timeout) != "" {
		if _, err := validatePositiveDuration("advisor.timeout", c.Advisor.Timeout); err != nil {
			return err
		}
	}
	return nil
}

// DefaultPath returns the resolved path to Drifter's settings file.
//
// Resolution order:
//  1. DRIFTER_CONFIG environment variable if set and non-empty.
//  2. "config.toml" in the current working directory if it exists.
//  3. $XDG_CONFIG_HOME/drifter/config.toml (or ~/.config/drifter/config.toml) if it exists.
//  4. Fallback to "config.toml" (which Load treats as Defaults when missing).
func DefaultPath() string {
	if custom := os.Getenv("DRIFTER_CONFIG"); strings.TrimSpace(custom) != "" {
		return strings.TrimSpace(custom)
	}
	if _, err := os.Stat("config.toml"); err == nil {
		return "config.toml"
	}
	base := os.Getenv("XDG_CONFIG_HOME")
	if base == "" {
		if home, err := os.UserHomeDir(); err == nil {
			base = filepath.Join(home, ".config")
		}
	}
	if base != "" {
		xdgPath := filepath.Join(base, "drifter", "config.toml")
		if _, err := os.Stat(xdgPath); err == nil {
			return xdgPath
		}
	}
	return "config.toml"
}
