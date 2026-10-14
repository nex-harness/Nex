// Package git ports packages/services/src/git (step 3.3): the IGitService and
// IGitCheckpointService Tier-1b surface.
//
// config.go ports git/config.ts verbatim: the timeout/output budgets, the git
// binary discovery candidates, the local-env scrub list, and the
// workspace-scope path helpers shared by every layer.
package git

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

// Budgets and limits, verbatim from config.ts.
const (
	DefaultGitDiscoveryTimeoutMs = 3_000
	DefaultGitCommandTimeoutMs   = 15_000
	// DefaultGitPushTimeoutMs: `git push` 可能会被仓库的 pre-push hook 阻塞较长时间
	// （例如执行 `pnpm test`）。继续沿用普通 Git 命令的 15s 超时会把显式 push 误判成
	// 失败，因此单独放宽 push 超时。
	DefaultGitPushTimeoutMs = 10 * 60_000
	DefaultGitDiffTimeoutMs = 20_000
	DefaultGitOutputBytes   = 512 * 1024
	// DefaultGitPushOutputBytes: pre-push hook 可能会输出完整测试日志；继续沿用普通
	// Git 命令的 512KB 上限，会在真正 push 完成前因为日志过多被截断终止。
	DefaultGitPushOutputBytes   = 8 * 1024 * 1024
	DefaultGitDiffBytes         = 1024 * 1024
	GitUntrackedStatMaxBytes    = 1024 * 1024
	GitUntrackedStatChunkBytes  = 64 * 1024
	GitUntrackedStatConcurrency = 4
)

// windowsGitBinaryCandidates mirrors WINDOWS_GIT_BINARY_CANDIDATES: the
// fallback list used when neither NEX_GIT_BINARY nor PATH resolve.
// The paths use forward slashes only because exec on Windows accepts them;
// the TS builds the same shapes from ProgramFiles env vars.
func windowsGitBinaryCandidates(env func(string) string) []string {
	programW6432 := firstNonEmpty(env("ProgramW6432"), env("ProgramFiles"), `C:\Program Files`)
	programX86 := firstNonEmpty(env("ProgramFiles(x86)"), `C:\Program Files (x86)`)
	return []string{
		filepath.Join(programW6432, "Git", "cmd", "git.exe"),
		filepath.Join(programW6432, "Git", "bin", "git.exe"),
		filepath.Join(programX86, "Git", "cmd", "git.exe"),
		filepath.Join(programX86, "Git", "bin", "git.exe"),
	}
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}

// gitLocalEnvVars is GIT_LOCAL_ENV_VARS: the per-repo env a pre-push hook
// injects. Clearing them keeps service commands from "串仓" to the hook's repo.
var gitLocalEnvVars = []string{
	"GIT_ALTERNATE_OBJECT_DIRECTORIES",
	"GIT_COMMON_DIR",
	"GIT_CONFIG",
	"GIT_CONFIG_COUNT",
	"GIT_CONFIG_PARAMETERS",
	"GIT_DIR",
	"GIT_GRAFT_FILE",
	"GIT_IMPLICIT_WORK_TREE",
	"GIT_INDEX_FILE",
	"GIT_INTERNAL_SUPER_PREFIX",
	"GIT_NAMESPACE",
	"GIT_OBJECT_DIRECTORY",
	"GIT_PREFIX",
	"GIT_REPLACE_REF_BASE",
	"GIT_SHALLOW_FILE",
	"GIT_WORK_TREE",
}

// GetGitBinaryCandidates ports getGitBinaryCandidates: NEX_GIT_BINARY, "git",
// then the Windows fallbacks. env is os.Getenv in production.
func GetGitBinaryCandidates(env func(string) string, isWindows bool) []string {
	candidates := []string{strings.TrimSpace(env("NEX_GIT_BINARY")), "git"}
	if isWindows {
		candidates = append(candidates, windowsGitBinaryCandidates(env)...)
	}
	out := candidates[:0]
	for _, candidate := range candidates {
		if candidate != "" {
			out = append(out, candidate)
		}
	}
	return out
}

// GetGitCommandEnv ports getGitCommandEnv: the process env minus the local git
// vars, plus the Nex constraint variables. Note the TS does NOT set
// GIT_TERMINAL_PROMPT — confirmed by reading config.ts; prompt avoidance comes
// from stdin being closed and argv-only invocations.
func GetGitCommandEnv(environ []string) []string {
	scrub := make(map[string]bool, len(gitLocalEnvVars))
	for _, name := range gitLocalEnvVars {
		scrub[name] = true
	}
	var out []string
	for _, entry := range environ {
		name, _, _ := strings.Cut(entry, "=")
		if scrub[name] {
			continue
		}
		out = append(out, entry)
	}
	return append(out,
		"GIT_OPTIONAL_LOCKS=0",
		"GIT_PAGER=cat",
		"PAGER=cat",
		"TERM=dumb",
		"LC_ALL=C",
		"LANG=C",
	)
}

// EnvironFunc adapts the environment source for tests.
func EnvironFunc() []string { return os.Environ() }

// IsWindowsOS reports the port's platform decision (runtime.GOOS).
func IsWindowsOS() bool { return runtime.GOOS == "windows" }

// GetGitNullDevicePath ports getGitNullDevicePath.
func GetGitNullDevicePath(isWindows bool) string {
	if isWindows {
		return "NUL"
	}
	return "/dev/null"
}

// NormalizeGitPath ports normalizeGitPath: backslashes to forward slashes.
func NormalizeGitPath(path string) string {
	return strings.ReplaceAll(path, "\\", "/")
}

// NormalizeWorkspaceInRepoPath ports normalizeWorkspaceInRepoPath: backslash
// normalization, then `/^\.?\//` (one optional dot + ONE slash), then trailing
// slashes. The exact TS regex order is preserved — `.//x//` legitimately
// normalizes to `/x` because the second leading slash is not stripped.
func NormalizeWorkspaceInRepoPath(path string) string {
	normalized := strings.ReplaceAll(path, "\\", "/")
	// /^\.?\//: strip an optional leading dot followed by exactly one slash.
	if strings.HasPrefix(normalized, "./") {
		normalized = normalized[2:]
	} else if strings.HasPrefix(normalized, "/") {
		normalized = normalized[1:]
	}
	// /\/+$/: strip every trailing slash.
	normalized = strings.TrimRight(normalized, "/")
	if normalized == "" {
		return "."
	}
	return normalized
}

// IsPathInWorkspaceScope ports isPathInWorkspaceScope.
func IsPathInWorkspaceScope(repoRelativePath, workspaceInRepoPath string) bool {
	normalizedPath := stripLeadingSlash(NormalizeGitPath(repoRelativePath))
	normalizedWorkspace := NormalizeWorkspaceInRepoPath(workspaceInRepoPath)
	if normalizedWorkspace == "." {
		return true
	}
	return normalizedPath == normalizedWorkspace || strings.HasPrefix(normalizedPath, normalizedWorkspace+"/")
}

// ToWorkspaceRelativeGitPath ports toWorkspaceRelativeGitPath.
func ToWorkspaceRelativeGitPath(repoRelativePath, workspaceInRepoPath string) string {
	normalizedPath := stripLeadingSlash(NormalizeGitPath(repoRelativePath))
	normalizedWorkspace := NormalizeWorkspaceInRepoPath(workspaceInRepoPath)
	if normalizedWorkspace == "." {
		return normalizedPath
	}
	if normalizedPath == normalizedWorkspace {
		return "."
	}
	if strings.HasPrefix(normalizedPath, normalizedWorkspace+"/") {
		return normalizedPath[len(normalizedWorkspace)+1:]
	}
	return normalizedPath
}

func stripLeadingSlash(path string) string {
	// /^\.?\//: same single-slash rule as NormalizeWorkspaceInRepoPath.
	if strings.HasPrefix(path, "./") {
		return path[2:]
	}
	return strings.TrimPrefix(path, "/")
}
