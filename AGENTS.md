# AGENTS.md

Single Go binary `ccp` — Claude Code profile launcher. One `package main`, no sub-packages, no tests.

## Build / verify

- Requires **Go >= 1.25** (`go.mod:3`).
- `make build` — `go build -o ccp .`
- `make install` — builds + installs to `~/.local/bin/ccp` (override with `CCP_BINDIR=...`)
- `make vet` — `go vet ./...` / `make fmt` — `gofmt -w .`
- **Required before submitting** (CI runs all three, `.github/workflows/ci.yml:21`):
  ```
  go build ./...
  golangci-lint run ./...   # v2.8, no config file — uses defaults
  go vet ./...
  gofmt -w .                # or make fmt; fail if git diff not empty
  ```
- Smoke test: `./ccp version` and `./ccp doctor`
- No `*_test.go` files exist; `go test ./...` is a no-op.
- Release cross-compile (`.github/workflows/release.yml:37`): `CGO_ENABLED=0 go build -ldflags "-s -w -X main.version=${VERSION#v}"`

## Architecture

`main.go` dispatches CLI (`launch`, `list`/`show`, `add`/`edit`/`remove`, `proxy`, `doctor`, `completion`). Other files are single-responsibility helpers:

- `config.go` — load `config.toml` + `profiles/*.toml` (`profiles/` wins on name collision, `config.go:179`), bootstrap seed profiles on first run (`config.go:323`)
- `profile.go` — env assembly (`buildEnv`, `assembleEnv`), auth resolution, managed-var stripping (`managedVars` — 20+ vars including `ANTHROPIC_*`, `CLAUDE_CODE_*`, Bedrock/Vertex toggles)
- `launch.go` — `exec(2)` replacement of `claude` binary (TTY/signals preserved); checks `~/.claude/settings.json` env-block conflicts before launch
- `proxy.go` / `proxycmd.go` / `daemon_unix.go`+`daemon_other.go` — CLIProxyAPI daemon lifecycle (pid file, log, `Setsid` on unix)
- `claudesettings.go` — reads `~/.claude/settings.json` for model inheritance and conflict detection
- `doctor.go`, `interactive.go`, `completion.go`, `util.go`

## Runtime / config paths

- Config dir: `$CCP_HOME` > `$XDG_CONFIG_HOME/ccp` > `~/.config/ccp` (`config.go:85`)
- State dir: `$CCP_STATE_HOME` > `$XDG_STATE_HOME/ccp` > `~/.local/state/ccp` (`config.go:95`) — holds `cliproxy.pid`, `cliproxy.log`, `bin/cli-proxy-api`
- First run creates `config.toml` + `profiles/glm.toml|kimi.toml|official.toml` — never overwrites existing files (`config.go:308`)
- Profile `type` must be `cliproxy` or `anthropic` (defaults to `anthropic` if empty, `config.go:212`). `cliproxy` reuses `api-keys[0]` from `cliproxy/config.yaml` as bearer token unless `auth_token_env`/`api_key_env` set.
- `${VAR}` / `$VAR` expanded from process env in profile values at launch time (`util.go:101`).
- Proxy binary resolution order (`proxy.go:52`): `config.proxy.binary` > `PATH` (`cli-proxy-api`/`CLIProxyAPI`) > `~/.local/bin/cli-proxy-api` > `<state>/bin/cli-proxy-api`
- `ccp show PROFILE` is the non-destructive way to verify env without launching claude.

## Gotchas

- Managed vars are **fully stripped** then re-applied; stray `ANTHROPIC_*` in shell env will not leak across profiles — but `env` blocks in `~/.claude/settings.json` or `.claude/settings*.json` **override process env** and will silently defeat profiles. `ccp doctor` fails on this; fix by removing keys from settings files.
- `cli-proxy-api` is a daemon — `ccp` auto-starts it but leaves it running between sessions. Tests/manual checks must `ccp proxy stop` if they expect a clean state. `proxyReachable` probes `/` with 500ms timeout (`proxy.go:28`).
- Profile names restricted to `[a-z0-9._-]` (`config.go:218`).
- `install.sh` prefers prebuilt `ccp_<tag>_<os>_<arch>.tar.gz` (verified via `checksums.txt`) and falls back to `go build`; `REF` env selects branch/tag, `CCP_BINDIR` selects install dir.
