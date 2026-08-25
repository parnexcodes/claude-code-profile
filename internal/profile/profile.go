package profile

import (
	"bytes"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"

	"ccp/internal/config"
	"ccp/internal/routing"
	"ccp/internal/settings"
	"ccp/internal/util"
)

// ---------------------------------------------------------------------------
// managed environment variables
// ---------------------------------------------------------------------------

var modelAliasVars = []string{
	"ANTHROPIC_DEFAULT_OPUS_MODEL",
	"ANTHROPIC_DEFAULT_SONNET_MODEL",
	"ANTHROPIC_DEFAULT_HAIKU_MODEL",
	"ANTHROPIC_DEFAULT_FABLE_MODEL",
}

var ManagedVars = func() []string {
	vars := []string{
		"ANTHROPIC_API_KEY",
		"ANTHROPIC_AUTH_TOKEN",
		"ANTHROPIC_BASE_URL",
		"ANTHROPIC_MODEL",
		"ANTHROPIC_DEFAULT_MODEL",
		"ANTHROPIC_SMALL_FAST_MODEL",
		"ANTHROPIC_CUSTOM_HEADERS",
		"ANTHROPIC_CUSTOM_MODEL_OPTION",
		"CLAUDE_CODE_SUBAGENT_MODEL",
		"API_TIMEOUT_MS",
		"MAX_THINKING_TOKENS",
		"CLAUDE_CODE_MAX_OUTPUT_TOKENS",
		"CLAUDE_CODE_MAX_CONTEXT_TOKENS",
		"CLAUDE_CODE_DISABLE_UNKNOWN_MODEL_WINDOW_ENFORCEMENT",
		"DISABLE_PROMPT_CACHING",
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

type AuthResult struct {
	EnvVar string
	Value  string
	Source string
}

func accountAuthMode(a *config.Account, profileType string) string {
	if a.Auth != "" {
		return a.Auth
	}
	if profileType == "cliproxy" {
		return "bearer"
	}
	return "x-api-key"
}

func ResolveAccountAuth(a *config.Account, cfg *config.Config, profileType string) (*AuthResult, error) {
	if a != nil && a.UpstreamProtocolNormalized() == "responses" {
		keys := ReadProxyAPIKeys(cfg)
		if len(keys) > 0 {
			return &AuthResult{"ANTHROPIC_AUTH_TOKEN", keys[0], "shim (proxy api-keys[0])"}, nil
		}
		return &AuthResult{"ANTHROPIC_AUTH_TOKEN", "shim-dummy-token", "shim dummy"}, nil
	}
	mode := accountAuthMode(a, profileType)
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
	case a.AuthTokenEnv != "":
		v, err := getEnvRef(a.AuthTokenEnv, "auth_token_env")
		if err != nil {
			return nil, err
		}
		return &AuthResult{"ANTHROPIC_AUTH_TOKEN", v, "$" + a.AuthTokenEnv}, nil
	case a.APIKeyEnv != "":
		v, err := getEnvRef(a.APIKeyEnv, "api_key_env")
		if err != nil {
			return nil, err
		}
		return &AuthResult{"ANTHROPIC_API_KEY", v, "$" + a.APIKeyEnv}, nil
	case a.AuthToken != "":
		return &AuthResult{"ANTHROPIC_AUTH_TOKEN", util.ExpandEnvVars(a.AuthToken), "auth_token (config)"}, nil
	case a.APIKey != "":
		return &AuthResult{"ANTHROPIC_API_KEY", util.ExpandEnvVars(a.APIKey), "api_key (config)"}, nil
	}
	if mode == "bearer" {
		keys := ReadProxyAPIKeys(cfg)
		if len(keys) > 0 {
			return &AuthResult{"ANTHROPIC_AUTH_TOKEN", keys[0], "proxy config api-keys[0]"}, nil
		}
		return nil, nil
	}
	return nil, nil
}

func profileAuthMode(p *config.Profile) string {
	if p.Auth != "" {
		return p.Auth
	}
	if p.Type == "cliproxy" {
		return "bearer"
	}
	return "x-api-key"
}

func ResolveProfileAuth(p *config.Profile, cfg *config.Config) (*AuthResult, error) {
	if p.IsUpstreamResponses() {
		keys := ReadProxyAPIKeys(cfg)
		if len(keys) > 0 {
			return &AuthResult{"ANTHROPIC_AUTH_TOKEN", keys[0], "shim (proxy api-keys[0])"}, nil
		}
		return &AuthResult{"ANTHROPIC_AUTH_TOKEN", "shim-dummy-token", "shim dummy"}, nil
	}
	mode := profileAuthMode(p)
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
		return &AuthResult{"ANTHROPIC_AUTH_TOKEN", v, "$" + p.AuthTokenEnv}, nil
	case p.APIKeyEnv != "":
		v, err := getEnvRef(p.APIKeyEnv, "api_key_env")
		if err != nil {
			return nil, err
		}
		return &AuthResult{"ANTHROPIC_API_KEY", v, "$" + p.APIKeyEnv}, nil
	case p.AuthToken != "":
		return &AuthResult{"ANTHROPIC_AUTH_TOKEN", util.ExpandEnvVars(p.AuthToken), "auth_token (config)"}, nil
	case p.APIKey != "":
		return &AuthResult{"ANTHROPIC_API_KEY", util.ExpandEnvVars(p.APIKey), "api_key (config)"}, nil
	}
	if mode == "bearer" {
		keys := ReadProxyAPIKeys(cfg)
		if len(keys) > 0 {
			return &AuthResult{"ANTHROPIC_AUTH_TOKEN", keys[0], "proxy config api-keys[0]"}, nil
		}
		return nil, nil
	}
	return nil, nil
}

// proxyYAML for reading api-keys
type proxyYAML struct {
	APIKeys []string `yaml:"api-keys"`
	Port    int      `yaml:"port"`
	AuthDir string   `yaml:"auth-dir"`
}

func unmarshalYAML(data []byte, out interface{}) error {
	if err := yaml.Unmarshal(data, out); err == nil {
		return nil
	}
	fixed := bytes.ReplaceAll(data, []byte("\\"), []byte("/"))
	if err2 := yaml.Unmarshal(fixed, out); err2 == nil {
		return nil
	}
	return yaml.Unmarshal(data, out)
}

func readProxyConfigFile(cfg *config.Config) *proxyYAML {
	data, err := os.ReadFile(cfg.ProxyConfigFile())
	if err != nil {
		return nil
	}
	var y proxyYAML
	if err := unmarshalYAML(data, &y); err != nil {
		util.Warnf("cannot parse %s: %v", cfg.ProxyConfigFile(), err)
		return nil
	}
	return &y
}

func ReadProxyAPIKeys(cfg *config.Config) []string {
	y := readProxyConfigFile(cfg)
	if y == nil {
		return nil
	}
	return y.APIKeys
}

// ---------------------------------------------------------------------------
// endpoint + env assembly
// ---------------------------------------------------------------------------

func EffectiveBaseURL(p *config.Profile, cfg *config.Config) string {
	if p.BaseURL != "" {
		return strings.TrimRight(util.ExpandEnvVars(p.BaseURL), "/")
	}
	if p.IsUpstreamResponses() {
		host := "127.0.0.1"
		port := 8318
		if h := os.Getenv("CCP_SHIM_HOST"); h != "" {
			host = h
		}
		if pp := os.Getenv("CCP_SHIM_PORT"); pp != "" {
			if n, err := strconv.Atoi(pp); err == nil && n > 0 {
				port = n
			}
		}
		return fmt.Sprintf("http://%s", net.JoinHostPort(host, strconv.Itoa(port)))
	}
	if p.Type == "cliproxy" {
		return fmt.Sprintf("http://%s:%d", cfg.Proxy.Host, cfg.Proxy.Port)
	}
	return ""
}

func EffectiveBaseURLForAccount(p *config.Profile, cfg *config.Config, a *config.Account) string {
	if a != nil && a.BaseURL != "" {
		return strings.TrimRight(util.ExpandEnvVars(a.BaseURL), "/")
	}
	return EffectiveBaseURL(p, cfg)
}

func SelectAccountForLaunch(p *config.Profile, name string) (int, *config.Account, error) {
	if !p.IsPooled() {
		return -1, nil, nil
	}
	if err := p.ValidatePool(); err != nil {
		return 0, nil, err
	}
	idx, err := routing.NextRoutingIndex(name, len(p.Accounts))
	if err != nil {
		return 0, nil, err
	}
	if idx < 0 || idx >= len(p.Accounts) {
		idx = 0
	}
	return idx, &p.Accounts[idx], nil
}

func SelectAccountForPeek(p *config.Profile, name string) (int, *config.Account, error) {
	if !p.IsPooled() {
		return -1, nil, nil
	}
	if err := p.ValidatePool(); err != nil {
		return 0, nil, err
	}
	idx := routing.PeekRoutingIndex(name, len(p.Accounts))
	if idx < 0 || idx >= len(p.Accounts) {
		idx = 0
	}
	return idx, &p.Accounts[idx], nil
}

func InheritModel() (string, bool) {
	s, err := settings.ReadClaudeSettings(filepath.Join(util.HomeDir(), ".claude", "settings.json"))
	if err != nil || s == nil || s.Model == "" {
		return "", false
	}
	return s.Model, true
}

type BuiltEnv struct {
	Sets   map[string]string
	Strips []string
	Notes  []string
	Model  string
	URL    string

	PoolSize          int
	SelectedIdx       int
	SelectedAccount   *config.Account
	AccountAuthSource string
}

func BuildEnv(cfg *config.Config, name string, p *config.Profile) (*BuiltEnv, error) {
	idx, acct, err := SelectAccountForLaunch(p, name)
	if err != nil {
		return nil, fmt.Errorf("profile %s: %w", name, err)
	}
	return BuildEnvWithAccount(cfg, name, p, idx, acct)
}

func BuildEnvPeek(cfg *config.Config, name string, p *config.Profile) (*BuiltEnv, error) {
	idx, acct, err := SelectAccountForPeek(p, name)
	if err != nil {
		return nil, fmt.Errorf("profile %s: %w", name, err)
	}
	return BuildEnvWithAccount(cfg, name, p, idx, acct)
}

func BuildEnvWithAccount(cfg *config.Config, name string, p *config.Profile, selectedIdx int, selected *config.Account) (*BuiltEnv, error) {
	b := &BuiltEnv{Sets: map[string]string{}}
	if p.IsPooled() {
		b.PoolSize = len(p.Accounts)
		b.SelectedIdx = selectedIdx
		b.SelectedAccount = selected
	} else {
		b.SelectedIdx = -1
	}

	strip := map[string]bool{}
	for _, v := range ManagedVars {
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

	if p.IsPooled() {
		b.URL = EffectiveBaseURLForAccount(p, cfg, selected)
	} else {
		b.URL = EffectiveBaseURL(p, cfg)
	}
	if b.URL != "" {
		b.Sets["ANTHROPIC_BASE_URL"] = b.URL
		b.Notes = append(b.Notes, b.URL)
	} else {
		b.Notes = append(b.Notes, "api.anthropic.com")
	}

	if p.IsPooled() {
		auth, err := ResolveAccountAuth(selected, cfg, p.Type)
		if err != nil {
			return nil, fmt.Errorf("profile %s accounts[%d]: %w", name, selectedIdx, err)
		}
		if auth != nil {
			b.Sets[auth.EnvVar] = auth.Value
			b.AccountAuthSource = auth.Source
			b.Notes = append(b.Notes, "auth "+util.Paint(util.CDim, auth.Source))
			b.Notes = append(b.Notes, fmt.Sprintf("account %d/%d (%s)", selectedIdx+1, b.PoolSize, auth.Source))
		}
	} else {
		auth, err := ResolveProfileAuth(p, cfg)
		if err != nil {
			return nil, fmt.Errorf("profile %s: %w", name, err)
		}
		if auth != nil {
			b.Sets[auth.EnvVar] = auth.Value
			b.Notes = append(b.Notes, "auth "+util.Paint(util.CDim, auth.Source))
		}
	}

	model := p.Model
	if model == "" {
		if inherited, ok := InheritModel(); ok {
			model = inherited
			b.Notes = append(b.Notes, fmt.Sprintf("model %s (inherited from ~/.claude/settings.json)", util.Paint(util.CBold, inherited)))
		} else {
			b.Notes = append(b.Notes, fmt.Sprintf("model %s (claude default)", util.Paint(util.CDim, "auto")))
		}
	} else {
		b.Notes = append(b.Notes, "model "+util.Paint(util.CBold, model))
	}
	b.Model = model

	if model != "" {
		b.Sets["ANTHROPIC_MODEL"] = model
		b.Sets["ANTHROPIC_DEFAULT_OPUS_MODEL"] = pick(p.OpusModel, model)
		b.Sets["ANTHROPIC_DEFAULT_SONNET_MODEL"] = pick(p.SonnetModel, model)
		b.Sets["ANTHROPIC_DEFAULT_HAIKU_MODEL"] = pick(p.HaikuModel, model)
		if p.FableModel != "" {
			b.Sets["ANTHROPIC_DEFAULT_FABLE_MODEL"] = p.FableModel
		}
		b.Sets["CLAUDE_CODE_SUBAGENT_MODEL"] = pick(p.SubagentModel, model)
	}

	if p.CustomModelOption != "" {
		id := util.ExpandEnvVars(p.CustomModelOption)
		b.Sets["ANTHROPIC_CUSTOM_MODEL_OPTION"] = id
		b.Sets["ANTHROPIC_CUSTOM_MODEL_OPTION_NAME"] = name
		b.Sets["ANTHROPIC_CUSTOM_MODEL_OPTION_DESCRIPTION"] =
			fmt.Sprintf("ccp profile %q (%s)", name, id)
	}

	if p.APITimeoutMS > 0 {
		b.Sets["API_TIMEOUT_MS"] = fmt.Sprint(p.APITimeoutMS)
	}
	if p.MaxThinkingTokens > 0 {
		b.Sets["MAX_THINKING_TOKENS"] = fmt.Sprint(p.MaxThinkingTokens)
	}
	if p.MaxOutputTokens > 0 {
		b.Sets["CLAUDE_CODE_MAX_OUTPUT_TOKENS"] = fmt.Sprint(p.MaxOutputTokens)
	}
	if p.MaxContextTokens > 0 {
		b.Sets["CLAUDE_CODE_MAX_CONTEXT_TOKENS"] = fmt.Sprint(p.MaxContextTokens)
	}
	if p.DisablePromptCaching {
		b.Sets["DISABLE_PROMPT_CACHING"] = "1"
	}
	if p.DisableUnknownModelWindowEnforcement {
		b.Sets["CLAUDE_CODE_DISABLE_UNKNOWN_MODEL_WINDOW_ENFORCEMENT"] = "1"
	}

	for k, v := range p.ExtraEnv {
		b.Sets[k] = util.ExpandEnvVars(v)
	}

	return b, nil
}

func pick(a, fallback string) string {
	if a != "" {
		return a
	}
	return fallback
}

func AssembleEnv(environ []string, strips []string, sets map[string]string) []string {
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
