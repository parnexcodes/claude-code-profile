package testutil

import (
	"os"
	"path/filepath"
	"testing"
)

// TempCCPHome creates a temporary config and state directory, sets CCP_HOME
// and CCP_STATE_HOME for the duration of the test, and returns their paths.
// It also sets NO_COLOR=1 to disable ANSI output in tests.
func TempCCPHome(t *testing.T) (configDir, stateDir string) {
	t.Helper()
	configDir = t.TempDir()
	stateDir = t.TempDir()
	t.Setenv("CCP_HOME", configDir)
	t.Setenv("CCP_STATE_HOME", stateDir)
	t.Setenv("NO_COLOR", "1")
	return configDir, stateDir
}

// TempHome creates a temporary HOME directory and sets HOME for the test.
// Use for tests that exercise ~ expansion or settings.json reads.
func TempHome(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	return home
}

// MustWriteFile writes data to path with perm, creating parent dirs.
// It fails the test on error.
func MustWriteFile(t *testing.T, path, body string, perm os.FileMode) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatalf("MustWriteFile mkdir %s: %v", path, err)
	}
	if err := os.WriteFile(path, []byte(body), perm); err != nil {
		t.Fatalf("MustWriteFile %s: %v", path, err)
	}
}

// Setenv sets an env var for the test (wrapper around t.Setenv for symmetry).
func Setenv(t *testing.T, key, val string) {
	t.Helper()
	t.Setenv(key, val)
}
