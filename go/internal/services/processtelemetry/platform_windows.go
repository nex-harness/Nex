//go:build windows

// platform_windows.go: the Windows whole-machine table read.
//
// Windows has no procfs and GetProcessTimes cannot enumerate the machine, so
// the table comes from a single PowerShell CIM query — the same call the TS
// uses. The Go-side guarantee is stronger than the TS one: the PowerShell
// script text is a package constant and the process is created with
// CREATE_NO_WINDOW, so no console flashes and nothing user-controlled is ever
// interpolated into the command line.
//
// This file is compile-verified only in step 3.8 (per the phase plan); the
// behavioral tests run on Linux.
package processtelemetry

import (
	"context"
	"os/exec"
	"runtime"

	"golang.org/x/sys/windows"
)

// windowsTableTimeoutMs ports WINDOWS_TABLE_TIMEOUT_MS.
const windowsTableTimeoutMs = 5_000

// windowsCIMQuery is the Get-CimInstance projection the TS pipes through
// ForEach-Object; kept verbatim so both runtimes read identical fields.
const windowsCIMQuery = "Get-CimInstance Win32_Process | ForEach-Object { '{0} {1} {2} {3} {4}' -f $_.ProcessId, $_.ParentProcessId, $_.WorkingSetSize, ($_.KernelModeTime + $_.UserModeTime), $_.Name }"

// DefaultTableReader returns the platform's whole-machine table reader.
func DefaultTableReader() TableReader { return ReadWindowsTable }

// defaultLogicalCPUCount ports os.cpus().length with a floor of 1.
func defaultLogicalCPUCount() int {
	count := runtime.NumCPU()
	if count < 1 {
		return 1
	}
	return count
}

// ReadWindowsTable runs the CIM query and parses its output.
func ReadWindowsTable(ctx context.Context) ([]ResourceRow, bool) {
	command := exec.CommandContext(ctx, "powershell.exe",
		"-NoLogo", "-NoProfile", "-NonInteractive", "-Command", windowsCIMQuery)
	command.SysProcAttr = &windows.SysProcAttr{HideWindow: true}
	output, err := command.Output()
	if err != nil {
		return nil, false
	}
	return ParseWindowsProcessTable(string(output)), true
}
