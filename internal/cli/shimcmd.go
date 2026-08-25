package cli

import (
	"fmt"
	"os"

	"ccp/internal/shim"
)

func handleShim(args []string) {
	if len(args) == 0 {
		args = []string{"status"}
	}
	cfg := mustLoadConfig()
	switch args[0] {
	case "status":
		if shim.ShimReachable(cfg) {
			fmt.Printf("ok: shim running at %s\n", shim.ShimBaseURL(cfg))
		} else {
			fmt.Printf("shim not running (expected at %s)\n", shim.ShimBaseURL(cfg))
			os.Exit(1)
		}
	case "start":
		if err := shim.StartShim(cfg); err != nil {
			die("%v", err)
		}
	case "stop":
		if err := shim.StopShim(cfg); err != nil {
			die("%v", err)
		}
		fmt.Println("ok: shim stopped")
	case "restart":
		_ = shim.StopShim(cfg)
		if err := shim.StartShim(cfg); err != nil {
			die("%v", err)
		}
	case "logs":
		// simple: cat log file
		data, err := os.ReadFile(shim.ShimLogPath())
		if err != nil {
			die("cannot read shim log: %v", err)
		}
		fmt.Print(string(data))
	default:
		die("usage: ccp shim status|start|stop|restart|logs")
	}
}

func handleInternalShim(args []string) {
	// hidden: ccp internal-shim --config /path/to/shim.yaml
	cfgPath := ""
	for i := 0; i < len(args); i++ {
		if args[i] == "--config" && i+1 < len(args) {
			cfgPath = args[i+1]
			i++
		} else if args[i] == "--help" || args[i] == "-h" {
			fmt.Println("usage: ccp internal-shim --config PATH")
			return
		}
	}
	if cfgPath == "" {
		// fallback to default
		cfgPath = shim.ShimConfigPath()
	}
	if err := shim.RunFromConfig(cfgPath); err != nil {
		fmt.Fprintf(os.Stderr, "shim error: %v\n", err)
		os.Exit(1)
	}
}
