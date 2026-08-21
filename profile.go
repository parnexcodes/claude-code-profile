package main

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

// ---------------------------------------------------------------------------
// managed environment variables
//
// Before launching claude, ccp removes every managed variable from the child
// environment and then applies exactly what the profile defines. This makes
// switching profiles deterministic — a ZAI key exported in your shell can
// never leak into a Kimi session.
// ---------------------------------------------------------------------------

// modelAliasVars are the /model picker aliases; suffixes _NAME,
// _DESCRIPTION and _SUPPORTED_CAPABILITIES are derived from these.
var modelAliasVars = []string{
	"ANTHROPIC_DEFAULT_OPUS_MODEL",
	"ANTHROPIC_DEFAULT_SONNET_MODEL",
	"ANTHROPIC_DEFAULT_HAIKU_MODEL",
	"ANTHROPIC_DEFAULT_FABLE_MODEL",
}

var managedVars = func() []string {
	vars := []string{
		"ANTHROPIC_API_KEY",
		"ANTHROPIC_AUTH_TOKEN",
		"ANTHROPIC_BASE_URL",
		"ANTHROPIC_MODEL",
		"ANTHROPIC_DEFAULT_MODEL",
		"ANTHROPIC_SMALL_FAST_MODEL", // deprecated alias of DEFAULT_HAIKU_MODEL
		"ANTHROPIC_CUSTOM_HEADERS",
		"ANTHROPIC_CUSTOM_MODEL_OPTION",
		"CLAUDE_CODE_SUBAGENT_MODEL",
		"API_TIMEOUT_MS",
		"MAX_THINKING_TOKENS",
		"CLAUDE_CODE_MAX_OUTPUT_TOKENS",
		"DISABLE_PROMPT_CACHING",
		// provider toggles that would break third-party routing
		"CLAUDE_CODE_USE_BEDROCK",
		"CLAUDE_CODE_USE_VERTEX",
	}
	for _, base := range modelAliasVars {
		vars = append(vars, base,
			base+"_NAME",
			base+"_DESCRIPTION",
			base+"_SUPPORTED_CAPABILITIES")
	}
	for _, base := range []string{"ANTHROPIC_CUSTOM_MODEL_OPTION"} {
		vars = append(vars, base+"_NAME", base+"_DESCRIPTION", base+"_SUPPORTED_CAPABILITIES")
	}
	return vars
}()

// ---------------------------------------------------------------------------
// auth resolution
// ---------------------------------------------------------------------------

type authResult struct {
	EnvVar string // ANTHROPIC_AUTH_TOKEN or ANTHROPIC_API_KEY
	Value  string
	Source string // human-readable origin, e.g. "$ZAI_API_KEY"
}

func (p *Profile) authMode() string {
	if p.Auth != "" {
		return p.Auth
	}
	if p.Type == "cliproxy" {
		return "bearer"
	}
	return "x-api-key"
}

func (p *Profile) resolveAuth(cfg *Config) (*authResult, error) {
	mode := p.authMode()
	if mode == "none" {
		return nil, nil
	}

	getEnvRef := func(refName, refVal string) (string, error) {
		v := os.Getenv(refName)
		if v == "" {
			return "", fmt.Errorf("env var %s is not set (referenced by %s)", refName, refVal)
		}
		return v, nil
	}

	switch {
	case p.AuthTokenEnv != "":
		v, err := getEnvRef(p.AuthTokenEnv, "auth_token_env")
		if err != nil {
			return nil, err
		}
		return &authResult{"ANTHROPIC_AUTH_TOKEN", v, "$" + p.AuthTokenEnv}, nil
	case p.APIKeyEnv != "":
		v, err := getEnvRef(p.APIKeyEnv, "api_key_env")
		if err != nil {
			return nil, err
		}
		return &authResult{"ANTHROPIC_API_KEY", v, "$" + p.APIKeyEnv}, nil
	case p.AuthToken != "":
		return &authResult{"ANTHROPIC_AUTH_TOKEN", expandEnvVars(p.AuthToken), "auth_token (config)"}, nil
	case p.APIKey != "":
		return &authResult{"ANTHROPIC_API_KEY", expandEnvVars(p.APIKey), "api_key (config)"}, nil
	}

	if mode == "bearer" { // cliproxy default: reuse the proxy's own client key
		keys := readProxyAPIKeys(cfg)
		if len(keys) > 0 {
			return &authResult{"ANTHROPIC_AUTH_TOKEN", keys[0], "proxy config api-keys[0]"}, nil
		}
		return nil, nil // proxy may not require auth
	}
	return nil, nil // anthropic type with no creds: inherit login
}

// readProxyAPIKeys extracts the `api-keys` list from the CLIProxyAPI config.
type proxyYAML struct {
	APIKeys []string `yaml:"api-keys"`
	Port    int      `yaml:"port"`
	AuthDir string   `yaml:"auth-dir"`
}

func readProxyConfigFile(cfg *Config) *proxyYAML {
	data, err := os.ReadFile(cfg.proxyConfigFile())
	if err != nil {
		return nil
	}
	var y proxyYAML
	if err := yaml.Unmarshal(data, &y); err != nil {
		warnf("cannot parse %s: %v", cfg.proxyConfigFile(), err)
		return nil
	}
	return &y
}

func readProxyAPIKeys(cfg *Config) []string {
	y := readProxyConfigFile(cfg)
	if y == nil {
		return nil
	}
	return y.APIKeys
}

// ---------------------------------------------------------------------------
// endpoint + env assembly
// ---------------------------------------------------------------------------

func (p *Profile) effectiveBaseURL(cfg *Config) string {
	if p.BaseURL != "" {
		return strings.TrimRight(expandEnvVars(p.BaseURL), "/")
	}
	if p.Type == "cliproxy" {
		return fmt.Sprintf("http://%s:%d", cfg.Proxy.Host, cfg.Proxy.Port)
	}
	return ""
}

// inheritModel reads the fallback model from ~/.claude/settings.json.
func inheritModel() (string, bool) {
	s, err := readClaudeSettings(filepath.Join(homeDir(), ".claude", "settings.json"))
	if err != nil || s == nil || s.Model == "" {
		return "", false
	}
	return s.Model, true
}

type builtEnv struct {
	Sets   map[string]string // variables to set in the child environment
	Strips []string          // variables to remove from the inherited environment
	Notes  []string          // informational lines for the banner / ccp show
	Model  string            // effective primary model ("" = leave to claude)
	URL    string            // effective base URL ("" = official endpoint)
}

// buildEnv computes everything ccp will inject into the claude process.
func buildEnv(cfg *Config, name string, p *Profile) (*builtEnv, error) {
	b := &builtEnv{Sets: map[string]string{}}

	// --- strip set ---------------------------------------------------------
	strip := map[string]bool{}
	for _, v := range managedVars {
		strip[v] = true
	}
	for k := range p.ExtraEnv {
		strip[k] = true
	}
	for k := range b.Sets {
		strip[k] = true
	}
	for k := range strip {
		b.Strips = append(b.Strips, k)
	}
	sort.Strings(b.Strips)

	// --- endpoint ----------------------------------------------------------
	b.URL = p.effectiveBaseURL(cfg)
	if b.URL != "" {
		b.Sets["ANTHROPIC_BASE_URL"] = b.URL
		b.Notes = append(b.Notes, b.URL)
	} else {
		b.Notes = append(b.Notes, "api.anthropic.com")
	}

	// --- auth --------------------------------------------------------------
	auth, err := p.resolveAuth(cfg)
	if err != nil {
		return nil, fmt.Errorf("profile %s: %w", name, err)
	}
	if auth != nil {
		b.Sets[auth.EnvVar] = auth.Value
		b.Notes = append(b.Notes, "auth "+paint(cDim, auth.Source))
	}

	// --- models ------------------------------------------------------------
	model := p.Model
	if model == "" {
		if inherited, ok := inheritModel(); ok {
			model = inherited
			b.Notes = append(b.Notes, fmt.Sprintf("model %s (inherited from ~/.claude/settings.json)", paint(cBold, inherited)))
		} else {
			b.Notes = append(b.Notes, fmt.Sprintf("model %s (claude default)", paint(cDim, "auto")))
		}
	} else {
		b.Notes = append(b.Notes, "model "+paint(cBold, model))
	}
	b.Model = model

	if model != "" {
		b.Sets["ANTHROPIC_MODEL"] = model
		// Pin every alias to this backend so /model switching never falls
		// back to real Claude model IDs that the relay may not serve.
		b.Sets["ANTHROPIC_DEFAULT_OPUS_MODEL"] = pick(p.OpusModel, model)
		b.Sets["ANTHROPIC_DEFAULT_SONNET_MODEL"] = pick(p.SonnetModel, model)
		b.Sets["ANTHROPIC_DEFAULT_HAIKU_MODEL"] = pick(p.HaikuModel, model)
		if p.FableModel != "" {
			b.Sets["ANTHROPIC_DEFAULT_FABLE_MODEL"] = p.FableModel
		}
		b.Sets["CLAUDE_CODE_SUBAGENT_MODEL"] = pick(p.SubagentModel, model)
	}

	// --- custom /model picker entry ----------------------------------------
	if p.CustomModelOption != "" {
		id := expandEnvVars(p.CustomModelOption)
		b.Sets["ANTHROPIC_CUSTOM_MODEL_OPTION"] = id
		b.Sets["ANTHROPIC_CUSTOM_MODEL_OPTION_NAME"] = name
		b.Sets["ANTHROPIC_CUSTOM_MODEL_OPTION_DESCRIPTION"] =
			fmt.Sprintf("ccp profile %q (%s)", name, id)
	}

	// --- knobs --------------------------------------------------------------
	if p.APITimeoutMS > 0 {
		b.Sets["API_TIMEOUT_MS"] = fmt.Sprint(p.APITimeoutMS)
	}
	if p.MaxThinkingTokens > 0 {
		b.Sets["MAX_THINKING_TOKENS"] = fmt.Sprint(p.MaxThinkingTokens)
	}
	if p.MaxOutputTokens > 0 {
		b.Sets["CLAUDE_CODE_MAX_OUTPUT_TOKENS"] = fmt.Sprint(p.MaxOutputTokens)
	}
	if p.DisablePromptCaching {
		b.Sets["DISABLE_PROMPT_CACHING"] = "1"
	}

	// --- raw passthrough ----------------------------------------------------
	for k, v := range p.ExtraEnv {
		b.Sets[k] = expandEnvVars(v)
	}

	return b, nil
}

func pick(a, fallback string) string {
	if a != "" {
		return a
	}
	return fallback
}

// assembleEnv takes os.Environ()-style entries, drops stripped vars and adds
// the profile's sets. Result is deterministic (sorted additions).
func assembleEnv(environ []string, strips []string, sets map[string]string) []string {
	drop := map[string]bool{}
	for _, k := range strips {
		drop[k] = true
	}
	out := make([]string, 0, len(environ)+len(sets))
	for _, kv := range environ {
		k, _, _ := strings.Cut(kv, "=")
		if !drop[k] {
			out = append(out, kv)
		}
	}
	var keys []string
	for k := range sets {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		out = append(out, k+"="+sets[k])
	}
	return out
}
