package main

import (
	"fmt"
	"os"
	"strconv"
)

func handleProxy(args []string) {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, `usage: ccp proxy <subcommand>

  status              show whether the proxy is running
  start               start it (no-op if already up)
  stop                stop it (only processes started by ccp)
  restart             stop, then start
  install             download the latest CLIProxyAPI release binary
  init                scaffold a starter config.yaml
  logs [-n N] [-f]    print last N lines (default 100); -f follows
  models              list model IDs exposed by the proxy`)
		os.Exit(1)
	}

	cfg := mustLoadConfig()
	sub := args[0]

	switch sub {
	case "status":
		pid, alive := pidFromFile()
		up := proxyReachable(cfg)
		switch {
		case alive && up:
			okf("running (pid %d), endpoint %s", pid, paint(cBold, proxyBaseURL(cfg)))
			for _, l := range readLastLines(proxyLogPath(), 3) {
				fmt.Printf("    %s\n", l)
			}
		case up:
			fmt.Printf("  %s %s answers but was not started by ccp (no pid file)\n",
				paint(cYellow, "warn:"), proxyBaseURL(cfg))
			os.Exit(1)
		default:
			fmt.Printf("  %s stopped (%s unreachable)\n", paint(cRed, "down:"), proxyBaseURL(cfg))
			os.Exit(1)
		}

	case "start":
		if err := startProxy(cfg); err != nil {
			die("%v", err)
		}
	case "stop":
		if err := stopProxy(cfg); err != nil {
			die("%v", err)
		}
	case "restart":
		if _, alive := pidFromFile(); alive {
			if err := stopProxy(cfg); err != nil {
				die("%v", err)
			}
		} else if proxyReachable(cfg) {
			die("%s is answering but not managed by ccp; stop it manually first", proxyBaseURL(cfg))
		}
		if err := startProxy(cfg); err != nil {
			die("%v", err)
		}

	case "install":
		if err := installProxy(); err != nil {
			die("%v", err)
		}
	case "init":
		path := cfg.proxyConfigFile()
		scaffoldProxyConfig(path)
		okf("wrote %s", path)
		infof("client api-key: %s (also reused automatically by cliproxy profiles)",
			paint(cBold, maskSecret(readProxyAPIKeysKeyFirst(cfg))))

	case "logs":
		n := 100
		follow := false
		for i := 1; i < len(args); i++ {
			switch args[i] {
			case "-n":
				i++
				if i < len(args) {
					v, err := strconv.Atoi(args[i])
					if err != nil || v < 0 {
						die("-n expects a number")
					}
					n = v
				}
			case "-f", "--follow":
				follow = true
			default:
				die("unknown option %q", args[i])
			}
		}
		if follow {
			followLogs(n)
		} else {
			for _, l := range readLastLines(proxyLogPath(), n) {
				fmt.Println(l)
			}
		}

	case "models":
		if !proxyReachable(cfg) {
			if cfg.Proxy.autostart() {
				if err := startProxy(cfg); err != nil {
					die("%v", err)
				}
			} else {
				die("proxy at %s is down", proxyBaseURL(cfg))
			}
		}
		models, err := fetchProxyModels(cfg)
		if err != nil {
			die("listing models: %v", err)
		}
		for _, m := range models {
			fmt.Println(m)
		}

	default:
		die("unknown proxy subcommand %q", sub)
	}
}

// readProxyAPIKeysKeyFirst returns keys[0] or "" without warning spam.
func readProxyAPIKeysKeyFirst(cfg *Config) string {
	keys := readProxyAPIKeys(cfg)
	if len(keys) == 0 {
		return ""
	}
	return keys[0]
}
