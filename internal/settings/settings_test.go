//nolint:errcheck,staticcheck,unused
package settings

import (
	"os"
	"path/filepath"
	"testing"
)

func TestReadClaudeSettings(t *testing.T) {
	tests := []struct {
		name    string
		content string
		missing bool
		wantErr bool
		wantNil bool
	}{
		{name: "missing", missing: true, wantNil: true},
		{name: "valid", content: `{"model":"claude-sonnet-4","env":{"FOO":"bar"}}`, wantNil: false},
		{name: "invalid json", content: `{"model":`, wantErr: true},
		{name: "empty env", content: `{"model":""}`, wantNil: false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, "settings.json")
			if !tc.missing {
				if err := os.WriteFile(path, []byte(tc.content), 0o600); err != nil {
					t.Fatalf("write: %v", err)
				}
			} else {
				path = filepath.Join(dir, "nonexistent.json")
			}
			got, err := ReadClaudeSettings(path)
			if tc.wantErr && err == nil {
				t.Fatalf("expected error")
			}
			if !tc.wantErr && err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if tc.wantNil && got != nil {
				t.Fatalf("expected nil, got %#v", got)
			}
			if !tc.wantNil && !tc.wantErr && got == nil {
				t.Fatalf("expected non-nil")
			}
		})
	}
}

func TestFindEnvConflicts(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("NO_COLOR", "1")

	// create user settings with env block
	userDir := filepath.Join(home, ".claude")
	if err := os.MkdirAll(userDir, 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(userDir, "settings.json"), []byte(`{"env":{"ANTHROPIC_API_KEY":"key1","OTHER":"x"}}`), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	// create project settings
	projDir := t.TempDir()
	origWd, _ := os.Getwd()
	if err := os.Chdir(projDir); err != nil {
		t.Fatalf("chdir: %v", err)
	}
	t.Cleanup(func() { _ = os.Chdir(origWd) })
	if err := os.MkdirAll(filepath.Join(projDir, ".claude"), 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(projDir, ".claude", "settings.json"), []byte(`{"env":{"ANTHROPIC_API_KEY":"key2"}}`), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}

	conflicts := FindEnvConflicts([]string{"ANTHROPIC_API_KEY", "ANTHROPIC_AUTH_TOKEN"})
	if len(conflicts) != 2 {
		t.Fatalf("expected 2 conflicts, got %d: %#v", len(conflicts), conflicts)
	}
	// check scopes
	foundUser, foundProject := false, false
	for _, c := range conflicts {
		if c.Scope == "user" && c.Key == "ANTHROPIC_API_KEY" {
			foundUser = true
		}
		if c.Scope == "project" && c.Key == "ANTHROPIC_API_KEY" {
			foundProject = true
		}
	}
	if !foundUser || !foundProject {
		t.Fatalf("expected user and project conflicts, got %#v", conflicts)
	}

	// no collision
	noConf := FindEnvConflicts([]string{"NONEXISTENT"})
	if len(noConf) != 0 {
		t.Fatalf("expected 0, got %d", len(noConf))
	}
}

func TestInheritModel(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	dir := filepath.Join(home, ".claude")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "settings.json"), []byte(`{"model":"inherited-model"}`), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	got, ok := InheritModel()
	if !ok || got != "inherited-model" {
		t.Fatalf("InheritModel = %q, %v want inherited-model true", got, ok)
	}
	// no model case
	if err := os.WriteFile(filepath.Join(dir, "settings.json"), []byte(`{}`), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	_, ok = InheritModel()
	if ok {
		t.Fatalf("expected no model")
	}
}
