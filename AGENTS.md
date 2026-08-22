# AGENTS.md

Single Go binary `ccp` — Claude Code profile launcher. `cmd/ccp` + `internal/*` packages, comprehensive tests.

## Build / verify

- Requires **Go >= 1.25** (`go.mod:3`).
- `make build` — `go build -o ccp ./cmd/ccp`
- `make install` — builds + installs to `~/.local/bin/ccp` (override with `CCP_BINDIR=...`)
- `make vet` — `go vet ./...` / `make fmt` — `gofmt -w cmd internal .` / `make test` — `go test ./... -count=1`
- **Required before submitting** (CI runs all three, `.github/workflows/ci.yml:21`):
  ```
  go test ./... -count=1 -race   # on Linux; -count=1 elsewhere
  go build ./cmd/ccp
  golangci-lint run ./...   # v2.8, no config file — uses defaults
  go vet ./...
  gofmt -w cmd internal .                # or make fmt; fail if git diff not empty
  ```
- Smoke test: `./ccp version` and `./ccp doctor` (binary is `ccp` at repo root, built from `./cmd/ccp`)
- Tests: `go test ./... -count=1` runs all packages; `go test ./... -count=1 -race` on Linux. Each `internal/*` has `*_test.go`, hermetic via `t.TempDir()` + `CCP_HOME`/`CCP_STATE_HOME`/`HOME` overrides.
- Release cross-compile (`.github/workflows/release.yml:37`): `CGO_ENABLED=0 go build -ldflags "-s -w -X main.version=${VERSION#v}" -o bin/... ./cmd/ccp`

## Architecture

`cmd/ccp/main.go` is a thin entrypoint that calls `internal/cli.Run()`. All logic lives in `internal/*`:

- `internal/config` — load `config.toml` + `profiles/*.toml` (`profiles/` wins on name collision, `config:179`), bootstrap seed profiles on first run (`config:323`); pooled `[[accounts]]` + `[routing]` (round-robin only); path helpers `CcpConfigDir`/`CcpStateDir`
- `internal/profile` — env assembly (`BuildEnv`/`BuildEnvPeek`/`AssembleEnv`), auth resolution per-profile and per-`Account` (`ResolveAccountAuth`, `ResolveProfileAuth`), managed-var stripping (`ManagedVars` — 20+ vars including `ANTHROPIC_*`, `CLAUDE_CODE_*`, Bedrock/Vertex toggles)
- `internal/routing` + `routing/daemon_unix.go`/`daemon_other.go:lockRoutingState` — persisted round-robin counter at `<state>/routing/<profile>.json` (atomic tmp+rename, flock on unix)
- `internal/cli` — `launch` (exec replacement of `claude`), `show`/`list`, `add`/`edit`/`remove`/`default`, `proxy` dispatch, `doctor`, `completion`; uses `internal/tui` for prompts
- `internal/proxy` + `proxy/daemon_unix.go` — CLIProxyAPI daemon lifecycle (pid file, log, `Setsid` on unix), `FindProxyBinary`, `FetchProxyModels`, install
- `internal/settings` — reads `~/.claude/settings.json` for model inheritance and conflict detection (`ReadClaudeSettings`, `FindEnvConflicts`, `InheritModel`)
- `internal/tui` — `SelectOption`, `PromptLine`, `ConfirmYN` (arrow-key menu, `golang.org/x/term`)
- `internal/util` — `ExpandEnvVars`, `MaskSecret`, `ProbeURL`, `Paint`, `Die`/`Warnf`, `FileExists`, `HomeDir` etc.
- `internal/testutil` — `TempCCPHome`, `TempHome`, `MustWriteFile` helpers for hermetic tests

Dependency DAG: `util` ← `config` ← `routing` ← `profile` ← `proxy` (via config) ← `settings` ← `cli` ← `cmd/ccp` (acyclic, `go list -f '{{.Imports}}' ./...` has no cycle).

## Runtime / config paths

- Config dir: `$CCP_HOME` > `$XDG_CONFIG_HOME/ccp` > `~/.config/ccp` (`config:85`)
- State dir: `$CCP_STATE_HOME` > `$XDG_STATE_HOME/ccp` > `~/.local/state/ccp` (`config:95`) — holds `cliproxy.pid`, `cliproxy.log`, `bin/cli-proxy-api`, and `routing/<profile>.json` round-robin counters
- First run creates `config.toml` + `profiles/glm.toml|kimi.toml|official.toml` — never overwrites existing files (`config:308`)
- Profile `type` must be `cliproxy` or `anthropic` (defaults to `anthropic` if empty, `config:212`). `cliproxy` reuses `api-keys[0]` from `cliproxy/config.yaml` as bearer token unless `auth_token_env`/`api_key_env` set.
- `${VAR}` / `$VAR` expanded from process env in profile values at launch time (`util:101`).
- Proxy binary resolution order (`proxy:52`): `config.proxy.binary` > `PATH` (`cli-proxy-api`/`CLIProxyAPI`) > `~/.local/bin/cli-proxy-api` > `<state>/bin/cli-proxy-api`
- `ccp show PROFILE` is the non-destructive way to verify env without launching claude.

## Gotchas

- Managed vars are **fully stripped** then re-applied; stray `ANTHROPIC_*` in shell env will not leak across profiles — but `env` blocks in `~/.claude/settings.json` or `.claude/settings*.json` **override process env** and will silently defeat profiles. `ccp doctor` fails on this; fix by removing keys from settings files.
- `cli-proxy-api` is a daemon — `ccp` auto-starts it but leaves it running between sessions. Tests/manual checks must `ccp proxy stop` if they expect a clean state. `proxyReachable` probes `/` with 500ms timeout (`proxy:28`).
- Profile names restricted to `[a-z0-9._-]` (`config:218`).
- `install.sh` prefers prebuilt `ccp_<tag>_<os>_<arch>.tar.gz` (verified via `checksums.txt`) and falls back to `go build ./cmd/ccp`; `REF` env selects branch/tag, `CCP_BINDIR` selects install dir.
- Tests are hermetic: they set `CCP_HOME`/`CCP_STATE_HOME`/`HOME` to `t.TempDir()` and never touch real `~/.config/ccp`. Run with `NO_COLOR=1` to disable ANSI in golden comparisons.
