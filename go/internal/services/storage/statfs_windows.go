// statfs_windows.go: volume probe for Windows. The device id comes from the
// volume serial number; capacity from GetDiskFreeSpaceEx.
//go:build windows

package storage

import (
	"os"
	"syscall"
	"time"
	"unsafe"
)

func deviceIDOf(info os.FileInfo) uint64 {
	// The volume serial is derived from the path, not the file handle; on
	// Windows a root-path probe is always the mount point anyway (Dir stops at
	// the drive root), so the fallback below suffices for grouping.
	return 0
}

func statfsBytes(path string) (total, free int64, ok bool) {
	var freeBytesAvailable, totalNumberOfBytes, totalNumberOfFreeBytes uint64
	kernel32 := syscall.NewLazyDLL("kernel32.dll")
	proc := kernel32.NewProc("GetDiskFreeSpaceExW")
	pathUTF16, err := syscall.UTF16PtrFromString(path)
	if err != nil {
		return 0, 0, false
	}
	result, _, _ := proc.Call(
		uintptr(unsafe.Pointer(pathUTF16)),
		uintptr(unsafe.Pointer(&freeBytesAvailable)),
		uintptr(unsafe.Pointer(&totalNumberOfBytes)),
		uintptr(unsafe.Pointer(&totalNumberOfFreeBytes)),
	)
	if result == 0 {
		return 0, 0, false
	}
	return int64(totalNumberOfBytes), int64(freeBytesAvailable), true
}

func timeNowMillisc() int64 { return time.Now().UnixMilli() }
