//go:build windows

// kill_windows.go: the process liveness probe on Windows. The TS uses
// process.kill(pid, 0) which libuv maps to OpenProcess(PROCESS_SET_QUOTA…)+
// GetExitCodeProcess; here OpenProcess with the query right answers the same
// question (handle != 0 → the process object still exists).
package device

import (
	"errors"
	"syscall"

	"golang.org/x/sys/windows"
)

// errNoProcess mirrors ESRCH on Windows (ERROR_INVALID_PARAMETER from
// OpenProcess for a reaped pid).
var errNoProcess = errors.New("no such process")

// signalProbe returns nil when the pid's process object exists.
func signalProbe(pid int64) error {
	handle, err := syscall.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, uint32(pid))
	if err != nil {
		// Access denied means the process EXISTS (like EPERM in the TS).
		return err
	}
	syscall.CloseHandle(handle)
	return nil
}
