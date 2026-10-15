// errnoName maps syscall errno values to the Node-style code names the TS
// stores in StoragePathError.code ("ENOENT", "EACCES", ...).
package storage

import (
	"errors"
	"io/fs"
	"runtime"
)

func errnoName(err error) string {
	var errno interface{ Is(error) bool }
	_ = errno
	if errors.Is(err, fs.ErrNotExist) {
		return "ENOENT"
	}
	if errors.Is(err, fs.ErrPermission) {
		return "EACCES"
	}
	if errors.Is(err, fs.ErrExist) {
		return "EEXIST"
	}
	if runtime.GOOS == "windows" {
		// Windows shares ENOENT for "path not found"; good enough for the
		// display-only error code (the UI shows the string, nothing branches).
		return "UNKNOWN"
	}
	return "UNKNOWN"
}
