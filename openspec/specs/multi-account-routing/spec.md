## Purpose

Enables one logical `ccp` profile (e.g., `ccp codex`) to represent multiple interchangeable subscriptions/credentials for the same provider and to rotate among them deterministically per launch, so users can aggregate quota without manual profile switching.

## Requirements

### Requirement: Multi-credential profile declaration
A profile SHALL optionally declare an ordered list of interchangeable credentials ("accounts") instead of a single auth tuple, and the system SHALL treat the list as the source of truth for credential selection when it is non-empty.

#### Scenario: Single-credential profile unchanged
- **WHEN** a profile defines no `accounts` list (only the existing `auth_token_env`/`api_key_env`/`auth_token`/`api_key`/`auth` fields)
- **THEN** credential resolution behaves exactly as before the change (backward compatible, no rotation)

#### Scenario: Multi-credential profile declaration via file-per-profile
- **WHEN** a user writes `profiles/codex.toml` containing `[[accounts]]` array entries each with its own auth fields (e.g., `auth_token_env = "CODEX_TOKEN_A"`) and optional `base_url` override
- **THEN** the system SHALL parse all entries in order, reject the profile with a clear error if any entry lacks a resolvable auth source, and persist the ordered pool

#### Scenario: Multi-credential profile declaration inline in config.toml
- **WHEN** a user writes `[[profiles.codex.accounts]]` entries inside `config.toml`
- **THEN** the system SHALL parse them identically to the file-per-profile form, with `profiles/codex.toml` still winning on name collision per existing rule (`config.go:179`)

#### Scenario: Empty accounts list is not a pool
- **WHEN** `accounts = []` or zero `[[accounts]]` tables are present
- **THEN** the profile SHALL be treated as single-credential (as if the field were absent), not as an error

### Requirement: Account entry schema
Each account entry SHALL be an independent auth tuple that reuses the same auth field names as the top-level profile (`auth`, `auth_token_env`, `api_key_env`, `auth_token`, `api_key`, and optional `base_url`), and SHALL support `${VAR}`/`$VAR` expansion at launch time.

#### Scenario: Account auth from environment
- **WHEN** an account specifies `auth_token_env = "CODEX_TOKEN_B"` and `CODEX_TOKEN_B` is set in the launching shell
- **THEN** the selected launch SHALL inject `ANTHROPIC_AUTH_TOKEN=<value of CODEX_TOKEN_B>` exactly as a single-credential profile would

#### Scenario: Mixed account auth types in one profile
- **WHEN** a profile's pool contains one account with `auth_token_env`, one with `api_key_env`, and one with literal `auth_token` (all for the same logical provider/endpoint)
- **THEN** the system SHALL allow the mix and resolve each account according to its own fields, selecting the appropriate env var (`ANTHROPIC_AUTH_TOKEN` vs `ANTHROPIC_API_KEY`) per account

#### Scenario: Secret values expand env references
- **WHEN** an account's `auth_token = "${CODEX_TOKEN_C}"` or `base_url = "https://${CODEX_HOST}/v1"`
- **THEN** the value SHALL be expanded from the process environment at launch/show time, matching existing profile expansion (`util.go:101`)

### Requirement: Per-profile routing strategy
A profile with a non-empty `accounts` pool SHALL declare a routing strategy; `round-robin` SHALL be the default and the only strategy that `ccp` itself implements. `ccp` SHALL NOT attempt capacity/usage-weighted routing; that concern is delegated to `CLIProxyAPI` for `type = "cliproxy"` pools.

#### Scenario: Default strategy is round-robin
- **WHEN** a pool is declared without an explicit `routing.strategy` (or with `routing.strategy = "round-robin"`)
- **THEN** successive `ccp <profile>` launches SHALL cycle through accounts in declaration order, wrapping around

#### Scenario: Cliproxy capacity-weighted delegation
- **WHEN** a `type = "cliproxy"` profile uses `CLIProxyAPI` multi-account pooling (multiple OAuth files in `auth-dir` or multiple `api-keys`)
- **THEN** `ccp` SHALL document and allow that `CLIProxyAPI`'s own `routing.strategy` (including `weighted-round-robin`) provides per-request capacity-weighted routing, and `ccp` SHALL NOT duplicate usage fetching or quota inspection

#### Scenario: Unknown strategy rejected early
- **WHEN** a profile declares `routing.strategy = "capacity-weighted"` or any string other than the supported set (`round-robin`)
- **THEN** `ccp doctor` and `ccp show <profile>` SHALL report a validation failure naming the unknown strategy and the profile, and launch SHALL abort with a clear error

### Requirement: Deterministic per-invocation rotation with persistent state
The system SHALL select exactly one account per `ccp <profile>` invocation, advance a persistent per-profile counter so successive launches visit every account, and keep the selection stable for the lifetime of that Claude session.

#### Scenario: Successive launches rotate
- **WHEN** a pool has 3 accounts and the user runs `ccp codex` three times in sequence (separate processes)
- **THEN** the selected account index SHALL be `counter % 3` where `counter` starts at 0 and increments by exactly one per successful selection, producing the order `0,1,2,0,…` deterministically

#### Scenario: State persists across reboots
- **WHEN** the counter state file for a profile is persisted under the state directory (e.g., `<state>/routing/<profile>.json`)
- **THEN** a reboot or new shell SHALL continue the cycle from the last persisted counter value rather than resetting to 0

#### Scenario: One session keeps one credential
- **WHEN** a Claude session is launched with account index `k`
- **THEN** no request within that session SHALL re-select or rotate credentials; rotation only occurs on the next `ccp <profile>` invocation

#### Scenario: Corrupt or missing state file is recovered
- **WHEN** the state file is missing, empty, or contains invalid JSON
- **THEN** the system SHALL treat the counter as 0, recreate the file atomically, and continue rotation without crashing

#### Scenario: Concurrent launches do not double-select
- **WHEN** two `ccp <profile>` processes launch concurrently for the same profile
- **THEN** each SHALL acquire an exclusive file lock (or atomic read-modify-write) on the state file so they receive distinct indices where possible, and at minimum SHALL NOT corrupt the counter file

### Requirement: Base URL and endpoint handling for pools
A profile's `base_url` SHALL serve as the default endpoint for all accounts in the pool; an individual account's `base_url` SHALL override only that account's endpoint. `type = "cliproxy"` default endpoint derivation (`http://<proxy.host>:<proxy.port>`) SHALL remain unchanged.

#### Scenario: Shared endpoint for direct providers
- **WHEN** a `type = "anthropic"` pool sets `base_url = "https://api.openai.example/anthropic"` at the profile level and no per-account `base_url`
- **THEN** every rotated launch SHALL set `ANTHROPIC_BASE_URL` to that shared value regardless of which account is selected

#### Scenario: Per-account endpoint override
- **WHEN** account `B` declares `base_url = "https://api-b.example/v1"` while the profile default is different
- **THEN** a launch that selects `B` SHALL set `ANTHROPIC_BASE_URL` to `B`'s value, and a launch that selects `A` (no override) SHALL use the profile default

### Requirement: Environment assembly for selected account
The selected account's auth result SHALL be injected via the same managed-env stripping and assembly path as a single-credential profile, so no credential from another account in the pool leaks into the child environment.

#### Scenario: Only selected credential is present
- **WHEN** `ccp codex` selects account `B` (`auth_token_env = "CODEX_B"`)
- **THEN** the child environment SHALL contain exactly one of `ANTHROPIC_AUTH_TOKEN` or `ANTHROPIC_API_KEY` (whichever `B` resolves to), and SHALL NOT contain tokens/keys from accounts `A` or `C`

#### Scenario: Managed-vars stripping still applies
- **WHEN** the launching shell has stray `ANTHROPIC_API_KEY` / `ANTHROPIC_AUTH_TOKEN` / `CLAUDE_CODE_*` in its env
- **THEN** all managed vars SHALL be stripped before the selected account's credential is applied, identical to current `profile.go:managedVars` behavior

### Requirement: Observability — show, list, and logs
Users SHALL be able to inspect multi-account profiles without executing them and to tell from launch output which account was chosen.

#### Scenario: Show displays pool without leaking secrets
- **WHEN** the user runs `ccp show codex` for a 3-account pool
- **THEN** the output SHALL list each account by index (and optional `name`/`description` if provided) with the auth source (e.g., `$CODEX_TOKEN_A`, `api_key (config)`, `proxy api-keys[1]`), mask all secret values, indicate which index would be selected next, and note the routing strategy

#### Scenario: List indicates pool size
- **WHEN** the user runs `ccp list`
- **THEN** a profile with N>1 accounts SHALL show an indicator such as `×N` or `pool:N` alongside its type/model, while a single-credential profile SHALL show no such indicator (no change to column layout for the single-credential case)

#### Scenario: Launch banner names the chosen account
- **WHEN** `ccp codex` launches and selects account index `k` (e.g., `1` with source `$CODEX_TOKEN_B`)
- **THEN** the banner printed to stderr SHALL include `account k/N ($CODEX_TOKEN_B)` (or equivalent) in addition to the existing `profile · url · model · auth` notes, unless `--quiet` suppresses it

### Requirement: Validation and doctor checks
Configuration errors in a pool SHALL be surfaced by `ccp doctor` and by `ccp show`/`launch` with actionable messages; `ccp doctor` SHALL exit non-zero on any such failure.

#### Scenario: Profile with invalid account is flagged
- **WHEN** an account's `auth_token_env` names an env var that is unset at `doctor` time, or an entry has no auth fields at all, or a name contains illegal characters
- **THEN** `ccp doctor` SHALL report a `fail` for that profile (naming the account index and field), and `ccp show codex` SHALL warn and show partial config per existing error-then-show-partial pattern

#### Scenario: Profile name validation applies to pool
- **WHEN** a pool is declared for a profile whose name fails `safeName` (`config.go:218`, `[a-z0-9._-]` only)
- **THEN** loading SHALL reject it exactly as it would a single-credential profile

### Requirement: Interactive creation and editing
The existing `ccp add` wizard (no-arg form) and `ccp edit`/`ccp remove` pickers SHALL support multi-account profiles, and `ccp add NAME` flags SHALL allow at least a minimal declarative form for scripting.

#### Scenario: Wizard creates a pool
- **WHEN** the user runs `ccp add` without args and chooses `codex/multi-account`
- **THEN** the wizard SHALL prompt for how many accounts, loop to collect each account's auth source (env var name vs literal vs proxy default), optionally collect per-account base_url, prompt for routing strategy (default round-robin), write `profiles/<name>.toml` with `[[accounts]]` entries, and confirm with `ccp show <name>`

#### Scenario: Scripted add of a pool
- **WHEN** the user runs `ccp add codex --type anthropic --account auth_token_env=CODEX_A --account api_key_env=OPENCODE_B …` (exact flag spelling to be decided in design)
- **THEN** the command SHALL create the same on-disk TOML structure as the wizard, and fail with usage if `--account` syntax is malformed

#### Scenario: Edit and remove handle pools
- **WHEN** the user runs `ccp edit codex` or `ccp remove codex` for a pooled profile
- **THEN** edit SHALL open the file-per-profile TOML (or instruct to edit `config.toml` inline table), and remove SHALL delete the file and its routing state file

### Requirement: Profile file collision semantics preserved
File-per-profile (`profiles/<name>.toml`) SHALL continue to win over inline `[profiles.<name>]` in `config.toml` on name collision, including when either side declares a pool; the winning file's entire pool (or lack thereof) fully replaces the loser's.

#### Scenario: Override replaces entire pool
- **WHEN** `config.toml` declares `[profiles.codex]` with 2 `[[profiles.codex.accounts]]` and `profiles/codex.toml` declares 3 `[[accounts]]`
- **THEN** the loaded profile SHALL have exactly the 3 accounts from `profiles/codex.toml` with no merging

### Requirement: No breaking change to existing profiles
A `ccp` binary that implements this capability SHALL load and launch any configuration that was valid before the change with identical observable behavior (same env injected for same inputs).

#### Scenario: Old config launches identically
- **WHEN** a pre-change config has 3 single-credential profiles (`glm`, `kimi`, `official`)
- **THEN** after the update `ccp show glm`, `ccp list`, `ccp doctor`, and `ccp glm -- <args>` SHALL produce the same environment and exit behavior as before, and no new files are required
