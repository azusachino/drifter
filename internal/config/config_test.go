package config

import (
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"

	"github.com/azusachino/drifter/internal/matchingcatalog"
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

func TestLoadRejectsInvalidOrUnsafeConfiguration(t *testing.T) {
	for name, content := range map[string]string{
		"unknown-key":             "[log]\nlevel = \"info\"\n\n[sync]\nauto_write = true\n",
		"bad-level":               "[log]\nlevel = \"loud\"\n",
		"bad-addr":                "[probes]\naddr = \"9090\"\n",
		"bad-server":              "[server]\naddr = \"0.0.0.0\"\n",
		"wildcard-server":         "[server]\naddr = \":8081\"\n",
		"star-wildcard-server":    "[server]\naddr = \"*:8081\"\n",
		"unspecified-server":      "[server]\naddr = \"0.0.0.0:8081\"\n",
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
	if len(cfg.Matching.Catalogs) != 2 || cfg.Matching.Catalogs[0] != (matchingcatalog.Subscription{ID: "local-crosswalk", File: catalogPath}) || cfg.Matching.Catalogs[1].URL != "https://catalog.example.org/drifter/v1.json" {
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
