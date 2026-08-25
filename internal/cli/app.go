package cli

import (
	"ccp/internal/config"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
)

var Version = "0.4.0"

const usage = `ccp: Claude Code profile launcher

Launch Claude Code against any Anthropic-compatible endpoint or through a
locally-managed CLIProxyAPI, one env-var profile per model/backend.

usage:
  ccp [-q] [PROFILE] [args…]     launch claude with PROFILE (default if omitted);
                                 everything after PROFILE is passed to claude
  ccp <command> [args…]

commands:
  list                    list profiles ("*" marks the default)
  show PROFILE            print the exact environment a launch would apply
  add [NAME] [--opts]     create a profile (interactive wizard when no args)
  edit [NAME]             open in $EDITOR (picker when no args)
  remove [NAME]           delete a profile (picker when no args)
  default [PROFILE]       set or show default profile (bare ccp launches it; picker when no args)
  proxy status|start|stop|restart   manage local CLIProxyAPI
  proxy install           download the latest CLIProxyAPI release binary
  proxy init              scaffold a starter CLIProxyAPI config.yaml
  proxy logs [-n N] [-f]  show (or follow) proxy logs
  proxy models            list model IDs the proxy exposes
  doctor                  validate the whole setup
  completion zsh|bash     print shell completion script
  version                 print version

profile options (profiles/<name>.toml):
  type                    "cliproxy" (via local CLIProxyAPI) | "anthropic" (direct)
  model                   ANTHROPIC_MODEL; falls back to ~/.claude/settings.json
                          append [1m] for 1M context models, e.g. "stealth/ox-alpha[1m]"
  opus_model / sonnet_model / haiku_model / fable_model / subagent_model
                          alias overrides; default to ` + "`model`" + `
  base_url                endpoint override (cliproxy default: http://127.0.0.1:8317)
  auth                    "bearer" → ANTHROPIC_AUTH_TOKEN | "x-api-key" → ANTHROPIC_API_KEY | "none"
  auth_token_env          name of env var holding a bearer token (recommended)
  api_key_env             name of env var holding an API key (recommended)
  api_timeout_ms          API_TIMEOUT_MS
  max_thinking_tokens     MAX_THINKING_TOKENS
  max_output_tokens       CLAUDE_CODE_MAX_OUTPUT_TOKENS
  max_context_tokens      CLAUDE_CODE_MAX_CONTEXT_TOKENS (e.g. 1000000 for 1M)
  disable_prompt_caching  DISABLE_PROMPT_CACHING=1
  disable_unknown_model_window_enforcement  CLAUDE_CODE_DISABLE_UNKNOWN_MODEL_WINDOW_ENFORCEMENT=1
  extra_env               raw map of additional KEY=VALUE (${VAR} expanded)

examples:
  ccp glm                       # GLM via CLIProxyAPI (auto-starts it)
  ccp kimi --resume             # pass flags straight through to claude
  ccp official                  # vanilla Claude Code with your login
  ZAI_API_KEY=sk-… ccp glm      # secrets resolved from the environment at launch

files:
  config:   %[1]s/config.toml
  profiles: %[1]s/profiles/*.toml
  state:    %s
`

func Run() {
	args := os.Args[1:]

	quiet := false
	for len(args) > 0 {
		switch args[0] {
		case "-q", "--quiet":
			quiet = true
			args = args[1:]
		default:
			goto parsed
		}
	}
parsed:

	if len(args) == 0 {
		cfg := mustLoadConfig()
		if def := cfg.DefaultProfileName(); def != "" {
			launch(def, nil, quiet)
			return
		}
		printUsage()
		listProfiles()
		os.Exit(1)
	}

	cmd := args[0]
	rest := args[1:]
	switch cmd {
	case "-h", "--help", "help":
		printUsage()
	case "-v", "--version", "version":
		fmt.Printf("ccp %s\n", Version)

	case "list", "ls":
		listProfiles()
	case "show":
		if len(rest) != 1 {
			die("usage: ccp show PROFILE")
		}
		showProfile(rest[0])

	case "add":
		if len(rest) == 0 {
			runAddWizard()
		} else {
			handleAdd(rest)
		}
	case "edit":
		if len(rest) == 0 {
			runEditPicker()
		} else {
			handleEdit(rest)
		}
	case "remove", "rm":
		if len(rest) == 0 {
			runRemovePicker()
		} else if len(rest) != 1 {
			die("usage: ccp remove NAME")
		} else {
			handleRemove(rest[0])
		}

	case "default", "set-default", "default-profile":
		handleDefault(rest)

	case "proxy":
		handleProxy(rest)
	case "shim":
		handleShim(rest)
	case "internal-shim":
		handleInternalShim(rest)
	case "doctor":
		runDoctor()
	case "completion":
		if len(rest) != 1 || (rest[0] != "zsh" && rest[0] != "bash") {
			die("usage: ccp completion zsh|bash")
		}
		printCompletion(rest[0])
	case "__profiles": // hidden: used by completion scripts
		cfg := mustLoadConfig()
		for _, n := range cfg.ProfileNames() {
			fmt.Println(n)
		}

	default:
		if strings.HasPrefix(cmd, "-") {
			printUsage()
			die("unknown flag %q", cmd)
		}
		launch(cmd, rest, quiet)
	}
}

func printUsage() {
	fmt.Fprintf(os.Stderr, usage, ccpConfigDir(), ccpStateDir())
}

// ---------------------------------------------------------------------------
// add / edit / remove
// ---------------------------------------------------------------------------

type addOpts struct {
	typ, model, desc, baseURL     string
	authTokenEnv, apiKeyEnv       string
	haiku, sonnet, opus, subagent string
	timeoutMS                     int
	maxContextTokens              int
	disableUnknownWindow          bool
	extra                         []string // KEY=VAL
	accounts                      []string // repeatable --account key=val[,key=val...]
	upstreamBaseURL               string
	upstreamAPIKeyEnv             string
	upstreamAPIKey                string
	upstreamName                  string
	upstreamModel                 string
	upstreamModelAlias            string
	upstreamProtocol              string
}

func parseAccountSpec(spec string) config.Account {
	var a config.Account
	// Support "key=val" or "k1=v1,k2=v2" (comma-separated)
	parts := strings.Split(spec, ",")
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		k, v, ok := strings.Cut(part, "=")
		if !ok {
			die("--account expects KEY=VALUE (got %q); use auth_token_env=VAR or api_key_env=VAR[,base_url=URL][,name=NAME][,auth=none][,auth_token=TOKEN][,api_key=KEY]", spec)
		}
		k = strings.TrimSpace(strings.ToLower(k))
		v = strings.TrimSpace(v)
		switch k {
		case "name":
			a.Name = v
		case "description":
			a.Description = v
		case "base_url", "base-url":
			a.BaseURL = v
		case "auth":
			a.Auth = v
		case "auth_token_env", "auth-token-env":
			a.AuthTokenEnv = v
		case "api_key_env", "api-key-env":
			a.APIKeyEnv = v
		case "auth_token", "auth-token":
			a.AuthToken = v
		case "api_key", "api-key":
			a.APIKey = v
		case "upstream_base_url", "upstream-base-url":
			a.UpstreamBaseURL = v
		case "upstream_api_key_env", "upstream-api-key-env":
			a.UpstreamAPIKeyEnv = v
		case "upstream_api_key", "upstream-api-key":
			a.UpstreamAPIKey = v
		case "upstream_name", "upstream-name":
			a.UpstreamName = v
		case "upstream_model", "upstream-model":
			a.UpstreamModel = v
		case "upstream_model_alias", "upstream-model-alias", "upstream_alias", "upstream-alias":
			a.UpstreamModelAlias = v
		case "upstream_protocol", "upstream-protocol":
			a.UpstreamProtocol = v
		default:
			die("unknown account key %q in --account %q (allowed: name, base_url, auth, auth_token_env, api_key_env, auth_token, api_key, upstream_base_url, upstream_api_key_env, upstream_api_key, upstream_name, upstream_model, upstream_model_alias, upstream_protocol)", k, spec)
		}
	}
	return a
}

func handleAdd(args []string) {
	if len(args) == 0 || strings.HasPrefix(args[0], "-") {
		die("usage: ccp add NAME [--type cliproxy|anthropic] [--model ID] [--desc TEXT]\n" +
			"              [--base-url URL] [--auth-token-env VAR] [--api-key-env VAR]\n" +
			"              [--account KEY=VAL[,KEY=VAL...]] (repeatable, e.g. --account auth_token_env=CODEX_A)\n" +
			"              [--haiku-model ID] [--sonnet-model ID] [--opus-model ID]\n" +
			"              [--subagent-model ID] [--timeout-ms N] [--max-context-tokens N|--1m]\n" +
			"              [--disable-unknown-model-window-enforcement] [--set KEY=VAL]...\n" +
			"              [--upstream-base-url URL] [--upstream-api-key-env VAR] [--upstream-api-key KEY]\n" +
			"              [--upstream-name NAME] [--upstream-model MODEL] [--upstream-model-alias ALIAS]")
	}
	name := args[0]
	if !safeName(name) || name == "." || name == ".." {
		die("invalid profile name %q (use letters, digits, - _ .)", name)
	}
	var o addOpts
	for i := 1; i < len(args); i++ {
		take := func() string {
			i++
			if i >= len(args) {
				die("missing value after %s", args[i-1])
			}
			return args[i]
		}
		switch args[i] {
		case "--type":
			o.typ = take()
		case "--model":
			o.model = take()
		case "--desc", "--description":
			o.desc = take()
		case "--base-url":
			o.baseURL = take()
		case "--auth-token-env":
			o.authTokenEnv = take()
		case "--api-key-env":
			o.apiKeyEnv = take()
		case "--haiku-model":
			o.haiku = take()
		case "--sonnet-model":
			o.sonnet = take()
		case "--opus-model":
			o.opus = take()
		case "--subagent-model":
			o.subagent = take()
		case "--timeout-ms":
			v := take()
			if n, err := fmt.Sscanf(v, "%d", &o.timeoutMS); err != nil || n != 1 {
				die("--timeout-ms expects a number, got %q", v)
			}
		case "--max-context-tokens":
			v := take()
			if n, err := fmt.Sscanf(v, "%d", &o.maxContextTokens); err != nil || n != 1 {
				die("--max-context-tokens expects a number, got %q", v)
			}
		case "--1m":
			o.maxContextTokens = 1000000
		case "--disable-unknown-model-window-enforcement":
			o.disableUnknownWindow = true
		case "--set":
			o.extra = append(o.extra, take())
		case "--account":
			o.accounts = append(o.accounts, take())
		case "--upstream-base-url":
			o.upstreamBaseURL = take()
		case "--upstream-api-key-env":
			o.upstreamAPIKeyEnv = take()
		case "--upstream-api-key":
			o.upstreamAPIKey = take()
		case "--upstream-name":
			o.upstreamName = take()
		case "--upstream-model":
			o.upstreamModel = take()
		case "--upstream-model-alias", "--upstream-alias":
			o.upstreamModelAlias = take()
		case "--upstream-protocol":
			o.upstreamProtocol = take()
		default:
			die("unknown option %q", args[i])
		}
	}

	// Build account pool from --account flags.
	var accounts []config.Account
	for _, spec := range o.accounts {
		accounts = append(accounts, parseAccountSpec(spec))
	}
	if len(accounts) > 0 && (o.authTokenEnv != "" || o.apiKeyEnv != "") {
		warnf("both --auth-token-env/--api-key-env and --account given; pool wins and top-level auth is ignored")
	}

	var b strings.Builder
	if o.desc != "" {
		fmt.Fprintf(&b, "description = %q\n", o.desc)
	}
	if o.typ != "" {
		fmt.Fprintf(&b, "type = %q\n", o.typ)
	} else {
		b.WriteString("# type = \"cliproxy\"   # or \"anthropic\" for direct endpoints\n")
	}
	if o.model != "" {
		fmt.Fprintf(&b, "model = %q\n", o.model)
	} else {
		b.WriteString("# model = \"...\"        # omit to inherit ~/.claude/settings.json\n")
	}
	put := func(key, val string) {
		if val != "" {
			fmt.Fprintf(&b, "%s = %q\n", key, val)
		}
	}
	put("base_url", o.baseURL)
	put("upstream_base_url", o.upstreamBaseURL)
	put("upstream_api_key_env", o.upstreamAPIKeyEnv)
	put("upstream_api_key", o.upstreamAPIKey)
	put("upstream_name", o.upstreamName)
	put("upstream_model", o.upstreamModel)
	put("upstream_model_alias", o.upstreamModelAlias)
	put("upstream_protocol", o.upstreamProtocol)
	put("auth_token_env", o.authTokenEnv)
	put("api_key_env", o.apiKeyEnv)
	put("haiku_model", o.haiku)
	put("sonnet_model", o.sonnet)
	put("opus_model", o.opus)
	put("subagent_model", o.subagent)
	if o.timeoutMS > 0 {
		fmt.Fprintf(&b, "api_timeout_ms = %d\n", o.timeoutMS)
	}
	if o.maxContextTokens > 0 {
		fmt.Fprintf(&b, "max_context_tokens = %d\n", o.maxContextTokens)
	}
	if o.disableUnknownWindow {
		b.WriteString("disable_unknown_model_window_enforcement = true\n")
	}
	if len(accounts) > 0 {
		b.WriteString("\n# Order is rotation order for round-robin. ccp codex alternates across these accounts.\n")
		for _, a := range accounts {
			b.WriteString("\n[[accounts]]\n")
			if a.Name != "" {
				fmt.Fprintf(&b, "name = %q\n", a.Name)
			}
			if a.Description != "" {
				fmt.Fprintf(&b, "description = %q\n", a.Description)
			}
			if a.BaseURL != "" {
				fmt.Fprintf(&b, "base_url = %q\n", a.BaseURL)
			}
			if a.UpstreamBaseURL != "" {
				fmt.Fprintf(&b, "upstream_base_url = %q\n", a.UpstreamBaseURL)
			}
			if a.UpstreamAPIKeyEnv != "" {
				fmt.Fprintf(&b, "upstream_api_key_env = %q\n", a.UpstreamAPIKeyEnv)
			}
			if a.UpstreamAPIKey != "" {
				fmt.Fprintf(&b, "upstream_api_key = %q\n", a.UpstreamAPIKey)
			}
			if a.UpstreamName != "" {
				fmt.Fprintf(&b, "upstream_name = %q\n", a.UpstreamName)
			}
			if a.UpstreamModel != "" {
				fmt.Fprintf(&b, "upstream_model = %q\n", a.UpstreamModel)
			}
			if a.UpstreamModelAlias != "" {
				fmt.Fprintf(&b, "upstream_model_alias = %q\n", a.UpstreamModelAlias)
			}
			if a.Auth != "" {
				fmt.Fprintf(&b, "auth = %q\n", a.Auth)
			}
			if a.AuthTokenEnv != "" {
				fmt.Fprintf(&b, "auth_token_env = %q\n", a.AuthTokenEnv)
			}
			if a.APIKeyEnv != "" {
				fmt.Fprintf(&b, "api_key_env = %q\n", a.APIKeyEnv)
			}
			if a.AuthToken != "" {
				fmt.Fprintf(&b, "auth_token = %q\n", a.AuthToken)
			}
			if a.APIKey != "" {
				fmt.Fprintf(&b, "api_key = %q\n", a.APIKey)
			}
		}
	}
	if len(o.extra) > 0 {
		b.WriteString("\n[extra_env]\n")
		for _, kv := range o.extra {
			k, v, ok := strings.Cut(kv, "=")
			if !ok {
				die("--set expects KEY=VALUE, got %q", kv)
			}
			fmt.Fprintf(&b, "%s = %q\n", k, v)
		}
	}

	// Validate upstream fields and sync to proxy before writing profile.
	hasUpstream := o.upstreamBaseURL != "" || o.upstreamAPIKeyEnv != "" || o.upstreamAPIKey != "" || o.upstreamName != "" || o.upstreamModel != "" || o.upstreamModelAlias != "" || o.upstreamProtocol != ""
	hasAccountUpstream := false
	for _, a := range accounts {
		if a.HasUpstream() {
			hasAccountUpstream = true
			break
		}
	}
	if hasUpstream || hasAccountUpstream {
		// Implicitly require cliproxy when upstream is used.
		effectiveType := o.typ
		if effectiveType == "" {
			effectiveType = "cliproxy"
		}
		if effectiveType != "cliproxy" {
			die("upstream_* fields require --type cliproxy (got %q)", effectiveType)
		}
		// Build a temp profile for validation and sync.
		tmpProfile := &config.Profile{
			Type:               effectiveType,
			BaseURL:            o.baseURL,
			Model:              o.model,
			UpstreamBaseURL:    o.upstreamBaseURL,
			UpstreamAPIKeyEnv:  o.upstreamAPIKeyEnv,
			UpstreamAPIKey:     o.upstreamAPIKey,
			UpstreamName:       o.upstreamName,
			UpstreamModel:      o.upstreamModel,
			UpstreamModelAlias: o.upstreamModelAlias,
			UpstreamProtocol:   o.upstreamProtocol,
			Accounts:           accounts,
		}
		if o.typ == "" {
			tmpProfile.Type = "cliproxy"
		} else {
			tmpProfile.Type = o.typ
		}
		tmpProfile.Normalize()
		if err := tmpProfile.ValidatePool(); err != nil {
			die("%v", err)
		}
		// Normalize upstream base URL (handle full endpoint like /v1/responses).
		if tmpProfile.UpstreamBaseURL != "" {
			norm := cliNormalizeUpstreamBaseURL(tmpProfile.UpstreamBaseURL)
			tmpProfile.UpstreamBaseURL = norm
			if norm != o.upstreamBaseURL {
				s := b.String()
				origLine := fmt.Sprintf("upstream_base_url = %q\n", o.upstreamBaseURL)
				newLine := fmt.Sprintf("upstream_base_url = %q\n", norm)
				s = strings.Replace(s, origLine, newLine, 1)
				b.Reset()
				b.WriteString(s)
			}
		}
		for i := range tmpProfile.Accounts {
			if tmpProfile.Accounts[i].UpstreamBaseURL != "" {
				tmpProfile.Accounts[i].UpstreamBaseURL = cliNormalizeUpstreamBaseURL(tmpProfile.Accounts[i].UpstreamBaseURL)
			}
		}
		cfg := mustLoadConfig()
		// Sync to proxy (chat) and/or shim (responses) depending on protocol
		if tmpProfile.IsUpstreamResponses() {
			// If mixed pool, also sync chat part to proxy for chat accounts
			hasChat := false
			if tmpProfile.UpstreamProtocolNormalized() == "chat" {
				hasChat = true
			}
			for _, a := range tmpProfile.Accounts {
				if a.UpstreamProtocolNormalized() == "chat" && a.HasUpstream() {
					hasChat = true
					break
				}
			}
			if hasChat {
				if err := syncOpenAICompat(cfg, name, tmpProfile); err != nil {
					die("syncing upstream to proxy: %v", err)
				}
			} else {
				_ = removeOpenAICompat(cfg, name)
			}
			if err := ensureShimForUpstream(cfg, name, tmpProfile); err != nil {
				die("syncing upstream to shim: %v", err)
			}
		} else {
			if err := syncOpenAICompat(cfg, name, tmpProfile); err != nil {
				die("syncing upstream to proxy: %v", err)
			}
			// Clean any stale shim entry
			_ = removeShimEntry(cfg, name)
		}
		// If type was implicit cliproxy, ensure TOML has explicit type line.
		if o.typ == "" && hasUpstream {
			s := b.String()
			if !strings.Contains(s, "type =") {
				s = strings.Replace(s, "# type = \"cliproxy\"   # or \"anthropic\" for direct endpoints\n", "type = \"cliproxy\"\n", 1)
				b.Reset()
				b.WriteString(s)
			}
		}
	}

	path := filepath.Join(ccpConfigDir(), "profiles", name+".toml")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		die("%v", err)
	}
	if fileExists(path) {
		die("%s already exists; edit it directly or run `ccp edit %s`", path, name)
	}
	if err := os.WriteFile(path, []byte(b.String()), 0o600); err != nil {
		die("%v", err)
	}
	okf("created %s", path)
	infof("open it with %s; then try %s",
		paint(cBold, fmt.Sprintf("ccp edit %s", name)),
		paint(cBold, fmt.Sprintf("ccp show %s", name)))
}
func cliNormalizeUpstreamBaseURL(raw string) string {
	s := strings.TrimSpace(raw)
	// Expand env vars if present (like ${VAR})
	s = expandEnvVars(s)
	s = strings.TrimRight(s, "/")
	if strings.HasSuffix(s, "/v1/responses") {
		s = strings.TrimSuffix(s, "/responses")
		warnf("upstream base URL %q looks like a full /v1/responses endpoint; normalized to %q", raw, s)
	} else if strings.HasSuffix(s, "/v1/chat/completions") {
		s = strings.TrimSuffix(s, "/chat/completions")
		warnf("upstream base URL %q looks like a full /v1/chat/completions endpoint; normalized to %q", raw, s)
	} else if strings.HasSuffix(s, "/responses") {
		s = strings.TrimSuffix(s, "/responses")
		warnf("upstream base URL %q looks like a /responses endpoint; normalized to %q", raw, s)
	} else if strings.HasSuffix(s, "/chat/completions") {
		s = strings.TrimSuffix(s, "/chat/completions")
		warnf("upstream base URL %q looks like a /chat/completions endpoint; normalized to %q", raw, s)
	}
	return s
}

func handleEdit(args []string) {
	editor := lookPathAll(os.Getenv("EDITOR"), os.Getenv("VISUAL"))
	if editor == "" {
		editor = lookPathAll("vi", "nano")
	}
	if editor == "" {
		die("no editor found; set $EDITOR")
	}
	target := filepath.Join(ccpConfigDir(), "config.toml")
	label := "config.toml"
	if len(args) == 1 {
		path := filepath.Join(ccpConfigDir(), "profiles", args[0]+".toml")
		if !fileExists(path) {
			cfg := mustLoadConfig()
			if _, ok := cfg.Profiles[args[0]]; ok {
				die("profile %q lives inside config.toml ([profiles.%s]); edit that file directly",
					args[0], args[0])
			}
			die("no such profile %q", args[0])
		}
		target, label = path, args[0]+".toml"
	} else if len(args) > 1 {
		die("usage: ccp edit [PROFILE]")
	}
	if !fileExists(target) {
		if _, err := config.LoadConfig(); err != nil { // triggers bootstrap
			die("%v", err)
		}
	}
	infof("editing %s", paint(cBold, label))
	env := os.Environ()
	argv := []string{filepath.Base(editor), target}
	if err := runReplacing(editor, argv, env); err != nil {
		die("exec %s: %v", editor, err)
	}
}

func handleRemove(name string) {
	cfg := mustLoadConfig()
	if _, ok := cfg.Profiles[name]; !ok {
		cfg.ResolveProfile(name) // dies with available names
	}
	removed := false
	for _, cand := range []string{
		filepath.Join(cfg.Dir, "profiles", name+".toml"),
	} {
		if fileExists(cand) {
			if err := os.Remove(cand); err != nil {
				die("%v", err)
			}
			removed = true
		}
	}
	// Best-effort clean of routing state.
	clearRoutingState(name)
	// Best-effort clean of upstream proxy/shim entries.
	if err := removeOpenAICompat(cfg, name); err != nil {
		warnf("could not clean proxy upstream for %q: %v", name, err)
	}
	if err := removeShimEntry(cfg, name); err != nil {
		warnf("could not clean shim upstream for %q: %v", name, err)
	}
	if removed {
		okf("removed profiles/%s.toml", name)
		return
	}
	die("profile %q is defined in config.toml ([profiles.%s]); remove that table by hand",
		name, name)
}

func runAddWizard() {
	cfg := mustLoadConfig()
	fmt.Println(paint(cBold, "Add a new profile"))
	fmt.Println(paint(cDim, "Leave blank for defaults where shown. Ctrl-C to cancel."))
	fmt.Println()

	var name string
	for {
		name = promptLine("Profile name (letters, digits, - _ .)", "")
		if name == "" {
			continue
		}
		if !safeName(name) || name == "." || name == ".." {
			warnf("invalid profile name %q (use letters, digits, - _ .)", name)
			continue
		}
		if _, ok := cfg.Profiles[name]; ok {
			warnf("profile %q already exists", name)
			continue
		}
		if fileExists(filepath.Join(ccpConfigDir(), "profiles", name+".toml")) {
			warnf("profiles/%s.toml already exists", name)
			continue
		}
		break
	}

	typIdx, err := selectOption("Type:", []string{
		"cliproxy - through local CLIProxyAPI (127.0.0.1:8317)",
		"anthropic - direct endpoint",
	}, 0)
	if err != nil {
		return
	}
	typ := "cliproxy"
	if typIdx == 1 {
		typ = "anthropic"
	}

	var baseURL string
	if typ == "anthropic" {
		baseURL = promptLine("Base URL (blank for official api.anthropic.com)", "")
	} else {
		baseURL = promptLine("Base URL (blank for proxy default http://127.0.0.1:8317)", "")
	}

	var upstreamBaseURL, upstreamAPIKeyEnv, upstreamAPIKey, upstreamName, upstreamModel, upstreamModelAlias, upstreamProtocol string
	var hasUpstream bool
	if typ == "cliproxy" {
		if confirmYN("Translate generic OpenAI upstream (Opencode Go, OpenRouter, etc.)?", false) {
			hasUpstream = true
			for {
				raw := promptLine("Upstream OpenAI base URL (e.g. https://opencode.ai/zen/go/v1)", "")
				if raw == "" {
					warnf("upstream base URL cannot be empty")
					continue
				}
				norm := cliNormalizeUpstreamBaseURL(raw)
				if u, err := url.Parse(norm); err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
					warnf("invalid upstream base URL %q", norm)
					continue
				}
				upstreamBaseURL = norm
				break
			}
			upAuthOpts := []string{
				"api key from env var (recommended)",
				"api key literal (paste key, stored chmod 600)",
			}
			upChoice, err := selectOption("Upstream auth:", upAuthOpts, 0)
			if err != nil {
				return
			}
			switch upChoice {
			case 0:
				for {
					upstreamAPIKeyEnv = promptLine("Env var holding upstream API key", "")
					if upstreamAPIKeyEnv != "" {
						break
					}
					warnf("env var name cannot be empty")
				}
			case 1:
				for {
					upstreamAPIKey = promptLine("Paste upstream API key (stored plaintext, chmod 600)", "")
					if upstreamAPIKey != "" {
						break
					}
					warnf("key cannot be empty")
				}
			}
			upstreamModel = promptLine("Upstream model name (e.g. gpt-5, my-model)", "")
			if upstreamModel == "" {
				warnf("upstream model is empty; will default to local alias")
			}
			upstreamModelAlias = promptLine("Local alias (blank = same as upstream model)", "")
			if upstreamModelAlias == "" {
				upstreamModelAlias = upstreamModel
			}
			upstreamName = promptLine("Upstream provider name (blank = profile name)", "")
			if upstreamName != "" && !safeName(upstreamName) {
				warnf("invalid upstream name %q; ignoring", upstreamName)
				upstreamName = ""
			}
			upstreamProtocol = promptLine("Upstream protocol [chat/responses] (blank=chat)", "")
			upstreamProtocol = strings.ToLower(strings.TrimSpace(upstreamProtocol))
			if upstreamProtocol != "" && upstreamProtocol != "chat" && upstreamProtocol != "responses" {
				warnf("unknown upstream_protocol %q, using chat", upstreamProtocol)
				upstreamProtocol = "chat"
			}
			if upstreamProtocol == "responses" {
				infof("using OpenAI Responses API via shim")
			}
		}
	}

	desc := promptLine("Description", "")
	var model string
	if hasUpstream {
		model = upstreamModelAlias
		if model == "" {
			model = upstreamModel
		}
		if model == "" {
			model = promptLine("Model (blank to inherit, append [1m] for 1M models)", "")
		} else {
			fmt.Printf("  model alias: %s\n", paint(cCyan, model))
		}
	} else {
		model = promptLine("Model (blank to inherit, append [1m] for 1M models)", "")
	}

	// auth
	var auth, authTokenEnv, apiKeyEnv, authToken, apiKey string
	if hasUpstream {
		// proxy auth stays auto; no prompt
	} else {
		var authOpts []string
		var authDefault int
		if typ == "cliproxy" {
			authOpts = []string{
				"auto - use proxy config api-keys[0] (recommended)",
				"bearer token from env var (ANTHROPIC_AUTH_TOKEN)",
				"bearer token literal (paste key, stored chmod 600)",
				"api key from env var (ANTHROPIC_API_KEY)",
				"api key literal (paste key, stored chmod 600)",
			}
			authDefault = 0
		} else {
			authOpts = []string{
				"none - use Claude login (for official)",
				"bearer token from env var (ANTHROPIC_AUTH_TOKEN)",
				"bearer token literal (paste key, stored chmod 600)",
				"api key from env var (ANTHROPIC_API_KEY)",
				"api key literal (paste key, stored chmod 600)",
			}
			authDefault = 0
		}
		authChoice, err := selectOption("Auth:", authOpts, authDefault)
		if err != nil {
			return
		}
		if typ == "cliproxy" {
			switch authChoice {
			case 0:
				// auto
			case 1:
				for {
					authTokenEnv = promptLine("Env var holding bearer token", "")
					if authTokenEnv != "" {
						break
					}
					warnf("env var name cannot be empty")
				}
			case 2:
				for {
					authToken = promptLine("Paste bearer token (stored plaintext, chmod 600)", "")
					if authToken != "" {
						break
					}
					warnf("token cannot be empty")
				}
			case 3:
				for {
					apiKeyEnv = promptLine("Env var holding API key", "")
					if apiKeyEnv != "" {
						break
					}
					warnf("env var name cannot be empty")
				}
			case 4:
				for {
					apiKey = promptLine("Paste API key (stored plaintext, chmod 600)", "")
					if apiKey != "" {
						break
					}
					warnf("key cannot be empty")
				}
			}
		} else {
			switch authChoice {
			case 0:
				auth = "none"
			case 1:
				for {
					authTokenEnv = promptLine("Env var holding bearer token", "")
					if authTokenEnv != "" {
						break
					}
					warnf("env var name cannot be empty")
				}
			case 2:
				for {
					authToken = promptLine("Paste bearer token (stored plaintext, chmod 600)", "")
					if authToken != "" {
						break
					}
					warnf("token cannot be empty")
				}
			case 3:
				for {
					apiKeyEnv = promptLine("Env var holding API key", "")
					if apiKeyEnv != "" {
						break
					}
					warnf("env var name cannot be empty")
				}
			case 4:
				for {
					apiKey = promptLine("Paste API key (stored plaintext, chmod 600)", "")
					if apiKey != "" {
						break
					}
					warnf("key cannot be empty")
				}
			}
		}
	}

	// Optional multi-account pool (round-robin).
	var accounts []config.Account
	var routing *config.Routing
	if confirmYN("Pool multiple subscriptions under this profile? (round-robin across accounts)", false) {
		countStr := promptLine("How many accounts in the pool? (2-10)", "2")
		count := 2
		if n, err := fmt.Sscanf(countStr, "%d", &count); err != nil || n != 1 || count < 2 || count > 10 {
			warnf("invalid count %q; using 2", countStr)
			count = 2
		}
		for idx := 0; idx < count; idx++ {
			fmt.Println()
			fmt.Printf("%s %d/%d\n", paint(cBold, fmt.Sprintf("Account %d", idx+1)), idx+1, count)
			var aAuth, aAuthTokenEnv, aAPIKeyEnv, aAuthToken, aAPIKey, aBaseURL, aName string
			var aUpstreamBaseURL, aUpstreamAPIKeyEnv, aUpstreamAPIKey, aUpstreamName, aUpstreamModel, aUpstreamModelAlias string
			aName = promptLine("  Account name (optional, for display)", "")
			if aName != "" && !safeName(aName) {
				warnf("invalid account name %q; ignoring", aName)
				aName = ""
			}
			aBaseURL = promptLine("  Base URL override (blank = use profile base_url)", "")
			if hasUpstream {
				aUpstreamBaseURL = promptLine("  Upstream base URL override (blank = use profile upstream base_url)", "")
				if aUpstreamBaseURL != "" {
					norm := cliNormalizeUpstreamBaseURL(aUpstreamBaseURL)
					if u, err := url.Parse(norm); err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
						warnf("invalid upstream base URL %q; ignoring", aUpstreamBaseURL)
						aUpstreamBaseURL = ""
					} else {
						aUpstreamBaseURL = norm
					}
				}
				upAuthOpts2 := []string{
					"api key from env var (recommended)",
					"api key literal (paste key, stored chmod 600)",
					"inherit from profile upstream",
				}
				upChoice2, err := selectOption("  Upstream auth for this account:", upAuthOpts2, 0)
				if err != nil {
					return
				}
				switch upChoice2 {
				case 0:
					for {
						aUpstreamAPIKeyEnv = promptLine("  Env var holding upstream API key", "")
						if aUpstreamAPIKeyEnv != "" {
							break
						}
						warnf("env var name cannot be empty")
					}
				case 1:
					for {
						aUpstreamAPIKey = promptLine("  Paste upstream API key (stored plaintext, chmod 600)", "")
						if aUpstreamAPIKey != "" {
							break
						}
						warnf("key cannot be empty")
					}
				case 2:
					// inherit
				}
				aUpstreamModel = promptLine("  Upstream model override (blank = use profile upstream model)", "")
				aUpstreamModelAlias = promptLine("  Upstream alias override (blank = same)", "")
				aUpstreamName = promptLine("  Upstream provider name override (blank = default)", "")
				if aUpstreamName != "" && !safeName(aUpstreamName) {
					warnf("invalid upstream name %q; ignoring", aUpstreamName)
					aUpstreamName = ""
				}
				// proxy auth for translated pool stays auto
			} else {
				var authOpts2 []string
				var authDefault2 int
				if typ == "cliproxy" {
					authOpts2 = []string{
						"auto - use proxy config api-keys[0] (recommended)",
						"bearer token from env var (ANTHROPIC_AUTH_TOKEN)",
						"bearer token literal (paste key, stored chmod 600)",
						"api key from env var (ANTHROPIC_API_KEY)",
						"api key literal (paste key, stored chmod 600)",
					}
					authDefault2 = 0
				} else {
					authOpts2 = []string{
						"none - use Claude login (for official)",
						"bearer token from env var (ANTHROPIC_AUTH_TOKEN)",
						"bearer token literal (paste key, stored chmod 600)",
						"api key from env var (ANTHROPIC_API_KEY)",
						"api key literal (paste key, stored chmod 600)",
					}
					authDefault2 = 0
				}
				authChoice2, err := selectOption("  Auth for this account:", authOpts2, authDefault2)
				if err != nil {
					return
				}
				if typ == "cliproxy" {
					switch authChoice2 {
					case 0:
					case 1:
						for {
							aAuthTokenEnv = promptLine("  Env var holding bearer token", "")
							if aAuthTokenEnv != "" {
								break
							}
							warnf("env var name cannot be empty")
						}
					case 2:
						for {
							aAuthToken = promptLine("  Paste bearer token (stored plaintext, chmod 600)", "")
							if aAuthToken != "" {
								break
							}
							warnf("token cannot be empty")
						}
					case 3:
						for {
							aAPIKeyEnv = promptLine("  Env var holding API key", "")
							if aAPIKeyEnv != "" {
								break
							}
							warnf("env var name cannot be empty")
						}
					case 4:
						for {
							aAPIKey = promptLine("  Paste API key (stored plaintext, chmod 600)", "")
							if aAPIKey != "" {
								break
							}
							warnf("key cannot be empty")
						}
					}
				} else {
					switch authChoice2 {
					case 0:
						aAuth = "none"
					case 1:
						for {
							aAuthTokenEnv = promptLine("  Env var holding bearer token", "")
							if aAuthTokenEnv != "" {
								break
							}
							warnf("env var name cannot be empty")
						}
					case 2:
						for {
							aAuthToken = promptLine("  Paste bearer token (stored plaintext, chmod 600)", "")
							if aAuthToken != "" {
								break
							}
							warnf("token cannot be empty")
						}
					case 3:
						for {
							aAPIKeyEnv = promptLine("  Env var holding API key", "")
							if aAPIKeyEnv != "" {
								break
							}
							warnf("env var name cannot be empty")
						}
					case 4:
						for {
							aAPIKey = promptLine("  Paste API key (stored plaintext, chmod 600)", "")
							if aAPIKey != "" {
								break
							}
							warnf("key cannot be empty")
						}
					}
				}
			}
			accounts = append(accounts, config.Account{
				Name:               aName,
				BaseURL:            aBaseURL,
				Auth:               aAuth,
				AuthTokenEnv:       aAuthTokenEnv,
				APIKeyEnv:          aAPIKeyEnv,
				AuthToken:          aAuthToken,
				APIKey:             aAPIKey,
				UpstreamBaseURL:    aUpstreamBaseURL,
				UpstreamAPIKeyEnv:  aUpstreamAPIKeyEnv,
				UpstreamAPIKey:     aUpstreamAPIKey,
				UpstreamName:       aUpstreamName,
				UpstreamModel:      aUpstreamModel,
				UpstreamModelAlias: aUpstreamModelAlias,
			})
		}
		routing = &config.Routing{Strategy: "round-robin"}
		if len(accounts) > 0 {
			if !hasUpstream && (auth != "" || authTokenEnv != "" || apiKeyEnv != "" || authToken != "" || apiKey != "") {
				infof("pool wins: ignoring top-level auth in favor of per-account auth")
				auth, authTokenEnv, apiKeyEnv, authToken, apiKey = "", "", "", "", ""
			}
		}
	}

	haiku := promptLine("Background / haiku model (blank = same as model)", "")
	timeoutStr := promptLine("API timeout ms (blank = default 600000)", "")
	timeoutMS := 0
	if timeoutStr != "" {
		if n, err := fmt.Sscanf(timeoutStr, "%d", &timeoutMS); err != nil || n != 1 || timeoutMS < 0 {
			warnf("invalid timeout %q; using default", timeoutStr)
			timeoutMS = 0
		}
	}
	ctxStr := promptLine("Max context tokens (blank = 200k default, 1000000 or 1m for 1M)", "")
	ctxTokens := 0
	if ctxStr != "" {
		if ctxStr == "1m" || ctxStr == "1M" {
			ctxTokens = 1000000
		} else if n, err := fmt.Sscanf(ctxStr, "%d", &ctxTokens); err != nil || n != 1 || ctxTokens < 0 {
			warnf("invalid context tokens %q; using default", ctxStr)
			ctxTokens = 0
		}
	}
	disableUnknown := confirmYN("Disable unknown-model window enforcement (pre-2.1 behavior)?", false)

	// summary
	fmt.Println()
	fmt.Println(paint(cBold, "Summary:"))
	fmt.Printf("  name:        %s\n", paint(cCyan, name))
	fmt.Printf("  type:        %s\n", typ)
	if baseURL != "" {
		fmt.Printf("  base_url:    %s\n", baseURL)
	}
	if hasUpstream {
		fmt.Printf("  upstream:    %s\n", upstreamBaseURL)
		if upstreamAPIKeyEnv != "" {
			fmt.Printf("  up auth:     api key $%s\n", upstreamAPIKeyEnv)
		} else if upstreamAPIKey != "" {
			fmt.Printf("  up auth:     api key %s\n", maskSecret(upstreamAPIKey))
		}
		if upstreamModel != "" {
			fmt.Printf("  up model:    %s\n", upstreamModel)
			if upstreamModelAlias != "" && upstreamModelAlias != upstreamModel {
				fmt.Printf("  alias:       %s\n", upstreamModelAlias)
			}
		}
		if upstreamName != "" {
			fmt.Printf("  up name:     %s\n", upstreamName)
		}
	}
	if desc != "" {
		fmt.Printf("  description: %s\n", desc)
	}
	if model != "" {
		fmt.Printf("  model:       %s\n", model)
	} else {
		fmt.Printf("  model:       %s\n", paint(cDim, "(inherit)"))
	}
	if len(accounts) > 0 {
		fmt.Printf("  pool:        %d accounts (round-robin)\n", len(accounts))
		for i, a := range accounts {
			src := ""
			if hasUpstream {
				switch {
				case a.UpstreamAPIKeyEnv != "":
					src = fmt.Sprintf("upstream api key $%s", a.UpstreamAPIKeyEnv)
				case a.UpstreamAPIKey != "":
					src = fmt.Sprintf("upstream api key %s", maskSecret(a.UpstreamAPIKey))
				default:
					src = "upstream inherit"
				}
				if a.UpstreamBaseURL != "" {
					src += " upstream_base_url=" + a.UpstreamBaseURL
				}
				if a.UpstreamModel != "" {
					src += " upstream_model=" + a.UpstreamModel
				}
				if a.UpstreamModelAlias != "" {
					src += " alias=" + a.UpstreamModelAlias
				}
				if a.Name != "" {
					src += " name=" + a.Name
				}
			} else {
				switch {
				case a.Auth == "none":
					src = "none"
				case a.AuthTokenEnv != "":
					src = fmt.Sprintf("bearer $%s", a.AuthTokenEnv)
				case a.APIKeyEnv != "":
					src = fmt.Sprintf("api key $%s", a.APIKeyEnv)
				case a.AuthToken != "":
					src = fmt.Sprintf("bearer %s", maskSecret(a.AuthToken))
				case a.APIKey != "":
					src = fmt.Sprintf("api key %s", maskSecret(a.APIKey))
				default:
					if typ == "cliproxy" {
						src = "auto (proxy api-keys[0])"
					}
				}
				if a.BaseURL != "" {
					src += " base_url=" + a.BaseURL
				}
				if a.Name != "" {
					src += " name=" + a.Name
				}
			}
			fmt.Printf("    [%d] %s\n", i, src)
		}
	} else {
		if hasUpstream {
			// already shown upstream above
		} else {
			switch {
			case auth == "none":
				fmt.Printf("  auth:        none\n")
			case authTokenEnv != "":
				fmt.Printf("  auth:        bearer from $%s\n", authTokenEnv)
			case authToken != "":
				fmt.Printf("  auth:        bearer literal %s\n", maskSecret(authToken))
			case apiKeyEnv != "":
				fmt.Printf("  auth:        api key from $%s\n", apiKeyEnv)
			case apiKey != "":
				fmt.Printf("  auth:        api key literal %s\n", maskSecret(apiKey))
			default:
				if typ == "cliproxy" {
					fmt.Printf("  auth:        auto (proxy api-keys[0])\n")
				}
			}
		}
	}
	if haiku != "" {
		fmt.Printf("  haiku_model: %s\n", haiku)
	}
	if timeoutMS > 0 {
		fmt.Printf("  timeout:     %d ms\n", timeoutMS)
	}
	if ctxTokens > 0 {
		fmt.Printf("  context:     %d tokens\n", ctxTokens)
	}
	if disableUnknown {
		fmt.Printf("  window enforcement: disabled\n")
	}
	fmt.Println()

	if !confirmYN(fmt.Sprintf("Create profile %q?", name), true) {
		infof("cancelled")
		return
	}

	p := &config.Profile{
		Description:                          desc,
		Type:                                 typ,
		BaseURL:                              baseURL,
		Model:                                model,
		Auth:                                 auth,
		AuthTokenEnv:                         authTokenEnv,
		APIKeyEnv:                            apiKeyEnv,
		AuthToken:                            authToken,
		APIKey:                               apiKey,
		UpstreamBaseURL:                      upstreamBaseURL,
		UpstreamAPIKeyEnv:                    upstreamAPIKeyEnv,
		UpstreamAPIKey:                       upstreamAPIKey,
		UpstreamName:                         upstreamName,
		UpstreamModel:                        upstreamModel,
		UpstreamModelAlias:                   upstreamModelAlias,
		UpstreamProtocol:                     upstreamProtocol,
		HaikuModel:                           haiku,
		APITimeoutMS:                         timeoutMS,
		MaxContextTokens:                     ctxTokens,
		DisableUnknownModelWindowEnforcement: disableUnknown,
		Accounts:                             accounts,
		Routing:                              routing,
	}
	// Validate including upstream.
	p.Normalize()
	if err := p.ValidatePool(); err != nil {
		die("%v", err)
	}
	if hasUpstream {
		if p.IsUpstreamResponses() {
			if err := ensureShimForUpstream(cfg, name, p); err != nil {
				die("syncing upstream to shim: %v", err)
			}
			// Also ensure proxy entry is cleaned if previously was chat
			_ = removeOpenAICompat(cfg, name)
		} else {
			if err := syncOpenAICompat(cfg, name, p); err != nil {
				die("syncing upstream to proxy: %v", err)
			}
			_ = removeShimEntry(cfg, name)
		}
	}
	if err := saveProfile(name, p); err != nil {
		die("%v", err)
	}
	okf("created profiles/%s.toml", name)
	infof("try %s", paint(cBold, fmt.Sprintf("ccp show %s", name)))
	if cfg.DefaultProfile == "" && confirmYN("Set as default profile (bare `ccp` launches it)?", false) {
		if err := setDefaultProfile(name); err != nil {
			warnf("could not update default_profile: %v", err)
		} else {
			okf("default profile set to %q", name)
		}
	}
}

func saveProfile(name string, p *config.Profile) error {
	path := filepath.Join(ccpConfigDir(), "profiles", name+".toml")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	if fileExists(path) {
		return fmt.Errorf("%s already exists", path)
	}
	content := renderProfileToml(p)
	return os.WriteFile(path, []byte(content), 0o600)
}

func renderProfileToml(p *config.Profile) string {
	var b strings.Builder
	if p.Description != "" {
		fmt.Fprintf(&b, "description = %q\n", p.Description)
	}
	if p.Type != "" {
		fmt.Fprintf(&b, "type = %q\n", p.Type)
	}
	if p.BaseURL != "" {
		fmt.Fprintf(&b, "base_url = %q\n", p.BaseURL)
	}
	if p.UpstreamBaseURL != "" {
		fmt.Fprintf(&b, "upstream_base_url = %q\n", p.UpstreamBaseURL)
	}
	if p.UpstreamAPIKeyEnv != "" {
		fmt.Fprintf(&b, "upstream_api_key_env = %q\n", p.UpstreamAPIKeyEnv)
	}
	if p.UpstreamAPIKey != "" {
		fmt.Fprintf(&b, "upstream_api_key = %q\n", p.UpstreamAPIKey)
	}
	if p.UpstreamName != "" {
		fmt.Fprintf(&b, "upstream_name = %q\n", p.UpstreamName)
	}
	if p.UpstreamModel != "" {
		fmt.Fprintf(&b, "upstream_model = %q\n", p.UpstreamModel)
	}
	if p.UpstreamModelAlias != "" {
		fmt.Fprintf(&b, "upstream_model_alias = %q\n", p.UpstreamModelAlias)
	}
	if p.UpstreamProtocol != "" {
		fmt.Fprintf(&b, "upstream_protocol = %q\n", p.UpstreamProtocol)
	}
	if p.Model != "" {
		fmt.Fprintf(&b, "model = %q\n", p.Model)
	} else {
		b.WriteString("# model = \"...\"  # omit to inherit ~/.claude/settings.json\n")
	}
	if len(p.Accounts) > 0 {
		// Pool form wins; top-level auth is intentionally omitted.
		if p.Routing != nil && p.Routing.Strategy != "" && p.Routing.Strategy != "round-robin" {
			fmt.Fprintf(&b, "\n[routing]\nstrategy = %q\n", p.Routing.Strategy)
		}
	} else {
		if p.Auth != "" {
			fmt.Fprintf(&b, "auth = %q\n", p.Auth)
		}
		if p.AuthTokenEnv != "" {
			fmt.Fprintf(&b, "auth_token_env = %q\n", p.AuthTokenEnv)
		}
		if p.APIKeyEnv != "" {
			fmt.Fprintf(&b, "api_key_env = %q\n", p.APIKeyEnv)
		}
		if p.AuthToken != "" {
			fmt.Fprintf(&b, "auth_token = %q\n", p.AuthToken)
		}
		if p.APIKey != "" {
			fmt.Fprintf(&b, "api_key = %q\n", p.APIKey)
		}
	}
	if p.HaikuModel != "" {
		fmt.Fprintf(&b, "haiku_model = %q\n", p.HaikuModel)
	}
	if p.SonnetModel != "" {
		fmt.Fprintf(&b, "sonnet_model = %q\n", p.SonnetModel)
	}
	if p.OpusModel != "" {
		fmt.Fprintf(&b, "opus_model = %q\n", p.OpusModel)
	}
	if p.SubagentModel != "" {
		fmt.Fprintf(&b, "subagent_model = %q\n", p.SubagentModel)
	}
	if p.CustomModelOption != "" {
		fmt.Fprintf(&b, "custom_model_option = %q\n", p.CustomModelOption)
	}
	if p.APITimeoutMS > 0 {
		fmt.Fprintf(&b, "api_timeout_ms = %d\n", p.APITimeoutMS)
	}
	if p.MaxThinkingTokens > 0 {
		fmt.Fprintf(&b, "max_thinking_tokens = %d\n", p.MaxThinkingTokens)
	}
	if p.MaxOutputTokens > 0 {
		fmt.Fprintf(&b, "max_output_tokens = %d\n", p.MaxOutputTokens)
	}
	if p.MaxContextTokens > 0 {
		fmt.Fprintf(&b, "max_context_tokens = %d\n", p.MaxContextTokens)
	}
	if p.DisablePromptCaching {
		b.WriteString("disable_prompt_caching = true\n")
	}
	if p.DisableUnknownModelWindowEnforcement {
		b.WriteString("disable_unknown_model_window_enforcement = true\n")
	}
	if len(p.Accounts) > 0 {
		b.WriteString("\n# Round-robin order = declaration order. `ccp <profile>` cycles through these.\n")
		for _, a := range p.Accounts {
			b.WriteString("\n[[accounts]]\n")
			if a.Name != "" {
				fmt.Fprintf(&b, "name = %q\n", a.Name)
			}
			if a.Description != "" {
				fmt.Fprintf(&b, "description = %q\n", a.Description)
			}
			if a.BaseURL != "" {
				fmt.Fprintf(&b, "base_url = %q\n", a.BaseURL)
			}
			if a.UpstreamBaseURL != "" {
				fmt.Fprintf(&b, "upstream_base_url = %q\n", a.UpstreamBaseURL)
			}
			if a.UpstreamAPIKeyEnv != "" {
				fmt.Fprintf(&b, "upstream_api_key_env = %q\n", a.UpstreamAPIKeyEnv)
			}
			if a.UpstreamAPIKey != "" {
				fmt.Fprintf(&b, "upstream_api_key = %q\n", a.UpstreamAPIKey)
			}
			if a.UpstreamName != "" {
				fmt.Fprintf(&b, "upstream_name = %q\n", a.UpstreamName)
			}
			if a.UpstreamModel != "" {
				fmt.Fprintf(&b, "upstream_model = %q\n", a.UpstreamModel)
			}
			if a.UpstreamModelAlias != "" {
				fmt.Fprintf(&b, "upstream_model_alias = %q\n", a.UpstreamModelAlias)
			}
			if a.UpstreamProtocol != "" {
				fmt.Fprintf(&b, "upstream_protocol = %q\n", a.UpstreamProtocol)
			}
			if a.Auth != "" {
				fmt.Fprintf(&b, "auth = %q\n", a.Auth)
			}
			if a.AuthTokenEnv != "" {
				fmt.Fprintf(&b, "auth_token_env = %q\n", a.AuthTokenEnv)
			}
			if a.APIKeyEnv != "" {
				fmt.Fprintf(&b, "api_key_env = %q\n", a.APIKeyEnv)
			}
			if a.AuthToken != "" {
				fmt.Fprintf(&b, "auth_token = %q\n", a.AuthToken)
			}
			if a.APIKey != "" {
				fmt.Fprintf(&b, "api_key = %q\n", a.APIKey)
			}
		}
	}
	if len(p.ExtraEnv) > 0 {
		b.WriteString("\n[extra_env]\n")
		for k, v := range p.ExtraEnv {
			fmt.Fprintf(&b, "%s = %q\n", k, v)
		}
	}
	return b.String()
}

func setDefaultProfile(name string) error {
	cfgPath := filepath.Join(ccpConfigDir(), "config.toml")
	data, err := os.ReadFile(cfgPath)
	if err != nil {
		return err
	}
	content := string(data)
	// replace existing default_profile line if present
	if strings.Contains(content, "default_profile") {
		lines := strings.Split(content, "\n")
		for i, l := range lines {
			trim := strings.TrimSpace(l)
			if strings.HasPrefix(trim, "default_profile") {
				lines[i] = fmt.Sprintf("default_profile = %q", name)
				break
			}
		}
		content = strings.Join(lines, "\n")
	} else {
		content = fmt.Sprintf("default_profile = %q\n\n", name) + content
	}
	return os.WriteFile(cfgPath, []byte(content), 0o600)
}

func clearDefaultProfile() error {
	cfgPath := filepath.Join(ccpConfigDir(), "config.toml")
	data, err := os.ReadFile(cfgPath)
	if err != nil {
		return err
	}
	content := string(data)
	if !strings.Contains(content, "default_profile") {
		return nil
	}
	lines := strings.Split(content, "\n")
	var out []string
	for _, l := range lines {
		trim := strings.TrimSpace(l)
		if strings.HasPrefix(trim, "default_profile") {
			continue
		}
		out = append(out, l)
	}
	content = strings.Join(out, "\n")
	return os.WriteFile(cfgPath, []byte(content), 0o600)
}

func handleDefault(args []string) {
	if len(args) == 0 {
		runDefaultPicker()
		return
	}
	if len(args) == 1 && (args[0] == "-h" || args[0] == "--help" || args[0] == "help") {
		fmt.Fprintln(os.Stderr, "usage: ccp default [PROFILE]")
		fmt.Fprintln(os.Stderr, "       ccp default --unset | --clear | --none  # clear default")
		fmt.Fprintln(os.Stderr, "       ccp default                             # picker")
		cfg := mustLoadConfig()
		if def := cfg.DefaultProfileName(); def != "" {
			fmt.Fprintf(os.Stderr, "\ncurrent default: %q\n", def)
		} else {
			fmt.Fprintln(os.Stderr, "\ncurrent default: (none)")
		}
		return
	}
	if len(args) == 1 && (args[0] == "--unset" || args[0] == "--clear" || args[0] == "--none" || args[0] == "-u") {
		if err := clearDefaultProfile(); err != nil {
			die("%v", err)
		}
		okf("default profile cleared (bare `ccp` will require explicit profile)")
		return
	}
	if len(args) != 1 {
		die("usage: ccp default [PROFILE] [--unset]\n       ccp default --clear  # clear default")
	}
	name := args[0]
	if strings.HasPrefix(name, "-") {
		die("usage: ccp default [PROFILE] [--unset]")
	}
	cfg := mustLoadConfig()
	if _, ok := cfg.Profiles[name]; !ok {
		cfg.ResolveProfile(name)
	}
	if err := setDefaultProfile(name); err != nil {
		die("%v", err)
	}
	okf("default profile set to %q", name)
}

func runDefaultPicker() {
	cfg := mustLoadConfig()
	names := cfg.ProfileNames()
	if len(names) == 0 {
		die("no profiles available")
	}
	def := cfg.DefaultProfileName()
	// Build display options, marking current default.
	options := make([]string, len(names))
	defIdx := 0
	for i, n := range names {
		label := n
		if n == def {
			label = fmt.Sprintf("%s %s", n, paint(cDim, "(current default)"))
			defIdx = i
		} else if p, ok := cfg.Profiles[n]; ok && p.Description != "" {
			label = fmt.Sprintf("%-12s %s", n, paint(cDim, p.Description))
		}
		options[i] = label
	}
	idx, err := selectOption("Select default profile (bare `ccp` launches it):", options, defIdx)
	if err != nil {
		return
	}
	name := names[idx]
	if name == def {
		infof("already default: %q", name)
		return
	}
	if err := setDefaultProfile(name); err != nil {
		die("%v", err)
	}
	okf("default profile set to %q", name)
}

func runRemovePicker() {
	cfg := mustLoadConfig()
	names := cfg.ProfileNames()
	if len(names) == 0 {
		infof("no profiles to remove")
		return
	}
	idx, err := selectOption("Remove which profile?", names, 0)
	if err != nil {
		return
	}
	name := names[idx]
	if !confirmYN(fmt.Sprintf("Delete profile %q?", name), false) {
		infof("cancelled")
		return
	}
	handleRemove(name)
}

func runEditPicker() {
	cfg := mustLoadConfig()
	names := cfg.ProfileNames()
	// Build options: config.toml + each profile with hint
	options := []string{paint(cDim, "config.toml (global + proxy)")}
	isConfig := []bool{true}
	for _, n := range names {
		path := filepath.Join(cfg.Dir, "profiles", n+".toml")
		label := n
		if !fileExists(path) {
			label = fmt.Sprintf("%s %s", n, paint(cDim, "(in config.toml)"))
		}
		options = append(options, label)
		isConfig = append(isConfig, false)
	}
	idx, err := selectOption("Edit which file?", options, 0)
	if err != nil {
		return
	}
	if isConfig[idx] {
		handleEdit(nil)
	} else {
		// idx-1 maps to names
		handleEdit([]string{names[idx-1]})
	}
}
