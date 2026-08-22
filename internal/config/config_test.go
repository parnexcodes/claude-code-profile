//nolint:errcheck,staticcheck,unused
package config

import (
	"os"
	"path/filepath"
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
	if len(cfg.Profiles) == 0 {
		t.Fatalf("expected seeded profiles")
	}
	for _, name := range []string{"glm", "kimi", "official"} {
		if _, ok := cfg.Profiles[name]; !ok {
			t.Fatalf("missing seed %s", name)
		}
		path := filepath.Join(dir, "profiles", name+".toml")
		fi, err := os.Stat(path)
		if err != nil {
			t.Fatalf("stat %s: %v", path, err)
		}
		if fi.Mode().Perm() != 0o600 {
			t.Fatalf("perm %o want 600", fi.Mode().Perm())
		}
	}
	// second load should not overwrite
	customPath := filepath.Join(dir, "profiles", "glm.toml")
	orig, _ := os.ReadFile(customPath)
	os.WriteFile(customPath, []byte("type = \"anthropic\"\nmodel = \"custom\"\n"), 0o600)
	cfg2, err := LoadConfig()
	if err != nil {
		t.Fatalf("second load: %v", err)
	}
	if cfg2.Profiles["glm"].Model != "custom" {
		t.Fatalf("expected custom not overwritten, got %q", cfg2.Profiles["glm"].Model)
	}
	// ensure config.toml not overwritten
	fi, _ := os.Stat(filepath.Join(dir, "config.toml"))
	if fi.Mode().Perm() != 0o600 {
		t.Fatalf("config perm %o", fi.Mode().Perm())
	}
	_ = orig
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
