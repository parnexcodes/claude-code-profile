//nolint:errcheck,staticcheck,unused
package config

import (
	"path/filepath"
	"testing"
)

func TestCcpConfigDir_Precedence(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	tests := []struct {
		name string
		env  map[string]string
		want string
	}{
		{name: "CCP_HOME wins", env: map[string]string{"CCP_HOME": "/tmp/myccp", "XDG_CONFIG_HOME": "/tmp/xdg"}, want: "/tmp/myccp"},
		{name: "XDG_CONFIG_HOME", env: map[string]string{"XDG_CONFIG_HOME": "/tmp/xdg"}, want: filepath.Join("/tmp/xdg", "ccp")},
		{name: "HOME fallback", env: map[string]string{}, want: filepath.Join(home, ".config", "ccp")},
		{name: "CCP_HOME tilde", env: map[string]string{"CCP_HOME": "~/myccp"}, want: filepath.Join(home, "myccp")},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			// clear
			t.Setenv("CCP_HOME", "")
			t.Setenv("XDG_CONFIG_HOME", "")
			for k, v := range tc.env {
				t.Setenv(k, v)
			}
			got := CcpConfigDir()
			if got != tc.want {
				t.Fatalf("CcpConfigDir=%q want %q", got, tc.want)
			}
		})
	}
}

func TestCcpStateDir_Precedence(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	tests := []struct {
		name string
		env  map[string]string
		want string
	}{
		{name: "CCP_STATE_HOME wins", env: map[string]string{"CCP_STATE_HOME": "/tmp/state", "XDG_STATE_HOME": "/tmp/xdg_state"}, want: "/tmp/state"},
		{name: "XDG_STATE_HOME", env: map[string]string{"XDG_STATE_HOME": "/tmp/xdg_state"}, want: filepath.Join("/tmp/xdg_state", "ccp")},
		{name: "HOME fallback", env: map[string]string{}, want: filepath.Join(home, ".local", "state", "ccp")},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("CCP_STATE_HOME", "")
			t.Setenv("XDG_STATE_HOME", "")
			for k, v := range tc.env {
				t.Setenv(k, v)
			}
			got := CcpStateDir()
			if got != tc.want {
				t.Fatalf("CcpStateDir=%q want %q", got, tc.want)
			}
		})
	}
}
