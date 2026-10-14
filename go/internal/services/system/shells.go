package system

// shells.go ports packages/services/src/system/integratedTerminalShells.ts —
// Windows-only shell discovery: CMD (from ComSpec) plus Git Bash (well-known
// install paths, then resolved from `git` on PATH). Pure functions on
// (env, isExecutable, platform) so a Linux CI run exercises every branch.

import (
	"path/filepath"
	"strings"
)

// ShellOption is IntegratedTerminalShellOption.
type ShellOption struct {
	Dialect string `json:"dialect"`
	ID      string `json:"id"`
	Label   string `json:"label"`
	Path    string `json:"path"`
	Source  string `json:"source"`
}

var windowsGitBashPaths = []string{
	`C:\Program Files\Git\bin\bash.exe`,
	`C:\Program Files (x86)\Git\bin\bash.exe`,
}

func listIntegratedTerminalShellOptions(env map[string]string, isExecutable func(string) bool, platform string) []ShellOption {
	if platform != "win32" {
		return []ShellOption{}
	}
	options := []ShellOption{createCommandPromptOption(env)}
	if gitBash, ok := resolveWindowsGitBash(env, isExecutable); ok {
		options = append(options, ShellOption{
			Dialect: "git-bash",
			ID:      "git-bash:" + gitBash.path,
			Label:   "Git Bash",
			Path:    gitBash.path,
			Source:  gitBash.source,
		})
	}
	return options
}

func createCommandPromptOption(env map[string]string) ShellOption {
	path := strings.TrimSpace(getEnvValue(env, "ComSpec"))
	if path == "" {
		path = "cmd.exe"
	}
	return ShellOption{Dialect: "cmd", ID: "cmd:" + path, Label: "CMD", Path: path, Source: "system"}
}

func resolveWindowsGitBash(env map[string]string, isExecutable func(string) bool) (struct{ path, source string }, bool) {
	for _, candidate := range windowsGitBashPaths {
		if isExecutable(candidate) {
			return struct{ path, source string }{candidate, "system"}, true
		}
	}
	gitExe, ok := findWindowsExecutable("git", env, isExecutable)
	if !ok {
		return struct{ path, source string }{}, false
	}
	for _, inferred := range inferWindowsGitBashPaths(gitExe) {
		if isExecutable(inferred) {
			return struct{ path, source string }{inferred, "path"}, true
		}
	}
	return struct{ path, source string }{}, false
}

func inferWindowsGitBashPaths(gitExe string) []string {
	// win32.dirname understands backslash separators; filepath.Dir on a
	// non-Windows host would treat the whole string as one segment.
	gitDir := winDirname(gitExe)
	return []string{
		winNormalize(winJoin(winJoin(gitDir, ".."), "bin"), "bash.exe"),
		winNormalize(winJoin(winJoin(winJoin(gitDir, ".."), ".."), "bin"), "bash.exe"),
	}
}

// winDirname is win32.dirname: everything before the last backslash.
func winDirname(p string) string {
	if i := strings.LastIndexByte(p, '\\'); i >= 0 {
		return p[:i]
	}
	return "."
}

// winNormalize applies win32.normalize to an already-joined path: resolve
// the `..` segments lexically on backslash-separated components.
func winNormalize(dir, name string) string {
	var parts []string
	for _, seg := range strings.Split(dir, "\\") {
		switch seg {
		case ".":
			continue
		case "..":
			if len(parts) > 0 && parts[len(parts)-1] != ".." {
				parts = parts[:len(parts)-1]
			} else {
				parts = append(parts, seg)
			}
		default:
			parts = append(parts, seg)
		}
	}
	parts = append(parts, name)
	return strings.Join(parts, "\\")
}

// winJoin joins two fragments with a backslash.
func winJoin(dir, name string) string {
	if dir == "" {
		return name
	}
	return strings.TrimSuffix(dir, "\\") + "\\" + name
}

func normalizeWindowsPath(p string) string {
	return filepath.Clean(p)
}

func windowsExecutableCandidates(command string, env map[string]string) []string {
	if strings.ContainsAny(command, `/\`) {
		return []string{command}
	}
	pathValue := getEnvValue(env, "PATH")
	if pathValue == "" {
		return []string{command}
	}
	var out []string
	for _, entry := range strings.Split(pathValue, ";") {
		if strings.TrimSpace(entry) == "" {
			continue
		}
		for _, ext := range windowsExecutableExtensions(env) {
			// the TS uses win32.join: always backslashes, even when running
			// (and testing) on a non-Windows host
			out = append(out, toWindowsJoin(entry, command+ext))
		}
	}
	return out
}

// toWindowsJoin joins two path fragments with a backslash (win32.join).
func toWindowsJoin(dir, name string) string {
	return strings.TrimSuffix(dir, `\`) + `\` + name
}

func findWindowsExecutable(command string, env map[string]string, isExecutable func(string) bool) (string, bool) {
	for _, candidate := range windowsExecutableCandidates(command, env) {
		if isExecutable(candidate) {
			return candidate, true
		}
	}
	return "", false
}

func windowsExecutableExtensions(env map[string]string) []string {
	raw := getEnvValue(env, "PATHEXT")
	var exts []string
	if raw == "" {
		exts = []string{".com", ".exe", ".bat", ".cmd"}
	} else {
		for _, e := range strings.Split(raw, ";") {
			if t := strings.ToLower(strings.TrimSpace(e)); t != "" {
				exts = append(exts, t)
			}
		}
	}
	for _, e := range exts {
		if e == ".exe" {
			return exts
		}
	}
	return append([]string{".exe"}, exts...)
}

// getEnvValue ports getWindowsEnvValue: case-insensitive env lookup.
func getEnvValue(env map[string]string, key string) string {
	if v, ok := env[key]; ok {
		return v
	}
	lower := strings.ToLower(key)
	for k, v := range env {
		if strings.ToLower(k) == lower {
			return v
		}
	}
	return ""
}
