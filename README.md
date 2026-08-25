# ccp — Claude Code profile launcher

Launch [Claude Code](https://code.claude.com) with per-profile environment overrides — endpoint, auth, and model — so any Anthropic-compatible API or OpenAI-style model (via [CLIProxyAPI](https://github.com/router-for-me/CLIProxyAPI)) is one command away:

```sh
ccp myprofile   # your profile (proxy auto-started if needed)
ccp work --resume  # pass flags straight through to claude
ccp list        # see all profiles; bare `ccp` lists or launches default
```

## How it works

Each launch:

1. **Strips** all managed vars (`ANTHROPIC_*`, `CLAUDE_CODE_SUBAGENT_MODEL`, `API_TIMEOUT_MS`, Bedrock/Vertex toggles, …) — no cross-profile leakage.
2. **Applies** exactly what the profile defines.
3. **Warns** if `~/.claude/settings.json` (or `.claude/settings*.json`) pins any managed key in its `env` block — settings-file env overrides process env and silently defeats profiles.
4. **`exec(2)`s** `claude` in place — TTY, signals, and exit codes are native.

| Profile field | Env var |
|---|---|
| `model` | `ANTHROPIC_MODEL` |
| `opus_model` / `sonnet_model` / `haiku_model` / `fable_model` | `ANTHROPIC_DEFAULT_OPUS_MODEL` / `_SONNET_` / `_HAIKU_` / `_FABLE_` |
| `subagent_model` | `CLAUDE_CODE_SUBAGENT_MODEL` |
| `auth = "bearer"` | `ANTHROPIC_AUTH_TOKEN` |
| `auth = "x-api-key"` | `ANTHROPIC_API_KEY` |
| endpoint | `ANTHROPIC_BASE_URL` |
| `api_timeout_ms` | `API_TIMEOUT_MS` |
| `max_thinking_tokens` | `MAX_THINKING_TOKENS` |
| `max_output_tokens` | `CLAUDE_CODE_MAX_OUTPUT_TOKENS` |
| `disable_prompt_caching` | `DISABLE_PROMPT_CACHING=1` |
| `custom_model_option` | `ANTHROPIC_CUSTOM_MODEL_OPTION*` |
| `extra_env` | verbatim map (`${VAR}` / `$VAR` expanded) |

Alias fields default to `model`, so `/model` inside Claude Code stays on your provider instead of requesting upstream Claude IDs. A profile without `model` inherits `model` from `~/.claude/settings.json`.

## Install

```sh
# one-liner — prebuilt binary (sha256 verified) or compile from source
curl -fsSL https://raw.githubusercontent.com/parnexcodes/claude-code-profile/master/install.sh | bash

# from source (requires Go >= 1.25)
git clone https://github.com/parnexcodes/claude-code-profile && cd claude-code-profile
make install  # → ~/.local/bin/ccp
```

`CCP_BINDIR=...` overrides the install dir, `REF=...` selects a branch/tag.

## First run

First invocation creates an empty config:

```
~/.config/ccp/
  config.toml              # global + [proxy] settings (no default_profile)
  profiles/                # empty — create with `ccp add`
  cliproxy/config.yaml     # scaffolded on first `ccp proxy start`
~/.local/state/ccp/        # pid, logs, binaries, routing counters
```

Config dir: `$CCP_HOME` > `$XDG_CONFIG_HOME/ccp` > `~/.config/ccp`
State dir: `$CCP_STATE_HOME` > `$XDG_STATE_HOME/ccp` > `~/.local/state/ccp`

Create your first profile:

```sh
ccp add myprofile --type anthropic   # interactive wizard when no flags
ccp list                             # verify
```


## Profiles

Two types — `type = "anthropic"` (direct relay) and `type = "cliproxy"` (via local proxy):

```toml
# Direct Anthropic-compatible relay
# ~/.config/ccp/profiles/deepseek.toml
description = "DeepSeek direct"
type        = "anthropic"
base_url    = "https://api.deepseek.com/anthropic"
api_key_env = "DEEPSEEK_KEY"   # resolved at launch, never stored
model       = "deepseek-chat"
```

`${VAR}` is expanded anywhere in profile values.

### CLIProxyAPI (OpenAI-type models)

```sh
ccp proxy install   # download latest CLIProxyAPI release
ccp proxy start     # scaffolds config with generated api-key, then daemonizes
ccp proxy models    # list models the proxy exposes
ccp add myprofile --type cliproxy --model gpt-5  # create a proxy profile
ccp myprofile       # auto-starts proxy if down; daemon persists between sessions
ccp proxy stop      # shut down when done
```

`type = "cliproxy"` profiles point `ANTHROPIC_BASE_URL` at `http://127.0.0.1:<port>` and reuse `cliproxy/config.yaml:api-keys[0]` as the bearer token unless `auth_token_env` / `api_key_env` is set. See [help.router-for.me](https://help.router-for.me/) for proxy account setup.

### Translated OpenAI upstreams

Any `https://.../v1/responses` or `.../v1/chat/completions` endpoint works via translation. `ccp` owns `cliproxy/config.yaml:openai-compatibility` — `upstream_*` fields in `profiles/*.toml` are the source of truth.

```sh
export OPENCODE_GO_API_KEY=sk-...

ccp add muse --type cliproxy --model muse-spark-1.2-contributor \
  --upstream-base-url https://opencode.ai/zen/go/v1 \
  --upstream-api-key-env OPENCODE_GO_API_KEY

ccp show muse  # base URL, masked key, alias, sync status
ccp muse       # claude → /v1/messages → 127.0.0.1:8317 → OpenAI Responses → upstream
```

Wizard alternative: `ccp add` → `cliproxy` → `Translate generic OpenAI upstream? y` → prompts for base URL, key, model, alias. Pasted endpoints like `.../v1/responses` are normalized to `.../v1` with a warning. `ccp list` marks translated profiles, `ccp show`/`ccp doctor` surface sync drift, `ccp remove` cleans the derived YAML entry, and `ccp <profile>` auto-repairs drift.

Pooled translated profiles (multiple upstreams under one command):

```sh
ccp add relay --type cliproxy \
  --account upstream_base_url=https://a.example/v1,upstream_api_key_env=KEY_A \
  --account upstream_api_key_env=KEY_B
```

Per-account `upstream_base_url` overrides the profile default.

### Pooling

**OAuth accounts (Codex, ChatGPT, etc.) — let the proxy pool.** These have no direct Anthropic endpoint. Add each login in CLIProxyAPI and use a single `cliproxy` profile:

```toml
# ~/.config/ccp/profiles/codex.toml
description = "Codex via CLIProxyAPI (OAuth)"
type = "cliproxy"
model = "gpt-5-codex"
# Add OAuth logins via CLIProxyAPI (config.yaml → auth-dir).
# Proxy pools them internally (weighted-round-robin).
```

**Static keys/tokens — `ccp` round-robins via `[[accounts]]`:**

```toml
# ~/.config/ccp/profiles/relay.toml
description = "Relay ×3 (round-robin)"
type = "anthropic"
base_url = "https://api.example.com/anthropic"
model = "my-model"

[[accounts]]
api_key_env = "RELAY_KEY_A"

[[accounts]]
api_key_env = "RELAY_KEY_B"

[[accounts]]
api_key_env = "RELAY_KEY_C"
# per-account overrides: base_url, name, auth_token_env, ...
```

`ccp relay` cycles `A → B → C → A …` — counter persisted at `~/.local/state/ccp/routing/relay.json`. Mixing `api_key_env` / `auth_token_env` / literals is allowed. `ccp show` enumerates the pool (masked), `ccp list` shows `×3`, launch banner prints `account 2/3 ($RELAY_KEY_B)`.

```sh
ccp add relay --type anthropic \
  --account api_key_env=RELAY_KEY_A \
  --account api_key_env=RELAY_KEY_B \
  --account api_key_env=RELAY_KEY_C
```

> The two pooling layers are complementary: proxy `weighted-round-robin` for OAuth upstreams, `ccp` `[[accounts]]` for bearer keys or direct relays.

## Commands

```
ccp [-q] [PROFILE] [args…]   launch claude (trailing args pass through: ccp myprofile --resume -c)
ccp list                     list profiles (* = default, ×N = pooled, translated = OpenAI compat)
ccp show PROFILE             env a launch would apply (secrets masked, pools enumerated)
ccp add [NAME] [flags]       create profile — no args → interactive wizard (arrow keys)
ccp edit [NAME]              $EDITOR on profiles/NAME.toml (no args → picker)
ccp remove [NAME]            delete profile + routing state (no args → picker)
ccp proxy status|start|stop|restart|install|init|logs|models
ccp doctor                   validate binaries, secrets, conflicts, connectivity (pool-aware)
```

`ccp add` flags: `--type`, `--model`, `--api-key-env`, `--account KEY=VAL[,KEY=VAL...]`, `--set K=V`, `--upstream-*` (see `ccp add --help`).

## Notes

- The proxy is a daemon — `ccp` auto-starts it but leaves it running; `ccp proxy stop` to shut down.
- `ANTHROPIC_BASE_URL` away from `api.anthropic.com` disables some first-party features (Remote Control, MCP tool search); set `ENABLE_TOOL_SEARCH=true` in `extra_env` if your proxy forwards `tool_reference` blocks.
- Secrets are referenced, never stored — prefer `*_env` fields.
- Managed vars are fully stripped then re-applied; stray `ANTHROPIC_*` in your shell cannot leak across profiles — but `env` blocks in Claude settings files override process env.

## Development

```
cmd/ccp/main.go        thin entrypoint → internal/cli
internal/config        config loading, paths, validation
internal/profile       env assembly, auth, managed vars
internal/routing       round-robin state (flock + atomic rename)
internal/proxy         daemon lifecycle, openai-compatibility sync
internal/settings      Claude settings interop
internal/tui           prompts / selection
internal/util          helpers
```

```sh
make build      # go build -o ccp ./cmd/ccp
make vet        # go vet ./...
make fmt        # gofmt -w cmd internal .
make test       # go test ./... -count=1
make test-race  # go test ./... -count=1 -race  (CI on Linux)
```

Tests are hermetic (`t.TempDir()` + `CCP_HOME`/`CCP_STATE_HOME`/`HOME`) — no proxy or `claude` binary required.
