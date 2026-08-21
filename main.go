package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

var version = "0.1.0"

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
  add NAME [--opts]       create a new profile file
  edit [NAME]             open config.toml (or profiles/NAME.toml) in $EDITOR
  remove NAME             delete a profile
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
  opus_model / sonnet_model / haiku_model / fable_model / subagent_model
                          alias overrides; default to ` + "`model`" + `
  base_url                endpoint override (cliproxy default: http://127.0.0.1:8317)
  auth                    "bearer" → ANTHROPIC_AUTH_TOKEN | "x-api-key" → ANTHROPIC_API_KEY | "none"
  auth_token_env          name of env var holding a bearer token (recommended)
  api_key_env             name of env var holding an API key (recommended)
  api_timeout_ms          API_TIMEOUT_MS
  max_thinking_tokens     MAX_THINKING_TOKENS
  max_output_tokens       CLAUDE_CODE_MAX_OUTPUT_TOKENS
  disable_prompt_caching  DISABLE_PROMPT_CACHING=1
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
		handleAdd(rest)
	case "edit":
		handleEdit(rest)
	case "remove", "rm":
		if len(rest) != 1 {
			die("usage: ccp remove NAME")
		}
		handleRemove(rest[0])

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
	extra                         []string // KEY=VAL
}

func handleAdd(args []string) {
	if len(args) == 0 || strings.HasPrefix(args[0], "-") {
		die("usage: ccp add NAME [--type cliproxy|anthropic] [--model ID] [--desc TEXT]\n" +
			"              [--base-url URL] [--auth-token-env VAR] [--api-key-env VAR]\n" +
			"              [--haiku-model ID] [--sonnet-model ID] [--opus-model ID]\n" +
			"              [--subagent-model ID] [--timeout-ms N] [--set KEY=VAL]...")
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
