//go:build windows

package fsx

import (
	"os"
	"syscall"
	"unsafe"
)

var (
	kernel32         = syscall.NewLazyDLL("kernel32.dll")
	procLockFileEx   = kernel32.NewProc("LockFileEx")
	procUnlockFileEx = kernel32.NewProc("UnlockFileEx")
)

const (
	// lockfileExclusiveRange and lockfileFailImmediately are the LockFileEx
	// flags: exclusive + return immediately instead of queueing behind another
	// holder, so acquireFileLock's retry ladder drives the waiting.
	lockfileExclusiveRange  = 0x2
	lockfileFailImmediately = 0x1
)

// lockFile takes an exclusive LockFileEx range over the whole file — the
// Windows equivalent of flock. A held lock returns immediately with an error,
// so the caller's retry ladder works the same as on Unix. The byte range
// (offset 0, length max) covers the file regardless of its size.
func lockFile(f *os.File) error {
	const lockedLength = 0xFFFFFFFF
	overlapped := syscall.Overlapped{}
	r0, _, err := procLockFileEx.Call(
		uintptr(f.Fd()),
		lockfileExclusiveRange|lockfileFailImmediately,
		0,
		uintptr(lockedLength&0xFFFF), // nNumberOfBytesToLockLow
		uintptr(lockedLength>>16),    // nNumberOfBytesToLockHigh
		uintptr(unsafe.Pointer(&overlapped)),
	)
	if r0 == 0 {
		return err
	}
	return nil
}

// unlockFile releases the byte range; closing the handle does the same.
func unlockFile(f *os.File) error {
	const lockedLength = 0xFFFFFFFF
	overlapped := syscall.Overlapped{}
	r0, _, err := procUnlockFileEx.Call(
		uintptr(f.Fd()),
		0,
		uintptr(lockedLength&0xFFFF),
		uintptr(lockedLength>>16),
		uintptr(unsafe.Pointer(&overlapped)),
	)
	if r0 == 0 {
		return err
	}
	return nil
}
