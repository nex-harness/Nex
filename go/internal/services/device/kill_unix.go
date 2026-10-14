//go:build !windows

// kill_unix.go: the signal-0 process liveness probe (Unix).
package device

import "syscall"

// signalProbe implements isProcessAlive's syscall on Unix: kill(pid, 0)
// succeeds for any process we may signal; ESRCH means it is gone; anything
// else (EPERM — exists but not ours) means alive.
func signalProbe(pid int64) error {
	return syscall.Kill(int(pid), 0)
}

// errNoProcess is the ESRCH alias.
var errNoProcess = syscall.ESRCH
