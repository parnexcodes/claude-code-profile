package main

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
	cReset  = "\x1b[0m"
	cBold   = "\x1b[1m"
	cDim    = "\x1b[2m"
	cRed    = "\x1b[31m"
	cGreen  = "\x1b[32m"
	cYellow = "\x1b[33m"
	cCyan   = "\x1b[36m"
)

func colorEnabled() bool {
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

var useColor = colorEnabled()

func paint(code, s string) string {
	if !useColor {
		return s
	}
	return code + s + cReset
}

// die prints an error to stderr and exits 1.
func die(format string, a ...any) {
	fmt.Fprintf(os.Stderr, "%s %s\n", paint(cRed, "error:"), fmt.Sprintf(format, a...))
	os.Exit(1)
}

func warnf(format string, a ...any) {
	fmt.Fprintf(os.Stderr, "%s %s\n", paint(cYellow, "warn:"), fmt.Sprintf(format, a...))
}

func infof(format string, a ...any) {
	fmt.Fprintf(os.Stderr, "%s %s\n", paint(cCyan, "ccp:"), fmt.Sprintf(format, a...))
}

func okf(format string, a ...any) {
	fmt.Fprintf(os.Stderr, "%s %s\n", paint(cGreen, "ok:"), fmt.Sprintf(format, a...))
}

// ---------------------------------------------------------------------------
// paths / strings
// ---------------------------------------------------------------------------

func homeDir() string {
	h, err := os.UserHomeDir()
	if err != nil {
		die("cannot determine home directory: %v", err)
	}
	return h
}

// expandPath expands a leading ~ to the user's home directory.
func expandPath(p string) string {
	if p == "~" {
		return homeDir()
	}
	if strings.HasPrefix(p, "~/") {
		return filepath.Join(homeDir(), p[2:])
	}
	return p
}

var (
	reBracedVar = regexp.MustCompile(`\$\{([A-Za-z_][A-Za-z0-9_]*)\}`)
	reBareVar   = regexp.MustCompile(`\$([A-Za-z_][A-Za-z0-9_]*)`)
)

// expandEnvVars expands ${VAR} and $VAR references from the current process
// environment. Unknown references are left untouched.
func expandEnvVars(s string) string {
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

// maskSecret renders a secret safe for display.
func maskSecret(s string) string {
	switch {
	case s == "":
		return ""
	case len(s) <= 10:
		return s[:2] + strings.Repeat("*", 6)
	default:
		return s[:6] + "…" + s[len(s)-4:]
	}
}

// closeQuietly closes c, discarding the error. For read-side bodies and
// best-effort resource cleanup where a Close failure carries no signal.
func closeQuietly(c io.Closer) {
	_ = c.Close()
}

func randHex(nBytes int) string {
	b := make([]byte, nBytes)
	if _, err := io.ReadFull(rand.Reader, b); err != nil {
		die("crypto/rand failed: %v", err)
	}
	return hex.EncodeToString(b)
}

func fileExists(p string) bool {
	fi, err := os.Stat(p)
	return err == nil && !fi.IsDir()
}

func isExecutable(p string) bool {
	fi, err := os.Stat(p)
	return err == nil && !fi.IsDir() && fi.Mode()&0o111 != 0
}

// ---------------------------------------------------------------------------
// misc
// ---------------------------------------------------------------------------

// probeURL returns nil if the URL answers with *any* HTTP response
// (401/404 still mean the server is up).
func probeURL(url string, timeout time.Duration) error {
	client := &http.Client{Timeout: timeout}
	resp, err := client.Get(url)
	if err != nil {
		return err
	}
	closeQuietly(resp.Body)
	return nil
}

// lookPathAll behaves like exec.LookPath but accepts several candidate names
// and returns the first hit.
func lookPathAll(names ...string) string {
	for _, n := range names {
		if p, err := exec.LookPath(n); err == nil {
			return p
		}
	}
	return ""
}

// procCmdline reads /proc/<pid>/cmdline (Linux) and returns it space-joined.
func procCmdline(pid int) string {
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
