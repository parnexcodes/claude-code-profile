## Why

The project is a single `package main` with 14 Go files (~3,900 LOC) in the repository root and zero tests (`go test ./...` is a no-op, `AGENTS.md:19`). Every change touches the same package, `go vet`/`golangci-lint` have no package boundaries to enforce, and there is no automated regression safety for critical logic (env assembly, auth resolution, routing state, config loading, proxy lifecycle, settings interop). Reorganizing into idiomatic Go packages and adding a real test suite is needed now before further feature work (routing, pooled accounts) increases coupling and risk.

## What Changes

- **Reorganize from flat `package main` to `cmd/ccp` + `internal/*` packages** — move existing files into focused packages without changing user-visible behavior:
  - `cmd/ccp` — thin entrypoint (`main.go` dispatcher only)
  - `internal/config` — types (`Profile`, `Account`, `ProxyConfig`, `Config`), `loadConfig`, `bootstrap`, `safeName`, path helpers (`ccpConfigDir`, `ccpStateDir`)
  - `internal/profile` — env assembly (`buildEnv`, `buildEnvPeek`, `assembleEnv`), `resolveAuth` per-profile and per-`Account`, `managedVars`, endpoint derivation
  - `internal/routing` — persisted round-robin counter (`nextRoutingIndex`, `peekRoutingIndex`, `clearRoutingState`, file locking)
  - `internal/proxy` — daemon lifecycle (`startProxy`, `stopProxy`, `proxyReachable`, `findProxyBinary`), `/v1/models` fetch, `installProxy`, log tailing
  - `internal/settings` — `readClaudeSettings`, `findEnvConflicts`, model inheritance
  - `internal/cli` — `launch`, `showProfile`, `listProfiles`, `add`/`edit`/`remove`/`default` handlers, wizard, picker via `internal/tui`
  - `internal/tui` + `internal/util` — shared helpers (`paint`, `expandEnvVars`, `maskSecret`, `probeURL`, `fileExists`, etc.)
  - Exact package cut is finalized in `design.md`; goal is one responsibility per package, no import cycles, `package main` only in `cmd/ccp`.
- **Add comprehensive tests** — table-driven unit tests for every package plus integration tests that use `t.TempDir()` + env isolation (`CCP_HOME`, `CCP_STATE_HOME`, `HOME`). No new dependencies beyond stdlib; `testify` optional only if already used elsewhere (currently none — prefer stdlib `testing`).
  - `internal/config` — loading (`config.toml` + `profiles/*.toml` precedence, `profiles/` wins on collision per `config.go:179`), bootstrap seed creation, validation (`validatePool`, `safeName`, `routingStrategy`), path resolution via `CCP_HOME`/`XDG_*`.
  - `internal/profile` — `managedVars` stripping, `buildEnv`/`assembleEnv` determinism, auth priority (`auth_token_env` > `api_key_env` > literal > proxy `api-keys[0]` > `none`), `${VAR}`/`$VAR` expansion, pooled vs single-credential assembly, `extra_env` passthrough.
  - `internal/routing` — counter increment, persistence (`<state>/routing/<profile>.json`), atomic tmp+rename, flock on Unix, corrupt/missing file recovery, concurrent access safety.
  - `internal/proxy` — `findProxyBinary` precedence (`config.proxy.binary` > `PATH` > `~/.local/bin` > `<state>/bin`), `proxyReachable` probe (500 ms, `/`), model fetch parsing.
  - `internal/settings` — JSON parsing, `findEnvConflicts` across `~/.claude/settings.json` + `.claude/settings*.json`, precedence (`settings.json` env block overrides process env).
  - `internal/util` — `expandEnvVars` (braced vs bare, unknown left untouched), `maskSecret`, `expandPath`, etc.
  - `internal/cli` — golden-file tests for `ccp show`/`list` output and `renderProfileToml`, wizard input validation.
- **Update build, CI, and docs** — root `Makefile` delegates to `./...`, CI (`ci.yml`, `release.yml`) builds `cmd/ccp`, `go vet`/`golangci-lint`/`gofmt` operate on `./...` without regressions, `AGENTS.md` updated to reflect new layout, smoke test still `go build -o ccp ./cmd/ccp && ./ccp version && ./ccp doctor`.

## Capabilities

### New Capabilities

- `project-structure`: Idiomatic Go package layout for `ccp` — package boundaries, import rules, file locations, and build entrypoint guarantees. Each package has a single responsibility, `package main` exists only under `cmd/ccp`, and the public user contract (binary name `ccp`, config paths, profile TOML schema) is unchanged.
- `testing`: Automated test coverage for the reorganized codebase — unit + integration tests per package, CI-gated (`go test ./...` must pass), temp-dir isolated, no reliance on real `~/.config/ccp` or running proxy daemon.

### Modified Capabilities

- None — no user-visible behavior changes. Existing capability `multi-account-routing` (`openspec/specs/multi-account-routing/spec.md`) must continue to pass unchanged; the reorganization preserves its env assembly, routing, and validation semantics.

## Impact

- **Code**: All 14 `.go` files moved/renamed; `go.mod` module stays `ccp`; package imports rewritten from intra-package globals to explicit `internal/*` imports. No change to `config.toml` schema, profile `TOML` format, state file layout, or CLI surface.
- **APIs / CLI**: None. `ccp` binary name, flags, subcommands (`launch`, `list`/`show`, `add`/`edit`/`remove`, `proxy`, `doctor`, `completion`) and exit codes remain identical.
- **Dependencies**: No new runtime dependencies. Test-only stdlib `testing`; optional `testify` only if team prefers, not required.
- **Build / CI**: `Makefile` (`build`, `vet`, `fmt`), `.github/workflows/ci.yml:21` lint/vet/build steps, and `.github/workflows/release.yml:37` cross-compile `LDFLAGS` updated to build `./cmd/ccp`. `golangci-lint run ./...` must pass with default config (`openspec/config.yaml` has no custom rules).
- **Risk**: Import-cycle or missed global if package cut is wrong; mitigated by `go vet ./...` + `go build ./...` + existing smoke tests on every package move (small, atomic commits).
