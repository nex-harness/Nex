//go:build windows

// nofollow_windows.go: O_NOFOLLOW and Stat_t do not exist on Windows; the
// open follows symlinks there and the lstat identity checks carry the guard
// (dev/ino/mtime come back zero, so the name+readdir checks do the work).
package memory

import "os"

// noFollowSupported reports whether O_NOFOLLOW can be used.
func noFollowSupported() bool { return false }

// openNoFollow: Windows has no O_NOFOLLOW; a plain read-only open.
func openNoFollow(path string) (*os.File, error) {
	return os.Open(path)
}
