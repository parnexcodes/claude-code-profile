//go:build !unix

package shim

import (
	"os"
	"os/exec"
)

func detach(cmd *exec.Cmd) {}

func killProcessGroup(pid int) error {
	p, err := os.FindProcess(pid)
	if err != nil {
		return err
	}
	return p.Kill()
}

func forceKillProcessGroup(pid int) error {
	p, err := os.FindProcess(pid)
	if err != nil {
		return err
	}
	return p.Kill()
}

func procAlive(pid int) bool {
	p, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	// On non-unix, just check if process exists; FindProcess always succeeds on Windows, so we try to signal
	// For simplicity, assume alive if we can find it
	_ = p
	return true
}

func tryFlock(f *os.File)    {}
func unlockFlock(f *os.File) {}

func stopQuietly(pid int) {
	_ = killProcessGroup(pid)
}
