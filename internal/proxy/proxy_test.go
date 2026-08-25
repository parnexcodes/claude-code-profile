//nolint:errcheck,staticcheck,unused
package proxy

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"ccp/internal/config"
	"ccp/internal/util"
)

func TestFindProxyBinary_Precedence(t *testing.T) {
	// config binary > PATH > ~/.local/bin > <state>/bin
	dir := t.TempDir()
	state := t.TempDir()
	home := t.TempDir()
	t.Setenv("CCP_STATE_HOME", state)
	t.Setenv("HOME", home)

	// create fake binaries
	binConfig := filepath.Join(dir, "custom-proxy")
	os.WriteFile(binConfig, []byte("#!/bin/sh\necho hi"), 0o755)
	binName := "cli-proxy-api"
	if runtime.GOOS == "windows" {
		binName = "cli-proxy-api.exe"
	}
	binPath := filepath.Join(dir, binName)
	os.WriteFile(binPath, []byte("#!/bin/sh\necho hi"), 0o755)
	binLocal := filepath.Join(home, ".local", "bin", "cli-proxy-api")
	if runtime.GOOS == "windows" {
		// On Windows the local/state binaries are checked directly via IsExecutable, not via PATH, so keep plain name.
		// But keep original name for test determinism.
	}
	os.MkdirAll(filepath.Join(home, ".local", "bin"), 0o755)
	os.WriteFile(binLocal, []byte("#!/bin/sh"), 0o755)
	binState := filepath.Join(state, "bin", "cli-proxy-api")
	os.MkdirAll(filepath.Join(state, "bin"), 0o755)
	os.WriteFile(binState, []byte("#!/bin/sh"), 0o755)

	// PATH contains binPath
	origPath := os.Getenv("PATH")
	t.Setenv("PATH", dir+string(os.PathListSeparator)+origPath)

	// case 1: config binary set
	cfg := &config.Config{Proxy: config.ProxyConfig{Binary: binConfig}}
	if got := FindProxyBinary(cfg); got != binConfig {
		t.Fatalf("config binary: got %q want %q", got, binConfig)
	}
	// case 2: config binary empty, PATH wins
	cfg.Proxy.Binary = ""
	gotPath := FindProxyBinary(cfg)
	// On Windows LookPath returns the .exe path, so compare base names loosely.
	if runtime.GOOS == "windows" {
		if gotPath != binPath && gotPath != filepath.Join(dir, "cli-proxy-api") {
			t.Fatalf("PATH: got %q want %q", gotPath, binPath)
		}
	} else {
		if gotPath != binPath {
			t.Fatalf("PATH: got %q want %q", gotPath, binPath)
		}
	}
	// remove PATH binary, should pick ~/.local/bin
	os.Remove(binPath)
	// isolate PATH so real HOME's binary via PATH doesn't interfere
	t.Setenv("PATH", dir)
	if gotHome := util.HomeDir(); gotHome != home {
		t.Fatalf("HomeDir=%q want %q (HOME=%q)", gotHome, home, os.Getenv("HOME"))
	}
	if got := FindProxyBinary(cfg); got != binLocal {
		t.Fatalf("local bin: got %q want %q (HomeDir=%q PATH=%q)", got, binLocal, util.HomeDir(), os.Getenv("PATH"))
	}
	// remove local, should pick state
	os.Remove(binLocal)
	if got := FindProxyBinary(cfg); got != binState {
		t.Fatalf("state bin: got %q want %q", got, binState)
	}
	// remove all, should be empty
	os.Remove(binState)
	if got := FindProxyBinary(cfg); got != "" {
		t.Fatalf("expected empty, got %q", got)
	}
}

func TestProxyReachable(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(200)
	}))
	defer srv.Close()
	// parse host port
	hostPort := strings.TrimPrefix(srv.URL, "http://")
	parts := strings.Split(hostPort, ":")
	host := parts[0]
	var port int
	if _, err := fmt.Sscanf(parts[1], "%d", &port); err != nil {
		t.Fatalf("Sscanf: %v", err)
	}
	cfg := &config.Config{Proxy: config.ProxyConfig{Host: host, Port: port}}
	if !ProxyReachable(cfg) {
		t.Fatalf("expected reachable")
	}
	srv.Close()
	if ProxyReachable(cfg) {
		t.Fatalf("expected not reachable")
	}
}

func TestFetchProxyModels(t *testing.T) {
	// success with unsorted ids, should be sorted
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer test-key" {
			t.Errorf("auth header %q", r.Header.Get("Authorization"))
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintln(w, `{"data":[{"id":"z"},{"id":"a"},{"id":"m"}]}`)
	}))
	defer srv.Close()
	hostPort := strings.TrimPrefix(srv.URL, "http://")
	parts := strings.Split(hostPort, ":")
	host := parts[0]
	var port int
	if _, err := fmt.Sscanf(parts[1], "%d", &port); err != nil {
		t.Fatalf("Sscanf: %v", err)
	}

	// need config with proxy api key file
	dir := t.TempDir()
	state := t.TempDir()
	t.Setenv("CCP_HOME", dir)
	t.Setenv("CCP_STATE_HOME", state)
	t.Setenv("HOME", t.TempDir())
	// bootstrap to create config
	os.MkdirAll(dir, 0o700)
	// create proxy config
	proxyCfgDir := filepath.Join(dir, "cliproxy")
	os.MkdirAll(proxyCfgDir, 0o700)
	os.WriteFile(filepath.Join(proxyCfgDir, "config.yaml"), []byte("api-keys:\n  - test-key\n"), 0o600)

	cfg := &config.Config{Proxy: config.ProxyConfig{Host: host, Port: port}, Dir: dir}
	// need to set Proxy.ConfigFile empty so it uses default Dir/cliproxy/config.yaml
	ids, err := FetchProxyModels(cfg)
	if err != nil {
		t.Fatalf("fetch: %v", err)
	}
	if len(ids) != 3 || ids[0] != "a" || ids[1] != "m" || ids[2] != "z" {
		t.Fatalf("ids %v", ids)
	}

	// non-200
	srv2 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(500)
	}))
	defer srv2.Close()
	hostPort2 := strings.TrimPrefix(srv2.URL, "http://")
	parts2 := strings.Split(hostPort2, ":")
	host2 := parts2[0]
	var port2 int
	if _, err := fmt.Sscanf(parts2[1], "%d", &port2); err != nil {
		t.Fatalf("Sscanf: %v", err)
	}
	cfg2 := &config.Config{Proxy: config.ProxyConfig{Host: host2, Port: port2}, Dir: dir}
	if _, err := FetchProxyModels(cfg2); err == nil {
		t.Fatalf("expected error for 500")
	}
}

func TestIsBinaryName(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want bool
	}{
		{"cli-proxy-api", "cli-proxy-api", true},
		{"cli-proxy-api64", "cli-proxy-api64", true},
		{"CLIProxyAPI", "CLIProxyAPI", true},
		{"cli_proxy_api", "cli_proxy_api", true},
		{"other", "some-binary", false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := IsBinaryName(tc.in); got != tc.want {
				t.Fatalf("IsBinaryName(%q)=%v want %v", tc.in, got, tc.want)
			}
		})
	}
}

func TestPickAsset(t *testing.T) {
	rel := &GhRelease{
		TagName: "v1.0",
		Assets: []struct {
			Name               string `json:"name"`
			BrowserDownloadURL string `json:"browser_download_url"`
		}{
			{Name: fmt.Sprintf("ccp_v1.0_%s_%s.tar.gz", runtime.GOOS, runtime.GOARCH), BrowserDownloadURL: "https://example.com/a.tar.gz"},
			{Name: fmt.Sprintf("ccp_v1.0_%s_%s.zip", runtime.GOOS, runtime.GOARCH), BrowserDownloadURL: "https://example.com/b.zip"},
		},
	}
	url, err := PickAsset(rel)
	if err != nil {
		t.Fatalf("pick: %v", err)
	}
	if !strings.HasSuffix(url, ".tar.gz") {
		t.Fatalf("expected tar.gz preferred, got %q", url)
	}
	// no matching
	rel2 := &GhRelease{
		TagName: "v1.0",
		Assets: []struct {
			Name               string `json:"name"`
			BrowserDownloadURL string `json:"browser_download_url"`
		}{{Name: "other.deb", BrowserDownloadURL: "https://example.com/other.deb"}},
	}
	if _, err := PickAsset(rel2); err == nil {
		t.Fatalf("expected error")
	}
}

func TestArchVariants(t *testing.T) {
	if got := ArchVariants("amd64"); len(got) != 2 || got[0] != "amd64" {
		t.Fatalf("amd64 %v", got)
	}
	if got := ArchVariants("arm64"); len(got) != 2 {
		t.Fatalf("arm64 %v", got)
	}
	if got := ArchVariants("386"); len(got) != 1 || got[0] != "386" {
		t.Fatalf("386 %v", got)
	}
}

func TestSyncOpenAICompat_AddAndPreserve(t *testing.T) {
	dir := t.TempDir()
	state := t.TempDir()
	home := t.TempDir()
	t.Setenv("CCP_HOME", dir)
	t.Setenv("CCP_STATE_HOME", state)
	t.Setenv("HOME", home)
	t.Setenv("OPENCODE_GO_API_KEY", "sk-test-123")
	os.MkdirAll(dir, 0o700)
	os.MkdirAll(filepath.Join(dir, "cliproxy"), 0o700)
	// Pre-existing config with port and api-keys and unrelated openai entry (use ToSlash for Windows backslash safety)
	existing := "port: 8317\nauth-dir: \"" + filepath.ToSlash(filepath.Join(home, ".cli-proxy-api")) + "\"\napi-keys:\n  - \"existing-key\"\nopenai-compatibility:\n  - name: other-provider\n    base-url: https://other.example/v1\n    api-key-entries:\n      - api-key: other-key\n    models:\n      - name: other-model\n        alias: other-alias\n"
	cfgPath := filepath.Join(dir, "cliproxy", "config.yaml")
	if err := os.WriteFile(cfgPath, []byte(existing), 0o600); err != nil {
		t.Fatalf("write existing: %v", err)
	}
	cfg, err := config.LoadConfig()
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	p := &config.Profile{
		Type:              "cliproxy",
		Model:             "muse-spark-1.2-contributor",
		UpstreamBaseURL:   "https://opencode.ai/zen/go/v1",
		UpstreamAPIKeyEnv: "OPENCODE_GO_API_KEY",
		UpstreamModel:     "muse-spark-1.2-contributor",
	}
	if err := SyncOpenAICompat(cfg, "muse", p); err != nil {
		t.Fatalf("Sync: %v", err)
	}
	data, _ := os.ReadFile(cfgPath)
	// Check new entry exists and old preserved
	if !strings.Contains(string(data), "muse") {
		t.Fatalf("new entry not found: %s", string(data))
	}
	if !strings.Contains(string(data), "other-provider") {
		t.Fatalf("old entry lost: %s", string(data))
	}
	if !strings.Contains(string(data), "https://opencode.ai/zen/go/v1") {
		t.Fatalf("base url not found")
	}
	if !strings.Contains(string(data), "sk-test-123") {
		t.Fatalf("api key not expanded: %s", string(data))
	}
	// Check perms (skip on Windows where chmod is not enforced)
	if runtime.GOOS != "windows" {
		if fi, err := os.Stat(cfgPath); err == nil {
			if fi.Mode().Perm() != 0o600 {
				t.Fatalf("perm %o want 0600", fi.Mode().Perm())
			}
		}
	}
	// Now test IsUpstreamSynced
	synced, reason := IsUpstreamSynced(cfg, "muse", p)
	if !synced {
		t.Fatalf("expected synced, got %q", reason)
	}
}

func TestSyncOpenAICompat_UpdateAndRemove(t *testing.T) {
	dir := t.TempDir()
	state := t.TempDir()
	t.Setenv("CCP_HOME", dir)
	t.Setenv("CCP_STATE_HOME", state)
	t.Setenv("HOME", t.TempDir())
	t.Setenv("KEY_A", "sk-a")
	os.MkdirAll(filepath.Join(dir, "cliproxy"), 0o700)
	cfg, _ := config.LoadConfig()
	p := &config.Profile{
		Type:              "cliproxy",
		Model:             "m1",
		UpstreamBaseURL:   "https://a.example/v1",
		UpstreamAPIKeyEnv: "KEY_A",
	}
	if err := SyncOpenAICompat(cfg, "test", p); err != nil {
		t.Fatalf("sync: %v", err)
	}
	// Update with new base
	t.Setenv("KEY_B", "sk-b")
	p.UpstreamBaseURL = "https://b.example/v1"
	p.UpstreamAPIKeyEnv = "KEY_B"
	if err := SyncOpenAICompat(cfg, "test", p); err != nil {
		t.Fatalf("update sync: %v", err)
	}
	data, _ := os.ReadFile(cfg.ProxyConfigFile())
	if !strings.Contains(string(data), "https://b.example/v1") {
		t.Fatalf("update not reflected: %s", string(data))
	}
	if strings.Contains(string(data), "https://a.example/v1") {
		t.Fatalf("old base still present: %s", string(data))
	}
	// Remove
	if err := RemoveOpenAICompat(cfg, "test"); err != nil {
		t.Fatalf("remove: %v", err)
	}
	data, _ = os.ReadFile(cfg.ProxyConfigFile())
	if strings.Contains(string(data), "test") {
		t.Fatalf("entry not removed: %s", string(data))
	}
	synced, _ := IsUpstreamSynced(cfg, "test", p)
	if synced {
		t.Fatalf("expected not synced after remove")
	}
}

func TestSyncOpenAICompat_NormalizesResponsesEndpoint(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("CCP_HOME", dir)
	t.Setenv("CCP_STATE_HOME", t.TempDir())
	t.Setenv("HOME", t.TempDir())
	t.Setenv("K", "sk-123")
	os.MkdirAll(filepath.Join(dir, "cliproxy"), 0o700)
	cfg, _ := config.LoadConfig()
	p := &config.Profile{
		Type:            "cliproxy",
		Model:           "muse",
		UpstreamBaseURL: "https://opencode.ai/zen/go/v1/responses",
		UpstreamAPIKey:  "sk-123",
	}
	if err := SyncOpenAICompat(cfg, "muse", p); err != nil {
		t.Fatalf("sync: %v", err)
	}
	data, _ := os.ReadFile(cfg.ProxyConfigFile())
	if !strings.Contains(string(data), "https://opencode.ai/zen/go/v1") {
		t.Fatalf("normalized base not found: %s", string(data))
	}
	if strings.Contains(string(data), "/responses") {
		t.Fatalf("should not contain /responses suffix: %s", string(data))
	}
}
