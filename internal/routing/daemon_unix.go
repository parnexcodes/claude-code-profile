//go:build unix

package routing

import (
	"os"
	"syscall"
)

// lockRoutingState tries to acquire an exclusive flock on a sidecar lock file
// for the routing state. It returns an unlock function that is always safe to call.
func lockRoutingState(statePath string) func() {
	lockPath := statePath + ".lock"
	f, err := os.OpenFile(lockPath, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return func() {}
	}
	_ = syscall.Flock(int(f.Fd()), syscall.LOCK_EX)
	return func() {
		_ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
		_ = f.Close()
	}
}
