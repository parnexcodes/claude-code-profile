package util

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

// ---------------------------------------------------------------------------
// terminal output helpers
// ---------------------------------------------------------------------------

const (
	CReset  = "\x1b[0m"
	CBold   = "\x1b[1m"
	CDim    = "\x1b[2m"
	CRed    = "\x1b[31m"
	CGreen  = "\x1b[32m"
	CYellow = "\x1b[33m"
	CCyan   = "\x1b[36m"
)

func ColorEnabled() bool {
	if os.Getenv("NO_COLOR") != "" {
		return false
	}
	for _, f := range []*os.File{os.Stdout, os.Stderr} {
		fi, err := f.Stat()
		if err != nil || fi.Mode()&os.ModeCharDevice == 0 {
			return false
		}
	}
	return true
}

var UseColor = ColorEnabled()

func Paint(code, s string) string {
	if !UseColor {
		return s
	}
	return code + s + CReset
}

// Die prints an error to stderr and exits 1.
func Die(format string, a ...any) {
	fmt.Fprintf(os.Stderr, "%s %s\n", Paint(CRed, "error:"), fmt.Sprintf(format, a...))
	os.Exit(1)
}

func Warnf(format string, a ...any) {
	fmt.Fprintf(os.Stderr, "%s %s\n", Paint(CYellow, "warn:"), fmt.Sprintf(format, a...))
}

func Infof(format string, a ...any) {
	fmt.Fprintf(os.Stderr, "%s %s\n", Paint(CCyan, "ccp:"), fmt.Sprintf(format, a...))
}

func Okf(format string, a ...any) {
	fmt.Fprintf(os.Stderr, "%s %s\n", Paint(CGreen, "ok:"), fmt.Sprintf(format, a...))
}

// ---------------------------------------------------------------------------
// paths / strings
// ---------------------------------------------------------------------------

func HomeDir() string {
	if h := os.Getenv("HOME"); h != "" {
		return h
	}
	h, err := os.UserHomeDir()
	if err != nil {
		Die("cannot determine home directory: %v", err)
	}
	return h
}

// ExpandPath expands a leading ~ to the user's home directory.
func ExpandPath(p string) string {
	if p == "~" {
		return HomeDir()
	}
	if strings.HasPrefix(p, "~/") {
		return filepath.Join(HomeDir(), p[2:])
	}
	return p
}

var (
	reBracedVar = regexp.MustCompile(`\$\{([A-Za-z_][A-Za-z0-9_]*)\}`)
	reBareVar   = regexp.MustCompile(`\$([A-Za-z_][A-Za-z0-9_]*)`)
)

// ExpandEnvVars expands ${VAR} and $VAR references from the current process
// environment. Unknown references are left untouched.
func ExpandEnvVars(s string) string {
	expand := func(match, name string) string {
		if v, ok := os.LookupEnv(name); ok {
			return v
		}
		return match
	}
	s = reBracedVar.ReplaceAllStringFunc(s, func(m string) string {
		return expand(m, reBracedVar.FindStringSubmatch(m)[1])
	})
	s = reBareVar.ReplaceAllStringFunc(s, func(m string) string {
		return expand(m, reBareVar.FindStringSubmatch(m)[1])
	})
	return s
}

// MaskSecret renders a secret safe for display.
func MaskSecret(s string) string {
	switch {
	case s == "":
		return ""
	case len(s) <= 2:
		return s + strings.Repeat("*", 6)
	case len(s) <= 10:
		return s[:2] + strings.Repeat("*", 6)
	default:
		return s[:6] + "…" + s[len(s)-4:]
	}
}

// CloseQuietly closes c, discarding the error. For read-side bodies and
// best-effort resource cleanup where a Close failure carries no signal.
func CloseQuietly(c io.Closer) {
	_ = c.Close()
}

func RandHex(nBytes int) string {
	b := make([]byte, nBytes)
	if _, err := io.ReadFull(rand.Reader, b); err != nil {
		Die("crypto/rand failed: %v", err)
	}
	return hex.EncodeToString(b)
}

func FileExists(p string) bool {
	fi, err := os.Stat(p)
	return err == nil && !fi.IsDir()
}

func IsExecutable(p string) bool {
	fi, err := os.Stat(p)
	if err != nil || fi.IsDir() {
		return false
	}
	// On Windows the executable bit is not meaningful; existence is enough.
	if isWindows() {
		return true
	}
	return fi.Mode()&0o111 != 0
}

func isWindows() bool {
	return os.PathSeparator == '\\'
}

// ---------------------------------------------------------------------------
// misc
// ---------------------------------------------------------------------------

// ProbeURL returns nil if the URL answers with *any* HTTP response
// (401/404 still mean the server is up).
func ProbeURL(url string, timeout time.Duration) error {
	client := &http.Client{Timeout: timeout}
	resp, err := client.Get(url)
	if err != nil {
		return err
	}
	CloseQuietly(resp.Body)
	return nil
}

// LookPathAll behaves like exec.LookPath but accepts several candidate names
// and returns the first hit.
func LookPathAll(names ...string) string {
	for _, n := range names {
		if p, err := exec.LookPath(n); err == nil {
			return p
		}
	}
	return ""
}

// ProcCmdline reads /proc/<pid>/cmdline (Linux) and returns it space-joined.
func ProcCmdline(pid int) string {
	b, err := os.ReadFile(fmt.Sprintf("/proc/%d/cmdline", pid))
	if err != nil {
		return ""
	}
	parts := strings.Split(string(b), "\x00")
	var out []string
	for _, p := range parts {
		if p != "" {
			out = append(out, p)
		}
	}
	return strings.Join(out, " ")
}
