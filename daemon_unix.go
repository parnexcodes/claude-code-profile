//go:build unix

package main

import (
	"os"
	"os/exec"
	"syscall"
)

// detach starts cmd in its own process group / session so ccp can stop the
// whole daemon later and so the child survives ccp exiting.
func detach(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
}

func killProcessGroup(pid int) error {
	return syscall.Kill(-pid, syscall.SIGTERM)
}

func forceKillProcessGroup(pid int) error {
	return syscall.Kill(-pid, syscall.SIGKILL)
}

// procAlive reports whether a process with this pid exists.
func procAlive(pid int) bool {
	p, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	return p.Signal(syscall.Signal(0)) == nil
}

// runReplacing replaces the current process (execve). On success it never
// returns.
func runReplacing(path string, argv []string, env []string) error {
	return syscall.Exec(path, argv, env)
}
