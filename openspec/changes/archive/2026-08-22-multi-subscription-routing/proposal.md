## Why

Users who own multiple subscriptions for the same provider (e.g., several Codex / OpenAI OAuth accounts, multiple OpenCode Go plans, or several Kimi/Anthropic quota pools) currently must create a separate `ccp` profile per subscription and manually switch between them (`ccp codex-a`, `ccp codex-b`). This defeats the goal of a single `ccp codex` command that spreads load across all owned quota, leads to one account hitting rate-limits while others sit idle, and forces users to write external wrappers to rotate credentials. `CLIProxyAPI` already pools multiple upstream accounts with round-robin / weighted routing, but `ccp` only injects a single credential per profile and has no way to express "this profile represents N interchangeable accounts".

## What Changes

- Extend profile configuration (`profiles/*.toml` and `[profiles.*]` in `config.toml`) to allow a single logical profile to hold multiple interchangeable credentials (auth entries) instead of exactly one.
- Add a per-profile routing strategy with at least `round-robin` (default, works for every provider type) and a passthrough/delegate mode for `cliproxy` where `CLIProxyAPI`'s native multi-account pool does the per-request routing (no `ccp`-side usage fetching required). Capacity/usage-weighted routing is explicitly out of scope for `ccp` itself; it is delegated to `CLIProxyAPI` when the upstream exposes usage.
- Persist minimal, per-profile rotation state under the state directory so successive `ccp <profile>` launches cycle deterministically through the pool rather than picking randomly each time. A single long-lived Claude session keeps the selected credential for its lifetime.
- Update CLI surfaces (`ccp show`, `ccp list`, `ccp doctor`, wizard `ccp add`) to display, validate, and interactively create multi-account profiles, while remaining fully backwards compatible with single-credential profiles.
- Document the relationship to `CLIProxyAPI` native pooling: adding more OAuth accounts to `~/.cli-proxy-api` is still the recommended way to scale `cliproxy` profiles; the new `ccp` feature covers direct `anthropic` providers and the `api-keys` bearer pool, and can optionally complement `CLIProxyAPI` for users who want `ccp`-side selection.

## Capabilities

### New Capabilities
- `multi-account-routing`: Single logical profile maps to N interchangeable credentials with a configurable routing strategy; `ccp` selects exactly one credential per launch and injects it as the profile env (`ANTHROPIC_AUTH_TOKEN` / `ANTHROPIC_API_KEY` + `ANTHROPIC_BASE_URL`). Includes config syntax, per-invocation rotation persistence, strategy semantics, and observability.

### Modified Capabilities
- _None_ — existing `profile` / `proxy` behavior is extended, not redefined; single-credential profiles continue to work unchanged. If later review shows requirement text in an existing spec must change, a delta will be added before `tasks` is finalized.

## Impact

- **Config**: `config.go` profile loading / `Profile.normalize`, new `Account` / `Routing` structs, validation of pool size / strategy / name regex. No change to global `config.toml` `[proxy]` semantics.
- **Env assembly / launch**: `profile.go` `buildEnv`/`resolveAuth` and `launch.go` `launch`/`showProfile` — selection of credential per invocation, writing to `ccpStateDir()/routing/<profile>.json` (or similar) for round-robin counter, atomic file update.
- **UX**: `main.go` wizard + `doctor.go` checks, `ccp show` masking per-account secrets, `ccp list` hinting `×N` pool size.
- **Docs**: `README.md` + `AGENTS.md` updated with multi-account example. No new external dependencies; `golang.org/x/sys` etc. unchanged.
- **Breaking**: None. New fields are optional; absent pool = current single-credential behavior.
