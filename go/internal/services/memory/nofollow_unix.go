//go:build !windows

// nofollow_unix.go: O_NOFOLLOW is available on all Unix-likes.
package memory

import (
	"os"
	"syscall"
)

func noFollowSupported() bool { return true }

// openNoFollow opens with O_NOFOLLOW (the guard against reading through a
// concurrently-planted symlink).
func openNoFollow(path string) (*os.File, error) {
	return os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW, 0)
}
