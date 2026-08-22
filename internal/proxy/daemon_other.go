//go:build !unix

package proxy

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
	return killProcessGroup(pid)
}

func procAlive(pid int) bool {
	_, err := os.FindProcess(pid)
	return err == nil
}
