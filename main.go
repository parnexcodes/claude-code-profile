package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

var version = "0.3.0"

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

func main() {
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
		if def := cfg.defaultProfileName(); def != "" {
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
		fmt.Printf("ccp %s\n", version)

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

	case "proxy":
		handleProxy(rest)
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
}

func handleAdd(args []string) {
	if len(args) == 0 || strings.HasPrefix(args[0], "-") {
		die("usage: ccp add NAME [--type cliproxy|anthropic] [--model ID] [--desc TEXT]\n" +
			"              [--base-url URL] [--auth-token-env VAR] [--api-key-env VAR]\n" +
			"              [--haiku-model ID] [--sonnet-model ID] [--opus-model ID]\n" +
			"              [--subagent-model ID] [--timeout-ms N] [--max-context-tokens N|--1m]\n" +
			"              [--disable-unknown-model-window-enforcement] [--set KEY=VAL]...")
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
		default:
			die("unknown option %q", args[i])
		}
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
		if _, err := loadConfig(); err != nil { // triggers bootstrap
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
		cfg.resolveProfile(name) // dies with available names
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

	desc := promptLine("Description", "")
	model := promptLine("Model (blank to inherit, append [1m] for 1M models)", "")

	// auth
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
	var auth, authTokenEnv, apiKeyEnv, authToken, apiKey string
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
	if desc != "" {
		fmt.Printf("  description: %s\n", desc)
	}
	if model != "" {
		fmt.Printf("  model:       %s\n", model)
	} else {
		fmt.Printf("  model:       %s\n", paint(cDim, "(inherit)"))
	}
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

	p := &Profile{
		Description:                          desc,
		Type:                                 typ,
		BaseURL:                              baseURL,
		Model:                                model,
		Auth:                                 auth,
		AuthTokenEnv:                         authTokenEnv,
		APIKeyEnv:                            apiKeyEnv,
		AuthToken:                            authToken,
		APIKey:                               apiKey,
		HaikuModel:                           haiku,
		APITimeoutMS:                         timeoutMS,
		MaxContextTokens:                     ctxTokens,
		DisableUnknownModelWindowEnforcement: disableUnknown,
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

func saveProfile(name string, p *Profile) error {
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

func renderProfileToml(p *Profile) string {
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
	if p.Model != "" {
		fmt.Fprintf(&b, "model = %q\n", p.Model)
	} else {
		b.WriteString("# model = \"...\"  # omit to inherit ~/.claude/settings.json\n")
	}
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
