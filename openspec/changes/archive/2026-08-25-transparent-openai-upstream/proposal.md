## Why

`ccp` currently half-hides `CLIProxyAPI`: `type=cliproxy` auto-starts the daemon and reuses `api-keys[0]`, but any generic OpenAI upstream (Opencode Go `https://opencode.ai/zen/go/v1/responses` + `/v1/chat/completions`, OpenRouter, custom gateways) still requires the user to hand-edit `~/.config/ccp/cliproxy/config.yaml:openai-compatibility` and run `ccp proxy restart`. Direct `type=anthropic` with those URLs fails because `claude` speaks Anthropic `POST /v1/messages` and the upstream speaks OpenAI Responses. Users expect `ccp add` alone to make `ccp <profile>` work for any model.

## What Changes

- Extend `ccp add` (wizard + `NAME --upstram-*` flags) to capture an OpenAI-compatible upstream in one step: upstream base URL, upstream API key (env-var or literal), upstream model name(s), and a local alias. No `ccp proxy *` or YAML editing required.
- Make `ccp` own `cliproxy/config.yaml:openai-compatibility` declaratively: on `ccp add` / `ccp launch` / `ccp remove`, merge the profile's upstream into the YAML (preserve all other keys, atomic `tmp+rename`, restart/hot-reload daemon if running).
- Keep `type=anthropic` for native Anthropic relays and `type=cliproxy` as "translated OpenAI via local proxy" — `ccp show`/`ccp doctor`/`ccp list` surface upstream health and `openai-compatibility` drift.
- Add a persistent per-profile upstream mapping so edits persist across launches without re-entering `ccp add`.

## Capabilities

### New Capabilities
- `openai-translation`: Transparent OpenAI→Anthropic translation via locally-managed CLIProxyAPI — declarative upstream registration, YAML merge, daemon lifecycle, and observability without exposing CLIProxyAPI to the user.

### Modified Capabilities

## Impact

- Affected packages: `internal/config` (new `Upstream*` fields on `Profile`/`Account`), `internal/proxy` (YAML merge + `openai-compatibility` lifecycle), `internal/cli` (wizard, `handleAdd`, `launch`, `show`, `doctor`, `remove`), `internal/routing` unchanged.
- Config: `~/.config/ccp/profiles/*.toml` gains optional upstream fields; `~/.config/ccp/cliproxy/config.yaml` gains managed `openai-compatibility` entries.
- State: `~/.local/state/ccp/` pid/log unchanged; `ccp proxy models` continues to list alias.
- No breaking change to existing `type=anthropic` or `type=cliproxy` OAuth profiles; old configs launch identically.
