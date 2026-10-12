package fsx

import (
	"errors"
	"syscall"
)

// errnoMeta returns the libuv code/description pair Node formats errno messages
// with ("ENOTDIR: not a directory, stat '<path>'"). Only the errnos the read/write
// paths can realistically hit are listed; anything else falls back to the Go error
// text rather than inventing a message Node would never produce.
func errnoMeta(errno syscall.Errno) (string, string, bool) {
	switch errno {
	case syscall.ENOTDIR:
		return "ENOTDIR", "not a directory", true
	case syscall.EEXIST:
		return "EEXIST", "file already exists", true
	case syscall.ELOOP:
		return "ELOOP", "too many symbolic links encountered", true
	case syscall.ENOSPC:
		return "ENOSPC", "no space left on device", true
	case syscall.EROFS:
		return "EROFS", "read-only file system", true
	case syscall.ENAMETOOLONG:
		return "ENAMETOOLONG", "name too long", true
	case syscall.EBUSY:
		return "EBUSY", "resource busy or locked", true
	case syscall.EINVAL:
		return "EINVAL", "invalid argument", true
	case syscall.EFBIG:
		return "EFBIG", "file too large", true
	case syscall.ENXIO:
		return "ENXIO", "no such device or address", true
	case syscall.EMFILE:
		return "EMFILE", "too many open files", true
	case syscall.ENFILE:
		return "ENFILE", "file table overflow", true
	}
	return "", "", false
}

// errnoOf extracts the underlying syscall.Errno from a wrapped path error.
func errnoOf(err error) (syscall.Errno, bool) {
	var errno syscall.Errno
	if errors.As(err, &errno) {
		return errno, true
	}
	return 0, false
}
