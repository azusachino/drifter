# Changelog

All notable changes to Drifter are documented in this file.

The format follows [Keep a Changelog](https://keepachangelog.com/en/1.1.0/), and
this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [0.1.3] - 2026-11-19

### Added

- Background FIFO sync job worker, bounded in-memory TTL cache, `openai-go` SDK
  adoption, and multi-model advisor fallback chain (#40).
- Configurable operational defaults in `config.toml` (all defaulting to existing
  values) for `[server]` timeouts, `[jobs]` worker budgets/pacing and
  `attempt_cooldown`, `[cache]` capacity/TTLs, `[providers]` timeout/intervals
  and `weread_skill_version`, and `[advisor]` `models` and `cache_ttl` (#41).
- Sample base `Containerfile` for registry-less builds and shared `media-base`
  r2 runtime (#39).

### Fixed

- Finalize `sync_runs` with a bounded background context on timeout or
  cancellation, recover stale `in_progress` sync runs on startup, and raise the
  default Phase 2 reconcile budget to `15m` (#41).
- Isolate Phase 2 background reconcile onto a dedicated `reconcileSlot` so
  interactive endpoints (`/api/pending`, `/api{/v1}/mappings/*`,
  `/api{/v1}/auto-match`) are not starved during long reconciles (#41).
- Move job worker cache/config off package-global maps onto `Server` fields and
  share `buildAdvisor` across `jobs_worker`, `handleReconcile`, and
  `handleAutoMatch` (#41).
- Unify CLI `--config` as a persistent root flag with config-aware provider and
  advisor factories, and pass the SQLite store into `review` advisor caching
  (#41).
- Keep config and state lookups off the host machine during tests (#38).

### Changed

- Deduplicate shared domain defaults (`DefaultMinConfidence`,
  `DefaultLookbackDays`, `MaxLookbackDays`, `DefaultRecentJobDays`) across
  `companion`, `httpserver`, and `cli` (#41).
- Store schema is version 9 (migrates automatically from v8; adds `sync_jobs`
  and `auto_match_attempts`).

## [0.1.2] - 2026-11-12

### Added

- Accept a wildcard `server.addr` (such as `0.0.0.0:8081`) when
  `server.allowed_hosts` is configured (#37).
- Add `make image` target for local k3s containerd import (#37).

### Changed

- Update CLI, architecture, and server documentation for wildcard listener
  validation (#37).
- Store schema is version 8.

## [0.1.1] - 2026-11-10

### Added

- Support `--adopt-target` flag in `preview`, `sync`, and `reconcile` to
  resolve manual Hardcover status advancements as accepted baselines (#28, #29).
- Surface Hardcover GraphQL error messages with bearer token redaction
  (#28, #29).
- Add request pacing to Douban ISBN scraper to prevent rate-limiting (#28, #29).

### Fixed

- Handle 100% reading progress boundary cleanly without dropping into unmapped
  holds (#28, #29).
- Improve guidance for WeRead accounts without reviews or notebooks (#28, #29).

## [0.1.0] - 2026-10-29

### Added

- Initial release of Drifter: interactive terminal review (`drifter review`),
  authoritative ISBN lookup (`drifter isbn`), three-way reading status sync and
  reconcile with two-phase intents, zero-JS local web dashboard
  (`drifter-server`), and hexagonal provider adapters (`weread`, `hardcover`).
