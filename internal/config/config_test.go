//nolint:errcheck,staticcheck,unused
package config

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestLoad_Precedence_ProfilesDirWins(t *testing.T) {
	dir := t.TempDir()
	state := t.TempDir()
	t.Setenv("CCP_HOME", dir)
	t.Setenv("CCP_STATE_HOME", state)
	// write config.toml with inline profile foo model a
	os.MkdirAll(dir, 0o700)
	if err := os.WriteFile(filepath.Join(dir, "config.toml"), []byte(`
default_profile = "foo"
[profiles.foo]
type = "anthropic"
model = "a"
`), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	os.MkdirAll(filepath.Join(dir, "profiles"), 0o700)
	if err := os.WriteFile(filepath.Join(dir, "profiles", "foo.toml"), []byte(`
type = "anthropic"
model = "b"
`), 0o600); err != nil {
		t.Fatalf("write profile: %v", err)
	}
	cfg, err := LoadConfig()
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	if cfg.Profiles["foo"].Model != "b" {
		t.Fatalf("expected model b, got %q", cfg.Profiles["foo"].Model)
	}
}

func TestBootstrap_CreatesSeedsAndNotOverwrite(t *testing.T) {
	dir := t.TempDir()
	state := t.TempDir()
	t.Setenv("CCP_HOME", dir)
	t.Setenv("CCP_STATE_HOME", state)
	t.Setenv("HOME", t.TempDir())
	cfg, err := LoadConfig()
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	if len(cfg.Profiles) != 0 {
		t.Fatalf("expected no seeded profiles, got %d: %v", len(cfg.Profiles), cfg.ProfileNames())
	}
	// config.toml must exist with 0600
	fi, err := os.Stat(filepath.Join(dir, "config.toml"))
	if err != nil {
		t.Fatalf("stat config.toml: %v", err)
	}
	if runtime.GOOS != "windows" && fi.Mode().Perm() != 0o600 {
		t.Fatalf("config perm %o want 600", fi.Mode().Perm())
	}
	// profiles dir exists and is empty
	entries, err := os.ReadDir(filepath.Join(dir, "profiles"))
	if err != nil {
		t.Fatalf("ReadDir profiles: %v", err)
	}
	if len(entries) != 0 {
		t.Fatalf("expected empty profiles dir, got %d entries", len(entries))
	}
	// content should not contain seeded names, but should have default_profile commented
	data, _ := os.ReadFile(filepath.Join(dir, "config.toml"))
	if got := string(data); !strings.Contains(got, "# default_profile") {
		t.Fatalf("config.toml should contain commented default_profile, got %q", got)
	}
	// second load should not overwrite config.toml
	orig, _ := os.ReadFile(filepath.Join(dir, "config.toml"))
	// create a custom profile and ensure it persists
	customPath := filepath.Join(dir, "profiles", "custom.toml")
	if err := os.WriteFile(customPath, []byte("type = \"anthropic\"\nmodel = \"custom\"\n"), 0o600); err != nil {
		t.Fatalf("write custom: %v", err)
	}
	cfg2, err := LoadConfig()
	if err != nil {
		t.Fatalf("second load: %v", err)
	}
	if cfg2.Profiles["custom"].Model != "custom" {
		t.Fatalf("expected custom not overwritten, got %q", cfg2.Profiles["custom"].Model)
	}
	// ensure config.toml not overwritten
	data2, _ := os.ReadFile(filepath.Join(dir, "config.toml"))
	if string(orig) != string(data2) {
		t.Fatalf("config.toml was overwritten")
	}
	if runtime.GOOS != "windows" {
		fi2, _ := os.Stat(customPath)
		if fi2.Mode().Perm() != 0o600 {
			t.Fatalf("custom perm %o want 600", fi2.Mode().Perm())
		}
	}
}

func TestSafeName(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want bool
	}{
		{"empty", "", false},
		{"valid", "glm-1_test.2", true},
		{"uppercase", "GLM", false},
		{"space", "a b", false},
		{"slash", "a/b", false},
		{"dot", ".", true}, // but name == "." should be rejected elsewhere, safeName allows? original checks safeName but also checks name == "." separately in main? For config, safeName allows "." but profile handling also checks. We'll test as true per function.
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := SafeName(tc.in); got != tc.want {
				t.Fatalf("SafeName(%q)=%v want %v", tc.in, got, tc.want)
			}
		})
	}
}

func TestValidatePool(t *testing.T) {
	tests := []struct {
		name    string
		profile Profile
		wantErr bool
	}{
		{name: "empty not pooled", profile: Profile{}, wantErr: false},
		{name: "valid 2 accounts", profile: Profile{Type: "anthropic", Accounts: []Account{{AuthTokenEnv: "A"}, {APIKeyEnv: "B"}}}, wantErr: false},
		{name: "invalid name", profile: Profile{Type: "anthropic", Accounts: []Account{{Name: "Bad Name", AuthTokenEnv: "A"}}}, wantErr: true},
		{name: "unknown strategy", profile: Profile{Type: "anthropic", Routing: &Routing{Strategy: "capacity-weighted"}, Accounts: []Account{{AuthTokenEnv: "A"}}}, wantErr: true},
		{name: "anthropic empty auth", profile: Profile{Type: "anthropic", Accounts: []Account{{}}}, wantErr: true},
		{name: "cliproxy empty auth allowed", profile: Profile{Type: "cliproxy", Accounts: []Account{{}}}, wantErr: false},
		{name: "auth none allowed", profile: Profile{Type: "anthropic", Accounts: []Account{{Auth: "none"}}}, wantErr: false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.profile.ValidatePool()
			if tc.wantErr && err == nil {
				t.Fatalf("expected error")
			}
			if !tc.wantErr && err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
		})
	}
}

func TestRoutingStrategy_Default(t *testing.T) {
	p := Profile{}
	if got := p.RoutingStrategy(); got != "round-robin" {
		t.Fatalf("got %q want round-robin", got)
	}
	p.Routing = &Routing{Strategy: ""}
	if got := p.RoutingStrategy(); got != "round-robin" {
		t.Fatalf("got %q", got)
	}
	p.Routing.Strategy = "round-robin"
	if got := p.RoutingStrategy(); got != "round-robin" {
		t.Fatalf("got %q", got)
	}
}

func TestProxyConfigDefaults(t *testing.T) {
	dir := t.TempDir()
	state := t.TempDir()
	t.Setenv("CCP_HOME", dir)
	t.Setenv("CCP_STATE_HOME", state)
	t.Setenv("HOME", t.TempDir())
	cfg, err := LoadConfig()
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	if cfg.Proxy.Host != "127.0.0.1" {
		t.Fatalf("host %q", cfg.Proxy.Host)
	}
	if cfg.Proxy.Port != 8317 {
		t.Fatalf("port %d", cfg.Proxy.Port)
	}
	if cfg.Proxy.StartTimeoutSecs != 20 {
		t.Fatalf("timeout %d", cfg.Proxy.StartTimeoutSecs)
	}
	if !cfg.Proxy.Autostart() {
		t.Fatalf("autostart default true")
	}
}

func TestLoad_InlineVsFile(t *testing.T) {
	// ensure file wins even when inline has more fields
	dir := t.TempDir()
	state := t.TempDir()
	t.Setenv("CCP_HOME", dir)
	t.Setenv("CCP_STATE_HOME", state)
	os.MkdirAll(dir, 0o700)
	os.WriteFile(filepath.Join(dir, "config.toml"), []byte(`
[profiles.bar]
type = "anthropic"
model = "inline"
description = "inline desc"
`), 0o600)
	os.MkdirAll(filepath.Join(dir, "profiles"), 0o700)
	os.WriteFile(filepath.Join(dir, "profiles", "bar.toml"), []byte(`
type = "cliproxy"
model = "file"
description = "file desc"
`), 0o600)
	cfg, _ := LoadConfig()
	if cfg.Profiles["bar"].Type != "cliproxy" || cfg.Profiles["bar"].Model != "file" {
		t.Fatalf("file should win, got %+v", cfg.Profiles["bar"])
	}
}

func TestUpstreamFields_LoadAndRoundTrip(t *testing.T) {
	dir := t.TempDir()
	state := t.TempDir()
	t.Setenv("CCP_HOME", dir)
	t.Setenv("CCP_STATE_HOME", state)
	os.MkdirAll(dir, 0o700)
	os.MkdirAll(filepath.Join(dir, "profiles"), 0o700)
	tomlContent := `
type = "cliproxy"
model = "muse-spark-1.2-contributor"
upstream_base_url = "https://opencode.ai/zen/go/v1"
upstream_api_key_env = "OPENCODE_GO_API_KEY"
upstream_model = "muse-spark-1.2-contributor"
upstream_model_alias = "muse"
upstream_name = "opencode-go"
`
	if err := os.WriteFile(filepath.Join(dir, "profiles", "muse.toml"), []byte(tomlContent), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	cfg, err := LoadConfig()
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	p := cfg.Profiles["muse"]
	if !p.HasUpstream() {
		t.Fatalf("expected HasUpstream true")
	}
	if p.UpstreamBaseURL != "https://opencode.ai/zen/go/v1" {
		t.Fatalf("base %q", p.UpstreamBaseURL)
	}
	if p.UpstreamAPIKeyEnv != "OPENCODE_GO_API_KEY" {
		t.Fatalf("key env %q", p.UpstreamAPIKeyEnv)
	}
	if p.UpstreamModel != "muse-spark-1.2-contributor" {
		t.Fatalf("model %q", p.UpstreamModel)
	}
	if p.UpstreamName != "opencode-go" {
		t.Fatalf("name %q", p.UpstreamName)
	}
	// Validation should pass
	if err := p.ValidatePool(); err != nil {
		t.Fatalf("ValidatePool should pass: %v", err)
	}
	// Test inline vs file precedence already covered, but add inline upstream
	os.WriteFile(filepath.Join(dir, "config.toml"), []byte(`
[profiles.inlineUp]
type = "cliproxy"
model = "test"
upstream_base_url = "https://example.com/v1"
upstream_api_key = "sk-test"
`), 0o600)
	cfg2, _ := LoadConfig()
	if cfg2.Profiles["inlineUp"].UpstreamBaseURL != "https://example.com/v1" {
		t.Fatalf("inline upstream not loaded")
	}
}

func TestUpstreamValidation(t *testing.T) {
	cases := []struct {
		name    string
		profile Profile
		wantErr bool
	}{
		{
			name: "missing auth",
			profile: Profile{
				Type:            "cliproxy",
				Model:           "m",
				UpstreamBaseURL: "https://example.com/v1",
			},
			wantErr: true,
		},
		{
			name: "bad url",
			profile: Profile{
				Type:            "cliproxy",
				Model:           "m",
				UpstreamBaseURL: "not-a-url",
				UpstreamAPIKey:  "sk-123",
			},
			wantErr: true,
		},
		{
			name: "wrong type",
			profile: Profile{
				Type:            "anthropic",
				Model:           "m",
				UpstreamBaseURL: "https://example.com/v1",
				UpstreamAPIKey:  "sk-123",
			},
			wantErr: true,
		},
		{
			name: "valid",
			profile: Profile{
				Type:            "cliproxy",
				Model:           "m",
				UpstreamBaseURL: "https://example.com/v1",
				UpstreamAPIKey:  "sk-123",
			},
			wantErr: false,
		},
		{
			name: "valid env var",
			profile: Profile{
				Type:              "cliproxy",
				Model:             "m",
				UpstreamBaseURL:   "https://example.com/v1",
				UpstreamAPIKeyEnv: "MY_KEY",
			},
			wantErr: false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tc.profile.Normalize()
			err := tc.profile.ValidatePool()
			if tc.wantErr && err == nil {
				t.Fatalf("expected error")
			}
			if !tc.wantErr && err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
		})
	}
}

func TestUpstreamMixedPoolRejected(t *testing.T) {
	p := Profile{
		Type:            "cliproxy",
		Model:           "m",
		UpstreamBaseURL: "https://example.com/v1",
		UpstreamAPIKey:  "sk-123",
		Accounts: []Account{
			{Name: "a1", UpstreamBaseURL: "https://example.com/v1", UpstreamAPIKey: "sk-a1"},
			{Name: "a2"}, // no upstream -> mixed
		},
	}
	p.Normalize()
	if err := p.ValidatePool(); err == nil {
		t.Fatalf("expected mixed pool error")
	}
	p2 := Profile{
		Type:            "cliproxy",
		Model:           "m",
		UpstreamBaseURL: "https://example.com/v1",
		UpstreamAPIKey:  "sk-123",
		Accounts: []Account{
			{Name: "a1", UpstreamBaseURL: "https://example.com/v1", UpstreamAPIKey: "sk-a1"},
			{Name: "a2", UpstreamBaseURL: "https://example.com/v1", UpstreamAPIKey: "sk-a2"},
		},
	}
	p2.Normalize()
	if err := p2.ValidatePool(); err != nil {
		t.Fatalf("unexpected error for uniform pool: %v", err)
	}
}
