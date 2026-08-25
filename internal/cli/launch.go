package cli

import (
	"fmt"
	"os"
	"os/exec"
	"sort"
	"strings"
)

// launch runs claude with the profile's environment applied via exec(2),
// so signals, TTY handling and exit codes behave exactly like a native run.
func launch(name string, rest []string, quiet bool) {
	cfg := mustLoadConfig()
	name, p := cfg.ResolveProfile(name)

	// For translated OpenAI upstreams, ensure the proxy YAML is in sync and daemon is up.
	if p.HasUpstream() {
		if err := ensureProxyForUpstream(cfg, name, p); err != nil {
			die("ensuring upstream proxy for %q: %v", name, err)
		}
	}
	if p.Type == "cliproxy" && !proxyReachable(cfg) {
		if cfg.Proxy.Autostart() {
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

	// Settings-file env blocks beat process env; surface collisions now.
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
		die("claude not found on PATH; install Claude Code first")
	}

	if !quiet {
		infof("profile %s · %s", paint(cBold, name), strings.Join(built.Notes, " · "))
	}

	env := assembleEnv(os.Environ(), built.Strips, built.Sets)
	argv := append([]string{"claude"}, rest...)
	if err := runReplacing(claudePath, argv, env); err != nil {
		die("exec %s: %v", claudePath, err)
	}
}

// ---------------------------------------------------------------------------
// ccp show / list
// ---------------------------------------------------------------------------

func showProfile(name string) {
	cfg := mustLoadConfig()
	name, p := cfg.ResolveProfile(name)

	built, err := buildEnvPeek(cfg, name, p)
	if err != nil {
		warnf("%v", err)
		infof("showing partial configuration")
		// For pooled profiles, still try to enumerate accounts even if selected account fails.
		if p.IsPooled() {
			fmt.Printf("%s  %s\n", paint(cBold, name), p.Description)
			fmt.Printf("  type:         %s\n", p.Type)
			fmt.Printf("  pool:         %d accounts (%s)\n", len(p.Accounts), p.RoutingStrategy())
			for i, a := range p.Accounts {
				src := ""
				val := ""
				if a.AuthTokenEnv != "" {
					src = "$" + a.AuthTokenEnv
					if v := os.Getenv(a.AuthTokenEnv); v != "" {
						val = maskSecret(v)
					} else {
						val = "(unset)"
					}
				} else if a.APIKeyEnv != "" {
					src = "$" + a.APIKeyEnv
					if v := os.Getenv(a.APIKeyEnv); v != "" {
						val = maskSecret(v)
					} else {
						val = "(unset)"
					}
				} else if a.AuthToken != "" {
					src = "auth_token (config)"
					val = maskSecret(expandEnvVars(a.AuthToken))
				} else if a.APIKey != "" {
					src = "api_key (config)"
					val = maskSecret(expandEnvVars(a.APIKey))
				} else if a.Auth == "none" {
					src = "none"
					val = "(none)"
				} else if p.Type == "cliproxy" {
					src = "proxy config api-keys[0]"
					if keys := readProxyAPIKeys(cfg); len(keys) > 0 {
						val = maskSecret(keys[0])
					} else {
						val = "(no proxy key)"
					}
				}
				nameDisp := a.Name
				if nameDisp != "" {
					nameDisp = " (" + nameDisp + ")"
				}
				endpointNote := ""
				if a.BaseURL != "" {
					endpointNote = " base_url=" + expandEnvVars(a.BaseURL)
				}
				fmt.Printf("    [%d]%s %s %s%s\n", i, nameDisp, src, val, endpointNote)
			}
			return
		}
	}

	fmt.Printf("%s  %s\n", paint(cBold, name), p.Description)
	fmt.Printf("  type:         %s", p.Type)
	if p.HasUpstream() {
		fmt.Printf(" (translated OpenAI → Anthropic)\n")
		fmt.Printf("  upstream:     %s\n", expandEnvVars(p.UpstreamBaseURL))
		if p.UpstreamAPIKeyEnv != "" {
			val := os.Getenv(p.UpstreamAPIKeyEnv)
			if val != "" {
				fmt.Printf("  up auth:      $%s (%s)\n", p.UpstreamAPIKeyEnv, maskSecret(val))
			} else {
				fmt.Printf("  up auth:      $%s (unset)\n", p.UpstreamAPIKeyEnv)
			}
		} else if p.UpstreamAPIKey != "" {
			fmt.Printf("  up auth:      literal %s\n", maskSecret(expandEnvVars(p.UpstreamAPIKey)))
		}
		if p.UpstreamModel != "" {
			fmt.Printf("  up model:     %s\n", p.UpstreamModel)
			if p.UpstreamModelAlias != "" && p.UpstreamModelAlias != p.UpstreamModel {
				fmt.Printf("  alias:        %s\n", p.UpstreamModelAlias)
			}
		} else if p.UpstreamModelAlias != "" {
			fmt.Printf("  alias:        %s\n", p.UpstreamModelAlias)
		}
		if p.UpstreamName != "" {
			fmt.Printf("  up name:      %s\n", p.UpstreamName)
		}
		if synced, reason := isUpstreamSynced(cfg, name, p); !synced {
			fmt.Printf("  proxy sync:   drifted (%s)\n", reason)
		} else {
			fmt.Printf("  proxy sync:   in sync\n")
		}
	} else {
		fmt.Printf("\n")
	}
	if p.IsPooled() {
		fmt.Printf("  pool:         %d accounts (%s)  next=%d\n", len(p.Accounts), p.RoutingStrategy(), peekRoutingIndex(name, len(p.Accounts)))
		for i, a := range p.Accounts {
			src := ""
			displayVal := ""
			var baseNote string
			if p.HasUpstream() {
				// Upstream per-account
				if a.UpstreamAPIKeyEnv != "" {
					src = "$" + a.UpstreamAPIKeyEnv
					if v := os.Getenv(a.UpstreamAPIKeyEnv); v != "" {
						displayVal = maskSecret(v)
					} else {
						displayVal = "(unset)"
					}
				} else if a.UpstreamAPIKey != "" {
					src = "upstream api_key (config)"
					displayVal = maskSecret(expandEnvVars(a.UpstreamAPIKey))
				} else if p.UpstreamAPIKeyEnv != "" {
					src = "$" + p.UpstreamAPIKeyEnv + " (inherit)"
					if v := os.Getenv(p.UpstreamAPIKeyEnv); v != "" {
						displayVal = maskSecret(v)
					} else {
						displayVal = "(unset)"
					}
				} else if p.UpstreamAPIKey != "" {
					src = "upstream api_key (inherit)"
					displayVal = maskSecret(expandEnvVars(p.UpstreamAPIKey))
				} else {
					src = "(no upstream auth)"
					displayVal = ""
				}
				if a.UpstreamBaseURL != "" {
					baseNote = " upstream_base_url=" + expandEnvVars(a.UpstreamBaseURL)
				} else if p.UpstreamBaseURL != "" && i == 0 {
					baseNote = " upstream_base_url=" + expandEnvVars(p.UpstreamBaseURL) + " (profile)"
				}
				if a.UpstreamModel != "" {
					baseNote += " upstream_model=" + a.UpstreamModel
				}
				if a.UpstreamModelAlias != "" {
					baseNote += " alias=" + a.UpstreamModelAlias
				}
				if a.BaseURL != "" {
					baseNote += " base_url=" + expandEnvVars(a.BaseURL)
				}
			} else {
				if a.AuthTokenEnv != "" {
					src = "$" + a.AuthTokenEnv
					if v := os.Getenv(a.AuthTokenEnv); v != "" {
						displayVal = maskSecret(v)
					} else {
						displayVal = "(unset)"
					}
				} else if a.APIKeyEnv != "" {
					src = "$" + a.APIKeyEnv
					if v := os.Getenv(a.APIKeyEnv); v != "" {
						displayVal = maskSecret(v)
					} else {
						displayVal = "(unset)"
					}
				} else if a.AuthToken != "" {
					src = "auth_token (config)"
					displayVal = maskSecret(expandEnvVars(a.AuthToken))
				} else if a.APIKey != "" {
					src = "api_key (config)"
					displayVal = maskSecret(expandEnvVars(a.APIKey))
				} else if a.Auth == "none" {
					src = "none"
					displayVal = "(none)"
				} else if p.Type == "cliproxy" {
					src = "proxy config api-keys[0]"
					if keys := readProxyAPIKeys(cfg); len(keys) > 0 {
						displayVal = maskSecret(keys[0])
					} else {
						displayVal = "(no proxy key)"
					}
				} else {
					src = "(no auth)"
				}
				if a.BaseURL != "" {
					baseNote = " base_url=" + expandEnvVars(a.BaseURL)
				}
			}
			marker := " "
			if built != nil && i == built.SelectedIdx {
				marker = "→"
			}
			nameDisp := ""
			if a.Name != "" {
				nameDisp = " (" + a.Name + ")"
			}
			fmt.Printf("    %s [%d]%s %s %s%s\n", marker, i, nameDisp, src, displayVal, baseNote)
		}
	}
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

	fmt.Printf("\n%s\n", paint(cDim, "environment overrides (selected account):"))
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
	def := cfg.DefaultProfileName()
	if len(names) == 0 {
		infof("no profiles yet; create one with %s", paint(cBold, "ccp add <name>"))
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
		poolTag := ""
		if p.IsPooled() {
			poolTag = paint(cDim, fmt.Sprintf(" ×%d", len(p.Accounts)))
		}
		if p.HasUpstream() {
			if poolTag != "" {
				poolTag += paint(cDim, " translated")
			} else {
				poolTag = paint(cDim, " translated")
			}
		}
		marker := " "
		if n == def {
			marker = paint(cGreen, "*")
		}
		fmt.Printf(" %s%s  %-10s %-16s %s%s\n",
			marker, pad(n, width), pad(p.Type, 10), pad(modelPlain(model), 16), p.Description, poolTag)
	}
	if def != "" {
		fmt.Printf("\n%s\n", paint(cDim, fmt.Sprintf("  default: bare `ccp` launches %q", def)))
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
