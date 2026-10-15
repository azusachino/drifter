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
	"os"
	"path/filepath"
	"strings"

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
	Server struct {
		Addr string `toml:"addr"`
	} `toml:"server"`
	Database struct {
		// Path overrides the CLI default mapping database location.
		Path string `toml:"path"`
	} `toml:"database"`
	Matching struct {
		Catalogs []catalog.Subscription `toml:"catalogs"`
	} `toml:"matching"`
}

// Defaults returns the configuration used when no file is present.
func Defaults() Config {
	var c Config
	c.Log.Level = LevelInfo
	c.Probes.Enabled = false
	c.Probes.Addr = "127.0.0.1:8080"
	c.Server.Addr = "127.0.0.1:8081"
	return c
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
	if serverHost == "" || serverHost == "*" || (serverIP != nil && serverIP.IsUnspecified()) {
		return fmt.Errorf("server.addr must name a concrete listener host for Host validation")
	}
	if strings.TrimSpace(c.Database.Path) != "" && !filepath.IsAbs(c.Database.Path) {
		return fmt.Errorf("database.path must be absolute")
	}
	if err := catalog.ValidateSubscriptions(c.Matching.Catalogs); err != nil {
		return err
	}
	return nil
}
