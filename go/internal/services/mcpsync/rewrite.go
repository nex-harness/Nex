// rewrite.go: the filesystem-MCP config rewriting for remote import
// (rewriteFilesystemMcpConfig + path helpers). The join style follows the
// REMOTE path's shape (posix vs win32), never the local process platform —
// a Windows client syncing to a Linux host must not write backslashes.
package mcpsync

import (
	"math/rand"
	"path"
	"regexp"
	"strings"
)

var windowsPathRe = regexp.MustCompile(`^[a-zA-Z]:[\\/]`)

func readRandom(buffer []byte) (int, error) {
	// crypto/rand indirection for testability of the marker ids.
	return rand.Read(buffer)
}

// rewriteFilesystemMcpConfig ports rewriteFilesystemMcpConfig.
func rewriteFilesystemMcpConfig(name string, config map[string]any, localHomeDir, localWorkspacePath, remoteHomeDir, remoteWorkspacePath string) map[string]any {
	if !isStdioMcpConfig(config) || !isFilesystemMcpServer(name, config) {
		return config
	}
	args, ok := config["args"].([]any)
	if !ok {
		return config
	}
	next := cloneMap(config)
	nextArgs := make([]any, 0, len(args))
	for _, arg := range args {
		if s, ok := arg.(string); ok {
			nextArgs = append(nextArgs, rewritePathArgForRemote(s, localHomeDir, localWorkspacePath, remoteHomeDir, remoteWorkspacePath))
		} else {
			nextArgs = append(nextArgs, arg)
		}
	}
	next["args"] = nextArgs
	return next
}

func isStdioMcpConfig(config map[string]any) bool {
	configType := ""
	if value, ok := config["type"].(string); ok {
		configType = strings.ToLower(strings.TrimSpace(value))
	}
	if configType == "" {
		if command, ok := config["command"].(string); ok && strings.TrimSpace(command) != "" {
			return true
		}
	}
	return configType == "stdio"
}

func isFilesystemMcpServer(name string, config map[string]any) bool {
	normalizedName := strings.ToLower(strings.TrimSpace(name))
	if normalizedName == "filesystem" || normalizedName == "file-system" || normalizedName == "fs" {
		return true
	}
	var parts []string
	if command, ok := config["command"].(string); ok {
		parts = append(parts, command)
	}
	if args, ok := config["args"].([]any); ok {
		for _, arg := range args {
			if s, ok := arg.(string); ok {
				parts = append(parts, s)
			}
		}
	}
	haystack := strings.ToLower(strings.Join(parts, " "))
	return strings.Contains(haystack, "@modelcontextprotocol/server-filesystem") ||
		strings.Contains(haystack, "mcp-server-filesystem")
}

func rewritePathArgForRemote(arg, localHomeDir, localWorkspacePath, remoteHomeDir, remoteWorkspacePath string) string {
	if workspaceRelative := getRelativePathIfWithin(localWorkspacePath, arg); workspaceRelative != nil && strings.TrimSpace(remoteWorkspacePath) != "" {
		return joinRemotePath(remoteWorkspacePath, *workspaceRelative)
	}
	if homeRelative := getRelativePathIfWithin(localHomeDir, arg); homeRelative != nil {
		return joinRemotePath(remoteHomeDir, *homeRelative)
	}
	return arg
}

// getRelativePathIfWithin ports getRelativePathIfWithin: candidate == base
// → "", candidate under base/ → the suffix, else nil. Comparison is
// case-insensitive for Windows-shaped paths, on the /-normalized form.
func getRelativePathIfWithin(basePath, candidatePath string) *string {
	if strings.TrimSpace(basePath) == "" {
		return nil
	}
	base := normalizeComparablePath(basePath)
	candidate := normalizeComparablePath(candidatePath)
	if base == nil || candidate == nil {
		return nil
	}
	baseValue := *base
	candidateValue := *candidate
	if isWindowsPath(basePath) {
		baseValue = strings.ToLower(*base)
		candidateValue = strings.ToLower(*candidate)
	}
	if candidateValue == baseValue {
		empty := ""
		return &empty
	}
	prefix := baseValue
	if !strings.HasSuffix(prefix, "/") {
		prefix += "/"
	}
	if !strings.HasPrefix(candidateValue, prefix) {
		return nil
	}
	relative := candidateValue[len(prefix):]
	return &relative
}

// normalizeComparablePath ports normalizeComparablePath: only absolute
// (posix or windows-drive/UNC) paths participate; backslashes become
// slashes; trailing slashes drop.
func normalizeComparablePath(rawPath string) *string {
	trimmed := strings.TrimSpace(rawPath)
	if trimmed == "" {
		return nil
	}
	isWindowsPath := windowsPathRe.MatchString(trimmed) || strings.HasPrefix(trimmed, "\\\\")
	isPosixPath := strings.HasPrefix(trimmed, "/")
	if !isWindowsPath && !isPosixPath {
		return nil
	}
	value := strings.ReplaceAll(trimmed, "\\", "/")
	value = strings.TrimRight(value, "/")
	if value == "" {
		value = "/"
	}
	return &value
}

func isWindowsPath(rawPath string) bool {
	trimmed := strings.TrimSpace(rawPath)
	return windowsPathRe.MatchString(trimmed) || strings.HasPrefix(trimmed, "\\\\")
}

// joinRemotePath ports joinRemotePath: posix-style join for / bases,
// win32-style for drive/UNC bases, posix otherwise.
func joinRemotePath(remoteBasePath, relativePath string) string {
	if relativePath == "" {
		return remoteBasePath
	}
	segments := splitAndFilterSegments(relativePath)
	normalizedBasePath := strings.TrimSpace(remoteBasePath)
	if strings.HasPrefix(normalizedBasePath, "/") {
		return path.Join(append([]string{strings.ReplaceAll(normalizedBasePath, "\\", "/")}, segments...)...)
	}
	if windowsPathRe.MatchString(normalizedBasePath) || strings.HasPrefix(normalizedBasePath, "\\\\") {
		return win32Join(normalizedBasePath, segments)
	}
	return path.Join(append([]string{strings.ReplaceAll(normalizedBasePath, "\\", "/")}, segments...)...)
}

func splitAndFilterSegments(relativePath string) []string {
	var segments []string
	for _, segment := range regexp.MustCompile(`[\\/]+`).Split(relativePath, -1) {
		if segment != "" {
			segments = append(segments, segment)
		}
	}
	return segments
}

// win32Join joins with backslashes (the win32.join shape).
func win32Join(base string, segments []string) string {
	joined := strings.TrimRight(base, "\\")
	for _, segment := range segments {
		joined += "\\" + segment
	}
	return joined
}
