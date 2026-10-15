// procfs.go: the Linux whole-machine table read straight out of /proc.
//
// No external process is spawned (the resource manager runs where spawning an
// external process is forbidden), and no shell quoting is involved. A process
// that exits mid-read is skipped: /proc entries are inherently racy.
package processtelemetry

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// ProcRoot is the /proc mount point; tests point it at a fixture tree.
const ProcRoot = "/proc"

// parseProcStat ports parseLinuxProcStat. comm is parenthesised and may itself
// contain spaces and parentheses, so the fields after it are split from the
// LAST ')'. rest[0]=state, rest[1]=ppid, rest[11]=utime, rest[12]=stime —
// those are /proc field numbers 4/5 and 14/15 counting from 1.
func parseProcStat(content string) (ResourceRow, bool) {
	open := strings.IndexByte(content, '(')
	closeIndex := strings.LastIndexByte(content, ')')
	if open < 0 || closeIndex < open {
		return ResourceRow{}, false
	}
	pid, ok := parseNonNegativeInt(strings.TrimSpace(content[:open]))
	if !ok || pid <= 0 {
		return ResourceRow{}, false
	}
	command := content[open+1 : closeIndex]
	fields := strings.Fields(strings.TrimSpace(content[closeIndex+1:]))
	if len(fields) < 13 {
		return ResourceRow{}, false
	}
	ppid, ok := parseNonNegativeInt(fields[1])
	if !ok {
		return ResourceRow{}, false
	}
	utime, ok := parseNonNegativeInt(fields[11])
	if !ok {
		return ResourceRow{}, false
	}
	stime, ok := parseNonNegativeInt(fields[12])
	if !ok {
		return ResourceRow{}, false
	}
	return ResourceRow{
		PID: int(pid), PPID: int(ppid), Command: command,
		// USER_HZ is always 100 on Linux regardless of the kernel CONFIG_HZ.
		CPUTimeMs: (utime + stime) * 1000 / linuxClockTicksPerSecond,
	}, true
}

// ParseProcStartTicks ports the field-22 read in refineLinuxProcessIdentity:
// the boot-relative start tick, which is more precise than ps's second-level
// lstart and therefore survives a same-second PID reuse.
func ParseProcStartTicks(content string) (int64, bool) {
	closeIndex := strings.LastIndexByte(content, ')')
	if closeIndex < 0 {
		return 0, false
	}
	fields := strings.Fields(strings.TrimSpace(content[closeIndex+1:]))
	// rest[0]=state ... rest[19]=starttime (field 22 counting from 1).
	if len(fields) < 20 {
		return 0, false
	}
	return parseNonNegativeInt(fields[19])
}

// ParseProcParentAndGroup ports the ppid/pgid read from /proc/<pid>/stat.
func ParseProcParentAndGroup(content string) (ppid, pgid int, ok bool) {
	closeIndex := strings.LastIndexByte(content, ')')
	if closeIndex < 0 {
		return 0, 0, false
	}
	fields := strings.Fields(strings.TrimSpace(content[closeIndex+1:]))
	if len(fields) < 3 {
		return 0, 0, false
	}
	parsedPPID, ok := parseNonNegativeInt(fields[1])
	if !ok {
		return 0, 0, false
	}
	parsedPGID, ok := parsePositiveInt(fields[2])
	if !ok {
		return 0, 0, false
	}
	return int(parsedPPID), parsedPGID, true
}

// parseVmRssKb ports parseLinuxVmRssKb: `VmRSS:\t 1234 kB` from /proc/status.
func parseVmRssKb(content string) int64 {
	for _, line := range strings.Split(content, "\n") {
		rest, ok := strings.CutPrefix(strings.TrimSpace(line), "VmRSS:")
		if !ok {
			continue
		}
		rest = strings.TrimSpace(rest)
		rest = strings.TrimSuffix(strings.TrimSpace(rest), "kB")
		value, ok := parseNonNegativeInt(strings.TrimSpace(rest))
		if !ok {
			return 0
		}
		return value
	}
	return 0
}

// ReadProcTable reads /proc and returns the rows for every readable process.
// It is the linux implementation of TableReader.
func ReadProcTable(ctx context.Context) ([]ResourceRow, bool) {
	return ReadProcTableAt(ctx, ProcRoot)
}

// ReadProcTableAt is ReadProcTable against an explicit /proc root (tests).
func ReadProcTableAt(ctx context.Context, root string) ([]ResourceRow, bool) {
	if err := ctx.Err(); err != nil {
		return nil, false
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		return nil, false
	}
	rows := make([]ResourceRow, 0, len(entries))
	for _, entry := range entries {
		name := entry.Name()
		if _, ok := parseNonNegativeInt(name); !ok {
			continue
		}
		if err := ctx.Err(); err != nil {
			return nil, false
		}
		row, ok := readProcRow(filepath.Join(root, name, "stat"), filepath.Join(root, name, "status"))
		if !ok {
			continue
		}
		rows = append(rows, row)
	}
	return rows, true
}

// readProcRow reads one process's stat + status; a process that exits between
// the two reads yields ok=false and is skipped.
func readProcRow(statPath, statusPath string) (ResourceRow, bool) {
	statBytes, err := os.ReadFile(statPath)
	if err != nil {
		return ResourceRow{}, false
	}
	row, ok := parseProcStat(string(statBytes))
	if !ok {
		return ResourceRow{}, false
	}
	statusBytes, err := os.ReadFile(statusPath)
	if err != nil {
		return ResourceRow{}, false
	}
	row.RSSKb = parseVmRssKb(string(statusBytes))
	return row, true
}

// parseNonNegativeInt ports parseNonNegativeInteger.
func parseNonNegativeInt(text string) (int64, bool) {
	if text == "" {
		return 0, false
	}
	value, err := strconv.ParseInt(text, 10, 64)
	if err != nil || value < 0 {
		return 0, false
	}
	return value, true
}

// parsePositiveInt ports parsePositiveInteger.
func parsePositiveInt(text string) (int, bool) {
	value, err := strconv.Atoi(text)
	if err != nil || value <= 0 {
		return 0, false
	}
	return value, true
}
