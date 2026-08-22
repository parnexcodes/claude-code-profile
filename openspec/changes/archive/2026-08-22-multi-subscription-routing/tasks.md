## 1. Config model and parsing

- [x] 1.1 Add `Account` and `Routing` structs to `config.go` and extend `Profile` with `Accounts []Account` + `Routing *Routing`; add TOML tags and `normalize()` validation for strategy (`round-robin` default), per-account auth presence, and optional `name` safeName check.
- [x] 1.2 Update `loadConfig()` to decode both file-per-profile `[[accounts]]` and inline `[[profiles.<name>.accounts]]` forms; verify profiles dir wins on collision replaces entire pool; warn when top-level auth and pool coexist.
- [x] 1.3 Add unit-level smoke helpers (no *_test.go required) to manually verify `go vet` and `gofmt` pass for new structs; ensure unknown `accounts`/`routing` keys surface via existing `Undecoded` warn path.

## 2. Routing state and selection

- [x] 2.1 Implement routing state helpers: `routingStatePath(profile)`, `readRoutingCounter`, `nextRoutingIndex(profile, poolSize)` with atomic temp-file+rename, JSON `{"counter":int}`, flock on unix (`daemon_unix.go` path) and best-effort fallback elsewhere; corrupt/missing file recovers to 0.
- [x] 2.2 Add `selectAccount(profile) (int, *Account, error)` using `nextCounter % len(Accounts)`; bypass entirely when `len(Accounts)==0`; integrate counter increment on every successful launch path only (not on `show` dry-run where read-only peek is used).
- [x] 2.3 Add `routingStateDir()` `<state>/routing` creation on demand with `0o700`; ensure `ccp remove <profile>` cleans `routing/<profile>.json` (best-effort).

## 3. Auth and env assembly

- [x] 3.1 Add `func (a *Account) resolveAuth(cfg *Config, profileType string) (*authResult, error)` mirroring `Profile.resolveAuth` but scoped to account fields; support `${VAR}` expansion, `cliproxy` bearer fallback via `readProxyAPIKeys` when account fields empty and profile type is cliproxy.
- [x] 3.2 Update `buildEnv(cfg, name, p)` to branch on pool: when `len(Accounts)>0` call `selectAccount` (or peek for `show`), use account's `BaseURL` override and `resolveAuth`, inject only selected credential; preserve managed-var stripping and model alias wiring.
- [x] 3.3 Update `effectiveBaseURL` handling so per-account `base_url` overrides profile `base_url` which overrides cliproxy default; ensure `${VAR}` expansion there as well.

## 4. Launch, show, list, doctor surfaces

- [x] 4.1 Update `launch.go:launch` banner to include `account K/N (source)` from `builtEnv` when pooled, respecting `-q/--quiet`; ensure single-account path banner unchanged.
- [x] 4.2 Update `launch.go:showProfile` to enumerate pool entries: index, optional name, masked auth source via `maskSecret`, endpoint per entry, strategy, and `next = counter % N`; handle partial errors per existing warn-then-show-partial pattern.
- [x] 4.3 Update `launch.go:listProfiles` to append `×N` indicator for N>1 pools without disturbing column alignment for single-account rows; reuse `modelPlain`/`pad` helpers.
- [x] 4.4 Extend `doctor.go:runDoctor` to validate each account (unknown strategy, missing auth source, unset `*_env` at doctor time); fail with profile + index + field; keep existing perf/no-proxy checks green when not in use.

## 5. CLI wizard and scripting flags

- [x] 5.1 Extend interactive wizard `main.go:runAddWizard` to offer "Pool multiple subscriptions?" after auth step; loop prompts for count and per-account auth (env vs literal vs proxy), optional per-account base_url, routing strategy; write `profiles/<name>.toml` with `[[accounts]]` tables and comments about rotation order.
- [x] 5.2 Extend `main.go:handleAdd` flag parsing to support repeatable `--account` (e.g., `--account auth_token_env=CODEX_A --account api_key_env=OPGO_B`) parsed into `Account` entries; validate `key=value` syntax and fail with usage on malformed input; update `renderProfileToml` to emit `[[accounts]]` blocks.
- [x] 5.3 Update `main.go:handleEdit`/`handleRemove` and `saveProfile` to handle pooled files; `remove` deletes routing state file; `add`/`saveProfile` error when target file already exists.

## 6. Docs, examples, and verification

- [x] 6.1 Update `README.md` with pooled profile example for `codex` (3 env-var accounts, round-robin) and a cliproxy note explaining `CLIProxyAPI` native pooling vs `ccp` pool; update `AGENTS.md` if architecture section needs new file note.
- [x] 6.2 Run required pre-submit checks: `go build ./...`, `golangci-lint run ./...`, `go vet ./...`, `gofmt -w .` and verify `git diff --stat` clean for formatted files.
- [x] 6.3 Manual smoke test: `CCP_HOME=$(mktemp -d) CCP_STATE_HOME=$(mktemp -d) ./ccp add codex --type anthropic --account auth_token_env=CODEX_A --account auth_token_env=CODEX_B` then `CODEX_A=sk-a CODEX_B=sk-b ./ccp show codex` shows pool and masked sources, `CODEX_A=sk-a CODEX_B=sk-b ./ccp list` shows `×2`, three launches (`--help` stubbed via `claude` shim) rotate `account 1/2, 2/2, 1/2`; `doctor` reports unset env as fail when vars missing.

