package shim

import (
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"ccp/internal/config"
	"ccp/internal/util"
	"gopkg.in/yaml.v3"
)

const (
	defaultShimHost = "127.0.0.1"
	defaultShimPort = 8318
)

var shimMu sync.Mutex

func shimHost(cfg *config.Config) string {
	// For now fixed, but allow override via env or future config
	if h := os.Getenv("CCP_SHIM_HOST"); h != "" {
		return h
	}
	return defaultShimHost
}

func shimPort(cfg *config.Config) int {
	if p := os.Getenv("CCP_SHIM_PORT"); p != "" {
		if n, err := strconv.Atoi(p); err == nil && n > 0 {
			return n
		}
	}
	return defaultShimPort
}

func ShimBaseURL(cfg *config.Config) string {
	return fmt.Sprintf("http://%s", net.JoinHostPort(shimHost(cfg), strconv.Itoa(shimPort(cfg))))
}

func ShimAddr(cfg *config.Config) string {
	return net.JoinHostPort(shimHost(cfg), strconv.Itoa(shimPort(cfg)))
}

func shimPidPath() string {
	return filepath.Join(config.CcpStateDir(), "shim.pid")
}

func shimLogPath() string {
	return filepath.Join(config.CcpStateDir(), "shim.log")
}

func shimConfigPath() string {
	// Use config dir for persistence: ~/.config/ccp/shim.yaml
	// But to avoid polluting, use state dir: ~/.local/state/ccp/shim.yaml
	// Use state dir for simplicity, as shim is daemon state.
	return filepath.Join(config.CcpStateDir(), "shim.yaml")
}

func ShimPidPath() string    { return shimPidPath() }
func ShimLogPath() string    { return shimLogPath() }
func ShimConfigPath() string { return shimConfigPath() }

func ShimReachable(cfg *config.Config) bool {
	return probeURL(ShimBaseURL(cfg)+"/", 500*time.Millisecond) == nil
}

func probeURL(url string, timeout time.Duration) error {
	return util.ProbeURL(url, timeout)
}

// SyncShimConfig ensures shim.yaml contains an entry for the given profile if it uses responses protocol.
// If profile does not use responses, it removes stale entry.
func SyncShimConfig(cfg *config.Config, profileName string, p *config.Profile) error {
	if p.Type != "cliproxy" {
		return nil
	}
	if !p.IsUpstreamResponses() {
		return RemoveShimEntry(cfg, profileName)
	}
	// Build upstream configs for shim
	var upstreams []UpstreamConfig
	// Determine model alias etc.
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
	baseURL := strings.TrimSpace(p.UpstreamBaseURL)
	baseURL = strings.TrimRight(baseURL, "/")
	// For pooled, collect per-account
	if p.IsPooled() {
		// For responses, we support pool by creating multiple upstreams with same alias but different keys
		// The shim will round-robin via its own logic? For now, create one entry per account with same alias.
		for i, a := range p.Accounts {
			if a.UpstreamProtocolNormalized() != "responses" {
				// skip non-responses accounts? But IsUpstreamResponses checks any, so we should handle mixed
				// If account is chat, it should be handled by proxy, not shim. For mixed, we need to split.
				// For now, only include responses accounts in shim.
				continue
			}
			ab := strings.TrimSpace(a.UpstreamBaseURL)
			if ab == "" {
				ab = baseURL
			}
			ab = strings.TrimRight(ab, "/")
			key := resolveUpstreamAPIKeyAccount(&a, p)
			if key == "" {
				return fmt.Errorf("profile %q accounts[%d] upstream auth missing", profileName, i)
			}
			am := strings.TrimSpace(a.UpstreamModel)
			if am == "" {
				am = upstreamModel
			}
			aa := strings.TrimSpace(a.UpstreamModelAlias)
			if aa == "" {
				aa = alias
			}
			if am == "" {
				am = aa
			}
			if aa == "" {
				aa = am
			}
			name := strings.TrimSpace(a.UpstreamName)
			if name == "" {
				name = upstreamEntryName(profileName, p)
				// for pooled, suffix with idx to avoid collision? But shim uses alias for lookup, so name not critical
				name = fmt.Sprintf("%s-%d", name, i)
			}
			upstreams = append(upstreams, UpstreamConfig{
				Name:    name,
				BaseURL: ab,
				APIKey:  key,
				Model:   am,
				Alias:   aa,
			})
		}
		if len(upstreams) == 0 {
			// fallback to profile-level if no account was responses
			key := resolveUpstreamAPIKey(p)
			if key == "" {
				return fmt.Errorf("profile %q upstream auth missing", profileName)
			}
			upstreams = []UpstreamConfig{{
				Name:    upstreamEntryName(profileName, p),
				BaseURL: baseURL,
				APIKey:  key,
				Model:   upstreamModel,
				Alias:   alias,
			}}
		}
	} else {
		key := resolveUpstreamAPIKey(p)
		if key == "" {
			return fmt.Errorf("profile %q upstream auth missing", profileName)
		}
		upstreams = []UpstreamConfig{{
			Name:    upstreamEntryName(profileName, p),
			BaseURL: baseURL,
			APIKey:  key,
			Model:   upstreamModel,
			Alias:   alias,
		}}
	}
	return upsertShimEntry(cfg, profileName, upstreams)
}

func RemoveShimEntry(cfg *config.Config, profileName string) error {
	path := shimConfigPath()
	if !fileExists(path) {
		return nil
	}
	shimMu.Lock()
	defer shimMu.Unlock()
	unlock := lockFile(path)
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
	var sc ShimConfig
	if err := yaml.Unmarshal(data, &sc); err != nil {
		return err
	}
	// Filter out upstreams that belong to this profile (by name prefix)
	// Since upstreams name is entryName, we can filter by name == profileName or prefix
	newUps := []UpstreamConfig{}
	removed := false
	for _, u := range sc.Upstreams {
		// If u.Name == profileName or has prefix profileName- (for pooled)
		if u.Name == profileName || strings.HasPrefix(u.Name, profileName+"-") {
			removed = true
			continue
		}
		// Also check alias match? For non-pooled, alias == profile's alias, but alias may be same as other profile's alias
		// So we rely on Name. For safety, if Name matches upstreamEntryName, we already handled.
		// For pooled, we used Name with suffix, so prefix check covers.
		newUps = append(newUps, u)
	}
	if !removed {
		return nil
	}
	sc.Upstreams = newUps
	return writeShimConfig(path, &sc)
}

func upsertShimEntry(cfg *config.Config, profileName string, upstreams []UpstreamConfig) error {
	path := shimConfigPath()
	shimMu.Lock()
	defer shimMu.Unlock()
	unlock := lockFile(path)
	defer unlock()
	var sc ShimConfig
	if fileExists(path) {
		data, err := os.ReadFile(path)
		if err == nil && len(data) > 0 {
			_ = yaml.Unmarshal(data, &sc)
		}
	}
	if sc.Host == "" {
		sc.Host = shimHost(cfg)
	}
	if sc.Port == 0 {
		sc.Port = shimPort(cfg)
	}
	// Remove existing entries for this profile
	newUps := []UpstreamConfig{}
	for _, u := range sc.Upstreams {
		if u.Name == profileName || strings.HasPrefix(u.Name, profileName+"-") {
			continue
		}
		newUps = append(newUps, u)
	}
	newUps = append(newUps, upstreams...)
	sc.Upstreams = newUps
	return writeShimConfig(path, &sc)
}

func writeShimConfig(path string, sc *ShimConfig) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	tmp := path + ".tmp"
	out, err := yaml.Marshal(sc)
	if err != nil {
		return err
	}
	if err := os.WriteFile(tmp, out, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

func lockFile(path string) func() {
	lockPath := path + ".lock"
	f, err := os.OpenFile(lockPath, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return func() {}
	}
	tryFlock(f)
	return func() {
		unlockFlock(f)
		_ = f.Close()
	}
}

func resolveUpstreamAPIKey(p *config.Profile) string {
	if p.UpstreamAPIKeyEnv != "" {
		envName := strings.TrimSpace(p.UpstreamAPIKeyEnv)
		if v := os.Getenv(envName); v != "" {
			return strings.TrimSpace(v)
		}
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

func resolveUpstreamAPIKeyAccount(a *config.Account, p *config.Profile) string {
	if a.UpstreamAPIKeyEnv != "" {
		if v := os.Getenv(strings.TrimSpace(a.UpstreamAPIKeyEnv)); v != "" {
			return strings.TrimSpace(v)
		}
		return ""
	}
	if a.UpstreamAPIKey != "" {
		return strings.TrimSpace(util.ExpandEnvVars(a.UpstreamAPIKey))
	}
	return resolveUpstreamAPIKey(p)
}

func upstreamEntryName(profileName string, p *config.Profile) string {
	if p.UpstreamName != "" {
		return strings.TrimSpace(util.ExpandEnvVars(p.UpstreamName))
	}
	return profileName
}

func fileExists(p string) bool { return util.FileExists(p) }

// EnsureShimForUpstream ensures shim config is synced and daemon is up.
func EnsureShimForUpstream(cfg *config.Config, profileName string, p *config.Profile) error {
	if !p.IsUpstreamResponses() {
		return nil
	}
	// Check if already in sync and reachable, then nothing to do
	if isShimSynced(cfg, profileName, p) && ShimReachable(cfg) {
		return nil
	}
	if err := SyncShimConfig(cfg, profileName, p); err != nil {
		return err
	}
	if ShimReachable(cfg) {
		// Config just changed, restart to pick it up
		_ = StopShim(cfg)
		time.Sleep(300 * time.Millisecond)
	}
	if err := StartShim(cfg); err != nil {
		return err
	}
	return nil
}

func isShimSynced(cfg *config.Config, profileName string, p *config.Profile) bool {
	path := shimConfigPath()
	if !fileExists(path) {
		return false
	}
	data, err := os.ReadFile(path)
	if err != nil || len(data) == 0 {
		return false
	}
	var sc ShimConfig
	if err := yaml.Unmarshal(data, &sc); err != nil {
		return false
	}
	// Check if any upstream with name == profileName exists and matches
	expectedName := upstreamEntryName(profileName, p)
	for _, u := range sc.Upstreams {
		if u.Name == expectedName || strings.HasPrefix(u.Name, expectedName+"-") {
			// Found, check base and model
			expectedBase := strings.TrimRight(strings.TrimSpace(p.UpstreamBaseURL), "/")
			if u.BaseURL != expectedBase {
				return false
			}
			expectedModel := strings.TrimSpace(p.UpstreamModel)
			if expectedModel == "" {
				expectedModel = strings.TrimSpace(p.Model)
			}
			if u.Model != expectedModel {
				return false
			}
			return true
		}
	}
	return false
}

func StartShim(cfg *config.Config) error {
	if ShimReachable(cfg) {
		return nil
	}
	bin, err := os.Executable()
	if err != nil {
		return err
	}
	// Ensure config exists
	cfgPath := shimConfigPath()
	if !fileExists(cfgPath) {
		// scaffold empty config
		sc := ShimConfig{Host: shimHost(cfg), Port: shimPort(cfg)}
		_ = writeShimConfig(cfgPath, &sc)
	}
	logPath := shimLogPath()
	if err := os.MkdirAll(filepath.Dir(logPath), 0o700); err != nil {
		return err
	}
	logFile, err := os.OpenFile(logPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	defer func() { _ = logFile.Close() }()

	// Use ccp internal command to run shim server
	// We need to find ccp binary; os.Executable gives current.
	args := []string{"internal-shim", "--config", cfgPath}
	cmd := exec.Command(bin, args...)
	detach(cmd)
	cmd.Stdin = nil
	cmd.Stdout = logFile
	cmd.Stderr = logFile
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("starting shim: %w", err)
	}
	pid := cmd.Process.Pid
	_ = writePidFile(pid)
	// wait for reachable
	timeout := 5 * time.Second
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if ShimReachable(cfg) {
			return nil
		}
		if !procAlive(pid) {
			tail := readLastLines(logPath, 20)
			return fmt.Errorf("shim exited immediately, log: %s", strings.Join(tail, "\n"))
		}
		time.Sleep(200 * time.Millisecond)
	}
	return fmt.Errorf("shim did not become reachable within %s", timeout)
}

func StopShim(cfg *config.Config) error {
	pid, alive := pidFromFile()
	if !alive {
		if ShimReachable(cfg) {
			return fmt.Errorf("shim at %s answers but no pid file", ShimBaseURL(cfg))
		}
		return fmt.Errorf("shim not running")
	}
	stopQuietly(pid)
	_ = os.Remove(shimPidPath())
	return nil
}

func pidFromFile() (int, bool) {
	data, err := os.ReadFile(shimPidPath())
	if err != nil {
		return 0, false
	}
	var pid int
	if _, err := fmt.Sscanf(strings.TrimSpace(string(data)), "%d", &pid); err != nil || pid <= 0 {
		return 0, false
	}
	return pid, procAlive(pid)
}

func writePidFile(pid int) error {
	if err := os.MkdirAll(filepath.Dir(shimPidPath()), 0o700); err != nil {
		return err
	}
	return os.WriteFile(shimPidPath(), []byte(fmt.Sprintf("%d\n", pid)), 0o600)
}

func readLastLines(path string, n int) []string {
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
