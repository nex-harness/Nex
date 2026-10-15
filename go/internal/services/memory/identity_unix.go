//go:build !windows

// identity_unix.go: full dev/ino/size/mtimeNs/ctimeNs identity.
package memory

import (
	"os"
	"syscall"
)

func statToIdentity(info os.FileInfo) fileIdentity {
	identity := fileIdentity{
		size:      info.Size(),
		isFile:    info.Mode().IsRegular(),
		isSymlink: info.Mode()&os.ModeSymlink != 0,
	}
	if sys, ok := info.Sys().(*syscall.Stat_t); ok {
		identity.dev = int64(sys.Dev)
		identity.ino = int64(sys.Ino)
		identity.mtimeNs = sys.Mtim.Nano()
		identity.ctimeNs = sys.Ctim.Nano()
	}
	return identity
}
