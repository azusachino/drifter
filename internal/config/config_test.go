package config

import (
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"

	"github.com/azusachino/drifter/internal/catalog"
)

func write(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestLoadDefaultsWhenFileMissing(t *testing.T) {
	cfg, err := Load(filepath.Join(t.TempDir(), "absent.toml"))
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(cfg, Defaults()) {
		t.Fatalf("unexpected defaults: %+v", cfg)
	}
	if cfg.Log.Level != LevelInfo || cfg.Probes.Addr != "127.0.0.1:8080" || cfg.Server.Addr != "127.0.0.1:8081" || cfg.Probes.Enabled {
		t.Fatalf("unexpected defaults: %+v", cfg)
	}
}

func TestLoadValidConfiguration(t *testing.T) {
	cfg, err := Load(write(t, `
[log]
level = "debug"

[probes]
enabled = true
addr = "0.0.0.0:9090"

[server]
addr = "127.0.0.1:9091"

[database]
path = "/var/lib/drifter/mappings.sqlite"
`))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Log.Level != LevelDebug || !cfg.Probes.Enabled || cfg.Probes.Addr != "0.0.0.0:9090" || cfg.Server.Addr != "127.0.0.1:9091" ||
		cfg.Database.Path != "/var/lib/drifter/mappings.sqlite" {
		t.Fatalf("unexpected config: %+v", cfg)
	}
}

func TestLoadAcceptsWildcardServerWithAllowedHosts(t *testing.T) {
	cfg, err := Load(write(t, `
[server]
addr = "0.0.0.0:8081"
allowed_hosts = ["drifter.harus-media", "drifter.harus-media:8081"]
`))
	if err != nil {
		t.Fatalf("wildcard listener with allowed_hosts rejected: %v", err)
	}
	if cfg.Server.Addr != "0.0.0.0:8081" || len(cfg.Server.AllowedHosts) != 2 {
		t.Fatalf("unexpected config: %+v", cfg)
	}
}

func TestLoadRejectsInvalidOrUnsafeConfiguration(t *testing.T) {
	for name, content := range map[string]string{
		"unknown-key":             "[log]\nlevel = \"info\"\n\n[sync]\nauto_write = true\n",
		"bad-level":               "[log]\nlevel = \"loud\"\n",
		"bad-addr":                "[probes]\naddr = \"9090\"\n",
		"bad-server":              "[server]\naddr = \"0.0.0.0\"\n",
		"wildcard-server":         "[server]\naddr = \":8081\"\n",
		"star-wildcard-server":    "[server]\naddr = \"*:8081\"\n",
		"unspecified-server":      "[server]\naddr = \"0.0.0.0:8081\"\n",
		"wildcard-blank-hosts":    "[server]\naddr = \"0.0.0.0:8081\"\nallowed_hosts = [\"  \"]\n",
		"ipv6-unspecified-server": "[server]\naddr = \"[::]:8081\"\n",
		"empty-addr":              "[probes]\naddr = \"\"\n",
		"relative-db":             "[database]\npath = \"drifter.sqlite\"\n",
		"api-key":                 "[providers]\napi_key = \"synthetic-secret\"\n",
		"token":                   "hardcover_token = \"synthetic-secret\"\n",
		"password-ish":            "[database]\npassword_file = \"/etc/pass\"\n",
		"not-toml":                "```synthetic```\n",
	} {
		t.Run(name, func(t *testing.T) {
			cfg, err := Load(write(t, content))
			if err == nil {
				t.Fatalf("invalid configuration accepted: %+v", cfg)
			}
			if strings.Contains(err.Error(), "synthetic-secret") {
				t.Fatal("error echoed secret")
			}
		})
	}
}

func TestLoadMatchingCatalogSubscriptions(t *testing.T) {
	catalogPath := filepath.Join(t.TempDir(), "$HOME", "catalog.json")
	contents := "[[matching.catalogs]]\nid = \"local-crosswalk\"\nfile = " + strconv.Quote(catalogPath) + "\n\n" + `[[matching.catalogs]]
id = "community-crosswalk"
url = "https://catalog.example.org/drifter/v1.json"
`
	cfg, err := Load(write(t, contents))
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.Matching.Catalogs) != 2 || cfg.Matching.Catalogs[0] != (catalog.Subscription{ID: "local-crosswalk", File: catalogPath}) || cfg.Matching.Catalogs[1].URL != "https://catalog.example.org/drifter/v1.json" {
		t.Fatalf("matching catalog subscriptions were not loaded literally: %+v", cfg.Matching.Catalogs)
	}
}

func TestLoadRejectsInvalidMatchingCatalogSubscriptions(t *testing.T) {
	for name, content := range map[string]string{
		"relative-file": `[[matching.catalogs]]
id = "local"
file = "catalog.json"
`,
		"query-url": `[[matching.catalogs]]
id = "remote"
url = "https://catalog.example.org/catalog.json?token=secret"
`,
		"duplicate-alias": `[[matching.catalogs]]
id = "same"
file = "/tmp/a.json"
[[matching.catalogs]]
id = "same"
file = "/tmp/b.json"
`,
		"both-source-types": `[[matching.catalogs]]
id = "same"
file = "/tmp/a.json"
url = "https://catalog.example.org/catalog.json"
`,
		"no-source": `[[matching.catalogs]]
id = "same"
`,
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := Load(write(t, content)); err == nil {
				t.Fatal("invalid matching catalog subscription accepted")
			}
		})
	}
}

func TestLoadReportsUnreadableFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	if err := os.Mkdir(path, 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(path); err == nil {
		t.Fatal("directory accepted as configuration")
	}
}

func TestEmptyPathRejected(t *testing.T) {
	if _, err := Load("  "); err == nil {
		t.Fatal("empty path accepted")
	}
}

func TestLoadISBNConfiguration(t *testing.T) {
	t.Run("default-values", func(t *testing.T) {
		cfg := Defaults()
		if cfg.Matching.ISBN.Enabled || cfg.Matching.ISBN.Provider != "default" {
			t.Fatalf("unexpected default ISBN config: %+v", cfg.Matching.ISBN)
		}
	})

	t.Run("custom-valid", func(t *testing.T) {
		content := `[matching.isbn]
enabled = true
provider = "custom"
url = "https://isbn.example.com/books/{isbn}"
`
		cfg, err := Load(write(t, content))
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !cfg.Matching.ISBN.Enabled || cfg.Matching.ISBN.Provider != "custom" || cfg.Matching.ISBN.URL != "https://isbn.example.com/books/{isbn}" {
			t.Fatalf("unexpected ISBN config: %+v", cfg.Matching.ISBN)
		}
	})

	t.Run("douban-and-disabled", func(t *testing.T) {
		content := `[matching.isbn]
enabled = false
provider = "douban"
`
		cfg, err := Load(write(t, content))
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if cfg.Matching.ISBN.Enabled || cfg.Matching.ISBN.Provider != "douban" {
			t.Fatalf("unexpected ISBN config: %+v", cfg.Matching.ISBN)
		}
	})
}

func TestLoadRejectsInvalidISBNConfiguration(t *testing.T) {
	cases := map[string]string{
		"unknown-provider": `[matching.isbn]
provider = "invalid-provider"
`,
		"custom-missing-url": `[matching.isbn]
provider = "custom"
url = ""
`,
		"custom-url-no-placeholder": `[matching.isbn]
provider = "custom"
url = "https://isbn.example.com/lookup"
`,
		"custom-url-invalid-scheme": `[matching.isbn]
provider = "custom"
url = "ftp://isbn.example.com/{isbn}"
`,
	}

	for name, content := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := Load(write(t, content)); err == nil {
				t.Fatalf("invalid ISBN config accepted: %s", name)
			}
		})
	}
}

func TestLoadAdvisorConfiguration(t *testing.T) {
	t.Run("default-values", func(t *testing.T) {
		cfg := Defaults()
		if cfg.Advisor.BaseURL != "" || cfg.Advisor.Model != "" || cfg.Advisor.Timeout != "" {
			t.Fatalf("unexpected default advisor config: %+v", cfg.Advisor)
		}
	})

	t.Run("valid-advisor-config", func(t *testing.T) {
		content := `[advisor]
base_url = "https://magpie.example.com/v1"
model = "opencode-go/beam-free"
timeout = "45s"
`
		cfg, err := Load(write(t, content))
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if cfg.Advisor.BaseURL != "https://magpie.example.com/v1" || cfg.Advisor.Model != "opencode-go/beam-free" || cfg.Advisor.Timeout != "45s" {
			t.Fatalf("unexpected advisor config: %+v", cfg.Advisor)
		}
	})
}

func TestLoadRejectsInvalidAdvisorConfiguration(t *testing.T) {
	cases := map[string]string{
		"invalid-url": `[advisor]
base_url = "not-a-valid-url"
`,
		"ftp-scheme": `[advisor]
base_url = "ftp://magpie.example.com/v1"
`,
		"invalid-timeout": `[advisor]
timeout = "forever"
`,
		"negative-timeout": `[advisor]
timeout = "-5s"
`,
		"api-key-rejected": `[advisor]
api_key = "secret-token"
`,
	}

	for name, content := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := Load(write(t, content)); err == nil {
				t.Fatalf("invalid advisor config accepted: %s", name)
			}
		})
	}
}

func TestDefaultPath(t *testing.T) {
	t.Run("env-override", func(t *testing.T) {
		custom := filepath.Join(t.TempDir(), "custom.toml")
		t.Setenv("DRIFTER_CONFIG", custom)
		if got := DefaultPath(); got != custom {
			t.Fatalf("DefaultPath() = %q, want %q", got, custom)
		}
	})

	t.Run("xdg-config-home", func(t *testing.T) {
		t.Setenv("DRIFTER_CONFIG", "")
		tmp := t.TempDir()
		t.Setenv("XDG_CONFIG_HOME", tmp)
		drifterDir := filepath.Join(tmp, "drifter")
		if err := os.MkdirAll(drifterDir, 0o700); err != nil {
			t.Fatal(err)
		}
		cfgPath := filepath.Join(drifterDir, "config.toml")
		if err := os.WriteFile(cfgPath, []byte("[log]\nlevel = \"info\"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		// If config.toml is not in cwd, it finds xdg path.
		origDir, _ := os.Getwd()
		emptyDir := t.TempDir()
		if err := os.Chdir(emptyDir); err != nil {
			t.Fatal(err)
		}
		defer func() { _ = os.Chdir(origDir) }()

		if got := DefaultPath(); got != cfgPath {
			t.Fatalf("DefaultPath() = %q, want %q", got, cfgPath)
		}
	})
}

func TestLoadConfigurableOperationalSettings(t *testing.T) {
	cfg, err := Load(write(t, `
[server]
addr = "127.0.0.1:8082"
operation_timeout = "3m"
read_header_timeout = "10s"
read_timeout = "30s"
write_timeout = "4m"
idle_timeout = "120s"
shutdown_timeout = "20s"

[jobs]
poll_interval = "10s"
batch_size = 12
book_step_timeout = "2m"
reconcile_timeout = "30m"
cooldown_base = "20s"
cooldown_max = "2m"
max_cooldown_wait = "5m"
max_rate_limit_retry = 8
advisor_pace_delay = "0s"

[cache]
max_entries = 25000
shelf_ttl = "10m"
book_ttl = "12h"
search_ttl = "30m"
target_ttl = "45m"

[providers]
timeout = "45s"
weread_interval = "1500ms"
hardcover_interval = "2s"

[advisor]
base_url = "https://gateway.example.com/v1"
model = "primary-model"
models = ["primary-model", "fallback-model"]
timeout = "60s"
cache_ttl = "168h"
`))
	if err != nil {
		t.Fatalf("valid operational config rejected: %v", err)
	}
	if cfg.Server.OperationTimeout != "3m" || cfg.Server.WriteTimeout != "4m" || cfg.Server.ShutdownTimeout != "20s" {
		t.Fatalf("unexpected server config: %+v", cfg.Server)
	}
	if cfg.Jobs.BatchSize != 12 || cfg.Jobs.ReconcileTimeout != "30m" || cfg.Jobs.AdvisorPaceDelay != "0s" || cfg.Jobs.MaxRateLimitRetry != 8 {
		t.Fatalf("unexpected jobs config: %+v", cfg.Jobs)
	}
	if cfg.Cache.MaxEntries != 25000 || cfg.Cache.ShelfTTL != "10m" || cfg.Cache.BookTTL != "12h" {
		t.Fatalf("unexpected cache config: %+v", cfg.Cache)
	}
	if cfg.Providers.Timeout != "45s" || cfg.Providers.WeReadInterval != "1500ms" || cfg.Providers.HardcoverInterval != "2s" {
		t.Fatalf("unexpected providers config: %+v", cfg.Providers)
	}
	if len(cfg.Advisor.Models) != 2 || cfg.Advisor.Models[1] != "fallback-model" || cfg.Advisor.CacheTTL != "168h" {
		t.Fatalf("unexpected advisor config: %+v", cfg.Advisor)
	}
}

func TestLoadRejectsInvalidOperationalSettings(t *testing.T) {
	cases := map[string]string{
		"zero-operation-timeout": "[server]\noperation_timeout = \"0s\"\n",
		"bad-reconcile-timeout":  "[jobs]\nreconcile_timeout = \"soon\"\n",
		"cooldown-max-lt-base":   "[jobs]\ncooldown_base = \"30s\"\ncooldown_max = \"10s\"\n",
		"negative-pace-delay":    "[jobs]\nadvisor_pace_delay = \"-1s\"\n",
		"zero-batch-size":        "[jobs]\nbatch_size = 0\n",
		"zero-rate-limit-retry":  "[jobs]\nmax_rate_limit_retry = 0\n",
		"zero-cache-entries":     "[cache]\nmax_entries = 0\n",
		"zero-shelf-ttl":         "[cache]\nshelf_ttl = \"0s\"\n",
		"zero-provider-timeout":  "[providers]\ntimeout = \"0s\"\n",
		"zero-advisor-cache-ttl": "[advisor]\ncache_ttl = \"0s\"\n",
	}
	for name, content := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := Load(write(t, content)); err == nil {
				t.Fatalf("invalid operational config accepted: %s", name)
			}
		})
	}
}
