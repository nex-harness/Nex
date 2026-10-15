//go:build linux

// platform_linux.go: the linux TableReader. Build-tagged so a Linux binary
// never links the exec path used by the darwin `ps` fallback or the Windows
// PowerShell query.
package processtelemetry

import "runtime"

// DefaultTableReader returns the platform's whole-machine table reader.
func DefaultTableReader() TableReader { return ReadProcTable }

// defaultLogicalCPUCount ports os.cpus().length with a floor of 1.
func defaultLogicalCPUCount() int {
	count := runtime.NumCPU()
	if count < 1 {
		return 1
	}
	return count
}
