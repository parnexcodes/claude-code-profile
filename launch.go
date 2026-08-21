package main

import (
	"fmt"
	"os"
	"os/exec"
	"sort"
	"strings"
	"syscall"
)

// launch runs claude with the profile's environment applied via exec(2),
// so signals, TTY handling and exit codes behave exactly like a native run.
func launch(name string, rest []string, quiet bool) {
	cfg := mustLoadConfig()
	name, p := cfg.resolveProfile(name)

	if p.Type == "cliproxy" && !proxyReachable(cfg) {
		if cfg.Proxy.autostart() {
			if err := startProxy(cfg); err != nil {
				die("%v", err)
			}
		} else if !quiet {
			warnf("auto_start is disabled and proxy at %s is down", proxyBaseURL(cfg))
		}
	}

	built, err := buildEnv(cfg, name, p)
	if err != nil {
		die("%v", err)
	}

	// Settings-file env blocks beat process env — surface collisions now.
	var appliedKeys []string
	for k := range built.Sets {
		appliedKeys = append(appliedKeys, k)
	}
	for _, c := range findEnvConflicts(appliedKeys) {
		warnf("%s pins %s=%s in its env block; settings files override process env,",
			c.Path, paint(cBold, c.Key), maskSecret(c.Value))
		fmt.Fprintf(os.Stderr, "       so this profile value will NOT apply. Remove it from that file's \"env\" map.\n")
	}

	claudePath, err := exec.LookPath("claude")
	if err != nil {
		die("claude not found on PATH — install Claude Code first")
	}

	if !quiet {
		infof("profile %s · %s", paint(cBold, name), strings.Join(built.Notes, " · "))
	}

	env := assembleEnv(os.Environ(), built.Strips, built.Sets)
	argv := append([]string{"claude"}, rest...)
	if err := syscall.Exec(claudePath, argv, env); err != nil {
		die("exec %s: %v", claudePath, err)
	}
}

// ---------------------------------------------------------------------------
// ccp show / list
// ---------------------------------------------------------------------------

func showProfile(name string) {
	cfg := mustLoadConfig()
	name, p := cfg.resolveProfile(name)

	built, err := buildEnv(cfg, name, p)
	if err != nil {
		warnf("%v", err)
		infof("showing partial configuration")
	}

	fmt.Printf("%s  %s\n", paint(cBold, name), p.Description)
	fmt.Printf("  type:         %s\n", p.Type)
	endpoint := "(official api.anthropic.com)"
	if built != nil && built.URL != "" {
		endpoint = built.URL
	}
	fmt.Printf("  endpoint:     %s\n", endpoint)
	model := ""
	if p.Model != "" {
		model = p.Model
	} else if m, ok := inheritModel(); ok {
		model = m
	}
	if model == "" {
		model = "(claude default)"
	}
	fmt.Printf("  model:        %s\n", model)
	extra := ""
	if p.HaikuModel != "" {
		extra += fmt.Sprintf("\n  haiku_model:  %s", p.HaikuModel)
	}
	if p.SubagentModel != "" {
		extra += fmt.Sprintf("\n  subagent:     %s", p.SubagentModel)
	}
	fmt.Print(extra + "\n")

	if built == nil {
		return // couldn't resolve auth; nothing more to show reliably
	}

	fmt.Printf("\n%s\n", paint(cDim, "environment overrides:"))
	secretVars := map[string]bool{"ANTHROPIC_API_KEY": true, "ANTHROPIC_AUTH_TOKEN": true}
	keys := make([]string, 0, len(built.Sets))
	for k := range built.Sets {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	width := 0
	for _, k := range keys {
		if len(k) > width {
			width = len(k)
		}
	}
	for _, k := range keys {
		v := built.Sets[k]
		if secretVars[k] {
			v = maskSecret(v)
		}
		fmt.Printf("  %-*s  = %s\n", width, k, v)
	}
	fmt.Printf("\n%s\n", paint(cDim, "stripped from inherited environment:"))
	fmt.Printf("  %s\n", strings.Join(built.Strips, " "))
}

func listProfiles() {
	cfg := mustLoadConfig()
	names := cfg.ProfileNames()
	def := cfg.defaultProfileName()
	if len(names) == 0 {
		infof("no profiles yet — create one with %s", paint(cBold, "ccp add <name>"))
		return
	}
	width := 0
	for _, n := range names {
		if len(n) > width {
			width = len(n)
		}
	}
	fmt.Println(paint(cDim, "  NAME"+strings.Repeat(" ", width)+"  TYPE       MODEL            DESCRIPTION"))
	for _, n := range names {
		p := cfg.Profiles[n]
		model := p.Model
		if model == "" {
			if m, ok := inheritModel(); ok {
				model = m + paint(cDim, " (from settings.json)")
			} else {
				model = paint(cDim, "(claude default)")
			}
		}
		marker := " "
		if n == def {
			marker = paint(cGreen, "*")
		}
		fmt.Printf(" %s%s  %-10s %-16s %s\n",
			marker, pad(n, width), pad(p.Type, 10), pad(modelPlain(model), 16), p.Description)
	}
	if def != "" {
		fmt.Printf("\n%s\n", paint(cDim, fmt.Sprintf("  * default — bare `ccp` launches %q", def)))
	}
}

// modelPlain strips ANSI for column math.
func modelPlain(s string) string {
	var b strings.Builder
	inEscape := false
	for _, r := range s {
		if inEscape {
			if r == 'm' {
				inEscape = false
			}
			continue
		}
		if r == '\x1b' {
			inEscape = true
			continue
		}
		b.WriteRune(r)
	}
	return b.String()
}

func pad(s string, n int) string {
	plain := modelPlain(s)
	for len(plain) < n {
		s += " "
		plain += " "
	}
	return s
}
