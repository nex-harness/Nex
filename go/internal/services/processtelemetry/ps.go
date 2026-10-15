// ps.go: the `ps` output parsers. macOS is the platform that actually shells
// out to ps (darwin.go); the parsers live here so both the table reader and
// the tests can reach them without a build tag.
package processtelemetry

import (
	"math"
	"strconv"
	"strings"
)

// ParseDarwinProcessTable ports parseDarwinProcessTable for
// `ps -axo pid=,ppid=,rss=,cputime=,comm=`. comm may contain spaces, so the
// first four columns are taken and the remainder is the command.
func ParseDarwinProcessTable(stdout string) []ResourceRow {
	var rows []ResourceRow
	for _, line := range strings.Split(stdout, "\n") {
		line = strings.TrimSuffix(line, "\r")
		fields := splitColumns(line, 5)
		if len(fields) < 5 {
			continue
		}
		pid, ok := parseNonNegativeInt(fields[0])
		if !ok || pid <= 0 {
			continue
		}
		ppid, ok := parseNonNegativeInt(fields[1])
		if !ok {
			continue
		}
		rssKb, ok := parseNonNegativeInt(fields[2])
		if !ok {
			continue
		}
		cpuTimeMs, ok := ParseCPUTimeText(fields[3])
		if !ok {
			continue
		}
		rows = append(rows, ResourceRow{
			PID: int(pid), PPID: int(ppid), RSSKb: rssKb,
			CPUTimeMs: cpuTimeMs, Command: strings.TrimSpace(fields[4]),
		})
	}
	return rows
}

// ParseCPUTimeText ports parseCpuTimeText: macOS `[[dd-]hh:]mm:ss.cc` and
// Linux `[dd-]hh:mm:ss` → milliseconds. A fraction is truncated to whole
// seconds exactly like Number("0.5") arithmetic in the TS.
func ParseCPUTimeText(text string) (int64, bool) {
	trimmed := strings.TrimSpace(text)
	if trimmed == "" {
		return 0, false
	}
	days := 0
	clock := trimmed
	if index := strings.Index(trimmed, "-"); index > 0 {
		parsedDays, ok := parseNonNegativeInt(trimmed[:index])
		if !ok {
			return 0, false
		}
		days = int(parsedDays)
		clock = trimmed[index+1:]
	}
	parts := strings.Split(clock, ":")
	if len(parts) == 0 || len(parts) > 3 {
		return 0, false
	}
	seconds, ok := parseCPUTimeComponent(parts[len(parts)-1])
	if !ok {
		return 0, false
	}
	minutes := 0.0
	hours := 0.0
	if len(parts) >= 2 {
		if minutes, ok = parseCPUTimeComponent(parts[len(parts)-2]); !ok {
			return 0, false
		}
	}
	if len(parts) == 3 {
		if hours, ok = parseCPUTimeComponent(parts[0]); !ok {
			return 0, false
		}
	}
	total := float64((days*24+int(hours))*60+int(minutes))*60 + seconds
	return int64(math.Round(total * 1000)), true
}

// parseCPUTimeComponent parses one colon-separated clock component; the macOS
// form carries a fractional-seconds suffix.
func parseCPUTimeComponent(text string) (float64, bool) {
	if text == "" {
		return 0, false
	}
	value, err := strconv.ParseFloat(text, 64)
	if err != nil || math.IsNaN(value) || math.IsInf(value, 0) || value < 0 {
		return 0, false
	}
	return value, true
}

// ParseWindowsProcessTable ports parseWindowsProcessTable for the PowerShell
// output `pid ppid workingSetBytes cpu100ns name...`.
func ParseWindowsProcessTable(stdout string) []ResourceRow {
	var rows []ResourceRow
	for _, line := range strings.Split(stdout, "\n") {
		line = strings.TrimSuffix(line, "\r")
		fields := splitColumns(line, 5)
		if len(fields) < 5 {
			continue
		}
		pid, ok := parseNonNegativeInt(fields[0])
		if !ok || pid <= 0 {
			continue
		}
		ppid, ok := parseNonNegativeInt(fields[1])
		if !ok {
			continue
		}
		workingSetBytes, ok := parseNonNegativeInt(fields[2])
		if !ok {
			continue
		}
		cpu100ns, ok := parseNonNegativeInt(fields[3])
		if !ok {
			continue
		}
		rows = append(rows, ResourceRow{
			PID: int(pid), PPID: int(ppid),
			// PowerShell reports bytes; the row carries KiB like every other
			// platform (JS rounds, Go integer division truncates the same way
			// for the non-negative values involved).
			RSSKb: workingSetBytes / 1024,
			// 100ns ticks → milliseconds.
			CPUTimeMs: cpu100ns / windows100nsPerMs,
			Command:   strings.TrimSpace(fields[4]),
		})
	}
	return rows
}

// windows100nsPerMs ports WINDOWS_100NS_PER_MS.
const windows100nsPerMs = 10_000

// splitColumns splits on runs of whitespace and returns at most limit fields;
// the final field keeps any remaining spaces (ps comm / Windows name do).
func splitColumns(line string, limit int) []string {
	fields := make([]string, 0, limit)
	remaining := strings.TrimSpace(line)
	for len(fields) < limit-1 && remaining != "" {
		index := strings.IndexAny(remaining, " \t")
		if index < 0 {
			fields = append(fields, remaining)
			return fields
		}
		fields = append(fields, remaining[:index])
		remaining = strings.TrimLeft(remaining[index:], " \t")
	}
	if remaining != "" {
		fields = append(fields, remaining)
	}
	return fields
}
