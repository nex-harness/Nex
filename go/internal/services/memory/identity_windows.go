//go:build windows

// identity_windows.go: Windows has no syscall.Stat_t; identity falls back to
// size + modtime only (dev/ino are unavailable), matching the platform's
// practical file-identity signal.
package memory

import "os"

func statToIdentity(info os.FileInfo) fileIdentity {
	return fileIdentity{
		size:      info.Size(),
		mtimeNs:   info.ModTime().UnixNano(),
		isFile:    info.Mode().IsRegular(),
		isSymlink: info.Mode()&os.ModeSymlink != 0,
	}
}
