// Package processtelemetry is the process telemetry port (step 3.8).
//
// Source: packages/services/src/process/*.ts (2.3k lines), of which the
// service-owned half is processResourceSampler.ts (whole-machine table read,
// CPU differential, Host descendant attribution) plus the process-tree
// snapshot/ownership pair. The terminator/waiter pair (processTreeTerminator.ts,
// processTreeWaiter.ts) belongs to the Desktop host lifecycle and stays with
// the Node sidecar — it drives signal delivery, which this package does not
// own.
//
// Design boundaries carried over from the TS comments:
//   - The whole-machine table is read ONCE per request, only from the resource
//     manager host. The main process must never spawn an external process.
//   - CPU is always machine-normalized (100% = every logical core saturated),
//     derived from the cputime delta between two samples.
//   - A PID is only ever reused after its baseline is dropped; the baseline
//     carries the command so a recycled PID cannot inherit the old delta.
//
// Platform: Linux reads /proc directly; macOS shells out to ps (build-tagged
// so the binary never links a fork/exec on Linux); Windows uses
// golang.org/x/sys/windows and is compile-verified only in this phase.
package processtelemetry

import (
	"runtime"
	"strings"
)

// Platform constants (the zod enum in nexProcessResourceSampleSchema).
const (
	PlatformLinux  = "linux"
	PlatformDarwin = "darwin"
	PlatformWin32  = "win32"
)

// Resource categories (ResourceUsageCategory).
const (
	CategoryBase            = "base"
	CategoryBuiltinPlugin   = "builtin-plugin"
	CategoryCommunityPlugin = "community-plugin"
)

// MCP sources (the nexProcessChildProcessSchema enum).
const (
	MCPSourceBuiltin = "builtin"
	MCPSourcePlugin  = "plugin"
	MCPSourceCustom  = "custom"
)

// CPU_BASELINE_TTL_MS ports CPU_BASELINE_TTL_MS: a baseline not seen again
// for this long is dropped, which is the PID-reuse guard.
const CPUBaselineTTLMs int64 = 60_000

// linuxClockTicksPerSecond ports LINUX_CLOCK_TICKS_PER_SECOND: Linux /proc
// always reports utime/stime in USER_HZ=100 regardless of the kernel HZ.
const linuxClockTicksPerSecond = 100

// ChildProcess ports NexProcessChildProcess (the CLI's process/childProcesses
// reply). pluginName is absent for custom MCP servers.
type ChildProcess struct {
	PID        int
	ServerName string
	MCPSource  string
	PluginName string
}

// Agent ports HostResourceUsageAgent: one Nex CLI agent plus the MCP children
// the CLI reported. A failed childProcesses request leaves Children empty, and
// every descendant then falls into the cli group.
type Agent struct {
	PID           int
	Provider      string
	WorkspacePath string
	Children      []ChildProcess
}

// UsageProcess ports HostResourceUsageProcess (ResourceUsageProcess without
// `sampled`: the host side only ever reports rows it has sampled).
type UsageProcess struct {
	PID         int     `json:"pid"`
	Name        string  `json:"name"`
	Category    string  `json:"category"`
	GroupKey    string  `json:"groupKey"`
	GroupLabel  string  `json:"groupLabel"`
	CPUPercent  float64 `json:"cpuPercent"`
	MemoryBytes int64   `json:"memoryBytes"`
}

// Sample ports ProcessResourceSample.
type Sample struct {
	PID        int     `json:"pid"`
	PPID       int     `json:"ppid"`
	RSSKb      int64   `json:"rssKb"`
	CPUPercent float64 `json:"cpuPercent"`
	Command    string  `json:"command"`
}

// ResourceRow is one whole-machine table row (ProcessResourceRow). The
// platform readers produce it; the sampler diffs it.
type ResourceRow struct {
	PID       int
	PPID      int
	RSSKb     int64
	CPUTimeMs int64
	Command   string
}

// CurrentPlatform returns the Node-compatible platform string.
func CurrentPlatform() string {
	switch runtime.GOOS {
	case "darwin":
		return PlatformDarwin
	case "windows":
		return PlatformWin32
	default:
		return PlatformLinux
	}
}

// CommandDisplayName ports commandDisplayName: the last path segment without
// a trailing .exe. An empty command still yields a name, because the resource
// manager must never show a blank row.
func CommandDisplayName(command string) string {
	trimmed := strings.TrimSpace(command)
	// basename: the last separator is either '/' or '\' (Windows paths reach
	// the sampler through the same table on every platform).
	name := trimmed
	if index := strings.LastIndexAny(name, `/\`); index >= 0 {
		name = name[index+1:]
	}
	name = strings.TrimSuffix(name, ".exe")
	if name == "" {
		name = trimmed
	}
	if name == "" {
		return "process"
	}
	return name
}

// FormatNexAgentProcessName ports formatNexAgentProcessName:
// `nex-agent-<provider>-<workspace-tag>`, each segment sanitized and capped.
func FormatNexAgentProcessName(provider, workspacePath string) string {
	return joinNexProcessName("agent", provider, workspaceTag(workspacePath))
}

const (
	nexProcessPrefix           = "nex"
	maxProcessNameSegmentChars = 24
)

// joinNexProcessName ports joinNexProcessName.
func joinNexProcessName(segments ...string) string {
	sanitized := make([]string, 0, len(segments)+1)
	sanitized = append(sanitized, nexProcessPrefix)
	for _, segment := range segments {
		if value := sanitizeProcessNameSegment(segment); value != "" {
			sanitized = append(sanitized, value)
		}
	}
	return strings.Join(sanitized, "-")
}

// sanitizeProcessNameSegment ports sanitizeProcessNameSegment: lowercased,
// non-alphanumerics collapsed to '-', trimmed, capped at 24 chars.
func sanitizeProcessNameSegment(value string) string {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return ""
	}
	// Go strings are bytes; ASCII folding is enough for the allowed charset
	// and keeps the truncation length in characters (the TS slice is UTF-16
	// units, and every surviving segment is ASCII by construction).
	var builder strings.Builder
	previousHyphen := false
	for _, r := range strings.ToLower(trimmed) {
		isAlphanumeric := (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9')
		if isAlphanumeric {
			builder.WriteRune(r)
			previousHyphen = false
			continue
		}
		if !previousHyphen {
			builder.WriteByte('-')
			previousHyphen = true
		}
	}
	normalized := strings.Trim(builder.String(), "-")
	if normalized == "" {
		return ""
	}
	if len(normalized) > maxProcessNameSegmentChars {
		return normalized[:maxProcessNameSegmentChars]
	}
	return normalized
}

// workspaceTag ports pickWorkspaceTag: the last non-empty path segment, so
// the process name never carries a full path.
func workspaceTag(workspacePath string) string {
	trimmed := strings.TrimSpace(workspacePath)
	if trimmed == "" {
		return ""
	}
	parts := strings.FieldsFunc(trimmed, func(r rune) bool { return r == '/' || r == '\\' })
	if len(parts) == 0 {
		return trimmed
	}
	return parts[len(parts)-1]
}

// roundPercent ports roundPercent: one decimal place, non-finite collapses to 0.
func roundPercent(value float64) float64 {
	if value != value || value > 1.7e308 || value < -1.7e308 {
		return 0
	}
	return roundPercentTenths(value)
}
