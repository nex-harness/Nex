// hostinfo.go: the two host facts the resolver needs, behind variables so a test
// can exercise the platform-scoped candidate chain without a real platform.
//
// The TS reads process.platform / process.arch / os.homedir() directly. Those
// are compile-time constants in Go, which would make the candidate chain
// untestable off-platform, so they are variables here and the resolver reads them
// through these functions.
package agentproc

import (
	"os"
	"runtime"
)

var (
	hostGOOS   = runtime.GOOS
	hostGOARCH = runtime.GOARCH
)

func homeDir() string {
	if dir, err := os.UserHomeDir(); err == nil {
		return dir
	}
	return os.Getenv("HOME")
}
