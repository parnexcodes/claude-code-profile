package settings

import (
	"encoding/json"
	"os"
	"path/filepath"

	"ccp/internal/util"
)

// ---------------------------------------------------------------------------
// Claude Code settings interop
// ---------------------------------------------------------------------------

type ClaudeSettings struct {
	Model string            `json:"model"`
	Env   map[string]string `json:"env"`
}

func ReadClaudeSettings(path string) (*ClaudeSettings, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var s ClaudeSettings
	if err := json.Unmarshal(data, &s); err != nil {
		return nil, err
	}
	return &s, nil
}

// ClaudeSettingsPaths returns the settings files that apply to a session
// started in the current directory (user scope + project scopes).
func ClaudeSettingsPaths() []string {
	return []string{
		filepath.Join(util.HomeDir(), ".claude", "settings.json"),
		filepath.Join(".claude", "settings.json"),
		filepath.Join(".claude", "settings.local.json"),
	}
}

type EnvConflict struct {
	Path  string
	Key   string
	Value string
	Scope string // user / project / local
}

// FindEnvConflicts reports which of the given env var names are pinned in any
// Claude Code settings file env block (those would override ccp).
func FindEnvConflicts(keys []string) []EnvConflict {
	want := map[string]bool{}
	for _, k := range keys {
		want[k] = true
	}
	var out []EnvConflict
	for i, path := range ClaudeSettingsPaths() {
		s, err := ReadClaudeSettings(path)
		if err != nil || s == nil {
			if err != nil {
				util.Warnf("cannot parse %s: %v", path, err)
			}
			continue
		}
		scope := []string{"user", "project", "local"}[i]
		for k, v := range s.Env {
			if want[k] {
				out = append(out, EnvConflict{Path: path, Key: k, Value: v, Scope: scope})
			}
		}
	}
	return out
}

// InheritModel reads the fallback model from ~/.claude/settings.json.
func InheritModel() (string, bool) {
	s, err := ReadClaudeSettings(filepath.Join(util.HomeDir(), ".claude", "settings.json"))
	if err != nil || s == nil || s.Model == "" {
		return "", false
	}
	return s.Model, true
}
