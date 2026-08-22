//nolint:errcheck,staticcheck,unused
package cli

import (
	"os"
	"path/filepath"
	"testing"
)

func TestDoctor_FindEnvConflicts(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("NO_COLOR", "1")
	dir := t.TempDir()
	t.Setenv("CCP_HOME", dir)
	t.Setenv("CCP_STATE_HOME", t.TempDir())
	os.MkdirAll(filepath.Join(home, ".claude"), 0o700)
	os.WriteFile(filepath.Join(home, ".claude", "settings.json"), []byte(`{"env":{"ANTHROPIC_API_KEY":"leaked"}}`), 0o600)
	// create a profile that would set ANTHROPIC_API_KEY
	os.MkdirAll(filepath.Join(dir, "profiles"), 0o700)
	os.WriteFile(filepath.Join(dir, "config.toml"), []byte(``), 0o600)
	os.WriteFile(filepath.Join(dir, "profiles", "test.toml"), []byte(`
type="anthropic"
model="m"
api_key_env="TEST_KEY"
`), 0o600)
	t.Setenv("TEST_KEY", "val")
	conflicts := findEnvConflicts([]string{"ANTHROPIC_API_KEY"})
	if len(conflicts) == 0 {
		t.Fatalf("expected conflict")
	}
	found := false
	for _, c := range conflicts {
		if c.Key == "ANTHROPIC_API_KEY" {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected ANTHROPIC_API_KEY conflict, got %#v", conflicts)
	}
}
