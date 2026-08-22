package cli

import (
	"ccp/internal/profile"
	"fmt"
	"os/exec"
)

// doctor runs a series of health checks over the whole ccp setup.
func runDoctor() {
	cfg := mustLoadConfig()
	var failures int

	check := func(status string, format string, a ...any) {
		switch status {
		case "ok":
			fmt.Printf("  %s %s\n", paint(cGreen, "✔"), fmt.Sprintf(format, a...))
		case "warn":
			fmt.Printf("  %s %s\n", paint(cYellow, "•"), fmt.Sprintf(format, a...))
		case "fail":
			failures++
			fmt.Printf("  %s %s\n", paint(cRed, "✘"), fmt.Sprintf(format, a...))
		}
	}

	fmt.Println(paint(cBold, "ccp doctor"))
	fmt.Printf("\n%s\n", paint(cDim, "config"))
	check("ok", "config dir: %s", cfg.Dir)
	check("ok", "state dir:  %s", ccpStateDir())

	fmt.Printf("\n%s\n", paint(cDim, "claude"))
	if p, err := exec.LookPath("claude"); err == nil {
		check("ok", "claude binary: %s", p)
	} else {
		check("fail", "claude not found on PATH")
	}

	fmt.Printf("\n%s\n", paint(cDim, "profiles"))
	names := cfg.ProfileNames()
	if len(names) == 0 {
		check("warn", "no profiles defined; create one with `ccp add <name>`")
	}
	for _, n := range names {
		p := cfg.Profiles[n]
		tag := ""
		if p.IsPooled() {
			tag = fmt.Sprintf(" pool=%d (%s)", len(p.Accounts), p.RoutingStrategy())
		}
		line := fmt.Sprintf("%-10s type=%-9s model=%q%s", n, p.Type, p.Model, tag)
		if n == cfg.DefaultProfileName() {
			line += " (default)"
		}
		check("ok", "%s", line)

		switch p.Type {
		case "anthropic", "cliproxy":
		default:
			check("fail", "%s: unknown type %q (use \"anthropic\" or \"cliproxy\")", n, p.Type)
		}
		if err := p.ValidatePool(); err != nil {
			check("fail", "%s: %v", n, err)
		}
		if p.IsPooled() {
			for i, a := range p.Accounts {
				if a.Name != "" && !safeName(a.Name) {
					check("fail", "%s accounts[%d]: invalid name %q", n, i, a.Name)
				}
				if _, err := profile.ResolveAccountAuth(&a, cfg, p.Type); err != nil {
					check("fail", "%s accounts[%d]: %v", n, i, err)
				}
			}
		} else {
			if _, err := profile.ResolveProfileAuth(p, cfg); err != nil {
				check("fail", "%v", err)
			}
		}
	}

	fmt.Printf("\n%s\n", paint(cDim, "proxy (CLIProxyAPI)"))
	needsProxy := false
	for _, n := range names {
		if cfg.Profiles[n].Type == "cliproxy" {
			needsProxy = true
			break
		}
	}
	bin := findProxyBinary(cfg)
	if bin != "" {
		check("ok", "binary: %s", bin)
	} else if needsProxy {
		check("warn", "binary not found; run `ccp proxy install`")
	}
	reachable := proxyReachable(cfg)
	if reachable {
		check("ok", "endpoint: %s is up", proxyBaseURL(cfg))
		models, err := fetchProxyModels(cfg)
		switch {
		case err == nil && len(models) > 0:
			check("ok", "%d models available; run `ccp proxy models` to list them", len(models))
		case err != nil:
			check("warn", "cannot list models: %v", err)
		default:
			check("warn", "proxy exposes no models yet; complete its OAuth login flow")
		}
	} else if needsProxy {
		check("warn", "endpoint: %s is down (auto_start=%v)", proxyBaseURL(cfg), cfg.Proxy.Autostart())
	} else {
		check("ok", "not in use by any profile")
	}

	fmt.Printf("\n%s\n", paint(cDim, "claude settings interop"))
	if s, err := readClaudeSettings(homeDir() + "/.claude/settings.json"); err != nil {
		check("fail", "~/.claude/settings.json: %v", err)
	} else {
		if s != nil && s.Model != "" {
			check("ok", "~/.claude/settings.json model = %q (fallback for profiles without a model)", s.Model)
		} else {
			check("ok", "~/.claude/settings.json has no pinned model")
		}
	}

	// conflict check across every profile's would-be keys
	conflictKeys := map[string]bool{}
	for _, n := range names {
		if built, err := buildEnvPeek(cfg, n, cfg.Profiles[n]); err == nil {
			for k := range built.Sets {
				conflictKeys[k] = true
			}
		}
	}
	var keys []string
	for k := range conflictKeys {
		keys = append(keys, k)
	}
	conflicts := findEnvConflicts(keys)
	if len(conflicts) == 0 {
		check("ok", "no env-block collisions with settings files")
	} else {
		for _, c := range conflicts {
			check("fail",
				"%s pins env.%s=%s; settings files override process env, remove it or ccp profiles won't apply",
				c.Path, c.Key, maskSecret(c.Value))
		}
	}

	fmt.Println()
	if failures > 0 {
		die("%d problem%s found", failures, plural(failures))
	}
	okf("all checks passed")
}

func plural(n int) string {
	if n == 1 {
		return ""
	}
	return "s"
}
