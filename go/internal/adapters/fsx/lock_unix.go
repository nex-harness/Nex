//go:build !windows

package fsx

import (
	"os"
	"syscall"
)

// lockFile takes an exclusive flock. Returning EWOULDBLOCK-style errors lets
// acquireFileLock retry, mirroring the TS's non-blocking probe + retry ladder.
func lockFile(f *os.File) error {
	return syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
}

// unlockFile releases the flock; a close also releases it, so a failure here is
// harmless and the caller closes the file regardless.
func unlockFile(f *os.File) error {
	return syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
}
