package proxy

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"ccp/internal/config"
	"ccp/internal/util"
	"compress/gzip"
	"encoding/json"
	"fmt"
	"gopkg.in/yaml.v3"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"
)

// ---------------------------------------------------------------------------
// lifecycle primitives
// ---------------------------------------------------------------------------

func proxyBaseURL(cfg *config.Config) string {
	return fmt.Sprintf("http://%s:%d", cfg.Proxy.Host, cfg.Proxy.Port)
}

func proxyReachable(cfg *config.Config) bool {
	return probeURL(proxyBaseURL(cfg)+"/", 500*time.Millisecond) == nil
}

func pidFromFile() (int, bool) {
	data, err := os.ReadFile(proxyPidPath())
	if err != nil {
		return 0, false
	}
	var pid int
	if _, err := fmt.Sscanf(strings.TrimSpace(string(data)), "%d", &pid); err != nil || pid <= 0 {
		return 0, false
	}
	return pid, procCmdline(pid) != ""
}

func writePidFile(pid int) error {
	if err := os.MkdirAll(filepath.Dir(proxyPidPath()), 0o700); err != nil {
		return err
	}
	return os.WriteFile(proxyPidPath(), []byte(fmt.Sprintf("%d\n", pid)), 0o600)
}

// findProxyBinary resolves the cli-proxy-api executable.
func findProxyBinary(cfg *config.Config) string {
	if cfg.Proxy.Binary != "" {
		p := expandPath(cfg.Proxy.Binary)
		if isExecutable(p) {
			return p
		}
	}
	if p := lookPathAll("cli-proxy-api", "CLIProxyAPI", "cli-proxy-api64"); p != "" {
		return p
	}
	for _, cand := range []string{
		filepath.Join(homeDir(), ".local", "bin", "cli-proxy-api"),
		filepath.Join(homeDir(), "cliproxyapi", "cli-proxy-api"),
		filepath.Join(ccpStateDir(), "bin", "cli-proxy-api"),
	} {
		if isExecutable(cand) {
			return cand
		}
	}
	return ""
}

func startProxy(cfg *config.Config) error {
	if proxyReachable(cfg) {
		return nil // already up
	}
	bin := findProxyBinary(cfg)
	if bin == "" {
		return fmt.Errorf("CLIProxyAPI binary not found; run %s or set binary = \"...\" under [proxy]",
			paint(cBold, "ccp proxy install"))
	}

	cfgFile := cfg.ProxyConfigFile()
	if !fileExists(cfgFile) && cfg.Proxy.ConfigFile == "" {
		scaffoldProxyConfig(cfgFile)
		infof("scaffolded starter proxy config at %s", paint(cBold, cfgFile))
		infof("add OAuth accounts with the CLIProxyAPI login flow; see https://help.router-for.me/")
	}

	logFile := proxyLogPath()
	if err := os.MkdirAll(filepath.Dir(logFile), 0o700); err != nil {
		return err
	}
	log, err := os.OpenFile(logFile, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	defer closeQuietly(log)

	args := append([]string{"--config", cfgFile}, cfg.Proxy.ExtraArgs...)
	cmd := exec.Command(bin, args...)
	detach(cmd)
	cmd.Stdin = nil
	cmd.Stdout = log
	cmd.Stderr = log

	if err := cmd.Start(); err != nil {
		return fmt.Errorf("starting %s: %w", bin, err)
	}
	pid := cmd.Process.Pid
	_ = writePidFile(pid)

	timeout := time.Duration(cfg.Proxy.StartTimeoutSecs) * time.Second
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if proxyReachable(cfg) {
			okf("proxy up on %s (pid %d)", paint(cBold, proxyBaseURL(cfg)), pid)
			return nil
		}
		// did it die immediately?
		if !procAlive(pid) {
			err := cmd.Wait()
			tail := tailLines(logFile, 12)
			return fmt.Errorf("proxy exited immediately (%v). Last log lines from %s:\n%s",
				err, logFile, indent(tail))
		}
		time.Sleep(200 * time.Millisecond)
	}
	stopQuietly(pid)
	return fmt.Errorf("proxy did not become reachable within %s; check %s",
		timeout, logFile)
}

func stopQuietly(pid int) {
	_ = killProcessGroup(pid)
	for i := 0; i < 50; i++ {
		if !procAlive(pid) {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	_ = forceKillProcessGroup(pid)
}

func stopProxy(cfg *config.Config) error {
	pid, alive := pidFromFile()
	if !alive {
		if proxyReachable(cfg) {
			return fmt.Errorf("%s answers but no valid pid file exists at %s (not started by ccp)?",
				proxyBaseURL(cfg), proxyPidPath())
		}
		return fmt.Errorf("proxy is not running")
	}
	stopQuietly(pid)
	_ = os.Remove(proxyPidPath())
	okf("stopped proxy (pid %d)", pid)
	return nil
}

func scaffoldProxyConfig(path string) {
	key := randHex(24)
	authDir := filepath.Join(homeDir(), ".cli-proxy-api")
	authDir = strings.ReplaceAll(authDir, "\\", "/")
	body := fmt.Sprintf(`# Minimal CLIProxyAPI config scaffolded by ccp.
# Full reference: https://help.router-for.me/
port: 8317
auth-dir: "%s"

# Keys clients must present. ccp reuses api-keys[0] automatically for
# cliproxy profiles unless a profile sets its own auth.
api-keys:
  - "%s"

remote-management:
  allowremote: false
  secret-key: ""
`, authDir, key)
	if _, err := writeFileIfMissing(path, body, 0o600); err != nil {
		die("writing %s: %v", path, err)
	}
}

// unmarshalYAML is tolerant of Windows paths with unescaped backslashes in
// double-quoted strings (e.g., auth-dir: "C:\Users\..."). gopkg.in/yaml.v3
// reports "did not find expected hexdecimal number" for \U escapes. We
// fallback to forward-slash replacement which is safe for this config (only
// paths contain backslashes).
func unmarshalYAML(data []byte, out interface{}) error {
	if err := yaml.Unmarshal(data, out); err == nil {
		return nil
	}
	// Fallback for legacy Windows configs with unescaped backslashes in
	// double-quoted auth-dir (e.g., "C:\Users\..."). Converts to forward
	// slashes which are YAML-safe and valid on Windows.
	fixed := bytes.ReplaceAll(data, []byte("\\"), []byte("/"))
	if err2 := yaml.Unmarshal(fixed, out); err2 == nil {
		return nil
	}
	return yaml.Unmarshal(data, out)
}

// ---------------------------------------------------------------------------
// openai-compatibility upstream management
// ---------------------------------------------------------------------------

type openAICompatAPIKey struct {
	APIKey string `yaml:"api-key"`
}

type openAICompatModel struct {
	Name  string `yaml:"name"`
	Alias string `yaml:"alias"`
}

type openAICompatEntry struct {
	Name          string               `yaml:"name"`
	BaseURL       string               `yaml:"base-url"`
	APIKeyEntries []openAICompatAPIKey `yaml:"api-key-entries"`
	Models        []openAICompatModel  `yaml:"models"`
}

var proxyUpstreamMu sync.Mutex

// upstreamEntryName returns the openai-compatibility entry name for a profile.
func upstreamEntryName(profileName string, p *config.Profile) string {
	if p.UpstreamName != "" {
		return strings.TrimSpace(util.ExpandEnvVars(p.UpstreamName))
	}
	return profileName
}

// resolveUpstreamAPIKey returns the expanded upstream API key for a profile (literal or env var).
func resolveUpstreamAPIKey(p *config.Profile) string {
	if p.UpstreamAPIKeyEnv != "" {
		envName := strings.TrimSpace(p.UpstreamAPIKeyEnv)
		if v := os.Getenv(envName); v != "" {
			return strings.TrimSpace(v)
		}
		// Fallback: try expanding env var name itself (if user set ${VAR} style)
		if expanded := util.ExpandEnvVars(envName); expanded != envName {
			if v := os.Getenv(expanded); v != "" {
				return strings.TrimSpace(v)
			}
		}
		return ""
	}
	if p.UpstreamAPIKey != "" {
		return strings.TrimSpace(util.ExpandEnvVars(p.UpstreamAPIKey))
	}
	return ""
}

func resolveAccountUpstreamAPIKey(a *config.Account, p *config.Profile) string {
	if a.UpstreamAPIKeyEnv != "" {
		if v := os.Getenv(strings.TrimSpace(a.UpstreamAPIKeyEnv)); v != "" {
			return strings.TrimSpace(v)
		}
		return ""
	}
	if a.UpstreamAPIKey != "" {
		return strings.TrimSpace(util.ExpandEnvVars(a.UpstreamAPIKey))
	}
	// Fallback to profile upstream key
	return resolveUpstreamAPIKey(p)
}

func normalizeUpstreamBaseURL(raw string) string {
	s := strings.TrimSpace(util.ExpandEnvVars(raw))
	s = strings.TrimRight(s, "/")
	// If user pasted full /v1/responses or /v1/chat/completions endpoint, strip to /v1 prefix.
	if strings.HasSuffix(s, "/v1/responses") {
		s = strings.TrimSuffix(s, "/responses")
		util.Warnf("upstream base URL %q looks like a full /v1/responses endpoint; normalized to %q", raw, s)
	} else if strings.HasSuffix(s, "/v1/chat/completions") {
		s = strings.TrimSuffix(s, "/chat/completions")
		util.Warnf("upstream base URL %q looks like a full /v1/chat/completions endpoint; normalized to %q", raw, s)
	} else if strings.HasSuffix(s, "/responses") {
		s = strings.TrimSuffix(s, "/responses")
		util.Warnf("upstream base URL %q looks like a /responses endpoint; normalized to %q", raw, s)
	} else if strings.HasSuffix(s, "/chat/completions") {
		s = strings.TrimSuffix(s, "/chat/completions")
		util.Warnf("upstream base URL %q looks like a /chat/completions endpoint; normalized to %q", raw, s)
	}
	return s
}

func validateUpstreamBaseURLForProxy(raw string) error {
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

// SyncOpenAICompat ensures cliproxy/config.yaml contains an openai-compatibility entry
// for the given profile. It is the source of truth; YAML entry is derived. If p.HasUpstream()
// is false, it removes any stale entry.
func SyncOpenAICompat(cfg *config.Config, profileName string, p *config.Profile) error {
	if p.Type != "cliproxy" {
		return nil
	}
	if !p.HasUpstream() {
		// No upstream declared — remove stale entry if present.
		return RemoveOpenAICompat(cfg, profileName)
	}
	if p.IsUpstreamResponses() {
		// Responses upstreams are handled by the shim, not CLIProxyAPI; ensure no stale openai-compatibility entry.
		_ = RemoveOpenAICompat(cfg, profileName)
		return nil
	}
	// Validate upstream fields exist (also validated in config.ValidatePool, but double-check for direct calls).
	rawBase := p.UpstreamBaseURL
	if rawBase == "" && p.IsPooled() {
		// For pooled, profile base may be empty if accounts override; fallback to first account base.
		for _, a := range p.Accounts {
			if a.UpstreamBaseURL != "" {
				rawBase = a.UpstreamBaseURL
				break
			}
		}
	}
	baseURL := normalizeUpstreamBaseURL(rawBase)
	if baseURL == "" {
		return fmt.Errorf("upstream_base_url is required for profile %q", profileName)
	}
	if err := validateUpstreamBaseURLForProxy(baseURL); err != nil {
		return fmt.Errorf("profile %q upstream_base_url %q: %w", profileName, baseURL, err)
	}
	entryName := upstreamEntryName(profileName, p)
	var apiKeys []openAICompatAPIKey
	var models []openAICompatModel
	// Determine model mapping.
	upstreamModel := strings.TrimSpace(p.UpstreamModel)
	if upstreamModel == "" {
		upstreamModel = strings.TrimSpace(p.Model)
	}
	alias := strings.TrimSpace(p.UpstreamModelAlias)
	if alias == "" {
		alias = strings.TrimSpace(p.Model)
	}
	if alias == "" {
		alias = upstreamModel
	}
	if upstreamModel == "" {
		upstreamModel = alias
	}
	if p.IsPooled() {
		// Collect api keys from each account; validate base URLs are consistent.
		seenBases := map[string]bool{}
		for i, a := range p.Accounts {
			key := resolveAccountUpstreamAPIKey(&a, p)
			if key == "" {
				return fmt.Errorf("profile %q accounts[%d] upstream auth missing: set upstream_api_key_env or upstream_api_key", profileName, i)
			}
			apiKeys = append(apiKeys, openAICompatAPIKey{APIKey: key})
			ab := strings.TrimSpace(a.UpstreamBaseURL)
			if ab != "" {
				abNorm := normalizeUpstreamBaseURL(ab)
				seenBases[abNorm] = true
			}
		}
		if len(seenBases) > 1 {
			return fmt.Errorf("profile %q pooled upstream_base_url differs across accounts; use a single base URL per profile", profileName)
		}
		// If any account overrides baseURL, prefer that (they are all same per check).
		for ab := range seenBases {
			baseURL = ab
			break
		}
		models = []openAICompatModel{{Name: upstreamModel, Alias: alias}}
	} else {
		key := resolveUpstreamAPIKey(p)
		if key == "" {
			return fmt.Errorf("profile %q upstream auth missing: set upstream_api_key_env or upstream_api_key", profileName)
		}
		apiKeys = []openAICompatAPIKey{{APIKey: key}}
		models = []openAICompatModel{{Name: upstreamModel, Alias: alias}}
	}
	entry := openAICompatEntry{
		Name:          entryName,
		BaseURL:       baseURL,
		APIKeyEntries: apiKeys,
		Models:        models,
	}
	return upsertOpenAICompatEntry(cfg, entry)
}

// RemoveOpenAICompat removes the openai-compatibility entry for the given profile name.
func RemoveOpenAICompat(cfg *config.Config, profileName string) error {
	path := cfg.ProxyConfigFile()
	if !fileExists(path) {
		return nil
	}
	proxyUpstreamMu.Lock()
	defer proxyUpstreamMu.Unlock()
	// Lock via file lock sidecar if possible (best effort).
	unlock := lockProxyFile(path)
	defer unlock()
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	if len(data) == 0 {
		return nil
	}
	// Decode into generic map to preserve unknown keys.
	var fm map[string]interface{}
	if err := unmarshalYAML(data, &fm); err != nil {
		return fmt.Errorf("parsing %s: %w", path, err)
	}
	if fm == nil {
		fm = map[string]interface{}{}
	}
	raw, ok := fm["openai-compatibility"]
	if !ok {
		return nil
	}
	var list []interface{}
	switch v := raw.(type) {
	case []interface{}:
		list = v
	case []map[string]interface{}:
		for _, m := range v {
			list = append(list, m)
		}
	default:
		// Unexpected type; leave unchanged.
		return nil
	}
	newList := []interface{}{}
	removed := false
	for _, item := range list {
		m, ok := item.(map[string]interface{})
		if !ok {
			// Try generic map
			if mm, ok2 := item.(map[interface{}]interface{}); ok2 {
				// Convert to string-key map
				m = map[string]interface{}{}
				for k, v := range mm {
					if ks, ok := k.(string); ok {
						m[ks] = v
					}
				}
			} else {
				newList = append(newList, item)
				continue
			}
		}
		nameVal, _ := m["name"].(string)
		if nameVal == profileName {
			removed = true
			continue
		}
		newList = append(newList, m)
	}
	if !removed {
		return nil
	}
	if len(newList) == 0 {
		delete(fm, "openai-compatibility")
	} else {
		fm["openai-compatibility"] = newList
	}
	return writeProxyFileAtomically(path, fm)
}

// IsUpstreamSynced checks if the proxy YAML entry for the profile matches the profile's upstream definition.
func IsUpstreamSynced(cfg *config.Config, profileName string, p *config.Profile) (bool, string) {
	if !p.HasUpstream() {
		return true, "no upstream"
	}
	if p.IsUpstreamResponses() {
		return true, "responses upstream (shim)"
	}
	path := cfg.ProxyConfigFile()
	if !fileExists(path) {
		return false, "proxy config missing"
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return false, "cannot read proxy config"
	}
	if len(data) == 0 {
		return false, "proxy config empty"
	}
	var fm map[string]interface{}
	if err := unmarshalYAML(data, &fm); err != nil {
		return false, "proxy config parse error"
	}
	raw, ok := fm["openai-compatibility"]
	if !ok {
		return false, "openai-compatibility missing"
	}
	var list []interface{}
	switch v := raw.(type) {
	case []interface{}:
		list = v
	case []map[string]interface{}:
		for _, m := range v {
			list = append(list, m)
		}
	default:
		return false, "invalid openai-compatibility format"
	}
	entryName := upstreamEntryName(profileName, p)
	for _, item := range list {
		var m map[string]interface{}
		switch vv := item.(type) {
		case map[string]interface{}:
			m = vv
		case map[interface{}]interface{}:
			m = map[string]interface{}{}
			for k, v := range vv {
				if ks, ok := k.(string); ok {
					m[ks] = v
				}
			}
		default:
			continue
		}
		if name, _ := m["name"].(string); name == entryName {
			// Compare base-url
			baseVal, _ := m["base-url"].(string)
			expectedBase := normalizeUpstreamBaseURL(p.UpstreamBaseURL)
			if p.IsPooled() && expectedBase == "" {
				for _, a := range p.Accounts {
					if a.UpstreamBaseURL != "" {
						expectedBase = normalizeUpstreamBaseURL(a.UpstreamBaseURL)
						break
					}
				}
			}
			if baseVal != expectedBase {
				return false, fmt.Sprintf("base-url drift: have %q want %q", baseVal, expectedBase)
			}
			// Check api-key-entries length matches expectation (basic)
			// For pooled, expect len == accounts, else 1
			if rawKeys, ok := m["api-key-entries"]; ok {
				var keysList []interface{}
				switch kv := rawKeys.(type) {
				case []interface{}:
					keysList = kv
				case []map[string]interface{}:
					for _, mm := range kv {
						keysList = append(keysList, mm)
					}
				}
				expectedCount := 1
				if p.IsPooled() {
					expectedCount = len(p.Accounts)
				}
				if len(keysList) != expectedCount {
					return false, fmt.Sprintf("api-keys drift: have %d want %d", len(keysList), expectedCount)
				}
			}
			// Models check (alias)
			expectedModel := p.UpstreamModel
			if expectedModel == "" {
				expectedModel = p.Model
			}
			expectedAlias := p.UpstreamModelAlias
			if expectedAlias == "" {
				expectedAlias = p.Model
			}
			if expectedAlias == "" {
				expectedAlias = expectedModel
			}
			if rawModels, ok := m["models"]; ok {
				var mList []interface{}
				switch mv := rawModels.(type) {
				case []interface{}:
					mList = mv
				case []map[string]interface{}:
					for _, mm := range mv {
						mList = append(mList, mm)
					}
				}
				if len(mList) > 0 {
					if mm, ok := mList[0].(map[string]interface{}); ok {
						nameVal, _ := mm["name"].(string)
						aliasVal, _ := mm["alias"].(string)
						if nameVal != expectedModel || aliasVal != expectedAlias {
							return false, fmt.Sprintf("model drift: have %q->%q want %q->%q", nameVal, aliasVal, expectedModel, expectedAlias)
						}
					}
				}
			}
			return true, "in sync"
		}
	}
	return false, "entry not found"
}

func upsertOpenAICompatEntry(cfg *config.Config, entry openAICompatEntry) error {
	path := cfg.ProxyConfigFile()
	// Ensure config file exists; scaffold if missing.
	if !fileExists(path) {
		scaffoldProxyConfig(path)
	}
	proxyUpstreamMu.Lock()
	defer proxyUpstreamMu.Unlock()
	unlock := lockProxyFile(path)
	defer unlock()
	data, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	var fm map[string]interface{}
	if len(data) > 0 {
		if err := unmarshalYAML(data, &fm); err != nil {
			return fmt.Errorf("parsing %s: %w", path, err)
		}
	}
	if fm == nil {
		fm = map[string]interface{}{}
	}
	// Ensure required top-level keys exist (port etc.) if file was empty; scaffold already did.
	// Extract existing list.
	var list []interface{}
	if raw, ok := fm["openai-compatibility"]; ok {
		switch v := raw.(type) {
		case []interface{}:
			list = v
		case []map[string]interface{}:
			for _, m := range v {
				list = append(list, m)
			}
		default:
			list = []interface{}{}
		}
	}
	// Convert entry to map for storage.
	entryMap := map[string]interface{}{
		"name":     entry.Name,
		"base-url": entry.BaseURL,
	}
	// api-key-entries
	var apiList []interface{}
	for _, ak := range entry.APIKeyEntries {
		apiList = append(apiList, map[string]interface{}{"api-key": ak.APIKey})
	}
	entryMap["api-key-entries"] = apiList
	// models
	var modelList []interface{}
	for _, m := range entry.Models {
		modelList = append(modelList, map[string]interface{}{"name": m.Name, "alias": m.Alias})
	}
	entryMap["models"] = modelList
	// Upsert by name.
	found := false
	for i, item := range list {
		m, ok := item.(map[string]interface{})
		if !ok {
			if mm, ok2 := item.(map[interface{}]interface{}); ok2 {
				m = map[string]interface{}{}
				for k, v := range mm {
					if ks, ok := k.(string); ok {
						m[ks] = v
					}
				}
				list[i] = m
			} else {
				continue
			}
		}
		if name, _ := m["name"].(string); name == entry.Name {
			list[i] = entryMap
			found = true
			break
		}
	}
	if !found {
		list = append(list, entryMap)
	}
	fm["openai-compatibility"] = list
	return writeProxyFileAtomically(path, fm)
}

func writeProxyFileAtomically(path string, fm map[string]interface{}) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	tmp := path + ".tmp"
	out, err := yaml.Marshal(fm)
	if err != nil {
		return err
	}
	if err := os.WriteFile(tmp, out, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

func lockProxyFile(path string) func() {
	// Best-effort file lock via sidecar lock file; uses sync.Mutex plus flock on unix.
	lockPath := path + ".lock"
	f, err := os.OpenFile(lockPath, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return func() {}
	}
	// Try flock if available (unix). On other platforms, rely on mutex only.
	tryFlock(f)
	return func() {
		unlockFlock(f)
		_ = f.Close()
	}
}

// tryFlock / unlockFlock are implemented per-OS in daemon_*.go files.
// Provide no-op defaults here for builds without those files.
func tryFlock(f *os.File)    {}
func unlockFlock(f *os.File) {}

// EnsureProxyForUpstream ensures the proxy binary is installed, config is scaffolded and synced,
// and the daemon is up (or reloads). It is idempotent.
func EnsureProxyForUpstream(cfg *config.Config, profileName string, p *config.Profile) error {
	if !p.HasUpstream() {
		return nil
	}
	if p.IsUpstreamResponses() {
		return nil
	}
	if findProxyBinary(cfg) == "" {
		if err := installProxy(); err != nil {
			return fmt.Errorf("proxy binary not found and install failed: %w", err)
		}
	}
	if err := SyncOpenAICompat(cfg, profileName, p); err != nil {
		return err
	}
	if !fileExists(cfg.ProxyConfigFile()) {
		scaffoldProxyConfig(cfg.ProxyConfigFile())
	}
	if proxyReachable(cfg) {
		// File watcher will reload; give it a moment.
		time.Sleep(300 * time.Millisecond)
		return nil
	}
	if cfg.Proxy.Autostart() {
		if err := startProxy(cfg); err != nil {
			return err
		}
	}
	return nil
}

// UpstreamHealthProbe checks if the upstream base URL is reachable (GET /models with 500ms).
func UpstreamHealthProbe(upstreamBaseURL, apiKey string) error {
	base := strings.TrimRight(strings.TrimSpace(upstreamBaseURL), "/")
	if base == "" {
		return fmt.Errorf("empty upstream base URL")
	}
	if !strings.HasSuffix(base, "/v1") && !strings.HasSuffix(base, "/v1/models") {
		// Normalize probe to /v1/models or /models.
		if strings.Contains(base, "/v1") {
			base = strings.TrimSuffix(base, "/")
		}
	}
	probe := base + "/models"
	if strings.HasSuffix(upstreamBaseURL, "/models") {
		probe = base
	}
	// Use generic probe helper with timeout.
	_ = apiKey // apiKey may be needed for auth, but probe without auth often returns 401 which still counts as reachable.
	return probeURL(probe, 500*time.Millisecond)
}

// ---------------------------------------------------------------------------
// /v1/models listing
// ---------------------------------------------------------------------------

type modelsResponse struct {
	Data []struct {
		ID string `json:"id"`
	} `json:"data"`
}

func fetchProxyModels(cfg *config.Config) ([]string, error) {
	url := proxyBaseURL(cfg) + "/v1/models"
	req, _ := http.NewRequest("GET", url, nil)
	if keys := readProxyAPIKeys(cfg); len(keys) > 0 {
		req.Header.Set("Authorization", "Bearer "+keys[0])
	}
	resp, err := (&http.Client{Timeout: 5 * time.Second}).Do(req)
	if err != nil {
		return nil, err
	}
	defer closeQuietly(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("HTTP %d from %s", resp.StatusCode, url)
	}
	var mr modelsResponse
	if err := json.NewDecoder(resp.Body).Decode(&mr); err != nil {
		return nil, err
	}
	var ids []string
	for _, m := range mr.Data {
		if m.ID != "" {
			ids = append(ids, m.ID)
		}
	}
	sort.Strings(ids)
	return ids, nil
}

// ---------------------------------------------------------------------------
// logs
// ---------------------------------------------------------------------------

func readLastLines(path string, n int) []string { return tailLines(path, n) }

func tailLines(path string, n int) []string {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	lines := strings.Split(strings.TrimRight(string(data), "\n"), "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return lines
}

func followLogs(n int) {
	path := proxyLogPath()
	f, err := os.Open(path)
	if err != nil {
		die("cannot open %s: %v", path, err)
	}
	defer closeQuietly(f)

	for _, l := range tailLines(path, n) {
		fmt.Println(l)
	}
	offset, _ := f.Seek(0, io.SeekEnd)
	for {
		fi, err := os.Stat(path)
		if err != nil || fi.Size() < offset { // truncated/rotated → reopen
			closeQuietly(f)
			f, err = os.Open(path)
			if err != nil {
				return
			}
			offset = 0
		} else if fi.Size() > offset {
			buf := make([]byte, fi.Size()-offset)
			if _, err := f.ReadAt(buf, offset); err == nil {
				fmt.Print(string(buf))
				offset = fi.Size()
			}
		}
		time.Sleep(400 * time.Millisecond)
	}
}

func indent(lines []string) string {
	for i, l := range lines {
		lines[i] = "    " + l
	}
	return strings.Join(lines, "\n")
}

// ---------------------------------------------------------------------------
// release installation
// ---------------------------------------------------------------------------

type ghRelease struct {
	TagName string `json:"tag_name"`
	Assets  []struct {
		Name               string `json:"name"`
		BrowserDownloadURL string `json:"browser_download_url"`
	} `json:"assets"`
}

func archVariants(a string) []string {
	switch a {
	case "amd64":
		return []string{"amd64", "x86_64"}
	case "arm64":
		return []string{"arm64", "aarch64"}
	default:
		return []string{a}
	}
}

func isOpenWrtSystem() bool {
	if _, err := os.Stat("/etc/openwrt_release"); err == nil {
		return true
	}
	if data, err := os.ReadFile("/etc/os-release"); err == nil {
		if strings.Contains(strings.ToLower(string(data)), "openwrt") {
			return true
		}
	}
	return false
}

func isMuslSystem() bool {
	if _, err := os.Stat("/etc/alpine-release"); err == nil {
		return true
	}
	for _, pat := range []string{"/lib/ld-musl-*.so*", "/usr/lib/ld-musl-*.so*"} {
		if matches, _ := filepath.Glob(pat); len(matches) > 0 {
			return true
		}
	}
	if out, err := exec.Command("ldd", "--version").CombinedOutput(); err == nil {
		if strings.Contains(strings.ToLower(string(out)), "musl") {
			return true
		}
	} else if out != nil && strings.Contains(strings.ToLower(string(out)), "musl") {
		return true
	}
	return false
}

func wantNoPluginAsset() bool {
	if runtime.GOOS != "linux" {
		return false
	}
	return isMuslSystem() || isOpenWrtSystem()
}

func pickAsset(r *ghRelease) (string, error) {
	goos := runtime.GOOS
	wantNoPlugin := wantNoPluginAsset()
	score := func(nameLower string) int {
		if !strings.Contains(nameLower, goos) {
			return -1
		}
		archOK := false
		for _, v := range archVariants(runtime.GOARCH) {
			if strings.Contains(nameLower, v) {
				archOK = true
				break
			}
		}
		if !archOK {
			return -1
		}
		switch {
		case strings.HasSuffix(nameLower, ".tar.gz"):
			return 4
		case strings.HasSuffix(nameLower, ".tgz"):
			return 3
		case strings.HasSuffix(nameLower, ".zip"):
			return 2
		case !strings.Contains(nameLower, ".deb") && !strings.Contains(nameLower, ".rpm") &&
			!strings.HasSuffix(nameLower, ".sha256") && !strings.HasSuffix(nameLower, ".txt"):
			return 1 // maybe raw binary
		default:
			return -1
		}
	}
	best, bestScore := "", -1
	fallback, fallbackScore := "", -1
	for _, a := range r.Assets {
		lower := strings.ToLower(a.Name)
		s := score(lower)
		if s < 0 {
			continue
		}
		hasNoPlugin := strings.Contains(lower, "no-plugin")
		// On Linux, prefer the variant matching the host libc.
		if runtime.GOOS == "linux" && hasNoPlugin != wantNoPlugin {
			if s > fallbackScore {
				fallbackScore, fallback = s, a.BrowserDownloadURL
			}
			continue
		}
		if s > bestScore {
			bestScore, best = s, a.BrowserDownloadURL
		}
	}
	if bestScore >= 0 {
		return best, nil
	}
	if fallbackScore >= 0 {
		return fallback, nil
	}
	return "", fmt.Errorf("no asset matching %s/%s in release %s", goos, runtime.GOARCH, r.TagName)
}

func downloadTo(url, dest string) error {
	resp, err := (&http.Client{Timeout: 5 * time.Minute}).Get(url)
	if err != nil {
		return err
	}
	defer closeQuietly(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("download %s: HTTP %d", url, resp.StatusCode)
	}
	return os.WriteFile(dest, mustReadAll(resp.Body), 0o600)
}

func mustReadAll(r io.Reader) []byte {
	b, err := io.ReadAll(r)
	if err != nil {
		die("read failed: %v", err)
	}
	return b
}

// extractBinary pulls the cli-proxy-api executable out of an archive.
func extractBinary(archive, destDir string) (string, error) {
	lower := strings.ToLower(archive)
	switch {
	case strings.HasSuffix(lower, ".tar.gz"), strings.HasSuffix(lower, ".tgz"):
		f, err := os.Open(archive)
		if err != nil {
			return "", err
		}
		defer closeQuietly(f)
		gz, err := gzip.NewReader(f)
		if err != nil {
			return "", err
		}
		defer closeQuietly(gz)
		tr := tar.NewReader(gz)
		for {
			hdr, err := tr.Next()
			if err == io.EOF {
				break
			}
			if err != nil {
				return "", err
			}
			base := filepath.Base(hdr.Name)
			if hdr.Typeflag == tar.TypeReg && isBinaryName(base) {
				dest := filepath.Join(destDir, "cli-proxy-api")
				out, err := os.OpenFile(dest, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o755)
				if err != nil {
					return "", err
				}
				if _, err := io.Copy(out, tr); err != nil {
					closeQuietly(out)
					return "", err
				}
				closeQuietly(out)
				return dest, nil
			}
		}
		return "", fmt.Errorf("no binary found inside %s", filepath.Base(archive))
	case strings.HasSuffix(lower, ".zip"):
		zr, err := zip.OpenReader(archive)
		if err != nil {
			return "", err
		}
		defer closeQuietly(zr)
		for _, zf := range zr.File {
			if isBinaryName(filepath.Base(zf.Name)) {
				rc, err := zf.Open()
				if err != nil {
					return "", err
				}
				dest := filepath.Join(destDir, "cli-proxy-api")
				out, err := os.OpenFile(dest, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o755)
				if err != nil {
					return "", err
				}
				if _, err := io.Copy(out, rc); err != nil {
					closeQuietly(out)
					return "", err
				}
				closeQuietly(out)
				closeQuietly(rc)
				return dest, nil
			}
		}
		return "", fmt.Errorf("no binary found inside %s", filepath.Base(archive))
	default:
		// raw single-binary asset
		dest := filepath.Join(destDir, "cli-proxy-api")
		if err := os.Rename(archive, dest); err != nil {
			return "", err
		}
		_ = os.Chmod(dest, 0o755)
		return dest, nil
	}
}

func isBinaryName(base string) bool {
	b := strings.ToLower(base)
	return strings.HasPrefix(b, "cli-proxy-api") || strings.HasPrefix(b, "cliproxyapi") ||
		strings.HasPrefix(b, "cli_proxy_api")
}

func installProxy() error {
	infof("querying latest CLIProxyAPI release…")
	req, _ := http.NewRequest("GET", "https://api.github.com/repos/router-for-me/CLIProxyAPI/releases/latest", nil)
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("User-Agent", "ccp")
	resp, err := (&http.Client{Timeout: 30 * time.Second}).Do(req)
	if err != nil {
		return err
	}
	defer closeQuietly(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("fetching release: HTTP %d", resp.StatusCode)
	}
	var rel ghRelease
	if err := json.NewDecoder(resp.Body).Decode(&rel); err != nil {
		return err
	}

	asset, err := pickAsset(&rel)
	if err != nil {
		return err
	}
	tmpDir, err := os.MkdirTemp("", "ccp-install-*")
	if err != nil {
		return err
	}
	defer func() { _ = os.RemoveAll(tmpDir) }()

	archivePath := filepath.Join(tmpDir, filepath.Base(asset))
	infof("downloading %s…", paint(cDim, filepath.Base(asset)))
	if err := downloadTo(asset, archivePath); err != nil {
		return err
	}
	binPath, err := extractBinary(archivePath, tmpDir)
	if err != nil {
		return err
	}

	target := filepath.Join(homeDir(), ".local", "bin", "cli-proxy-api")
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		target = filepath.Join(ccpStateDir(), "bin", "cli-proxy-api")
		if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
			return err
		}
	}
	data := mustReadAll(mustOpen(binPath))
	if err := os.WriteFile(target, data, 0o755); err != nil {
		return err
	}
	okf("installed %s (%s)", paint(cBold, target), rel.TagName)
	infof("ccp auto-detects this path; next: %s then %s",
		paint(cBold, "ccp proxy start"),
		paint(cBold, "ccp glm"))
	return nil
}

func mustOpen(p string) *os.File {
	f, err := os.Open(p)
	if err != nil {
		die("%v", err)
	}
	return f
}

// ---------------------------------------------------------------------------
// helpers delegating to util/config
// ---------------------------------------------------------------------------

func expandPath(p string) string                       { return util.ExpandPath(p) }
func homeDir() string                                  { return util.HomeDir() }
func fileExists(p string) bool                         { return util.FileExists(p) }
func isExecutable(p string) bool                       { return util.IsExecutable(p) }
func lookPathAll(names ...string) string               { return util.LookPathAll(names...) }
func probeURL(url string, timeout time.Duration) error { return util.ProbeURL(url, timeout) }
func closeQuietly(c io.Closer)                         { util.CloseQuietly(c) }
func randHex(n int) string                             { return util.RandHex(n) }
func paint(code, s string) string                      { return util.Paint(code, s) }
func procCmdline(pid int) string                       { return util.ProcCmdline(pid) }
func infof(format string, a ...any)                    { util.Infof(format, a...) }
func okf(format string, a ...any)                      { util.Okf(format, a...) }
func die(format string, a ...any)                      { util.Die(format, a...) }

func proxyPidPath() string { return config.ProxyPidPath() }
func proxyLogPath() string { return config.ProxyLogPath() }
func ccpStateDir() string  { return config.CcpStateDir() }

const (
	cBold = util.CBold
	cDim  = util.CDim
)

// proxyYAML for reading api-keys
type proxyYAML struct {
	APIKeys []string `yaml:"api-keys"`
	Port    int      `yaml:"port"`
	AuthDir string   `yaml:"auth-dir"`
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

func readProxyAPIKeys(cfg *config.Config) []string {
	y := readProxyConfigFile(cfg)
	if y == nil {
		return nil
	}
	return y.APIKeys
}

// writeFileIfMissing helper (used by scaffold)
func writeFileIfMissing(path, content string, perm os.FileMode) (bool, error) {
	if util.FileExists(path) {
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

// stub detach etc will be provided by daemon files; ensure they exist
// killProcessGroup etc are in daemon_unix.go for proxy package

// ---------------------------------------------------------------------------
// exported API
// ---------------------------------------------------------------------------

func ProxyBaseURL(cfg *config.Config) string                { return proxyBaseURL(cfg) }
func ProxyReachable(cfg *config.Config) bool                { return proxyReachable(cfg) }
func FindProxyBinary(cfg *config.Config) string             { return findProxyBinary(cfg) }
func StartProxy(cfg *config.Config) error                   { return startProxy(cfg) }
func StopProxy(cfg *config.Config) error                    { return stopProxy(cfg) }
func FetchProxyModels(cfg *config.Config) ([]string, error) { return fetchProxyModels(cfg) }
func InstallProxy() error                                   { return installProxy() }
func ScaffoldProxyConfig(path string)                       { scaffoldProxyConfig(path) }
func PidFromFile() (int, bool)                              { return pidFromFile() }
func WritePidFile(pid int) error                            { return writePidFile(pid) }
func ReadLastLines(path string, n int) []string             { return readLastLines(path, n) }
func TailLines(path string, n int) []string                 { return tailLines(path, n) }
func FollowLogs(n int)                                      { followLogs(n) }
func ExtractBinary(archive, destDir string) (string, error) { return extractBinary(archive, destDir) }
func IsBinaryName(base string) bool                         { return isBinaryName(base) }
func PickAsset(r *GhRelease) (string, error)                { return pickAsset(r) }
func ArchVariants(a string) []string                        { return archVariants(a) }

// exported types for testing
type GhRelease = ghRelease
