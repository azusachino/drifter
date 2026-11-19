package cli

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/azusachino/drifter/internal/advisor"
	"github.com/azusachino/drifter/internal/companion"
	"github.com/azusachino/drifter/internal/config"
)

type configTestSource struct {
	fakeReader
}

func (s *configTestSource) ResolveAccountFingerprint(context.Context, string) (string, error) {
	return "weread:config-test", nil
}

type configTestTarget struct {
	fakeCatalog
	cliMultiMockTarget
}

type configTestAdvisor struct{}

func (a *configTestAdvisor) ExplainConflict(context.Context, advisor.ConflictExplanationRequest) (advisor.ConflictExplanationResponse, error) {
	return advisor.ConflictExplanationResponse{RecommendedAction: advisor.ActionKeepSeparate}, nil
}

func (a *configTestAdvisor) ResolveOriginalWork(context.Context, advisor.OriginalWorkRequest) (advisor.OriginalWorkResponse, error) {
	return advisor.OriginalWorkResponse{Abstained: true}, nil
}

func TestCLIConfigFlagPropagatesToProvidersAndAdvisor(t *testing.T) {
	t.Setenv("DRIFTER_CONFIG", filepath.Join(t.TempDir(), "unused-default.toml"))

	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "custom.toml")
	cfgTOML := `
[providers]
timeout = "42s"
weread_interval = "2500ms"
hardcover_interval = "3s"

[advisor]
base_url = "https://advisor.example.invalid/v1"
model = "custom-model"
models = ["custom-model", "fallback-model"]
timeout = "19s"
cache_ttl = "12h"
`
	if err := os.WriteFile(cfgPath, []byte(cfgTOML), 0o600); err != nil {
		t.Fatal(err)
	}

	dbPath := filepath.Join(dir, "mappings.sqlite")
	store, err := companion.OpenStore(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Confirm(context.Background(), companion.Mapping{
		Provider:      "weread",
		SourceAccount: "default",
		BookID:        "123",
		TargetAccount: "default",
		WorkID:        500,
		SourceTitle:   "Synthetic book",
	}, false); err != nil {
		t.Fatal(err)
	}
	store.Close()

	commands := [][]string{
		{"match", "--book-id", "123", "--db", dbPath, "--config", cfgPath},
		{"sync", "--book-id", "123", "--dry-run", "--with-advisor", "--db", dbPath, "--config", cfgPath},
		{"reconcile", "--dry-run", "--with-advisor", "--db", dbPath, "--config", cfgPath},
	}

	for _, args := range commands {
		t.Run(args[0], func(t *testing.T) {
			var gotProvSrc, gotProvTgt config.ProvidersConfig
			var gotAdv config.AdvisorConfig
			var srcCalled, tgtCalled, advCalled bool
			var advStoreNonNil bool

			src := &configTestSource{}
			tgt := &configTestTarget{
				cliMultiMockTarget: cliMultiMockTarget{
					statuses:    map[int64]*companion.UserBookStatus{},
					fingerprint: "hardcover:config-test",
				},
			}

			providers := clients{
				sourceWithConfig: func(p config.ProvidersConfig) (source, error) {
					srcCalled = true
					gotProvSrc = p
					return src, nil
				},
				targetWithConfig: func(p config.ProvidersConfig) (target, error) {
					tgtCalled = true
					gotProvTgt = p
					return tgt, nil
				},
				advisorWithConfig: func(a config.AdvisorConfig, st *companion.Store) (advisor.Advisor, error) {
					advCalled = true
					gotAdv = a
					advStoreNonNil = st != nil
					return &configTestAdvisor{}, nil
				},
			}

			var out, stderr bytes.Buffer
			if err := run(context.Background(), args, &out, &stderr, providers); err != nil {
				t.Fatalf("%s failed: %v", args[0], err)
			}

			if !srcCalled || !tgtCalled || !advCalled {
				t.Fatalf("%s factories called: src=%v tgt=%v adv=%v", args[0], srcCalled, tgtCalled, advCalled)
			}
			if !advStoreNonNil {
				t.Fatalf("%s expected non-nil store passed to advisorWithConfig", args[0])
			}
			if gotProvSrc.Timeout != "42s" || gotProvSrc.WeReadInterval != "2500ms" {
				t.Fatalf("%s sourceWithConfig got unexpected providers config: %+v", args[0], gotProvSrc)
			}
			if gotProvTgt.Timeout != "42s" || gotProvTgt.HardcoverInterval != "3s" {
				t.Fatalf("%s targetWithConfig got unexpected providers config: %+v", args[0], gotProvTgt)
			}
			if gotAdv.BaseURL != "https://advisor.example.invalid/v1" || gotAdv.Model != "custom-model" ||
				gotAdv.Timeout != "19s" || gotAdv.CacheTTL != "12h" || len(gotAdv.Models) != 2 {
				t.Fatalf("%s advisorWithConfig got unexpected advisor config: %+v", args[0], gotAdv)
			}
		})
	}
}

func TestCLIInvalidConfigFailsFastBeforeProvidersOrDB(t *testing.T) {
	t.Setenv("DRIFTER_CONFIG", filepath.Join(t.TempDir(), "unused-default.toml"))

	dir := t.TempDir()
	badCfg := filepath.Join(dir, "bad.toml")
	if err := os.WriteFile(badCfg, []byte("not = [valid toml\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	dbPath := filepath.Join(dir, "should-not-be-created.sqlite")

	for _, args := range [][]string{
		{"preview", "--book-id", "123", "--db", dbPath, "--config", badCfg},
		{"sync", "--book-id", "123", "--db", dbPath, "--config", badCfg},
		{"reconcile", "--db", dbPath, "--config", badCfg},
	} {
		t.Run(args[0], func(t *testing.T) {
			called := false
			providers := clients{
				sourceWithConfig: func(config.ProvidersConfig) (source, error) {
					called = true
					return &configTestSource{}, nil
				},
				targetWithConfig: func(config.ProvidersConfig) (target, error) {
					called = true
					return &configTestTarget{}, nil
				},
			}
			var out, stderr bytes.Buffer
			err := run(context.Background(), args, &out, &stderr, providers)
			if err == nil {
				t.Fatalf("%s expected invalid config error, got nil", args[0])
			}
			if called {
				t.Fatalf("%s contacted provider despite invalid config", args[0])
			}
			if _, statErr := os.Stat(dbPath); !os.IsNotExist(statErr) {
				t.Fatalf("%s opened or created DB despite invalid config", args[0])
			}
		})
	}
}

func TestCLIReviewPassesStoreToAdvisor(t *testing.T) {
	t.Setenv("DRIFTER_CONFIG", filepath.Join(t.TempDir(), "unused-default.toml"))

	dir := t.TempDir()
	dbPath := filepath.Join(dir, "review.sqlite")

	opened := time.Now().UTC().Add(-time.Hour).Unix()
	secret := 0
	src := &fakeReader{
		shelf: &companion.ShelfResult{
			Complete: true,
			Books: []companion.ShelfBook{{
				ID:         "123",
				Title:      "Synthetic book",
				LastOpened: &opened,
				Secret:     &secret,
			}},
		},
	}
	tgt := &fakeCatalog{}

	var advStoreNonNil bool
	providers := clients{
		sourceWithConfig: func(config.ProvidersConfig) (source, error) { return src, nil },
		targetWithConfig: func(config.ProvidersConfig) (target, error) { return tgt, nil },
		advisorWithConfig: func(_ config.AdvisorConfig, store *companion.Store) (advisor.Advisor, error) {
			advStoreNonNil = store != nil
			return &configTestAdvisor{}, nil
		},
	}

	var out, stderr bytes.Buffer
	err := runWithInput(
		context.Background(),
		[]string{"review", "--account", "default", "--target-account", "default", "--db", dbPath},
		&out,
		&stderr,
		providers,
		strings.NewReader("match\nquit\n"),
	)
	if err != nil {
		t.Fatalf("review failed: %v", err)
	}
	if !advStoreNonNil {
		t.Fatal("expected review to pass non-nil *companion.Store to advisor factory")
	}
}
