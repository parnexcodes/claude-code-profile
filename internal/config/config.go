package config

import (
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"ccp/internal/util"
	"github.com/BurntSushi/toml"
)

// ---------------------------------------------------------------------------
// types
// ---------------------------------------------------------------------------

// Account is one interchangeable credential inside a multi-account profile.
// Order in the TOML file is the round-robin order.
type Account struct {
	Name         string `toml:"name"`
	Description  string `toml:"description"`
	BaseURL      string `toml:"base_url"`
	Auth         string `toml:"auth"`
	AuthTokenEnv string `toml:"auth_token_env"`
	APIKeyEnv    string `toml:"api_key_env"`
	AuthToken    string `toml:"auth_token"`
	APIKey       string `toml:"api_key"`
	// Upstream OpenAI-compatible provider fields (only valid for type=cliproxy translated profiles).
	UpstreamBaseURL    string `toml:"upstream_base_url"`
	UpstreamAPIKeyEnv  string `toml:"upstream_api_key_env"`
	UpstreamAPIKey     string `toml:"upstream_api_key"`
	UpstreamName       string `toml:"upstream_name"`
	UpstreamModel      string `toml:"upstream_model"`
	UpstreamModelAlias string `toml:"upstream_model_alias"`
	UpstreamProtocol   string `toml:"upstream_protocol"`
}

// Routing selects how a pool is cycled.
type Routing struct {
	Strategy string `toml:"strategy"` // "round-robin" (default)
}

// Profile describes how to launch Claude Code against one backend.
type Profile struct {
	Description string `toml:"description"`
	// Type selects where requests go:
	//   cliproxy  – through the local CLIProxyAPI (auto-managed by ccp)
	//   anthropic – direct Anthropic-compatible endpoint (official or any relay)
	Type string `toml:"type"`

	BaseURL string `toml:"base_url"` // override endpoint (else derived from type)

	// Model wiring. Empty fields fall back to Model, which itself falls back
	// to the "model" key in ~/.claude/settings.json.
	Model         string `toml:"model"`
	OpusModel     string `toml:"opus_model"`
	SonnetModel   string `toml:"sonnet_model"`
	HaikuModel    string `toml:"haiku_model"`
	FableModel    string `toml:"fable_model"`
	SubagentModel string `toml:"subagent_model"`

	CustomModelOption string `toml:"custom_model_option"` // extra entry in /model picker

	// Auth: bearer → ANTHROPIC_AUTH_TOKEN, x-api-key → ANTHROPIC_API_KEY,
	// none → set nothing (inherit your Claude login).
	Auth         string `toml:"auth"`
	AuthTokenEnv string `toml:"auth_token_env"` // name of env var holding a bearer token
	APIKeyEnv    string `toml:"api_key_env"`    // name of env var holding an API key
	AuthToken    string `toml:"auth_token"`     // literal value (discouraged)
	APIKey       string `toml:"api_key"`        // literal value (discouraged)

	// Upstream OpenAI-compatible provider (only for type=cliproxy translated profiles).
	// When set, ccp materializes an openai-compatibility entry in cliproxy/config.yaml
	// and the profile still points ANTHROPIC_BASE_URL at the local proxy.
	UpstreamBaseURL    string `toml:"upstream_base_url"`
	UpstreamAPIKeyEnv  string `toml:"upstream_api_key_env"`
	UpstreamAPIKey     string `toml:"upstream_api_key"`
	UpstreamName       string `toml:"upstream_name"`        // openai-compatibility[].name override, defaults to profile name
	UpstreamModel      string `toml:"upstream_model"`       // upstream real model name, defaults to Model
	UpstreamModelAlias string `toml:"upstream_model_alias"` // local alias, defaults to Model (or UpstreamModel)
	UpstreamProtocol   string `toml:"upstream_protocol"`    // "chat" (default, OpenAI chat/completions) or "responses" (OpenAI responses API)

	APITimeoutMS                         int  `toml:"api_timeout_ms"`
	MaxThinkingTokens                    int  `toml:"max_thinking_tokens"`
	MaxOutputTokens                      int  `toml:"max_output_tokens"`
	MaxContextTokens                     int  `toml:"max_context_tokens"` // CLAUDE_CODE_MAX_CONTEXT_TOKENS, e.g. 1000000 for 1M
	DisablePromptCaching                 bool `toml:"disable_prompt_caching"`
	DisableUnknownModelWindowEnforcement bool `toml:"disable_unknown_model_window_enforcement"` // CLAUDE_CODE_DISABLE_UNKNOWN_MODEL_WINDOW_ENFORCEMENT=1

	ExtraEnv map[string]string `toml:"extra_env"` // raw passthrough, ${VAR} expanded

	Accounts []Account `toml:"accounts"`
	Routing  *Routing  `toml:"routing"`
}

// ProxyConfig controls CLIProxyAPI lifecycle management.
type ProxyConfig struct {
	Binary           string   `toml:"binary"` // path to cli-proxy-api binary
	ConfigFile       string   `toml:"config"` // its config.yaml
	Host             string   `toml:"host"`
	Port             int      `toml:"port"`
	AutoStart        *bool    `toml:"auto_start"`         // default true
	StartTimeoutSecs int      `toml:"start_timeout_secs"` // default 20
	ExtraArgs        []string `toml:"extra_args"`
}

func (pc *ProxyConfig) autostart() bool {
	return pc.AutoStart == nil || *pc.AutoStart
}

// Config is the fully-loaded ccp configuration.
type Config struct {
	DefaultProfile string
	Proxy          ProxyConfig
	Profiles       map[string]*Profile // name -> profile

	Dir         string // config dir
	ProfilesDir string
}

// ---------------------------------------------------------------------------
// paths
// ---------------------------------------------------------------------------

func ccpConfigDir() string {
	if e := os.Getenv("CCP_HOME"); e != "" {
		return expandPath(e)
	}
	if x := os.Getenv("XDG_CONFIG_HOME"); x != "" {
		return filepath.Join(expandPath(x), "ccp")
	}
	return filepath.Join(homeDir(), ".config", "ccp")
}

func ccpStateDir() string {
	if e := os.Getenv("CCP_STATE_HOME"); e != "" {
		return expandPath(e)
	}
	if x := os.Getenv("XDG_STATE_HOME"); x != "" {
		return filepath.Join(expandPath(x), "ccp")
	}
	return filepath.Join(homeDir(), ".local", "state", "ccp")
}

func proxyPidPath() string { return filepath.Join(ccpStateDir(), "cliproxy.pid") }
func proxyLogPath() string { return filepath.Join(ccpStateDir(), "cliproxy.log") }

func (c *Config) proxyConfigFile() string {
	if c.Proxy.ConfigFile != "" {
		return expandPath(c.Proxy.ConfigFile)
	}
	return filepath.Join(c.Dir, "cliproxy", "config.yaml")
}

func (c *Config) defaultProfileName() string {
	if c.DefaultProfile != "" {
		return c.DefaultProfile
	}
	names := c.ProfileNames()
	if len(names) == 1 {
		return names[0]
	}
	return ""
}

// ---------------------------------------------------------------------------
// loading
// ---------------------------------------------------------------------------

type fileConfig struct {
	DefaultProfile string              `toml:"default_profile"`
	Proxy          ProxyConfig         `toml:"proxy"`
	Profiles       map[string]*Profile `toml:"profiles"`
}

func loadConfig() (*Config, error) {
	dir := ccpConfigDir()
	cfg := &Config{
		Dir:         dir,
		ProfilesDir: filepath.Join(dir, "profiles"),
		Profiles:    map[string]*Profile{},
		Proxy:       ProxyConfig{Host: "127.0.0.1", Port: 8317, StartTimeoutSecs: 20},
	}

	path := filepath.Join(dir, "config.toml")
	var fc fileConfig
	if fileExists(path) {
		md, err := toml.DecodeFile(path, &fc)
		if err != nil {
			return nil, fmt.Errorf("parsing %s: %w", path, err)
		}
		if undecoded := md.Undecoded(); len(undecoded) > 0 {
			var keys []string
			for _, k := range undecoded {
				keys = append(keys, k.String())
			}
			warnf("unknown keys in %s: %s", path, strings.Join(keys, ", "))
		}
	} else if err := bootstrap(dir); err != nil {
		return nil, err
	}

	cfg.DefaultProfile = fc.DefaultProfile
	cfg.Proxy = fc.Proxy
	if cfg.Proxy.Host == "" {
		cfg.Proxy.Host = "127.0.0.1"
	}
	if cfg.Proxy.Port == 0 {
		cfg.Proxy.Port = 8317
	}
	if cfg.Proxy.StartTimeoutSecs == 0 {
		cfg.Proxy.StartTimeoutSecs = 20
	}
	// profiles embedded in config.toml
	for name, p := range fc.Profiles {
		p.normalize()
		if err := p.validatePool(); err != nil {
			warnf("profile %q: %v", name, err)
		}
		cfg.Profiles[name] = p
	}
	// one-file-per-profile directory wins on collisions
	entries, err := os.ReadDir(cfg.ProfilesDir)
	if err == nil {
		for _, e := range entries {
			name := strings.TrimSuffix(e.Name(), ".toml")
			if e.IsDir() || !strings.HasSuffix(e.Name(), ".toml") || name == "" || !safeName(name) {
				continue
			}
			var p Profile
			if _, err := toml.DecodeFile(filepath.Join(cfg.ProfilesDir, e.Name()), &p); err != nil {
				warnf("skipping %s: %v", e.Name(), err)
				continue
			}
			p.normalize()
			if err := p.validatePool(); err != nil {
				warnf("profile %q (%s): %v", name, e.Name(), err)
			}
			if _, exists := cfg.Profiles[name]; exists {
				infof("profiles/%s.toml overrides [profiles.%s] from config.toml", name, name)
			}
			cfg.Profiles[name] = &p
		}
	}
	return cfg, nil
}

func mustLoadConfig() *Config {
	cfg, err := loadConfig()
	if err != nil {
		die("%v", err)
	}
	return cfg
}

func (p *Profile) normalize() {
	p.Type = strings.ToLower(strings.TrimSpace(p.Type))
	if p.Type == "" {
		p.Type = "anthropic"
	}
	p.Auth = strings.ToLower(strings.TrimSpace(p.Auth))
	if p.Routing != nil {
		p.Routing.Strategy = strings.ToLower(strings.TrimSpace(p.Routing.Strategy))
		if p.Routing.Strategy == "" {
			p.Routing.Strategy = "round-robin"
		}
	}
	// Upstream fields: trim spaces; base URL trimmed of trailing slash for stable comparison.
	p.UpstreamBaseURL = strings.TrimSpace(p.UpstreamBaseURL)
	p.UpstreamBaseURL = strings.TrimRight(p.UpstreamBaseURL, "/")
	p.UpstreamAPIKeyEnv = strings.TrimSpace(p.UpstreamAPIKeyEnv)
	p.UpstreamAPIKey = strings.TrimSpace(p.UpstreamAPIKey)
	p.UpstreamName = strings.TrimSpace(p.UpstreamName)
	p.UpstreamModel = strings.TrimSpace(p.UpstreamModel)
	p.UpstreamModelAlias = strings.TrimSpace(p.UpstreamModelAlias)
	p.UpstreamProtocol = strings.ToLower(strings.TrimSpace(p.UpstreamProtocol))
	if p.UpstreamProtocol != "" && p.UpstreamProtocol != "chat" && p.UpstreamProtocol != "responses" && p.UpstreamProtocol != "openai" && p.UpstreamProtocol != "openai-responses" {
		warnf("profile %q: unknown upstream_protocol %q (allowed: chat, responses)", "profile", p.UpstreamProtocol)
	}
	if p.UpstreamProtocol == "openai" {
		p.UpstreamProtocol = "chat"
	}
	if p.UpstreamProtocol == "openai-responses" {
		p.UpstreamProtocol = "responses"
	}
	for i := range p.Accounts {
		a := &p.Accounts[i]
		a.Auth = strings.ToLower(strings.TrimSpace(a.Auth))
		a.Name = strings.TrimSpace(a.Name)
		a.Description = strings.TrimSpace(a.Description)
		a.UpstreamBaseURL = strings.TrimSpace(a.UpstreamBaseURL)
		a.UpstreamBaseURL = strings.TrimRight(a.UpstreamBaseURL, "/")
		a.UpstreamAPIKeyEnv = strings.TrimSpace(a.UpstreamAPIKeyEnv)
		a.UpstreamAPIKey = strings.TrimSpace(a.UpstreamAPIKey)
		a.UpstreamName = strings.TrimSpace(a.UpstreamName)
		a.UpstreamModel = strings.TrimSpace(a.UpstreamModel)
		a.UpstreamModelAlias = strings.TrimSpace(a.UpstreamModelAlias)
		a.UpstreamProtocol = strings.ToLower(strings.TrimSpace(a.UpstreamProtocol))
		if a.UpstreamProtocol == "openai" {
			a.UpstreamProtocol = "chat"
		}
		if a.UpstreamProtocol == "openai-responses" {
			a.UpstreamProtocol = "responses"
		}
	}
	if len(p.Accounts) > 0 && p.hasTopLevelAuth() {
		warnf("profile has both top-level auth and [[accounts]] pool; pool wins and top-level auth is ignored")
	}
	if p.HasUpstream() && p.Type != "cliproxy" {
		warnf("profile %q has upstream_* fields but type=%q (only cliproxy supports upstream translation)", "profile", p.Type)
	}
}
func (p *Profile) hasTopLevelAuth() bool {
	return p.AuthTokenEnv != "" || p.APIKeyEnv != "" || p.AuthToken != "" || p.APIKey != "" || p.Auth != ""
}

func (p *Profile) HasUpstream() bool {
	return p.UpstreamBaseURL != "" || p.UpstreamAPIKeyEnv != "" || p.UpstreamAPIKey != "" || p.UpstreamModel != "" || p.UpstreamName != "" || p.UpstreamModelAlias != "" || p.UpstreamProtocol != ""
}

func (p *Profile) HasUpstreamAuth() bool {
	return p.UpstreamAPIKeyEnv != "" || p.UpstreamAPIKey != ""
}

func (a *Account) HasUpstream() bool {
	return a.UpstreamBaseURL != "" || a.UpstreamAPIKeyEnv != "" || a.UpstreamAPIKey != "" || a.UpstreamModel != "" || a.UpstreamName != "" || a.UpstreamModelAlias != "" || a.UpstreamProtocol != ""
}

func (p *Profile) UpstreamProtocolNormalized() string {
	if p.UpstreamProtocol == "responses" {
		return "responses"
	}
	return "chat"
}

func (a *Account) UpstreamProtocolNormalized() string {
	if a.UpstreamProtocol == "responses" {
		return "responses"
	}
	return "chat"
}

func (p *Profile) IsUpstreamResponses() bool {
	if p.UpstreamProtocol == "responses" {
		return true
	}
	for _, a := range p.Accounts {
		if a.UpstreamProtocol == "responses" {
			return true
		}
	}
	return false
}

func (a *Account) HasUpstreamAuth() bool {
	return a.UpstreamAPIKeyEnv != "" || a.UpstreamAPIKey != ""
}

func (p *Profile) routingStrategy() string {
	if p.Routing == nil || p.Routing.Strategy == "" {
		return "round-robin"
	}
	return p.Routing.Strategy
}

func (p *Profile) isPooled() bool { return len(p.Accounts) > 0 }
func (p *Profile) validatePool() error {
	// Upstream validation for top-level profile (even without pool).
	if p.HasUpstream() {
		if p.Type != "cliproxy" {
			return fmt.Errorf("upstream_* fields require type=\"cliproxy\" (got %q)", p.Type)
		}
		if p.UpstreamBaseURL == "" {
			return fmt.Errorf("upstream_base_url is required when any upstream_* field is set")
		}
		if err := validateUpstreamURL(p.UpstreamBaseURL); err != nil {
			return fmt.Errorf("upstream_base_url %q: %w", p.UpstreamBaseURL, err)
		}
		if !p.HasUpstreamAuth() {
			return fmt.Errorf("upstream auth required: set upstream_api_key_env or upstream_api_key")
		}
		if p.UpstreamName != "" && !safeName(p.UpstreamName) {
			return fmt.Errorf("upstream_name %q is invalid (allowed: [a-z0-9._-])", p.UpstreamName)
		}
	}
	if !p.isPooled() {
		return nil
	}
	strat := p.routingStrategy()
	if strat != "round-robin" {
		return fmt.Errorf("unknown routing.strategy %q (supported: \"round-robin\")", strat)
	}
	// Upstream mixed-pool check: either all accounts have upstream or none do.
	if p.HasUpstream() || anyAccountHasUpstream(p.Accounts) {
		upCount := 0
		for _, a := range p.Accounts {
			if a.HasUpstream() {
				upCount++
			}
		}
		if upCount != 0 && upCount != len(p.Accounts) {
			return fmt.Errorf("mixed upstream accounts: %d/%d accounts have upstream_* fields; either all or none must declare upstream", upCount, len(p.Accounts))
		}
		// If top-level has upstream but accounts override, ensure consistency handled via upCount check;
		// also validate each upstream account.
	}
	for i, a := range p.Accounts {
		if a.Name != "" && !safeName(a.Name) {
			return fmt.Errorf("accounts[%d] name %q is invalid (allowed: [a-z0-9._-])", i, a.Name)
		}
		if a.HasUpstream() {
			if a.UpstreamBaseURL == "" && p.UpstreamBaseURL == "" {
				return fmt.Errorf("accounts[%d] upstream_base_url is required when upstream_* fields are set (no profile default)", i)
			}
			if a.UpstreamBaseURL != "" {
				if err := validateUpstreamURL(a.UpstreamBaseURL); err != nil {
					return fmt.Errorf("accounts[%d] upstream_base_url %q: %w", i, a.UpstreamBaseURL, err)
				}
			}
			// Require per-account upstream auth if profile doesn't have it (or even if it does, allow override missing to inherit).
			// For strict mixed-pool we already ensured all have upstream, but allow account to inherit auth from profile default.
			hasAuth := a.HasUpstreamAuth() || p.HasUpstreamAuth()
			if !hasAuth {
				return fmt.Errorf("accounts[%d] upstream auth required: set upstream_api_key_env or upstream_api_key (profile or account)", i)
			}
			if a.UpstreamName != "" && !safeName(a.UpstreamName) {
				return fmt.Errorf("accounts[%d] upstream_name %q is invalid (allowed: [a-z0-9._-])", i, a.UpstreamName)
			}
		}
		if a.AuthTokenEnv == "" && a.APIKeyEnv == "" && a.AuthToken == "" && a.APIKey == "" && a.Auth == "none" {
			// auth=none is explicitly allowed as an account that injects no credential
			continue
		}
		if a.AuthTokenEnv == "" && a.APIKeyEnv == "" && a.AuthToken == "" && a.APIKey == "" {
			// For cliproxy, empty auth can be valid (reuse proxy api-keys[0]) but only if profile type is cliproxy;
			// we defer that check to resolve time. For anthropic, empty auth without auth=none is not a credential.
			// Allow it here and let resolveAuth decide; but warn if anthropic type with no auth.
			if p.Type != "cliproxy" {
				return fmt.Errorf("accounts[%d] has no auth source (set auth_token_env, api_key_env, auth_token, api_key, or auth=\"none\")", i)
			}
		}
	}
	return nil
}

func validateUpstreamURL(raw string) error {
	u, err := url.Parse(raw)
	if err != nil {
		return err
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return fmt.Errorf("must start with http:// or https://")
	}
	if u.Host == "" {
		return fmt.Errorf("missing host")
	}
	return nil
}

func anyAccountHasUpstream(accounts []Account) bool {
	for _, a := range accounts {
		if a.HasUpstream() {
			return true
		}
	}
	return false
}

func safeName(n string) bool {
	if n == "" {
		return false
	}
	for _, r := range n {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '-', r == '_', r == '.':
		default:
			return false
		}
	}
	return true
}

func (c *Config) ProfileNames() []string {
	var names []string
	for n := range c.Profiles {
		names = append(names, n)
	}
	sort.Strings(names)
	return names
}

// resolveProfile looks up a profile with friendly error output.
func (c *Config) resolveProfile(name string) (string, *Profile) {
	if p, ok := c.Profiles[name]; ok {
		return name, p
	}
	names := c.ProfileNames()
	die("no such profile %q; available: %s%s",
		name, strings.Join(names, ", "),
		orDefaultHint(c.defaultProfileName()))
	return "", nil
}

func orDefaultHint(def string) string {
	if def != "" {
		return fmt.Sprintf(" (default: %q)", def)
	}
	return ""
}

// ---------------------------------------------------------------------------
// first-run bootstrap
// ---------------------------------------------------------------------------

const configTemplate = `# ccp: Claude Code profile launcher
# Profiles live in profiles/*.toml (one file per profile), or define them
# inline here as [profiles.<name>] tables. Run ` + "`ccp help`" + ` for details.

# Which profile bare ` + "`" + `ccp` + "`" + ` launches.
default_profile = "glm"

[proxy]
# CLIProxyAPI lifecycle settings (used by profiles with type = "cliproxy").
# Leave binary empty to auto-detect: PATH, ~/.local/bin/cli-proxy-api,
# <state>/bin/cli-proxy-api; or run ` + "`ccp proxy install`" + `.
#binary = "~/.local/bin/cli-proxy-api"
config = ""              # default: <this dir>/cliproxy/config.yaml
host = "127.0.0.1"
port = 8317
auto_start = true        # start the proxy when a profile needs it and it is down
start_timeout_secs = 20
#extra_args = []
`

const glmTemplate = `# ` + "`" + `ccp glm` + "`" + `; GLM through local CLIProxyAPI.
# Run ` + "`" + `ccp proxy models` + "`" + ` to list model IDs your proxy actually exposes.
description = "GLM via local CLIProxyAPI"
type = "cliproxy"
model = "glm-4.6"
#haiku_model = "glm-4.5-air"   # cheaper model for background/haiku tasks
api_timeout_ms = 600000
`

const kimiTemplate = `# ` + "`" + `ccp kimi` + "`" + `; Kimi through local CLIProxyAPI.
# Run ` + "`" + `ccp proxy models` + "`" + ` to list model IDs your proxy actually exposes.
description = "Kimi via local CLIProxyAPI"
type = "cliproxy"
model = "kimi-k3"
api_timeout_ms = 600000
`

const officialTemplate = `# ` + "`" + `ccp official` + "`" + `; vanilla Claude Code with your Anthropic login.
description = "Official Anthropic (vanilla Claude Code)"
type = "anthropic"
auth = "none"
# ccp strips all managed variables below so your normal login/settings apply.
`

func writeFileIfMissing(path, content string, perm os.FileMode) (bool, error) {
	if fileExists(path) {
		return false, nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return false, err
	}
	if err := os.WriteFile(path, []byte(content), perm); err != nil {
		return false, err
	}
	return true, nil
}

// bootstrap creates config dirs, a starter config.toml and seed profiles on
// first run. It never overwrites existing files.
func bootstrap(dir string) error {
	stateDir := ccpStateDir()
	for _, d := range []string{
		dir,
		filepath.Join(dir, "profiles"),
		filepath.Join(dir, "cliproxy"),
		stateDir,
		filepath.Join(stateDir, "bin"),
	} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			return fmt.Errorf("creating %s: %w", d, err)
		}
	}
	created, err := writeFileIfMissing(filepath.Join(dir, "config.toml"), configTemplate, 0o600)
	if err != nil {
		return err
	}
	seeds := map[string]string{
		"glm":      glmTemplate,
		"kimi":     kimiTemplate,
		"official": officialTemplate,
	}
	var names []string
	for name, body := range seeds {
		ok, err := writeFileIfMissing(filepath.Join(dir, "profiles", name+".toml"), body, 0o600)
		if err != nil {
			return err
		}
		if ok {
			names = append(names, name)
		}
	}
	if created || len(names) > 0 {
		sort.Strings(names)
		infof("initialized %s %s", paint(cDim, dir),
			paint(cDim, "(created config.toml"+profileSeedNote(names)+")"))
	}
	return nil
}

func profileSeedNote(names []string) string {
	if len(names) == 0 {
		return ""
	}
	return " + profiles/" + strings.Join(names, ", ") + ".toml"
}

// ---------------------------------------------------------------------------
// helpers delegating to util (to keep original lower-case calls working)
// ---------------------------------------------------------------------------

func homeDir() string               { return util.HomeDir() }
func expandPath(p string) string    { return util.ExpandPath(p) }
func fileExists(p string) bool      { return util.FileExists(p) }
func warnf(format string, a ...any) { util.Warnf(format, a...) }
func infof(format string, a ...any) { util.Infof(format, a...) }
func die(format string, a ...any)   { util.Die(format, a...) }
func paint(code, s string) string   { return util.Paint(code, s) }

const (
	cDim = util.CDim
)

// ---------------------------------------------------------------------------
// exported API (wrappers for external packages & tests)
// ---------------------------------------------------------------------------

func CcpConfigDir() string         { return ccpConfigDir() }
func CcpStateDir() string          { return ccpStateDir() }
func ProxyPidPath() string         { return proxyPidPath() }
func ProxyLogPath() string         { return proxyLogPath() }
func LoadConfig() (*Config, error) { return loadConfig() }
func MustLoadConfig() *Config      { return mustLoadConfig() }
func SafeName(n string) bool       { return safeName(n) }

func (c *Config) ProxyConfigFile() string                       { return c.proxyConfigFile() }
func (c *Config) DefaultProfileName() string                    { return c.defaultProfileName() }
func (c *Config) ResolveProfile(name string) (string, *Profile) { return c.resolveProfile(name) }

func (p *Profile) Normalize()               { p.normalize() }
func (p *Profile) HasTopLevelAuth() bool    { return p.hasTopLevelAuth() }
func (p *Profile) RoutingStrategy() string  { return p.routingStrategy() }
func (p *Profile) IsPooled() bool           { return p.isPooled() }
func (p *Profile) ValidatePool() error      { return p.validatePool() }
func (a *Account) HasUpstreamAccount() bool { return a.HasUpstream() }

func (pc *ProxyConfig) Autostart() bool { return pc.autostart() }

// Exported aliases for constants/templates if needed
