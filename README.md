# ccp: Claude Code profile launcher

`ccp` launches Claude Code with a per-profile set of environment overrides
(endpoint, auth, model aliases), so you can run any model behind an
Anthropic-compatible API; or any OpenAI-style model through a locally managed
[CLIProxyAPI](https://github.com/router-for-me/CLIProxyAPI); by just typing:

```
ccp glm        # GLM via local CLIProxyAPI (proxy auto-started)
ccp kimi       # Kimi via local CLIProxyAPI
ccp official   # vanilla Claude Code with your Anthropic login
```

## How it works

For each launch ccp:

1. **Strips** every managed variable (`ANTHROPIC_*`, `CLAUDE_CODE_SUBAGENT_MODEL`,
   `API_TIMEOUT_MS`, Bedrock/Vertex toggles, …) from the inherited environment;
   a key exported for one provider can never leak into another session.
2. **Applies** exactly what the profile defines.
3. **Warns** if a Claude Code settings file pins any of those keys in its `env`
   block; settings-file env beats process env, which would silently defeat ccp.
4. **exec(2)s** claude in-place, so TTY handling, signals and exit codes are native.

Env vars used (see [Claude Code docs](https://code.claude.com/docs/en/env-vars)):

| Profile field        | Environment variable |
|----------------------|----------------------|
| `model`              | `ANTHROPIC_MODEL` |
| `opus_model` …       | `ANTHROPIC_DEFAULT_OPUS_MODEL` / `_SONNET_` / `_HAIKU_` / `_FABLE_` |
| `subagent_model`     | `CLAUDE_CODE_SUBAGENT_MODEL` |
| `auth = "bearer"`    | `ANTHROPIC_AUTH_TOKEN` |
| `auth = "x-api-key"` | `ANTHROPIC_API_KEY` |
| endpoint             | `ANTHROPIC_BASE_URL` |
| `api_timeout_ms`     | `API_TIMEOUT_MS` |
| `max_thinking_tokens`| `MAX_THINKING_TOKENS` |
| `max_output_tokens`  | `CLAUDE_CODE_MAX_OUTPUT_TOKENS` |
| `disable_prompt_caching` | `DISABLE_PROMPT_CACHING=1` |
| `custom_model_option`| `ANTHROPIC_CUSTOM_MODEL_OPTION*` (extra `/model` picker entry) |
| `extra_env`          | verbatim map, `${VAR}` expanded |

Alias fields default to `model`, so `/model` switching inside Claude Code stays
on your provider instead of requesting real Claude model IDs from a relay that
doesn't serve them. A profile without `model` inherits the `model` value from
`~/.claude/settings.json`.

## Install

One-liner:

```sh
curl -fsSL https://raw.githubusercontent.com/parnexcodes/claude-code-profile/master/install.sh | bash
```

The script installs a prebuilt binary from GitHub Releases when one exists for
your platform (verifying it against the published sha256 checksums) and falls
back to compiling from source if not, which needs Go >= 1.25.

Or from a clone:

```sh
git clone https://github.com/parnexcodes/claude-code-profile && cd claude-code-profile
make install        # → ~/.local/bin/ccp
```

Override the install location or ref with `CCP_BINDIR=...` / `REF=...`.

## First run

The first `ccp` invocation creates `~/.config/ccp/` containing:

```
config.toml            # global settings + [proxy] section
profiles/glm.toml      # seeded example profiles
profiles/kimi.toml
profiles/official.toml
cliproxy/config.yaml   # scaffolded on first proxy start
```

State (pid file, logs, downloaded binaries) lives under `~/.local/state/ccp/`.

### Using CLIProxyAPI (OpenAI-type models)

1. Install the binary once:

   ```sh
   ccp proxy install        # downloads latest release from GitHub
   ```

2. Start it (scaffolds a starter config with a generated client api-key):

   ```sh
   ccp proxy start
   ```

   Then follow [help.router-for.me](https://help.router-for.me/) to log in your
   accounts / configure upstream providers in its config. Check what models it
   exposes with `ccp proxy models`.

3. Launch a model:

   ```sh
   ccp glm     # auto-starts the proxy when it's down; daemon keeps running
               # between sessions; `ccp proxy stop` shuts it down
   ```

Profiles of `type = "cliproxy"` point `ANTHROPIC_BASE_URL` at
`http://127.0.0.1:<port>` and reuse the proxy's own `api-keys[0]` as bearer
token unless the profile sets `auth_token_env` / `api_key_env`. For generic
OpenAI endpoints that don't speak Anthropic, `ccp` transparently translates
via `openai-compatibility` — `upstream_*` fields in `profiles/*.toml` are the
source of truth and `cliproxy/config.yaml:openai-compatibility` is derived.

### Transparent OpenAI upstreams (Opencode Go, OpenRouter, custom)

Any `https://.../v1/responses` or `.../v1/chat/completions` endpoint (the
Opencode Go table's `@ai-sdk/openai` rows, e.g. `muse-spark-1.2-contributor`
at `https://opencode.ai/zen/go/v1/responses`) works via a translated
`cliproxy` profile. `ccp` owns the proxy config — no `ccp proxy` or YAML
editing needed:

```sh
export OPENCODE_GO_API_KEY=sk-...
ccp add muse --type cliproxy --model muse-spark-1.2-contributor \
  --upstream-base-url https://opencode.ai/zen/go/v1 \
  --upstream-api-key-env OPENCODE_GO_API_KEY

ccp show muse   # shows upstream base URL, masked key, alias, and proxy sync drift
ccp muse        # claude → Anthropic /v1/messages → 127.0.0.1:8317 → OpenAI Responses → Opencode Go
```

Or via the wizard: `ccp add` → `cliproxy` → `Translate generic OpenAI upstream? y`
→ prompts for upstream base URL, upstream API key, upstream model and local
alias. `ccp` normalizes a pasted full endpoint like
`https://opencode.ai/zen/go/v1/responses` → `.../v1` and warns.
`ccp show`/`ccp doctor` surface whether `cliproxy/config.yaml:openai-compatibility`
is in sync; `ccp remove` cleans it up; `ccp <profile>` auto-installs/starts the
proxy if needed. `ccp list` marks translated profiles as `translated`.

For pooled translated profiles (multiple interchangeable upstreams under one
`ccp` command): `ccp add relay --type cliproxy --account upstream_base_url=https://a.example/v1,upstream_api_key_env=KEY_A --account upstream_api_key_env=KEY_B` or follow the wizard's per-account upstream prompts. Per-account `upstream_base_url` overrides the profile default for that account.

### Direct Anthropic-compatible relays
```toml
# ~/.config/ccp/profiles/deepseek.toml
description = "DeepSeek direct"
type        = "anthropic"
base_url    = "https://api.deepseek.com/anthropic"
api_key_env = "DEEPSEEK_KEY"          # resolved at launch, nothing stored on disk
model       = "deepseek-chat"
```

### Pooling multiple subscriptions under one profile

Combine N interchangeable subscriptions for the same provider into one
`ccp` command that round-robins per launch. How you configure it depends on
the auth type.

#### OAuth accounts (Codex, ChatGPT, etc.) via CLIProxyAPI — let the proxy pool

Codex/ChatGPT logins are OAuth and have no direct `anthropic` endpoint — they
are accessed only through CLIProxyAPI. Add each login to CLIProxyAPI and use a
single `cliproxy` profile — no `[[accounts]]` needed:

```toml
# ~/.config/ccp/profiles/codex.toml
description = "Codex via CLIProxyAPI (OAuth)"
type = "cliproxy"
model = "gpt-5-codex"
# Add each Codex OAuth login via CLIProxyAPI (see https://help.router-for.me/
# and ~/.config/ccp/cliproxy/config.yaml -> auth-dir).
# CLIProxyAPI pools them per-request (weighted-round-robin) internally.
```

`ccp codex` then uses the proxy; the proxy handles rotation and token refresh.

#### Direct Anthropic-compatible relays with static keys/tokens — `ccp` round-robins

For relays that expose `https://.../anthropic` with static `ANTHROPIC_API_KEY` or
`ANTHROPIC_AUTH_TOKEN` values, define an `anthropic` pool:

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
# Optional per-account overrides:
# base_url = "https://api-b.example.com/anthropic"
# name = "secondary"
# auth_token_env = "RELAY_TOKEN_D"  # bearer variant also allowed
```

`ccp relay` now cycles `A → B → C → A …` — one account per session, counter
persisted at `~/.local/state/ccp/routing/relay.json` so successive shells keep
rotating. Mixing `auth_token_env` / `api_key_env` / literals in one pool is
allowed. `ccp show relay` lists the pool (masked), `ccp list` shows `×3`, and
the launch banner prints `account 2/3 ($RELAY_KEY_B)`.

Scripted creation:

```sh
ccp add relay --type anthropic \
  --account api_key_env=RELAY_KEY_A \
  --account api_key_env=RELAY_KEY_B \
  --account api_key_env=RELAY_KEY_C
```

> `ccp` pooling and CLIProxyAPI's own `auth-dir` pooling are complementary:
> use the proxy's `weighted-round-robin` for OAuth upstream accounts, and use
> `ccp`'s `[[accounts]]` pool for the bearer `api-keys` layer or any direct
> `anthropic` relay where no usage-weighted signal exists.

`${VAR}` references are expanded anywhere in profile values, so even endpoints
can come from your shell environment.

## Commands

```
ccp [-q] [PROFILE] [args…]   launch claude; trailing args pass straight through
ccp list                     list profiles ("*" = default, "×N" = pooled)
ccp show PROFILE             exact env a launch would apply (secrets masked, pools enumerated)
ccp add [NAME] [--type …] [--model …] [--api-key-env …] [--account KEY=VAL[,KEY=VAL...]] [--set K=V] …
                             # no args → interactive wizard (arrow keys / numbers, prompts for pool)
ccp edit [NAME]              $EDITOR on profiles/NAME.toml or config.toml
                             # no args → picker
ccp remove [NAME]            delete a profile (picker when no args, also clears routing state)
ccp proxy status|start|stop|restart|install|init|logs|models
ccp doctor                   validate binaries, secrets, conflicts, connectivity (pool-aware)
```

`ccp add` without arguments walks through name, type, model, auth and
writes `profiles/<name>.toml` for you; `ccp remove` / `ccp edit` show a
picker when no name is given. Flags still work for scripting.

Everything after the profile name goes to claude: `ccp kimi --resume -c`.

## Notes & caveats

- The proxy is a **daemon**: `ccp` starts it when needed but leaves it running
  between sessions (use `ccp proxy stop` to shut it down).
- If `~/.claude/settings.json` (or project `.claude/settings*.json`) has an
  `env` block touching managed vars, ccp prints warnings and `ccp doctor`
  fails; remove them there or profiles can't take effect.
- `ANTHROPIC_BASE_URL` pointing away from api.anthropic.com disables some
  first-party features (Remote Control, MCP tool search) per Anthropic docs;
  set `ENABLE_TOOL_SEARCH=true` in `extra_env` if your proxy forwards
  `tool_reference` blocks.
- Secrets are referenced, never stored: prefer `auth_token_env`/`api_key_env`.

## Development

Project layout:

```
cmd/ccp/main.go        # thin entrypoint
internal/config        # config loading, paths, validation
internal/profile       # env assembly, auth, managed vars
internal/routing       # round-robin state
internal/proxy         # daemon lifecycle, models
internal/settings      # Claude settings interop
internal/cli           # launch, show/list, doctor, completion
internal/tui           # prompts, selection
internal/util          # helpers
```

Build and test:

```sh
make build        # go build -o ccp ./cmd/ccp
make vet          # go vet ./...
make fmt          # gofmt -w cmd internal .
make test         # go test ./... -count=1
make test-race    # go test ./... -count=1 -race
go test ./... -count=1 -race  # CI runs this on Linux
```

All tests are hermetic (`t.TempDir()` + `CCP_HOME`/`CCP_STATE_HOME`/`HOME`); no proxy or `claude` binary required.
