//nolint:errcheck,staticcheck,unused
package profile

import (
	"os"
	"path/filepath"
	"testing"

	"ccp/internal/config"
)

func TestManagedVarsStripped(t *testing.T) {
	cfg := &config.Config{Proxy: config.ProxyConfig{Host: "127.0.0.1", Port: 8317}}
	p := &config.Profile{Type: "anthropic", Model: "test-model", ExtraEnv: map[string]string{"CUSTOM": "val"}}
	// set stray env
	t.Setenv("ANTHROPIC_API_KEY", "should-be-stripped")
	t.Setenv("ANTHROPIC_AUTH_TOKEN", "should-be-stripped")
	t.Setenv("CLAUDE_CODE_USE_BEDROCK", "1")
	t.Setenv("CUSTOM", "old")
	built, err := BuildEnv(cfg, "test", p)
	if err != nil {
		t.Fatalf("BuildEnv: %v", err)
	}
	// assemble with stray environ
	environ := []string{"ANTHROPIC_API_KEY=should-be-stripped", "ANTHROPIC_AUTH_TOKEN=should-be-stripped", "CLAUDE_CODE_USE_BEDROCK=1", "CUSTOM=old", "OTHER=keep"}
	out := AssembleEnv(environ, built.Strips, built.Sets)
	for _, kv := range out {
		if kv == "ANTHROPIC_API_KEY=should-be-stripped" || kv == "CLAUDE_CODE_USE_BEDROCK=1" {
			t.Fatalf("stray not stripped: %q", kv)
		}
		if kv == "CUSTOM=old" {
			t.Fatalf("CUSTOM old not stripped, got %q", kv)
		}
	}
	// should contain custom new val
	found := false
	for _, kv := range out {
		if kv == "CUSTOM=val" {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected CUSTOM=val in %v", out)
	}
	// OTHER should be kept
	foundOther := false
	for _, kv := range out {
		if kv == "OTHER=keep" {
			foundOther = true
		}
	}
	if !foundOther {
		t.Fatalf("OTHER should be kept")
	}
}

func TestResolveAuth_Priority(t *testing.T) {
	// auth_token_env > api_key_env > literal > proxy api-keys[0] > none
	dir := t.TempDir()
	state := t.TempDir()
	t.Setenv("CCP_HOME", dir)
	t.Setenv("CCP_STATE_HOME", state)
	t.Setenv("HOME", t.TempDir())
	// create proxy config with api-keys[0]
	proxyDir := filepath.Join(dir, "cliproxy")
	os.MkdirAll(proxyDir, 0o700)
	os.WriteFile(filepath.Join(proxyDir, "config.yaml"), []byte("api-keys:\n  - proxy-key\n"), 0o600)
	cfg := &config.Config{Dir: dir, Proxy: config.ProxyConfig{Host: "127.0.0.1", Port: 8317}}

	t.Setenv("TOKEN_ENV", "env-token")
	t.Setenv("KEY_ENV", "env-key")

	tests := []struct {
		name    string
		account config.Account
		wantVar string
		wantSrc string
	}{
		{name: "auth_token_env wins", account: config.Account{AuthTokenEnv: "TOKEN_ENV", APIKeyEnv: "KEY_ENV"}, wantVar: "ANTHROPIC_AUTH_TOKEN", wantSrc: "$TOKEN_ENV"},
		{name: "api_key_env", account: config.Account{APIKeyEnv: "KEY_ENV"}, wantVar: "ANTHROPIC_API_KEY", wantSrc: "$KEY_ENV"},
		{name: "auth_token literal", account: config.Account{AuthToken: "literal"}, wantVar: "ANTHROPIC_AUTH_TOKEN", wantSrc: "auth_token (config)"},
		{name: "api_key literal", account: config.Account{APIKey: "literal"}, wantVar: "ANTHROPIC_API_KEY", wantSrc: "api_key (config)"},
		{name: "proxy default", account: config.Account{}, wantVar: "ANTHROPIC_AUTH_TOKEN", wantSrc: "proxy config api-keys[0]"},
		{name: "none", account: config.Account{Auth: "none"}, wantVar: "", wantSrc: ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			// cliproxy type for proxy default case
			profileType := "cliproxy"
			if tc.name == "none" {
				profileType = "anthropic"
			}
			res, err := ResolveAccountAuth(&tc.account, cfg, profileType)
			if err != nil {
				t.Fatalf("err: %v", err)
			}
			if tc.wantVar == "" && res != nil {
				t.Fatalf("expected nil, got %#v", res)
			}
			if tc.wantVar != "" {
				if res == nil || res.EnvVar != tc.wantVar || res.Source != tc.wantSrc {
					t.Fatalf("got %#v want var %q src %q", res, tc.wantVar, tc.wantSrc)
				}
			}
		})
	}
}

func TestBuildEnv_SingleVsPooled(t *testing.T) {
	dir := t.TempDir()
	state := t.TempDir()
	home := t.TempDir()
	t.Setenv("CCP_HOME", dir)
	t.Setenv("CCP_STATE_HOME", state)
	t.Setenv("HOME", home)
	os.MkdirAll(filepath.Join(home, ".claude"), 0o700)
	os.WriteFile(filepath.Join(home, ".claude", "settings.json"), []byte(`{}`), 0o600)
	cfg := &config.Config{Dir: dir, Proxy: config.ProxyConfig{Host: "127.0.0.1", Port: 8317}}

	// single
	pSingle := &config.Profile{Type: "anthropic", Model: "m1", Auth: "none"}
	built, err := BuildEnv(cfg, "single", pSingle)
	if err != nil {
		t.Fatalf("BuildEnv single: %v", err)
	}
	if built.PoolSize != 0 || built.SelectedIdx != -1 {
		t.Fatalf("single pool %d idx %d", built.PoolSize, built.SelectedIdx)
	}
	// pooled
	t.Setenv("A", "a-val")
	t.Setenv("B", "b-val")
	pPooled := &config.Profile{Type: "anthropic", Model: "m1", Accounts: []config.Account{{AuthTokenEnv: "A"}, {APIKeyEnv: "B"}}}
	built2, err := BuildEnv(cfg, "pooled", pPooled)
	if err != nil {
		t.Fatalf("BuildEnv pooled: %v", err)
	}
	if built2.PoolSize != 2 {
		t.Fatalf("poolsize %d", built2.PoolSize)
	}
	if built2.SelectedIdx < 0 || built2.SelectedIdx > 1 {
		t.Fatalf("idx %d", built2.SelectedIdx)
	}
}

func TestBuildEnv_Expansion(t *testing.T) {
	dir := t.TempDir()
	state := t.TempDir()
	home := t.TempDir()
	t.Setenv("CCP_HOME", dir)
	t.Setenv("CCP_STATE_HOME", state)
	t.Setenv("HOME", home)
	os.MkdirAll(filepath.Join(home, ".claude"), 0o700)
	os.WriteFile(filepath.Join(home, ".claude", "settings.json"), []byte(`{}`), 0o600)
	t.Setenv("HOST", "example.com")
	t.Setenv("TOKEN", "secret123")
	cfg := &config.Config{Dir: dir, Proxy: config.ProxyConfig{Host: "127.0.0.1", Port: 8317}}
	p := &config.Profile{Type: "anthropic", Model: "m", BaseURL: "https://${HOST}/v1", AuthToken: "${TOKEN}"}
	built, err := BuildEnv(cfg, "test", p)
	if err != nil {
		t.Fatalf("BuildEnv: %v", err)
	}
	if built.URL != "https://example.com/v1" {
		t.Fatalf("url %q", built.URL)
	}
	if built.Sets["ANTHROPIC_AUTH_TOKEN"] != "secret123" {
		t.Fatalf("auth %q", built.Sets["ANTHROPIC_AUTH_TOKEN"])
	}
	// unknown var untouched
	p2 := &config.Profile{Type: "anthropic", Model: "m", BaseURL: "https://$UNKNOWN/v1"}
	built2, _ := BuildEnv(cfg, "test2", p2)
	if built2.URL != "https://$UNKNOWN/v1" {
		t.Fatalf("unknown should be untouched, got %q", built2.URL)
	}
}

func TestEffectiveBaseURL_PerAccountOverride(t *testing.T) {
	cfg := &config.Config{Proxy: config.ProxyConfig{Host: "127.0.0.1", Port: 8317}}
	p := &config.Profile{Type: "anthropic", BaseURL: "https://shared.example/v1"}
	a := &config.Account{BaseURL: "https://per.example/v1"}
	if got := EffectiveBaseURLForAccount(p, cfg, a); got != "https://per.example/v1" {
		t.Fatalf("got %q", got)
	}
	if got := EffectiveBaseURLForAccount(p, cfg, &config.Account{}); got != "https://shared.example/v1" {
		t.Fatalf("got %q", got)
	}
	// cliproxy default
	p2 := &config.Profile{Type: "cliproxy", BaseURL: ""}
	cfg2 := &config.Config{Proxy: config.ProxyConfig{Host: "1.2.3.4", Port: 9999}}
	if got := EffectiveBaseURL(p2, cfg2); got != "http://1.2.3.4:9999" {
		t.Fatalf("got %q", got)
	}
}

func TestAssembleEnv_DeterministicAndSorted(t *testing.T) {
	sets := map[string]string{"B": "2", "A": "1", "C": "3"}
	strips := []string{"OLD"}
	environ := []string{"OLD=old", "KEEP=keep"}
	out := AssembleEnv(environ, strips, sets)
	// OLD should be dropped, KEEP kept, sets sorted A, B, C
	expected := []string{"KEEP=keep", "A=1", "B=2", "C=3"}
	if len(out) != len(expected) {
		t.Fatalf("out %v want %v", out, expected)
	}
	for i, v := range expected {
		if out[i] != v {
			t.Fatalf("index %d got %q want %q", i, out[i], v)
		}
	}
}

func TestOnlySelectedCredentialPresent(t *testing.T) {
	dir := t.TempDir()
	state := t.TempDir()
	home := t.TempDir()
	t.Setenv("CCP_HOME", dir)
	t.Setenv("CCP_STATE_HOME", state)
	t.Setenv("HOME", home)
	os.MkdirAll(filepath.Join(home, ".claude"), 0o700)
	os.WriteFile(filepath.Join(home, ".claude", "settings.json"), []byte(`{}`), 0o600)
	t.Setenv("CODEX_A", "token-a")
	t.Setenv("CODEX_B", "key-b")
	t.Setenv("CODEX_C", "token-c")
	cfg := &config.Config{Dir: dir, Proxy: config.ProxyConfig{Host: "127.0.0.1", Port: 8317}}
	p := &config.Profile{
		Type: "anthropic", Model: "m",
		Accounts: []config.Account{
			{AuthTokenEnv: "CODEX_A"},
			{APIKeyEnv: "CODEX_B"},
			{AuthTokenEnv: "CODEX_C"},
		},
	}
	// Call BuildEnv 3 times, should cycle, and each should contain only one credential
	for i := 0; i < 3; i++ {
		built, err := BuildEnv(cfg, "pool", p)
		if err != nil {
			t.Fatalf("BuildEnv: %v", err)
		}
		count := 0
		for k := range built.Sets {
			if k == "ANTHROPIC_AUTH_TOKEN" || k == "ANTHROPIC_API_KEY" {
				count++
			}
		}
		if count != 1 {
			t.Fatalf("iteration %d expected 1 credential, got %d sets %v", i, count, built.Sets)
		}
		// ensure no other account's env is present
		// e.g., if selected is A, should not have B's value
	}
}
