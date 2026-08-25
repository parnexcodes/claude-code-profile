# openai-translation Specification

## Purpose

Enable `ccp` users to add any OpenAI-compatible model (Opencode Go `muse-spark-1.2-contributor` at `https://opencode.ai/zen/go/v1/responses`, `/v1/chat/completions` providers, OpenRouter) via `ccp add` alone — `ccp` transparently manages `CLIProxyAPI:openai-compatibility` translation so `claude` continues to speak Anthropic `POST /v1/messages` without the user ever touching `cliproxy` YAML or `ccp proxy` commands.

## Requirements

### Requirement: Declarative OpenAI upstream on a cliproxy profile
A `type = "cliproxy"` profile SHALL optionally declare a single OpenAI-compatible upstream (base URL + credential + upstream model mapping) that `ccp` materializes into `cliproxy/config.yaml:openai-compatibility`. The declaration SHALL live in `profiles/<name>.toml` and SHALL be the source of truth; the YAML entry is derived.

#### Scenario: Profile declares OpenAI upstream
- **WHEN** `profiles/muse.toml` contains `type = "cliproxy"`, `model = "muse-spark-1.2-contributor"`, `upstream_base_url = "https://opencode.ai/zen/go/v1"`, and `upstream_api_key_env = "OPENCODE_GO_API_KEY"` (or `upstream_api_key` literal)
- **THEN** the system SHALL treat the profile as "translated OpenAI" and SHALL NOT require `base_url` to be set to the proxy address (it defaults to `http://127.0.0.1:<port>`)

#### Scenario: Upstream fields support env expansion
- **WHEN** `upstream_base_url = "https://${OPENCODE_HOST}/v1"` or `upstream_api_key = "${TOKEN}"`
- **THEN** the value SHALL be expanded from the process environment at `launch`/`show`/`doctor` time, leaving unknown `$VAR` untouched, matching existing `util.ExpandEnvVars` behavior

#### Scenario: Per-account upstream override for pools
- **WHEN** a pooled `type = "cliproxy"` profile declares `[[accounts]]` where one account sets `upstream_base_url` or `upstream_api_key_env` distinct from the profile default
- **THEN** a launch that selects that account SHALL use its override for the upstream entry matched by alias, and other accounts SHALL use the profile default

#### Scenario: Profile without upstream unchanged
- **WHEN** a `type = "cliproxy"` profile declares no `upstream_*` fields (e.g., existing `glm`, `kimi` OAuth profiles)
- **THEN** it SHALL behave exactly as before (no `openai-compatibility` entry required, proxy pools OAuth accounts in `auth-dir`)

### Requirement: Single-command creation via wizard and flags
`ccp add` SHALL create a translated profile without any manual YAML edit or `ccp proxy` command. Both the interactive wizard and flag-based form SHALL be supported.

#### Scenario: Wizard creates translated profile
- **WHEN** the user runs `ccp add` with no args, picks `cliproxy` type, and answers the upstream prompts (upstream base URL `https://opencode.ai/zen/go/v1`, upstream key source env-var `OPENCODE_GO_API_KEY`, model `muse-spark-1.2-contributor`)
- **THEN** the wizard SHALL write `profiles/<name>.toml` with `upstream_base_url`, `upstream_api_key_env`, and `model`, and SHALL immediately materialize the corresponding `openai-compatibility` entry without further user steps

#### Scenario: Flag-based creation for scripting
- **WHEN** the user runs `ccp add muse --type cliproxy --model muse-spark-1.2-contributor --upstream-base-url https://opencode.ai/zen/go/v1 --upstream-api-key-env OPENCODE_GO_API_KEY` (and optional `--upstream-model`/`--alias` for distinct alias)
- **THEN** the command SHALL create the same TOML and YAML state as the wizard, fail with usage if required upstream flags are missing for a translated profile, and print `ccp show <name>` hint on success

#### Scenario: Upstream validation at creation
- **WHEN** `--upstream-base-url` is empty, malformed URL, or `--upstream-api-key-env` names an unset var and no literal key is provided
- **THEN** `ccp add` SHALL reject with an actionable error naming the field and SHALL NOT write a partial profile or YAML entry

### Requirement: Authoritative YAML merge and daemon lifecycle
`ccp` SHALL own the subset of `cliproxy/config.yaml:openai-compatibility` that corresponds to `ccp`-managed profiles. It SHALL merge atomically, preserve all non-managed keys, and ensure the daemon serves the new model without manual restart.

#### Scenario: Atomic merge preserves user config
- **WHEN** `cliproxy/config.yaml` already contains `port`, `api-keys`, `auth-dir`, and unrelated `openai-compatibility` entries not owned by `ccp`
- **THEN** materializing a new `ccp` profile SHALL retain those keys verbatim, add or update only the entry whose `name` equals the profile name (or `upstream_name` if provided), use `tmp+rename` under `0600`, and SHALL NOT reorder or strip comments beyond YAML round-trip

#### Scenario: Daemon hot-reload or auto-start
- **WHEN** the proxy is already reachable on `127.0.0.1:<port>` and a new upstream is materialized
- **THEN** `ccp` SHALL trigger the proxy to reload the config (config file watcher in `service_lifecycle.go:196` or explicit `SIGHUP`/`restart` fallback) so `ccp proxy models` lists the new alias within `start_timeout_secs` without the user running `ccp proxy restart`

#### Scenario: Proxy not installed is installed once
- **WHEN** `cliproxy` binary is absent and the user runs `ccp add` for a translated profile
- **THEN** `ccp` SHALL run the equivalent of `ccp proxy install` (download verified release) before scaffolding the YAML entry, and SHALL surface a clear error if install fails

#### Scenario: Removal cleans up deterministically
- **WHEN** the user runs `ccp remove <name>` for a translated profile
- **THEN** `ccp` SHALL delete `profiles/<name>.toml`, remove its `openai-compatibility` entry from `cliproxy/config.yaml` (leaving other entries), clear its `routing/<name>.json` state if pooled, and reload/restart the daemon if running

### Requirement: Env assembly and launch remain Anthropic-surface
Launch of a translated profile SHALL still strip all managed vars and set `ANTHROPIC_BASE_URL=http://127.0.0.1:<port>`, `ANTHROPIC_MODEL=<alias>`, and auth to the proxy's `api-keys[0]` (or profile override), so `claude` never sees the OpenAI endpoint.

#### Scenario: Translated launch env
- **WHEN** `ccp muse` launches a profile with `upstream_base_url=https://opencode.ai/zen/go/v1` and `model=muse-spark-1.2-contributor`
- **THEN** the child env SHALL contain `ANTHROPIC_BASE_URL=http://127.0.0.1:8317`, `ANTHROPIC_MODEL=muse-spark-1.2-contributor`, and `ANTHROPIC_AUTH_TOKEN=<proxy api-keys[0]>`, and SHALL NOT contain the upstream URL or Zen key; the proxy forwards `POST /v1/messages` as OpenAI `POST /v1/responses` upstream

#### Scenario: Auto-start on launch
- **WHEN** `p.Type == "cliproxy"` and the proxy is down at `ccp <profile>` time and `proxy.auto_start` is true
- **THEN** launch SHALL start the proxy (scaffolding YAML if missing) and block up to `start_timeout_secs` until `/` is reachable before `buildEnv`, identical to existing `launch.go:17-25` behavior

### Requirement: Observability — show, list, doctor, models
Users SHALL be able to verify a translated profile without launching `claude`, and misconfiguration SHALL be surfaced by `ccp doctor`.

#### Scenario: Show displays upstream without leaking secret
- **WHEN** the user runs `ccp show muse` for a translated profile
- **THEN** output SHALL list `type: cliproxy (translated)`, `model/alias`, `upstream base-url`, `upstream auth source` (e.g., `$OPENCODE_GO_API_KEY` or `literal ****abcd`), proxy URL, and whether the `openai-compatibility` entry is in sync or drifted, with all secret values masked via `MaskSecret`

#### Scenario: List indicates translated profiles
- **WHEN** the user runs `ccp list`
- **THEN** a translated profile SHALL show an indicator (e.g., `cliproxy:openai` or `translated`) distinct from plain `cliproxy` OAuth and `anthropic` direct, without breaking existing column layout

#### Scenario: Doctor validates upstream
- **WHEN** `ccp doctor` runs and a translated profile has `upstream_api_key_env` unset, `upstream_base_url` unreachable (probe `GET <base-url>/models` with 500 ms timeout), or YAML entry missing/drifted
- **THEN** `doctor` SHALL report `fail` naming the profile, field, and remediation (`export VAR`, `ccp add` re-run, `ccp proxy logs`), and exit non-zero

#### Scenario: Proxy models includes translated alias
- **WHEN** the proxy is up and the upstream is configured
- **THEN** `ccp proxy models` (fetch from `/v1/models`) SHALL list the alias, proving the translation path is live

### Requirement: Backward compatibility and safety
Existing valid configs SHALL load and launch identically after the change; secrets SHALL never be written to world-readable files; concurrent `ccp add`/`launch` SHALL NOT corrupt YAML.

#### Scenario: Old config launches identically
- **WHEN** a pre-change config has `profiles/glm.toml` (`type=cliproxy` OAuth), `profiles/kimi.toml`, and `profiles/official.toml` (`type=anthropic auth=none`)
- **THEN** after the update `ccp show glm`, `ccp list`, `ccp doctor`, and `ccp glm -- <args>` SHALL produce the same env and exit behavior, and no `openai-compatibility` entries are added implicitly

#### Scenario: Secret file permissions
- **WHEN** `ccp add` writes `profiles/<name>.toml` containing `upstream_api_key` literal or `cliproxy/config.yaml` containing an `api-key` literal
- **THEN** the file SHALL be created with `0600` and parent dirs `0700`, matching existing `saveProfile`/`scaffoldProxyConfig` perms

#### Scenario: Concurrent add/launch does not corrupt YAML
- **WHEN** two `ccp` processes materialize or launch the same translated profile concurrently
- **THEN** the YAML file SHALL remain valid YAML and contain exactly one entry per profile name, via file lock or `tmp+rename` atomicity, and no partial write SHALL be observed

#### Scenario: Opencode Go Muse Spark end-to-end
- **WHEN** the user runs `ccp add muse --type cliproxy --model muse-spark-1.2-contributor --upstream-base-url https://opencode.ai/zen/go/v1 --upstream-api-key-env OPENCODE_GO_API_KEY` with `OPENCODE_GO_API_KEY` exported, then `ccp show muse` and `ccp muse -- --help`
- **THEN** `show` SHALL display the upstream in sync, `list` SHALL mark it translated, and `muse` SHALL launch `claude` pointed at the local proxy without any `ccp proxy *` or hand-edited YAML
