## Context

`ccp` is a single-binary profile launcher (`cmd/ccp` → `internal/*`). Current translation path: `claude --Anthropic /v1/messages--> 127.0.0.1:8317 (CLIProxyAPI) --OpenAI Responses/Chat--> upstream`. `ccp` already auto-starts the daemon, scaffolds `cliproxy/config.yaml` with `port/auth-dir/api-keys`, and strips/re-applies `ManagedVars` (`internal/profile/profile.go`, `internal/cli/launch.go:17`, `internal/proxy/proxy.go:88`). The missing piece is upstream registration: `openai-compatibility[].base-url/api-key-entries/models` must be hand-edited (see `config.example.yaml:610`). Direct `type=anthropic` cannot speak OpenAI Responses (Opencode Go `muse-spark-1.2-contributor` at `https://opencode.ai/zen/go/v1/responses`), so that model is unusable without CLIProxyAPI. See proposal for motivation; specs in `specs/openai-translation/spec.md` define the observable contract.

Constraints: preserve `AGENTS.md` DAG `util←config←routing←profile←proxy←settings←cli←cmd/ccp`; no import cycles; `0700`/`0600` perms; atomic `tmp+rename` + `flock` on Unix (existing `routing/daemon_unix.go` pattern); `${VAR}` expansion at launch; tests hermetic via `CCP_HOME`/`CCP_STATE_HOME`.

## Goals / Non-Goals

**Goals:**
- One-command `ccp add` for any OpenAI-compatible upstream (Opencode Go, OpenRouter, custom) with zero `ccp proxy` or YAML editing; `ccp <profile>` always hits local proxy which translates.
- Declarative source of truth in `profiles/*.toml:upstream_*`; `cliproxy/config.yaml:openai-compatibility` is derived and stays in sync on `add`/`launch`/`remove`.
- Preserve/unify existing `type=anthropic` (direct) and `type=cliproxy` OAuth (no upstream) paths.

**Non-Goals:**
- Weighted/capacity routing for OpenAI upstreams (leave to CLIProxyAPI if later needed).
- Generic plugin or vertex/gemini upstream management.
- Remote management API or LAN exposure.
- Migrating existing profiles automatically (explicit `ccp add` only).

## Decisions

**Decision: Declarative `upstream_*` fields on `Profile`/`Account`, not hidden state file**
- Why: `config.go:37 Profile` already owns `BaseURL/Model/Auth`; adding `UpstreamBaseURL`, `UpstreamAPIKeyEnv`, `UpstreamAPIKey`, `UpstreamName`, `UpstreamModelAlias` keeps `profiles/*.toml` human-editable, diffable, and compatible with `${VAR}` expansion. Alternative one-shot YAML write with no TOML back-pointer would drift (user edits `config.yaml` not reflected) and break `ccp show`/`doctor` reasoning.
- Trade: TOML grows; mitigated by omitting fields when not translated (`hasUpstream()` guard).

**Decision: `ccp` merges `openai-compatibility` by profile name, YAML library `gopkg.in/yaml.v3` with typed struct + `yaml.Node` fallback**
- Why: preserve order/comments for non-managed entries. Approach: load YAML into `map[string]any` / typed `proxyYAML` carrying `OpenAICompatibility []OpenAICompatEntry`, replace only entry where `name == profileName` (or `upstream_name`), append if missing, remove on `ccp remove`. Use atomic `tmp+rename` (existing `routing` pattern). Alternative full rewrite from struct would clobber user comments.
- Chosen over `yq`/`sed` text edit (fragile).

**Decision: Daemon reload via file watcher + fallback `stopQuietly+startProxy`**
- Why: CLIProxyAPI already watches `config.yaml` (`service_lifecycle.go:196`, `core auth auto-refresh`). Poll `proxyReachable` after write; if not reloaded within 1 s, `stopQuietly(pid)` + `startProxy`. No new IPC needed. Alternative explicit `SIGHUP` via pid file adds platform branches.

**Decision: Auto-install binary on missing `cliproxy`**
- Why: `proxy.findProxyBinary` already searches `config.proxy.binary > PATH > ~/.local/bin > <state>/bin`; `installProxy()` downloads verified release. Calling it from `handleAdd` when `type=cliproxy` + upstream set avoids first-launch failure. No auto-install for `type=anthropic` direct.

**Decision: Wizard branching after `Type` selection**
- Flow `runAddWizard()`: if `typ=="cliproxy"` → ask "Translate generic OpenAI upstream? (y/N)" → if yes, prompt `Upstream base URL` (validate URL, normalize trailing `/`), `Upstream auth` (reuse same 5-option menu but for upstream), `Upstream model name` vs `alias` (alias defaults to `model`). If no, keep old OAuth path (no upstream fields). Keeps `anthropic` direct path unchanged.

**Decision: Flag spelling `--upstream-base-url`, `--upstream-api-key-env`, `--upstream-api-key`, `--upstream-model`, `--upstream-name`**
- Consistent with existing `--base-url/--api-key-env`; supports pooling: `--account upstream_base_url=...,upstream_api_key_env=...` via `parseAccountSpec` extension. Alternative `--openai-*` prefix rejected as CLIProxyAPI-internal naming leak.

**Decision: `internal/proxy` owns YAML, `internal/config` owns TOML, `internal/cli` orchestrates**
- Preserves DAG: `proxy` never imports `cli`; `config` remains leaf. `cli` calls `proxy.SyncOpenAICompat(profile)` before `saveProfile`. Tests mock `CCP_HOME` tmpdir.

## Risks / Trade-offs

- **YAML round-trip comment loss** → Mitigation: use `yaml.Node` to preserve comments where possible; document that managed block is rewritten but non-managed keys kept verbatim; add golden test for merge with pre-existing `openai-compatibility` entries.
- **Proxy reload race** (launch immediately after `add`) → Mitigation: `launch.go:17` already waits `start_timeout_secs` on `proxyReachable`; extend to poll `/v1/models` for alias presence.
- **Secret in `cliproxy/config.yaml` plaintext** → Mitigation: keep `0600`, prefer `upstream_api_key_env` (write placeholder `${VAR}` and let daemon expand or `ccp` expand at sync time? CLIProxyAPI expects literal `api-key`; so `ccp` resolves env at sync time and writes literal, or writes env-var reference if daemon supports it — fallback to literal with `warnf`).
- **Upstream base URL normalization** (`/v1` suffix) → Mitigation: normalize `TrimRight("/")` and require `/v1` prefix; warn if user pastes full `/v1/responses` endpoint (strip to `/v1`); CLIProxyAPI expects `base-url` like `https://opencode.ai/zen/go/v1`.
- **Concurrent `ccp add`/`launch` YAML corruption** → Mitigation: `flock` on `cliproxy/config.yaml.lock` (reuse `routing/daemon_unix.go` pattern) + `tmp+rename`.

## Migration Plan

- Deploy: binary update only; no migration for existing configs. New `upstream_*` fields are optional; `loadConfig` ignores unknown keys with warn, so old binary reads new TOML as warn-only (forward-compat via `BurntSushi/toml` strict mode already warns on undecoded keys — new fields will be preserved on round-trip via `renderProfileToml`).
- Rollback: new binary writes `upstream_*` to TOML; old binary warns but launches fallback to local proxy with last good YAML entry (since YAML is already materialized). To fully rollback, delete `upstream_*` lines and `openai-compatibility` entry manually.
- Verification: `go test ./... -count=1 -race`, `gofmt`, `golangci-lint`; hermetic tests with `t.TempDir()+CCP_HOME` covering wizard input sequencing, YAML merge golden files, and `doctor` fail cases.

## Open Questions

- CLIProxyAPI `api-key-entries[].api-key` env-var interpolation: if daemon does not expand `${VAR}`, should `ccp` expand at sync time (writing literal) vs asking user for literal? Defer to spike; default to expanding at sync time and documenting.
- Alias pooling (`same alias → multiple upstream names` for round-robin) out of scope for v1; single alias per profile suffices (spec covers per-account override, not multi-model pool).
