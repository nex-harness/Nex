package automations

import (
	"strings"
)

// workspaceScope mirrors AutomationWorkspaceScope: writes are restricted to
// the caller's workspace, reads without a path are global.
type workspaceScope struct {
	path     string
	identity string
}

func scopeFrom(path, identity string) workspaceScope {
	return workspaceScope{path: strings.TrimSpace(path), identity: strings.TrimSpace(identity)}
}

// key is workspaceIdentity?.trim() || workspacePath (resolveWorkspaceKey).
func (s workspaceScope) key() string {
	if s.identity != "" {
		return s.identity
	}
	return s.path
}

// matches reports whether the automation belongs to the scope; a scope
// without a path does not filter.
func (s workspaceScope) matches(a *Automation) bool {
	if s.path == "" {
		return true
	}
	return a.WorkspaceKey == s.key()
}

func workspaceKeyOf(path, identity string) string {
	return scopeFrom(path, identity).key()
}
