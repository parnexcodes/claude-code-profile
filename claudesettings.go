package main

import (
	"encoding/json"
	"os"
	"path/filepath"
)

// ---------------------------------------------------------------------------
// Claude Code settings interop
//
// Two reasons ccp reads Claude Code's own settings files:
//
//  1. Model inheritance: a profile without an explicit model inherits the
//     "model" key from ~/.claude/settings.json.
//
//  2. Conflict detection: env vars set in an `env` block of a settings file
//     BEAT process environment variables (documented precedence), so anything
//     pinned there silently defeats ccp. We surface those collisions before
//     launching instead of letting them bite at runtime.
// ---------------------------------------------------------------------------

type claudeSettings struct {
	Model string            `json:"model"`
	Env   map[string]string `json:"env"`
}

func readClaudeSettings(path string) (*claudeSettings, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var s claudeSettings
	if err := json.Unmarshal(data, &s); err != nil {
		return nil, err
	}
	return &s, nil
}

// claudeSettingsPaths returns the settings files that apply to a session
// started in the current directory (user scope + project scopes).
func claudeSettingsPaths() []string {
	return []string{
		filepath.Join(homeDir(), ".claude", "settings.json"),
		filepath.Join(".claude", "settings.json"),
		filepath.Join(".claude", "settings.local.json"),
	}
}

type envConflict struct {
	Path  string
	Key   string
	Value string
	Scope string // user / project / local
}

// findEnvConflicts reports which of the given env var names are pinned in any
// Claude Code settings file env block (those would override ccp).
func findEnvConflicts(keys []string) []envConflict {
	want := map[string]bool{}
	for _, k := range keys {
		want[k] = true
	}
	var out []envConflict
	for i, path := range claudeSettingsPaths() {
		s, err := readClaudeSettings(path)
		if err != nil || s == nil {
			if err != nil {
				warnf("cannot parse %s: %v", path, err)
			}
			continue
		}
		scope := []string{"user", "project", "local"}[i]
		for k, v := range s.Env {
			if want[k] {
				out = append(out, envConflict{Path: path, Key: k, Value: v, Scope: scope})
			}
		}
	}
	return out
}
