//nolint:errcheck,staticcheck,unused
package util

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestExpandEnvVars(t *testing.T) {
	tests := []struct {
		name string
		in   string
		env  map[string]string
		want string
	}{
		{name: "empty", in: "", want: ""},
		{name: "braced", in: "https://${HOST}/v1", env: map[string]string{"HOST": "example.com"}, want: "https://example.com/v1"},
		{name: "bare", in: "token=$TOKEN", env: map[string]string{"TOKEN": "sk-123"}, want: "token=sk-123"},
		{name: "mixed", in: "${A}_$B", env: map[string]string{"A": "x", "B": "y"}, want: "x_y"},
		{name: "unknown_untouched", in: "hello $UNKNOWN world", want: "hello $UNKNOWN world"},
		{name: "unknown_braced_untouched", in: "hello ${UNKNOWN} world", want: "hello ${UNKNOWN} world"},
		{name: "multiple_same", in: "$X $X", env: map[string]string{"X": "1"}, want: "1 1"},
		{name: "no_var", in: "plain text", want: "plain text"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			for k, v := range tc.env {
				t.Setenv(k, v)
			}
			// Ensure unknown not set
			if _, ok := tc.env["UNKNOWN"]; !ok {
				_ = os.Unsetenv("UNKNOWN")
			}
			got := ExpandEnvVars(tc.in)
			if got != tc.want {
				t.Fatalf("ExpandEnvVars(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

func TestMaskSecret(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{name: "empty", in: "", want: ""},
		{name: "short_5", in: "abcde", want: "ab******"},
		{name: "exactly_10", in: "0123456789", want: "01******"},
		{name: "short_2", in: "ab", want: "ab******"},
		{name: "long_11", in: "01234567890", want: "012345…7890"},
		{name: "long_20", in: "sk-abcdefghijklmnopqrstuvwxyz", want: "sk-abc…wxyz"},
		{name: "long_typical", in: "sk-ant-api03-1234567890abcd", want: "sk-ant…abcd"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := MaskSecret(tc.in)
			if got != tc.want {
				t.Fatalf("MaskSecret(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

func TestExpandPath(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	tests := []struct {
		name string
		in   string
		want string
	}{
		{name: "tilde", in: "~", want: home},
		{name: "tilde_slash", in: "~/a/b", want: filepath.Join(home, "a/b")},
		{name: "tilde_slash_only", in: "~/", want: home},
		{name: "absolute", in: "/tmp/foo", want: "/tmp/foo"},
		{name: "relative", in: "relative/path", want: "relative/path"},
		{name: "empty", in: "", want: ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := ExpandPath(tc.in)
			if got != tc.want {
				t.Fatalf("ExpandPath(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

func TestProbeURL(t *testing.T) {
	// success case
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(200)
	}))
	defer srv.Close()

	if err := ProbeURL(srv.URL, 1000000000); err != nil {
		t.Fatalf("ProbeURL success expected nil, got %v", err)
	}

	// any status counts as reachable
	srv2 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(404)
	}))
	defer srv2.Close()
	if err := ProbeURL(srv2.URL+"/", 1000000000); err != nil {
		t.Fatalf("ProbeURL 404 should still be nil, got %v", err)
	}

	// failure case: unreachable
	if err := ProbeURL("http://127.0.0.1:1/", 100000000); err == nil {
		t.Fatalf("ProbeURL expected error for unreachable")
	}
}

func TestFileExistsAndIsExecutable(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "file.txt")
	if err := os.WriteFile(file, []byte("x"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	execFile := filepath.Join(dir, "exec")
	if err := os.WriteFile(execFile, []byte("x"), 0o755); err != nil {
		t.Fatalf("write: %v", err)
	}

	// On Windows the executable bit is not meaningful; any existing file is considered executable.
	wantNoExec := false
	if runtime.GOOS == "windows" {
		wantNoExec = true
	}
	tests := []struct {
		name       string
		path       string
		wantExists bool
		wantExec   bool
	}{
		{name: "exists_no_exec", path: file, wantExists: true, wantExec: wantNoExec},
		{name: "exists_exec", path: execFile, wantExists: true, wantExec: true},
		{name: "missing", path: filepath.Join(dir, "missing"), wantExists: false, wantExec: false},
		{name: "dir", path: dir, wantExists: false, wantExec: false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := FileExists(tc.path); got != tc.wantExists {
				t.Fatalf("FileExists(%q) = %v, want %v", tc.path, got, tc.wantExists)
			}
			if got := IsExecutable(tc.path); got != tc.wantExec {
				t.Fatalf("IsExecutable(%q) = %v, want %v", tc.path, got, tc.wantExec)
			}
		})
	}
}

func TestPaint_NoColor(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	// Need to force UseColor re-evaluation? Paint checks UseColor var which was set at init.
	// Temporarily override UseColor.
	old := UseColor
	UseColor = false
	defer func() { UseColor = old }()
	if got := Paint(CRed, "hello"); got != "hello" {
		t.Fatalf("Paint with NO_COLOR should return plain, got %q", got)
	}
}

func TestPaint_WithColor(t *testing.T) {
	old := UseColor
	UseColor = true
	defer func() { UseColor = old }()
	got := Paint(CRed, "hello")
	want := CRed + "hello" + CReset
	if got != want {
		t.Fatalf("Paint = %q, want %q", got, want)
	}
}
