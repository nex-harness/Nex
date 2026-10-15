package bots

import "strings"

// workspace.go ports packages/services/src/bots/workspaceHelpers.ts.
//
// The identity rule lives here and is the one AGENTS.md fixes for every service:
// the key is `workspaceIdentity?.trim() || workspacePath`, so a remote workspace
// is never confused with a local one that happens to share a path.
func WorkspaceKey(workspacePath, workspaceIdentity string) string {
	if trimmed := strings.TrimSpace(workspaceIdentity); trimmed != "" {
		return trimmed
	}
	return workspacePath
}

// WorkspaceLabel ports getWorkspaceLabel: last path segment, both separators.
func WorkspaceLabel(workspacePath string) string {
	parts := strings.FieldsFunc(workspacePath, func(r rune) bool { return r == '/' || r == '\\' })
	if len(parts) == 0 {
		return workspacePath
	}
	return parts[len(parts)-1]
}

// CreateWorkspaceRef ports createWorkspaceRef.
func CreateWorkspaceRef(workspacePath, workspaceIdentity string) WorkspaceRef {
	return WorkspaceRef{
		ID:                WorkspaceKey(workspacePath, workspaceIdentity),
		Label:             WorkspaceLabel(workspacePath),
		WorkspacePath:     workspacePath,
		WorkspaceIdentity: workspaceIdentity,
	}
}

func isAllWorkspacesAllowed(allowed []string) bool {
	return len(allowed) == 0 || containsString(allowed, AllBotWorkspaces)
}

// NormalizeAllowedWorkspaces ports normalizeAllowedWorkspaces: an empty list and
// "*" both mean "everything", and duplicates collapse.
func NormalizeAllowedWorkspaces(allowed []string) []string {
	ids := make([]string, 0, len(allowed))
	for _, item := range allowed {
		if trimmed := strings.TrimSpace(item); trimmed != "" {
			ids = append(ids, trimmed)
		}
	}
	if isAllWorkspacesAllowed(ids) {
		return []string{AllBotWorkspaces}
	}
	seen := map[string]bool{}
	out := make([]string, 0, len(ids))
	for _, id := range ids {
		if seen[id] {
			continue
		}
		seen[id] = true
		out = append(out, id)
	}
	return out
}

// IsWorkspaceAllowed ports isWorkspaceAllowed.
func IsWorkspaceAllowed(workspaceID string, allowed []string) bool {
	return isAllWorkspacesAllowed(allowed) || containsString(allowed, workspaceID)
}

// FilterAllowedWorkspaces ports filterAllowedWorkspaces.
func FilterAllowedWorkspaces(refs []WorkspaceRef, allowed []string) []WorkspaceRef {
	if isAllWorkspacesAllowed(allowed) {
		return refs
	}
	filtered := make([]WorkspaceRef, 0, len(refs))
	for _, ref := range refs {
		if containsString(allowed, ref.ID) {
			filtered = append(filtered, ref)
		}
	}
	return filtered
}

// resolveCanonicalWorkspaceID ports resolveCanonicalWorkspaceId: an id saved
// before remote identities existed resolves to the single identity-carrying
// workspace with the same path.
func resolveCanonicalWorkspaceID(value string, refs []WorkspaceRef) string {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return ""
	}
	samePath := make([]WorkspaceRef, 0, len(refs))
	for _, ref := range refs {
		if ref.WorkspacePath == trimmed {
			samePath = append(samePath, ref)
		}
	}
	for _, ref := range refs {
		if ref.ID != trimmed {
			continue
		}
		if ref.WorkspaceIdentity == "" {
			identities := 0
			for _, candidate := range samePath {
				if candidate.WorkspaceIdentity != "" {
					identities++
				}
			}
			if identities == 1 {
				for _, candidate := range samePath {
					if candidate.WorkspaceIdentity != "" {
						return candidate.ID
					}
				}
			}
		}
		return ref.ID
	}
	if len(samePath) == 1 {
		return samePath[0].ID
	}
	return trimmed
}

// NormalizeConfiguredAllowedWorkspaces ports
// normalizeConfiguredAllowedWorkspaces.
func NormalizeConfiguredAllowedWorkspaces(allowed []string, refs []WorkspaceRef) []string {
	normalized := NormalizeAllowedWorkspaces(allowed)
	if isAllWorkspacesAllowed(normalized) {
		return []string{AllBotWorkspaces}
	}
	seen := map[string]bool{}
	out := make([]string, 0, len(normalized))
	for _, workspaceID := range normalized {
		canonical := resolveCanonicalWorkspaceID(workspaceID, refs)
		if seen[canonical] {
			continue
		}
		seen[canonical] = true
		out = append(out, canonical)
	}
	return out
}

// FirstAllowedWorkspace ports firstAllowedWorkspace.
func FirstAllowedWorkspace(refs []WorkspaceRef, bot Config) *WorkspaceRef {
	allowed := FilterAllowedWorkspaces(refs, bot.AllowedWorkspaces)
	if len(allowed) == 0 {
		return nil
	}
	first := allowed[0]
	return &first
}

// ResolveWorkspaceByValue ports resolveWorkspaceByValue: a 1-based index into
// the ALLOWED list wins, then id / label / path / key (case-insensitive).
func ResolveWorkspaceByValue(refs []WorkspaceRef, value string, allowedIDs []string) *WorkspaceRef {
	trimmed := strings.TrimSpace(value)
	workspaces := FilterAllowedWorkspaces(refs, allowedIDs)
	if index, ok := parsePositiveIndex(trimmed); ok {
		if index <= len(workspaces) {
			found := workspaces[index-1]
			return &found
		}
		return nil
	}
	normalized := strings.ToLower(trimmed)
	for _, workspace := range workspaces {
		if strings.ToLower(workspace.ID) == normalized ||
			strings.ToLower(workspace.Label) == normalized ||
			workspace.WorkspacePath == trimmed ||
			WorkspaceKey(workspace.WorkspacePath, workspace.WorkspaceIdentity) == trimmed {
			found := workspace
			return &found
		}
	}
	return nil
}

// parsePositiveIndex is the Number.parseInt + isFinite + `> 0` rule of
// resolveWorkspaceByValue. JS parseInt accepts a leading "+"/"-" and stops at
// the first non-digit, so "2abc" is index 2 there too.
func parsePositiveIndex(value string) (int, bool) {
	if value == "" {
		return 0, false
	}
	index := 0
	sign := 1
	switch value[0] {
	case '+':
		value = value[1:]
	case '-':
		sign = -1
		value = value[1:]
	}
	if value == "" {
		return 0, false
	}
	for i := 0; i < len(value); i++ {
		if value[i] < '0' || value[i] > '9' {
			break
		}
		index = index*10 + int(value[i]-'0')
	}
	if index == 0 {
		return 0, false
	}
	index *= sign
	if index <= 0 {
		return 0, false
	}
	return index, true
}
