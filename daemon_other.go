//go:build !unix

package main

import (
	"os"
	"os/exec"
)

// detach is a no-op on platforms without process groups; the child dies with
// the console session instead of outliving ccp.
func detach(cmd *exec.Cmd) {}

func killProcessGroup(pid int) error {
	p, err := os.FindProcess(pid)
	if err != nil {
		return err
	}
	return p.Kill()
}

func forceKillProcessGroup(pid int) error {
	return killProcessGroup(pid) // Windows Kill() is already unconditional
}

// procAlive reports whether a process with this pid exists.
func procAlive(pid int) bool {
	_, err := os.FindProcess(pid)
	return err == nil
}

// runReplacing falls back to a child process where execve is unavailable.
// The TUI still works, but Ctrl+C handling differs slightly from a native run.
func runReplacing(path string, argv []string, env []string) error {
	return runSpawned(path, argv, env)
}

// runSpawned runs path as a child process and exits with its exit code.
func runSpawned(path string, argv []string, env []string) error {
	cmd := exec.Command(argv[0], argv[1:]...)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.Env = env
	if err := cmd.Run(); err != nil {
		if ee, ok := err.(*exec.ExitError); ok {
			os.Exit(ee.ExitCode())
		}
		return err
	}
	os.Exit(0)
	return nil
}
