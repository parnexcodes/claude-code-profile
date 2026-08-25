package proxy

import (
	"archive/tar"
	"archive/zip"
	"ccp/internal/config"
	"ccp/internal/util"
	"compress/gzip"
	"encoding/json"
	"fmt"
	"gopkg.in/yaml.v3"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
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
`, filepath.Join(homeDir(), ".cli-proxy-api"), key)
	if _, err := writeFileIfMissing(path, body, 0o600); err != nil {
		die("writing %s: %v", path, err)
	}
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
	if err := yaml.Unmarshal(data, &y); err != nil {
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
