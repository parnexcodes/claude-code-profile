## Context

See `proposal.md` — Why.

Current state:
- Repo is one `package main` at the root; `main.go:1138` dispatches CLI, `config.go:451` loads/validates, `profile.go:453` assembles env and resolves auth, `routing.go:84` persists counters, `proxy.go:503` manages the daemon, `launch.go:299` execs `claude` and renders `show`/`list`, `doctor.go:157` runs health checks, etc. Total 14 `.go` files, ~3,884 LOC, no sub-packages, no tests (`AGENTS.md:3-4`).
- Build/CI expects `go build ./...` + `go vet ./...` + `golangci-lint run ./...` (default config) + `gofmt -w .` (`.github/workflows/ci.yml:21`, `release.yml:37`). Release cross-compiles `CGO_ENABLED=0 go build -ldflags "-s -w -X main.version=${VERSION#v}"`.
- Runtime contracts that must survive the move: config dir `$CCP_HOME` > `$XDG_CONFIG_HOME/ccp` > `~/.config/ccp` (`config.go:85`), state dir similarly (`config.go:95`), `profiles/*.toml` wins on collision (`config.go:179`), bootstrap seed profiles (`config.go:323`), `managedVars` 20+ stripped then re-applied (`profile.go:31`), `${VAR}`/`$VAR` expansion (`util.go:101`), routing counter at `<state>/routing/<profile>.json` with `flock` on Unix (`daemon_unix.go`), proxy binary precedence (`proxy.go:52`).

Constraints shaping the approach:
- Behavioral equivalence is mandatory — the reorg cannot change any `ccp` CLI output, env injection, or routing order. Golden-file or string-asserted tests must be written before or alongside moves.
- No circular imports: config ← profile ← cli is a natural hierarchy, but `profile` currently reads `Config` and `proxyYAML` (`profile.go:178`) and `routing` touches state paths from `config`. Need an explicit DAG up front.
- Minimal disruption: keep `go.mod` module `ccp`, keep `golangci-lint` default (no config file), keep `CGO_ENABLED=0`.

## Goals / Non-Goals

**Goals:**
- Establish an acyclic `internal/*` layout with one responsibility per package and `cmd/ccp` as the sole `package main`.
- Make `go build ./...`, `go vet ./...`, `golangci-lint run ./...`, `gofmt`, and `go test ./...` all pass on the new layout with no behavioral change.
- Add hermetic, table-driven tests per package covering the highest-risk logic (config precedence, env assembly, auth priority, routing atomicity, settings conflicts).
- Update `Makefile`, `ci.yml`/`release.yml`, `AGENTS.md`/`README.md` to reflect the new paths.

**Non-Goals:**
- Changing any `ccp` user-visible behavior, flag names, TOML schema, or filesystem paths.
- Introducing new runtime dependencies or a linter config file.
- Splitting `internal/cli` into micro-packages beyond what import acyclicity requires (keep iteration small).
- Adding e2e tests that launch a real `claude` binary or live `CLIProxyAPI` — unit + hermetic integration only.

## Decisions

### D1: `cmd/ccp` + `internal/*` layout (Go standard)
Chosen layout:
```
cmd/ccp/main.go          // package main — flag parse, dispatch only
internal/config/         // Config, Profile, Account, ProxyConfig, load, bootstrap, paths
internal/profile/        // managedVars, effectiveBaseURL, resolveAuth, buildEnv, assembleEnv
internal/routing/        // routingState, next/peek/clear + lock
internal/proxy/          // findProxyBinary, start/stop, probe, fetchProxyModels, install, logs
internal/settings/       // readClaudeSettings, findEnvConflicts, inheritModel
internal/cli/            // launch, show/list, add/edit/remove/default, doctor, completion dispatch
internal/tui/            // selectOption, promptLine, confirmYN (from interactive.go)
internal/util/           // paint, die/warn/info/ok, expandPath, expandEnvVars, maskSecret, probeURL, etc.
```
Rationale: `cmd/ccp` is the conventional entrypoint for a single binary; `internal/*` enforces that external importers cannot depend on `ccp` internals (Go tooling enforces it). Keeps `go build ./...` meaningful and lets `golangci-lint` lint per-package.

Alternatives considered:
- `pkg/` for reusable libraries — rejected because no external reuse is intended; `internal/` better signals "not for import."
- One `internal/app` super-package — rejected; it would recreate the monolith and lose per-package vet/test granularity.
- No move, just add tests to root — rejected; root `package main` cannot be imported by tests of other packages without polluting `main`.

### D2: Dependency DAG — `util ← config ← routing ← profile ← proxy ← settings ← cli ← cmd/ccp`
- `internal/util` is leaf: only stdlib, `golang.org/x/sys`/`term`, no internal imports.
- `internal/config` depends only on `util` + `toml`/`yaml`.
- `internal/routing` depends on `config` (for `ccpStateDir`) and `util`/file-lock helpers.
- `internal/profile` depends on `config`, `routing`, `settings` (for `inheritModel`), `util`.
- `internal/proxy` depends on `config`, `util`.
- `internal/settings` depends only on `util` + stdlib.
- `internal/cli` depends on all below; `cmd/ccp` depends only on `cli`/`config`.

Why: mirrors current call graph (`profile.go` already imports `config` concepts, `launch.go` calls `buildEnv` + `proxyReachable` + `findEnvConflicts`). Splitting along existing function groups minimizes interface churn. File-lock (`daemon_unix.go`/`daemon_other.go`) goes with `routing` because only routing needs it today; `proxy`'s `detach`/`killProcessGroup` stays with `proxy`.

Alternative: `type Config struct` shared via a `internal/model` package to avoid `config → profile` coupling — considered but deferred; current `Config` lives in `config` and is passed into `profile` functions exactly as today (`buildEnv(cfg, ...)`), so separation is clean without an extra layer.

### D3: Move incrementally, one package at a time, with tests alongside
Order: `util` → `settings` → `routing` → `config` → `proxy` → `profile` → `cli` → `cmd/ccp`. Each step:
1. Create `internal/<pkg>/` dir + move `*.go` files (preserve git history via `git mv` where possible).
2. Update package clause to `package <pkg>` and fix imports (replace same-package globals with qualified `config.X`, `util.Y`).
3. Run `go vet ./... && go build ./...` — must pass before next move.
4. Add `*_test.go` for that package (or earlier as safety net) — run `go test ./...`.

Rationale: smallest blast radius; `util` first because everything depends on it; `cli` last because it depends on everything. Avoids a big-bang rename that cannot be bisected.

### D4: Tests — stdlib `testing` only, `t.TempDir` + `t.Setenv`, table-driven, golden files for CLI output
- Testing framework: stdlib only. No `testify` addition keeps `go.mod` churn minimal and avoids opinionated assertion style. If a later decision adds `testify`, migration is mechanical.
- Isolation: every test that touches filesystem or env uses `t.TempDir()` and `t.Setenv("CCP_HOME", dir)` / `CCP_STATE_HOME` / `HOME`; no test touches real `~/.config`. `HOME` override is needed for `~` expansion and `settings.json` reads — tests set it to a temp dir and create `fakeHome/.claude/settings.json` there.
- Style: table-driven `[]struct{name string; ...}` with `t.Run`; helpers like `mustWriteFile`, `withEnv(key,val,fn)`.
- CLI output: `show`/`list` use `bytes.Buffer` capture + golden files under `internal/cli/testdata/*` (checked in); `renderProfileToml` likewise. Doctor's color can be disabled via `NO_COLOR=1` or a `useColor` toggle extracted to a testable var.
- Routing concurrency: uses `sync.WaitGroup` + barrier channel; Unix `flock` path tested on Linux/macOS, stubbed on Windows (`daemon_other.go`).

Why table-driven: matches Go idiom, scales to many cases (auth priority, expansion, masking), and produces `go test -run TestX/case_name` filtering.

Alternatives: `testify/assert` — nicer diffs but adds dependency and is unnecessary for stdlib `if got != want` with `cmp` helper. `bats`/`shell` e2e — heavier and flaky; deferred.

### D5: Build / CI / formatting updates
- `Makefile`: `build: go build -o ccp ./cmd/ccp`, `vet: go vet ./...`, `fmt: gofmt -w .` (or `gofmt -w cmd internal`). Keeps `make build` producing `ccp` at root for local ergonomics.
- `ci.yml`: lint job unchanged (`golangci-lint run ./...`), build job runs `go vet ./...` then `go test ./... -count=1` (optionally `-race` on ubuntu) then `go build -o ccp_test ./cmd/ccp && ./ccp_test version`. Matrix on `ubuntu/macos/windows` still valid — Go builds all packages via `./...`.
- `release.yml:37`: `go build` line becomes `go build ... -o "bin/ccp_${VERSION}_${os}_${arch}${ext}" ./cmd/ccp`.
- `AGENTS.md` / `README.md`: replace "one `package main`" diagram with the `cmd`+`internal` tree and add `go test ./...`.

### D6: Symbol visibility and no breaking `go vet`/`golangci-lint`
Export only what cross-package callers need (e.g., `config.LoadConfig`, `config.CCPConfigDir`, `profile.BuildEnv`, `routing.NextIndex`). Keep helpers unexported where possible. Extract `useColor`/`colorEnabled` to allow tests to disable ANSI without global mutation.

`golangci-lint run ./...` with defaults must stay green — run locally on every package move. If a new lint finding appears due to package boundaries (e.g., `unused` due to moved symbol), fix immediately; do not add `nolint`.

## Risks / Trade-offs

- [Risk] Missed global or `init` side-effect during move (e.g., `useColor = colorEnabled()` evaluated at init time, `managedVars` `func() []string` init) → Mitigation: move `var useColor` and `managedVars` with their init logic intact; add a test that `profile.ManagedVars` length >= 20 and contains `ANTHROPIC_API_KEY`.
- [Risk] Import cycle introduced (e.g., `profile` needing `proxy.readAPIKeys` while `proxy` needs `profile.effectiveBaseURL`) → Mitigation: decide ownership up front (proxy API keys belong to `config` or `proxy`, not `profile`; `profile.ResolveAuth` takes a key-reader func or `*config.Config` already available — keep `readProxyAPIKeys` in `config` or `proxy` and pass it in).
- [Risk] Path helpers duplicated or drift (`ccpConfigDir` logic copy-paste) → Mitigation: single source of truth in `internal/config`; other packages call `config.CCPConfigDir()` / `config.CCPStateDir()`; tests assert precedence (`CCP_HOME` > `XDG_CONFIG_HOME` > `~`).
- [Risk] File permission regression (seed files were `0600`, dirs `0700`, proxy binary `0755`) → Mitigation: tests stat temp files and assert modes; keep `writeFileIfMissing` behavior verbatim.
- [Risk] Color/terminal globals make tests flaky (`Term` detection) → Mitigation: tests set `NO_COLOR=1` or inject an `io.Writer`/bool param; `paint` is already gated on `useColor` which respects `NO_COLOR`.
- [Risk] Windows file-lock stub hides real concurrency bug → Mitigation: document that `daemon_other.go` (Windows) is best-effort; add a unit test that at least does not corrupt file even without flock, and skip strict concurrency assertion on `GOOS=windows`.
- [Trade-off] More packages = more import boilerplate vs. better ownership → Accept; files are small enough that per-package ownership pays off quickly. Keep `internal/util` from becoming a grab-bag by reviewing its contents at the end.

## Migration Plan

1. **Create package skeleton without moving code** — `mkdir -p cmd/ccp internal/{config,profile,routing,proxy,settings,cli,tui,util}`; add `doc.go` stubs so `go list ./...` sees them.
2. **Migrate `util` first** — move `util.go` → `internal/util/`; export needed symbols (`HomeDir`, `ExpandPath`, `ExpandEnvVars`, `MaskSecret`, `FileExists`, `IsExecutable`, `ProbeURL`, `LookPathAll`, `ProcCmdline`, `RandHex`, `CloseQuietly`, `MustReadAll`, color vars + `Paint`/`Die`/`Warnf`/etc. or keep `Die` in `cli`). Verify `go build ./...` + `go vet ./...`.
3. **Migrate `settings` and `routing`** — move `claudesettings.go` → `internal/settings`, `routing.go` + `daemon_*.go` lock helpers → `internal/routing`. Update imports in remaining root files to `internal/routing` for `nextRoutingIndex` etc.
4. **Migrate `config`** — move `config.go` → `internal/config`; adjust `Profile`/`Account` type locations; ensure `bootstrap` still writes to `CcpConfigDir()`.
5. **Migrate `proxy`** — move `proxy.go` + `proxycmd.go` + daemon detach helpers → `internal/proxy`; keep `findProxyBinary`/`proxyReachable`/`startProxy` APIs compatible.
6. **Migrate `profile` and `cli`** — move `profile.go` → `internal/profile`, `launch.go`+`doctor.go`+`interactive.go`+`completion.go`+`main.go` dispatch → `internal/cli` + `cmd/ccp/main.go` (thin wrapper). Run full `go test ./...` safety net.
7. **Update build glue** — edit `Makefile`, `.github/workflows/ci.yml`, `.github/workflows/release.yml`, `AGENTS.md`, `README.md`, `install.sh` references to binary path if any.
8. **Add/expand tests per package** — if not already done incrementally, ensure each `internal/*` has coverage as per `specs/testing/spec.md`; run `go test ./... -race` on ubuntu in CI.
9. **Verification** — `go vet ./... && golangci-lint run ./... && go build -o /tmp/ccp ./cmd/ccp && /tmp/ccp version && CCP_HOME=/tmp/fakehome /tmp/ccp doctor` (smoke). Rollback: `git revert` the package moves — binary path is the only user-visible artifact, and old binary still reads same config.
10. **Archive** — no data migration; state files stay in place.

## Open Questions

- **Q: Should `doctor` live in `internal/cli` or a separate `internal/doctor`?** Deferrable: either satisfies the spec as long as `cli` imports it; decision does not change specs or task breakdown.
- **Q: Keep `util.Die` that calls `os.Exit` vs. return errors?** Current `die` is convenient for CLI but hostile to tests. Plan: keep `Die` thin in `cli` and make core functions return `error`; tests assert errors. Exact refactor is an implementation detail.
- **Q: Golden file vs. inline string asserts for `show`/`list`?** Golden files ease review of whitespace/ANSI but add churn. Choose one consistently; either passes the spec.

