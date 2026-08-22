//go:build unix

package cli

import "syscall"

func runReplacing(path string, argv []string, env []string) error {
	return syscall.Exec(path, argv, env)
}
