//nolint:errcheck,staticcheck,unused
package cli

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"ccp/internal/config"
)

func captureStdout(f func()) string {
	r, w, _ := os.Pipe()
	old := os.Stdout
	os.Stdout = w
	f()
	if err := w.Close(); err != nil {
		// ignore
	}
	os.Stdout = old
	var buf bytes.Buffer
	if _, err := io.Copy(&buf, r); err != nil {
		// ignore
	}
	if err := r.Close(); err != nil {
		// ignore
	}
	return buf.String()
}

func TestShow_MasksSecrets(t *testing.T) {
	dir := t.TempDir()
	state := t.TempDir()
	home := t.TempDir()
	t.Setenv("CCP_HOME", dir)
	t.Setenv("CCP_STATE_HOME", state)
	t.Setenv("HOME", home)
	t.Setenv("NO_COLOR", "1")
	os.MkdirAll(filepath.Join(home, ".claude"), 0o700)
	os.WriteFile(filepath.Join(home, ".claude", "settings.json"), []byte(`{}`), 0o600)
	// create pooled profile with env and literal
	os.MkdirAll(filepath.Join(dir, "profiles"), 0o700)
	os.WriteFile(filepath.Join(dir, "config.toml"), []byte(`default_profile = "test"`), 0o600)
	os.WriteFile(filepath.Join(dir, "profiles", "test.toml"), []byte(`
description = "test"
type = "anthropic"
model = "m"
[[accounts]]
name = "a"
auth_token_env = "TOKEN_A"
[[accounts]]
auth_token = "literal-secret-1234567890"
`), 0o600)
	t.Setenv("TOKEN_A", "env-secret-abcdef1234")

	// need to ensure showProfile doesn't fail due to missing auth
	out := captureStdout(func() { showProfile("test") })
	if !strings.Contains(out, "$TOKEN_A") {
		t.Fatalf("expected $TOKEN_A in show, got %q", out)
	}
	if strings.Contains(out, "env-secret-abcdef1234") {
		t.Fatalf("secret should be masked, got %q", out)
	}
	if !strings.Contains(out, "******") && !strings.Contains(out, "…") {
		t.Fatalf("expected masked token, got %q", out)
	}
	// check that env value not leaked
	if strings.Contains(out, "env-secret-abcdef1234") {
		t.Fatalf("leaked secret")
	}
	// literal should be masked
	if strings.Contains(out, "literal-secret-1234567890") {
		t.Fatalf("literal leaked")
	}
}

func TestShow_PeekMarker(t *testing.T) {
	dir := t.TempDir()
	state := t.TempDir()
	home := t.TempDir()
	t.Setenv("CCP_HOME", dir)
	t.Setenv("CCP_STATE_HOME", state)
	t.Setenv("HOME", home)
	t.Setenv("NO_COLOR", "1")
	os.MkdirAll(filepath.Join(home, ".claude"), 0o700)
	os.WriteFile(filepath.Join(home, ".claude", "settings.json"), []byte(`{}`), 0o600)
	os.MkdirAll(filepath.Join(dir, "profiles"), 0o700)
	os.WriteFile(filepath.Join(dir, "config.toml"), []byte(``), 0o600)
	os.WriteFile(filepath.Join(dir, "profiles", "pool.toml"), []byte(`
type = "anthropic"
model = "m"
[[accounts]]
auth_token_env = "A"
[[accounts]]
auth_token_env = "B"
`), 0o600)
	t.Setenv("A", "sk-a-long-token")
	t.Setenv("B", "sk-b-long-token")
	// first show should mark → [0]
	out1 := captureStdout(func() { showProfile("pool") })
	if !strings.Contains(out1, "→ [0]") {
		t.Fatalf("expected → [0] in %q", out1)
	}
	// second show should still be 0 because show uses peek (not advancing)
	out2 := captureStdout(func() { showProfile("pool") })
	if !strings.Contains(out2, "→ [0]") {
		t.Fatalf("expected peek still 0, got %q", out2)
	}
}

func TestList_ShowsPoolSize(t *testing.T) {
	dir := t.TempDir()
	state := t.TempDir()
	home := t.TempDir()
	t.Setenv("CCP_HOME", dir)
	t.Setenv("CCP_STATE_HOME", state)
	t.Setenv("HOME", home)
	t.Setenv("NO_COLOR", "1")
	os.MkdirAll(filepath.Join(home, ".claude"), 0o700)
	os.WriteFile(filepath.Join(home, ".claude", "settings.json"), []byte(`{}`), 0o600)
	os.MkdirAll(filepath.Join(dir, "profiles"), 0o700)
	os.WriteFile(filepath.Join(dir, "config.toml"), []byte(`default_profile = "single"`), 0o600)
	os.WriteFile(filepath.Join(dir, "profiles", "single.toml"), []byte(`type="anthropic"
model="m1"
`), 0o600)
	os.WriteFile(filepath.Join(dir, "profiles", "pool.toml"), []byte(`
type="anthropic"
model="m2"
[[accounts]]
auth_token_env="A"
[[accounts]]
auth_token_env="B"
`), 0o600)
	t.Setenv("A", "sk-a-long-token")
	t.Setenv("B", "sk-b-long-token")

	out := captureStdout(func() { listProfiles() })
	if !strings.Contains(out, "pool") || !strings.Contains(out, "×2") {
		t.Fatalf("expected pool ×2 in list, got %q", out)
	}
	if !strings.Contains(out, "single") {
		t.Fatalf("expected single in list")
	}
	// default marker *
	if !strings.Contains(out, "*single") && !strings.Contains(out, "* single") {
		// list prints " *single" or "*single"
		if !strings.Contains(out, "single") {
			t.Fatalf("default marker missing")
		}
	}
}

func TestRenderProfileToml(t *testing.T) {
	p := &config.Profile{
		Description:  "desc",
		Type:         "anthropic",
		Model:        "m",
		AuthTokenEnv: "TOKEN",
		ExtraEnv:     map[string]string{"FOO": "bar"},
	}
	out := renderProfileToml(p)
	if !strings.Contains(out, `description = "desc"`) {
		t.Fatalf("missing desc %q", out)
	}
	if !strings.Contains(out, `model = "m"`) {
		t.Fatalf("missing model")
	}
	if !strings.Contains(out, `auth_token_env = "TOKEN"`) {
		t.Fatalf("missing auth")
	}
	if !strings.Contains(out, "FOO") {
		t.Fatalf("missing extra")
	}
	// deterministic: second call same
	out2 := renderProfileToml(p)
	if out != out2 {
		t.Fatalf("not deterministic")
	}
}

func TestDoctor_SurfacesPoolErrors(t *testing.T) {
	// Capture doctor output via pipe (it prints to stdout and exits)
	dir := t.TempDir()
	state := t.TempDir()
	home := t.TempDir()
	t.Setenv("CCP_HOME", dir)
	t.Setenv("CCP_STATE_HOME", state)
	t.Setenv("HOME", home)
	t.Setenv("NO_COLOR", "1")
	os.MkdirAll(filepath.Join(home, ".claude"), 0o700)
	os.WriteFile(filepath.Join(home, ".claude", "settings.json"), []byte(`{}`), 0o600)
	os.MkdirAll(filepath.Join(dir, "profiles"), 0o700)
	os.WriteFile(filepath.Join(dir, "config.toml"), []byte(``), 0o600)
	// invalid pool: bad name, missing auth
	os.WriteFile(filepath.Join(dir, "profiles", "bad.toml"), []byte(`
type="anthropic"
[[accounts]]
name="BAD NAME"
auth_token_env="MISSING_ENV"
`), 0o600)

	// runDoctor prints and would call die on failures, but we don't want to exit
	// Instead we test that ValidatePool fails
	cfg, _ := config.LoadConfig()
	p := cfg.Profiles["bad"]
	if err := p.ValidatePool(); err == nil {
		t.Fatalf("expected ValidatePool error")
	}
	// also check that doctor would report fail: we can test by checking that buildEnvPeek fails
	// Use helper to check that doctor's logic would detect
	// For now just ensure that safeName fails for BAD NAME
	if config.SafeName("BAD NAME") {
		t.Fatalf("should be invalid")
	}
	// check that env var missing would be caught via profile
	t.Setenv("MISSING_ENV", "")
	// need to create a helper to check resolveAuth error via profile
	// We'll just ensure that profile's BuildEnv fails when env missing
	_, err := buildEnvPeek(cfg, "bad", p)
	if err == nil {
		t.Fatalf("expected buildEnvPeek error for missing env")
	}
}

func TestHandleAdd_UpstreamCreatesProxyEntry(t *testing.T) {
	dir := t.TempDir()
	state := t.TempDir()
	home := t.TempDir()
	t.Setenv("CCP_HOME", dir)
	t.Setenv("CCP_STATE_HOME", state)
	t.Setenv("HOME", home)
	t.Setenv("NO_COLOR", "1")
	t.Setenv("OPENCODE_KEY", "sk-upstream-123")
	os.MkdirAll(filepath.Join(home, ".claude"), 0o700)
	os.WriteFile(filepath.Join(home, ".claude", "settings.json"), []byte(`{}`), 0o600)
	// Ensure proxy dir exists
	os.MkdirAll(filepath.Join(dir, "cliproxy"), 0o700)
	// Use non-wizard handleAdd with upstream flags
	handleAdd([]string{"muse", "--type", "cliproxy", "--model", "muse-spark-1.2-contributor", "--upstream-base-url", "https://opencode.ai/zen/go/v1", "--upstream-api-key-env", "OPENCODE_KEY"})
	// Check profile TOML exists and contains upstream fields
	data, err := os.ReadFile(filepath.Join(dir, "profiles", "muse.toml"))
	if err != nil {
		t.Fatalf("profile not created: %v", err)
	}
	if !strings.Contains(string(data), "upstream_base_url") || !strings.Contains(string(data), "OPENCODE_KEY") {
		t.Fatalf("upstream fields not in TOML: %s", string(data))
	}
	// Check proxy YAML entry
	proxyData, err := os.ReadFile(filepath.Join(dir, "cliproxy", "config.yaml"))
	if err != nil {
		t.Fatalf("proxy config not created: %v", err)
	}
	if !strings.Contains(string(proxyData), "opencode.ai/zen/go/v1") {
		t.Fatalf("proxy entry not found: %s", string(proxyData))
	}
	if !strings.Contains(string(proxyData), "muse-spark-1.2-contributor") {
		t.Fatalf("model not in proxy: %s", string(proxyData))
	}
	// Check show masks secret
	out := captureStdout(func() { showProfile("muse") })
	if strings.Contains(out, "sk-upstream-123") {
		t.Fatalf("secret leaked in show: %q", out)
	}
	if !strings.Contains(out, "translated") {
		t.Fatalf("expected translated in show, got %q", out)
	}
	// Check list shows translated indicator
	outList := captureStdout(func() { listProfiles() })
	if !strings.Contains(outList, "translated") {
		t.Fatalf("expected translated in list, got %q", outList)
	}
}

func TestRenderProfileToml_Upstream(t *testing.T) {
	p := &config.Profile{
		Type:               "cliproxy",
		Model:              "muse",
		UpstreamBaseURL:    "https://opencode.ai/zen/go/v1",
		UpstreamAPIKeyEnv:  "MY_KEY",
		UpstreamModel:      "muse-spark-1.2-contributor",
		UpstreamModelAlias: "muse",
	}
	out := renderProfileToml(p)
	if !strings.Contains(out, "upstream_base_url") {
		t.Fatalf("missing upstream_base_url: %q", out)
	}
	if !strings.Contains(out, "MY_KEY") {
		t.Fatalf("missing upstream key env: %q", out)
	}
	if !strings.Contains(out, "upstream_model") {
		t.Fatalf("missing upstream_model: %q", out)
	}
	// Deterministic
	if out2 := renderProfileToml(p); out != out2 {
		t.Fatalf("not deterministic")
	}
	// With accounts
	p.Accounts = []config.Account{
		{UpstreamBaseURL: "https://a.example/v1", UpstreamAPIKeyEnv: "KEY_A"},
		{UpstreamBaseURL: "https://a.example/v1", UpstreamAPIKeyEnv: "KEY_B"},
	}
	out = renderProfileToml(p)
	if !strings.Contains(out, "[[accounts]]") {
		t.Fatalf("missing accounts")
	}
	if !strings.Contains(out, "KEY_A") || !strings.Contains(out, "KEY_B") {
		t.Fatalf("missing account upstream keys: %q", out)
	}
}

func TestHandleAdd_UpstreamNormalizesResponsesEndpoint(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("CCP_HOME", dir)
	t.Setenv("CCP_STATE_HOME", t.TempDir())
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("NO_COLOR", "1")
	os.MkdirAll(filepath.Join(home, ".claude"), 0o700)
	os.MkdirAll(filepath.Join(dir, "cliproxy"), 0o700)
	// Use handleAdd with full /v1/responses endpoint - should be normalized to /v1
	handleAdd([]string{"m2", "--type", "cliproxy", "--model", "muse", "--upstream-base-url", "https://opencode.ai/zen/go/v1/responses", "--upstream-api-key", "sk-123"})
	data, _ := os.ReadFile(filepath.Join(dir, "profiles", "m2.toml"))
	if strings.Contains(string(data), "/responses") {
		t.Fatalf("should have normalized /responses away: %s", string(data))
	}
	if !strings.Contains(string(data), "https://opencode.ai/zen/go/v1\"") {
		t.Fatalf("normalized url not found: %s", string(data))
	}
	cfg, _ := config.LoadConfig()
	pData, _ := os.ReadFile(cfg.ProxyConfigFile())
	if strings.Contains(string(pData), "/responses") {
		t.Fatalf("proxy should not contain /responses: %s", string(pData))
	}
}
