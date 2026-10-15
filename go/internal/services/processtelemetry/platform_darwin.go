//go:build darwin

// platform_darwin.go: the macOS fallback. There is no procfs, so the table
// comes from `ps -axo pid=,ppid=,rss=,cputime=,comm=`.
package processtelemetry

import (
	"context"
	"os/exec"
	"runtime"
)

// DefaultTableReader returns the platform's whole-machine table reader.
func DefaultTableReader() TableReader { return ReadDarwinTable }

// defaultLogicalCPUCount ports os.cpus().length with a floor of 1.
func defaultLogicalCPUCount() int {
	count := runtime.NumCPU()
	if count < 1 {
		return 1
	}
	return count
}

// ReadDarwinTable runs ps and parses its output. Any failure (missing binary,
// timeout, non-zero exit) skips the round instead of erroring.
func ReadDarwinTable(ctx context.Context) ([]ResourceRow, bool) {
	// The timeout is enforced by the context the caller already owns, matching
	// the TS execFile timeout ownership.
	output, err := exec.CommandContext(ctx, "ps", "-axo", "pid=,ppid=,rss=,cputime=,comm=").Output()
	if err != nil {
		return nil, false
	}
	return ParseDarwinProcessTable(string(output)), true
}
