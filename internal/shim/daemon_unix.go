//go:build unix

package shim

import (
	"os"
	"os/exec"
	"syscall"
	"time"
)

func detach(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
}

func killProcessGroup(pid int) error {
	return syscall.Kill(-pid, syscall.SIGTERM)
}

func forceKillProcessGroup(pid int) error {
	return syscall.Kill(-pid, syscall.SIGKILL)
}

func procAlive(pid int) bool {
	p, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	return p.Signal(syscall.Signal(0)) == nil
}

func tryFlock(f *os.File) {
	// best effort flock
	_ = syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
}

func unlockFlock(f *os.File) {
	_ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
}

func stopQuietly(pid int) {
	_ = killProcessGroup(pid)
	for i := 0; i < 50; i++ {
		if !procAlive(pid) {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	_ = forceKillProcessGroup(pid)
}
