## Context

See proposal.md Why: users own N subscriptions for the same provider (Codex, OpenCode Go, Kimi, Anthropic relays) and want `ccp codex` to spread load without manual profile switching. Current `ccp` (`config.go:18`, `profile.go:74`, `launch.go:13`) models one `Profile` -> one auth tuple -> one env set, managed-var stripping then `exec`. First run scaffolds `config.toml` + `profiles/*.toml` and state lives under `ccpStateDir()` (`config.go:95`). `CLIProxyAPI` already does multi-account, per-request routing (round-robin / weighted-round-robin / fill-first via `routing.strategy` in its `config.yaml`), but `ccp` has no way to express a pool.

Constraints shaping the approach:
- Single Go binary, `package main`, no sub-packages, Go >=1.25; lean on `BurntSushi/toml`.
- `ccp` is not a request proxy — it prepares env and `exec`s `claude`. Per-request rotation within one Claude session would require a long-lived proxy.
- TOML already supports `[[accounts]]` (file-per-profile) and `[[profiles.<name>.accounts]]` (inline) with no new dependency.
- State dir already holds `cliproxy.pid`/`cliproxy.log`; adding `routing/<profile>.json` follows that pattern.

## Goals / Non-Goals

**Goals:**
- Single logical profile holds N interchangeable credentials; `ccp <profile>` picks one per invocation and keeps it for the session.
- Round-robin by default for all provider types; deterministic cycling via persisted counter.
- Cliproxy users can keep using `CLIProxyAPI` native pooling for per-request / weighted routing; `ccp` pool is complementary (covers direct `anthropic` relays and `api-keys` pool, optional alongside cliproxy OAuth pool).
- Zero breaking change for existing single-credential configs; new TOML fields optional.

**Non-Goals:**
- `ccp`-side capacity/usage-weighted routing (would require provider-specific quota APIs; delegated to CLIProxyAPI where available).
- Per-request rotation inside one `claude` process without a proxy shim.
- Failover/retry within a launch on 429/auth failure — future work; current behavior is pick-one-and-launch.
- Multi-profile transactions or cross-profile load balancing.

## Decisions

### D1: Pool modeled as ordered `[[accounts]]` inside `Profile`

Add to `config.go:18`:
```go
type Account struct {
    Name         string `toml:"name"`
    BaseURL      string `toml:"base_url"`
    Auth         string `toml:"auth"`
    AuthTokenEnv string `toml:"auth_token_env"`
    APIKeyEnv    string `toml:"api_key_env"`
    AuthToken    string `toml:"auth_token"`
    APIKey       string `toml:"api_key"`
}
type Routing struct {
    Strategy string `toml:"strategy"` // default "round-robin"
}
type Profile struct { // existing fields...
    Accounts []Account `toml:"accounts"`
    Routing  *Routing  `toml:"routing"`
}
```

Rationale: reuses existing field names so mental model and `${VAR}` expansion (`util.go:101`) transfer directly; ordered slice preserves declaration order for round-robin. `Routing` as pointer distinguishes absent (default) from explicit.

Alternative considered: top-level `accounts.toml` or separate `pools/` dir — rejected, breaks `config.go:179` override rule and scatters config.

### D2: Per-invocation, persisted counter — not random, not in-memory

Selector: `idx = nextCounter(profile) % len(pool)` where `nextCounter` atomically reads/increments a per-profile JSON file `<state>/routing/<profile>.json` containing `{"counter": int}`. Single-credential profiles bypass the file entirely.

Rationale: deterministic, survives reboots/shells, simple to test, no daemon. Matches proposal "route requests alternatively". State helpers reuse `ccpStateDir()` and `writeFileIfMissing`-style atomic write (`os.WriteFile` to temp then rename).

Alternative: random pick — rejected, causes uneven spread and flaky tests. Alternative: in-memory LRU inside a long-lived `ccp` daemon — rejected, `ccp` currently has no daemon except cliproxy; adds complexity.

Concurrency: file lock via `syscall.Flock` on unix (`daemon_unix.go` already has platform split) and advisory fallback on other platforms; on lock failure fall back to non-atomic read+write with best-effort but never corrupt (write to `.tmp` then `Rename`). Documented as at-least-once increment; duplicate index under high concurrency is acceptable, corruption is not.

### D3: `ccp` does NOT implement usage-weighted routing

If `type == "cliproxy"` and `Routing.Strategy == "round-robin"`, `ccp` rotates which bearer `api-keys` entry it sends to `CLIProxyAPI`; `CLIProxyAPI` itself still fans out across its own `auth-dir` OAuth pool per its own `routing.strategy` (which can be `weighted-round-robin`). For generic `anthropic` profiles there is no cross-provider usage API, so weighted would be guesswork.

Rationale: keeps `ccp` provider-agnostic; avoids polling provider-specific usage endpoints; aligns with user note "capacity weighted would only work with cliproxy". Future weighted support can be added without changing TOML shape by adding a `weight` field to `Account`.

### D4: Auth resolution per account reuses `profile.go:84` logic

New `func (a *Account) resolveAuth(cfg *Config, profileType string) (*authResult, error)` mirrors `Profile.resolveAuth` but scoped to one entry; `buildEnv` gains a `selectedAccount *Account` path. When `len(Accounts)>0`, top-level auth fields are ignored (with a `warnf` if both present to catch migration errors). Managed-var stripping (`profile.go:31`) unchanged; `ExtraEnv` and model fields remain profile-level (not per-account) to keep `/model` alias semantics simple.

Per-account `base_url` override: `effectiveBaseURL` checks `selectedAccount.BaseURL` first, then profile `BaseURL`, then cliproxy default.

### D5: Observability without secret leakage

- `ccp show <profile>`: iterates pool, calls `resolveAuth` per entry in dry-run mode (missing env var -> show `"$VAR (unset)"`), prints masked tokens via `maskSecret` (`util.go:118`), marks `next = counter % N`.
- `ccp list`: `pad` logic unchanged; append `×N` for N>1 (reuses `modelPlain` width math).
- Launch banner (`launch.go:49`): add `account 2/3 ($CODEX_TOKEN_B)` fragment to `built.Notes`.

### D6: Validation in `Profile.normalize` + `doctor.go`

`normalize` checks: `routing.strategy` in `{"", "round-robin"}`, each `Account` has at least one auth source, `safeName` for `Account.Name` if set. `doctor.go:44` loop extended to iterate `Accounts` and surface per-account failures; `showProfile` follows same error-then-partial pattern.

### D7: `ccp add` wizard and flags

Wizard (`main.go:362`): after type/model/auth, ask "Pool multiple subscriptions under this profile? [y/N]". If yes, prompt count (2..10) then loop: per-account prompts reuse existing auth menu. Flag form for scripting: `--accounts` not ideal for TOML; instead repeatable `--account` flag taking TOML-like `key=value` pairs e.g. `--account auth_token_env=CODEX_A --account api_key_env=CODE` (parsed as mini-TOML into `Account`). Alternative `--num-accounts` with env list rejected as less explicit.

## Risks / Trade-offs

- [Counter file is per-host] Users with shared home via NFS / multiple machines see independent counters -> Mitigation: document as per-machine; acceptable because correctness is "eventual spread", not strict global ordering.
- [Burst of concurrent launches can assign same index] if flock unavailable -> Mitigation: atomic temp+rename prevents corruption; doc notes at-most one duplicate under race.
- [Env var unset at launch for selected account] launch fails for one index while others would succeed -> Mitigation: fail fast with index + var name; future failover can skip to next account (out of scope now).
- [Top-level vs per-account auth both set confuses users] -> Mitigation: `warnf` when both present and prefer pool.
- [TOML array order is semantically significant] surprising for some -> Mitigation: comment in scaffolded file says order = rotation order.
- [State file gid/perm] rotation file holds only an int, not secrets -> `0o600` still fine.

## Migration Plan

1. Ship behind optional fields; no migration of existing files.
2. On first launch of a pooled profile, create `<state>/routing/<profile>.json` lazily; no pre-creation.
3. Rollback: remove `[[accounts]]` from TOML and delete state file — profile reverts to single-credential semantics; older `ccp` binary ignores unknown TOML keys? Actually `BurntSushi/toml` strict-undecoded warn path (`config.go:152`) would warn on `accounts`/`routing` with old binary, but still launch using top-level auth — safe rollback.
4. Docs: update `README.md` with pooled example (Codex ×3) and note "cliproxy native pooling is still the way to get per-request routing".

## Open Questions

- Flag spelling for scripted pool creation (`--account` vs `--pool-entry`) — bikeshed deferrable; spec requires *some* repeatable flag, not exact name.
- Whether to clean `routing/<profile>.json` on `ccp remove <profile>` automatically (spec says it SHALL) vs leaving it for reinstall — trivial.
- Future weighted support: add `weight int` to `Account` and extend `Routing.Strategy` to `weighted-round-robin`; no spec change needed beyond additive.

