# testing Specification

## Purpose
Provides automated regression safety for `ccp` by adding per-package unit and integration tests that exercise env assembly, auth, routing, config, and proxy logic in isolation and through CI.
## Requirements
### Requirement: Per-package test suites
Every `internal/*` package introduced by the reorganization SHALL have at least one `*_test.go` file with table-driven tests covering its public API and error cases; `go test ./...` SHALL be the single command to run all tests.

#### Scenario: All packages have tests
- **WHEN** `go test ./... -count=1` is run on the reorganized repo
- **THEN** each `internal/config`, `internal/profile`, `internal/routing`, `internal/proxy`, `internal/settings`, `internal/util`, and `internal/cli` (or equivalent split) reports `ok` with at least one test, and the overall run exits 0

#### Scenario: Tests are table-driven
- **WHEN** a reviewer inspects any `*_test.go`
- **THEN** cases are expressed as `[]struct{ name string; ... }` with `t.Run`, covering both success and error branches

### Requirement: Config loading coverage
`internal/config` tests SHALL verify loading precedence, `profiles/` over `config.toml` inline, bootstrap seed creation, and validation of pool, routing strategy, and `safeName`.

#### Scenario: File-per-profile wins on collision
- **WHEN** a temp `CCP_HOME` contains `config.toml` with `[profiles.foo]` (model `a`) and `profiles/foo.toml` (model `b`)
- **THEN** loading returns `model == "b"` for `foo`

#### Scenario: Bootstrap creates seeds without overwriting
- **WHEN** `loadConfig` is called with an empty `CCP_HOME`
- **THEN** it creates `config.toml` and `profiles/glm.toml|kimi.toml|official.toml` with `0600`/`0700` perms, and a second call does not overwrite existing files

#### Scenario: Invalid pool is rejected
- **WHEN** a profile declares `[[accounts]]` with an illegal `name` or `routing.strategy = "capacity-weighted"`
- **THEN** `validatePool` returns an error naming the index/field and `doctor`/`show` surface it as a `fail`

### Requirement: Profile env assembly coverage
`internal/profile` tests SHALL verify `managedVars` stripping, `buildEnv`/`assembleEnv` determinism, auth priority, `${VAR}`/`$VAR` expansion, and pooled-vs-single assembly.

#### Scenario: Managed vars are stripped
- **WHEN** the parent env contains stray `ANTHROPIC_API_KEY`, `ANTHROPIC_AUTH_TOKEN`, `CLAUDE_CODE_USE_BEDROCK`
- **THEN** `assembleEnv` drops all `managedVars` before applying `Sets`, and the child env contains exactly one credential from the selected account

#### Scenario: Auth priority is correct
- **WHEN** an `Account` sets both `auth_token_env` and `api_key_env` (or literal + env), or a `type=cliproxy` account sets nothing
- **THEN** resolution follows priority: `auth_token_env` > `api_key_env` > `auth_token` > `api_key` > proxy `api-keys[0]` > `none` (for `type=cliproxy`), and `none` yields no credential var

#### Scenario: Expansion at launch time
- **WHEN** a profile sets `base_url = "https://${HOST}/v1"` or `auth_token = "${TOKEN}"`
- **THEN** `expandEnvVars` resolves them from the process env at `buildEnv`/`buildEnvPeek` time, leaving unknown `$VAR` untouched

#### Scenario: Pool selects only one credential
- **WHEN** a 3-account pool selects index `k`
- **THEN** the child env contains exactly the credential for `k` and no key/token from the other two accounts

### Requirement: Routing state coverage
`internal/routing` tests SHALL verify round-robin increment, persistence, atomic write, missing/corrupt recovery, and concurrent safety.

#### Scenario: Successive launches rotate deterministically
- **WHEN** `nextRoutingIndex("codex", 3)` is called three times sequentially on a clean state dir
- **THEN** it returns `0, 1, 2` and the fourth call returns `0` again, with `counter` persisted as JSON `{"counter": N}` at `<state>/routing/<profile>.json`

#### Scenario: Missing or corrupt state recovers
- **WHEN** the state file is missing, empty, or contains invalid JSON, or `counter < 0`
- **THEN** the next call treats `counter` as `0`, recreates the file atomically via `tmp+rename`, and returns `0`

#### Scenario: Concurrent callers do not corrupt state
- **WHEN** two goroutines call `nextRoutingIndex` concurrently for the same profile (Unix `flock` path)
- **THEN** the file remains valid JSON and `counter` increments by exactly 2, yielding distinct indices

### Requirement: Proxy and settings coverage
`internal/proxy` tests SHALL verify binary resolution order and model fetch parsing; `internal/settings` tests SHALL verify `findEnvConflicts` across `~/.claude/settings.json` and `.claude/settings*.json`; `internal/util` tests SHALL verify `expandEnvVars`, `maskSecret`, `expandPath`, and helpers.

#### Scenario: Proxy binary resolution order
- **WHEN** `findProxyBinary` is called with `config.proxy.binary` set, then with it empty but `cli-proxy-api` on `PATH`, then with neither but present at `~/.local/bin/cli-proxy-api`
- **THEN** it returns the first existing executable in order: config `binary` > `PATH` > `~/.local/bin` > `<state>/bin`

#### Scenario: Settings conflict detection
- **WHEN** `findEnvConflicts` is called with keys that appear in `~/.claude/settings.json`'s `env` block and in `.claude/settings.json`
- **THEN** it returns one `envConflict` per colliding file, with `Path`, `Key`, `Value`, and `Scope` populated

#### Scenario: Masking and expansion edge cases
- **WHEN** `maskSecret` is called with `""`, `<=10` chars, and `>10` chars, and `expandEnvVars` is called with `${VAR}`, `$VAR`, and unknown `$UNKNOWN`
- **THEN** masking follows `s[:2]+"******"` for short, `s[:6]+"…"+s[last4:]` for long, and expansion leaves unknown refs unchanged

### Requirement: CLI output coverage
`internal/cli` tests SHALL verify `show`/`list` formatting, `renderProfileToml` output, and doctor checks via golden files or deterministic string assertions without requiring a real `claude` binary or running proxy.

#### Scenario: Show masks secrets
- **WHEN** `showProfile` (or its refactored equivalent) renders a pooled profile with `$TOKEN` set and literal `auth_token`
- **THEN** output lists each account by index, with auth source (`$TOKEN`, `proxy config api-keys[0]`) and masked values, and marks `→` next to the `peek` index

#### Scenario: List shows pool size
- **WHEN** `listProfiles` renders a mix of single and pooled profiles
- **THEN** pooled entries show `×N` and all columns remain aligned, with `*` marking the default

### Requirement: Isolation and CI gating
Tests SHALL NOT touch the real home directory or require a running proxy/`claude` binary; they SHALL use `t.TempDir()` plus `CCP_HOME`/`CCP_STATE_HOME`/`HOME` overrides and `t.Setenv`, and SHALL be gated in CI so `go test ./...` must pass.

#### Scenario: Hermetic execution
- **WHEN** tests run on a machine with no `~/.config/ccp` and no proxy running
- **THEN** all tests pass using only temp dirs and stub env vars; no test writes outside `t.TempDir()` or `t.Setenv` scope

#### Scenario: CI gates on tests
- **WHEN** `ci.yml` runs `go test ./... -count=1 -race` (or at minimum `go test ./...`)
- **THEN** a failing test causes the workflow to fail before `go vet`/`golangci-lint` are considered passing, and the `build` job matrix (ubuntu/macos/windows) still passes when tests pass

### Requirement: No regression for existing pooled routing
Tests SHALL reproduce the existing `multi-account-routing` invariants so the reorganization cannot silently change rotation, auth, or env semantics.

#### Scenario: Rotation matches spec
- **WHEN** the existing `openspec/specs/multi-account-routing/spec.md` scenarios are mapped to tests (state persists across reboots, one session keeps one credential, base URL per-account override, only selected credential present)
- **THEN** each scenario has a corresponding test case that passes on the new package layout

