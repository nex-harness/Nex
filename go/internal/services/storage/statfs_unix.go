// statfs_unix.go: statfs for unix (volumeProbe's capacity half).
//go:build !windows

package storage

import (
	"os"
	"syscall"
)

func deviceIDOf(info os.FileInfo) uint64 {
	if stat, ok := info.Sys().(*syscall.Stat_t); ok {
		return uint64(stat.Dev)
	}
	return 0
}

func statfsBytes(path string) (total, free int64, ok bool) {
	var stat syscall.Statfs_t
	if err := syscall.Statfs(path, &stat); err != nil {
		return 0, 0, false
	}
	return int64(stat.Blocks) * int64(stat.Bsize), int64(stat.Bavail) * int64(stat.Bsize), true
}

var _ = timeNowMillis
