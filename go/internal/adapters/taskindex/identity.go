package taskindex

import (
	"strconv"
	"strings"
)

// identity.go — the remote workspace identity rules, ported from
// packages/shared/src/remote-workspace-identity.ts. Construction and parsing
// live in one place; business code never concatenates or splits an authority
// itself (AGENTS.md: Workspace Identity).
//
//   remote:ssh:<host>:<port>:<username>:<posixPath>
//   remote:wsl:<distro>[:<user>]:<posixPath>
//   remote:docker:<container>:<posixPath>

const remoteIdentityPrefix = "remote:"

// authoritySegments is the number of mandatory authority segments (not counting
// the kind): ssh = host/port/username, the other kinds = one.
var authoritySegments = map[string]int{"ssh": 3, "wsl": 1, "docker": 1}

// normalizeWorkspacePathForIdentity ports the shared normalizer.
func normalizeWorkspacePathForIdentity(workspacePath string) string {
	normalized := strings.ReplaceAll(workspacePath, "\\", "/")
	for strings.Contains(normalized, "//") {
		normalized = strings.ReplaceAll(normalized, "//", "/")
	}
	return "/" + strings.Trim(normalized, "/")
}

// BuildRemoteWorkspaceIdentity ports the shared remote-identity constructor.
func BuildRemoteWorkspaceIdentity(workspacePath, kind, host string, port int, username, distro, user, container string) string {
	path := normalizeWorkspacePathForIdentity(workspacePath)
	switch kind {
	case "ssh":
		return "remote:ssh:" + strings.ToLower(strings.TrimSpace(host)) + ":" + strconv.Itoa(port) + ":" + strings.TrimSpace(username) + ":" + path
	case "wsl":
		d := strings.TrimSpace(distro)
		if d == "" {
			d = "default"
		}
		if u := strings.TrimSpace(user); u != "" {
			return "remote:wsl:" + d + ":" + u + ":" + path
		}
		return "remote:wsl:" + d + ":" + path
	case "docker":
		return "remote:docker:" + container + ":" + path
	}
	return path
}

// ParseRemoteWorkspaceIdentity ports the shared parser: an illegal or non-remote
// identity returns false, so the caller falls back to "local workspacePath".
func ParseRemoteWorkspaceIdentity(identity string) (string, bool) {
	if !strings.HasPrefix(identity, remoteIdentityPrefix) {
		return "", false
	}
	rest := identity[len(remoteIdentityPrefix):]
	kindEnd := strings.Index(rest, ":")
	if kindEnd <= 0 {
		return "", false
	}
	kind := rest[:kindEnd]
	segments, ok := authoritySegments[kind]
	if !ok {
		return "", false
	}
	// Consume the authority segment by segment: the path may contain ":", so the
	// remainder after the last authority segment is the whole path.
	cursor := kindEnd + 1
	for i := 0; i < segments; i++ {
		next := strings.Index(rest[cursor:], ":")
		if next <= 0 {
			return "", false
		}
		cursor += next + 1
	}
	// WSL grew an optional user segment; a remote path always starts with "/",
	// which separates the legacy no-user form from the explicit-user form.
	if kind == "wsl" && rest[cursor:cursor+1] != "/" {
		userEnd := strings.Index(rest[cursor:], ":")
		if userEnd <= 0 {
			return "", false
		}
		cursor += userEnd + 1
	}
	workspacePath := rest[cursor:]
	if !strings.HasPrefix(workspacePath, "/") {
		return "", false
	}
	return workspacePath, true
}

// IsRemoteWorkspaceIdentity ports the shared predicate.
func IsRemoteWorkspaceIdentity(identity string) bool {
	_, ok := ParseRemoteWorkspaceIdentity(identity)
	return ok
}
