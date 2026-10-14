// service_scope.go ports packages/services/src/git/commitMessageFileScope.ts
// (step 3.3): the current-session file scope filter for generateCommitMessage.
package git

import (
	"path/filepath"
	"strings"
)

func normalizeCommitMessageScopePath(path string) string {
	normalized := strings.TrimSpace(path)
	normalized = strings.ReplaceAll(normalized, "\\", "/")
	normalized = stripLeadingSlash(normalized)
	return strings.TrimRight(normalized, "/")
}

// isRelativePathInsideScope ports isRelativePathInsideScope.
func isRelativePathInsideScope(path string) bool {
	normalized := NormalizeGitPath(path)
	return normalized != "" &&
		normalized != ".." &&
		!strings.HasPrefix(normalized, "../") &&
		!filepath.IsAbs(normalized)
}

func addCommitMessageScopeCandidate(scope map[string]bool, path string) {
	normalizedPath := normalizeCommitMessageScopePath(path)
	if normalizedPath == "" {
		return
	}
	scope[normalizedPath] = true
}

// buildCommitMessageFileScope ports buildCommitMessageFileScope: the caller's
// session file paths (absolute, repo-relative or workspace-relative) expand
// into a set of repo-relative scope keys; nil means "no scope" (all files).
func buildCommitMessageFileScope(params struct {
	WorkspacePath           string
	RepoRoot                string
	WorkspaceInRepoPath     string
	CurrentSessionFilePaths []string
}) map[string]bool {
	sourcePaths := make([]string, 0, len(params.CurrentSessionFilePaths))
	for _, path := range params.CurrentSessionFilePaths {
		if trimmed := strings.TrimSpace(path); trimmed != "" {
			sourcePaths = append(sourcePaths, trimmed)
		}
	}
	if len(sourcePaths) == 0 {
		return nil
	}
	scope := map[string]bool{}
	normalizedWorkspaceInRepoPath := NormalizeWorkspaceInRepoPath(params.WorkspaceInRepoPath)
	for _, path := range sourcePaths {
		addCommitMessageScopeCandidate(scope, path)
		if filepath.IsAbs(path) {
			if repoRelative := relPath(params.RepoRoot, path); isRelativePathInsideScope(repoRelative) {
				addCommitMessageScopeCandidate(scope, repoRelative)
			}
			if workspaceRelative := relPath(params.WorkspacePath, path); isRelativePathInsideScope(workspaceRelative) {
				addCommitMessageScopeCandidate(scope, workspaceRelative)
			}
			continue
		}
		normalizedPath := normalizeCommitMessageScopePath(path)
		if normalizedWorkspaceInRepoPath != "." && !strings.HasPrefix(normalizedPath, normalizedWorkspaceInRepoPath+"/") {
			addCommitMessageScopeCandidate(scope, normalizedWorkspaceInRepoPath+"/"+normalizedPath)
		}
	}
	if len(scope) == 0 {
		return nil
	}
	return scope
}

// isCommitMessageFileInScope ports isCommitMessageFileInScope.
func isCommitMessageFileInScope(file GitFileChange, scope map[string]bool) bool {
	if scope == nil {
		return true
	}
	for _, path := range []string{file.Path, file.RepoRelativePath, file.WorkspaceRelativePath} {
		if scope[normalizeCommitMessageScopePath(path)] {
			return true
		}
	}
	return false
}

// FilterCommitMessageFilesByCurrentSession ports
// filterCommitMessageFilesByCurrentSession.
func FilterCommitMessageFilesByCurrentSession(files []GitFileChange, workspacePath, repoRoot, workspaceInRepoPath string, currentSessionFilePaths []string) []GitFileChange {
	scope := buildCommitMessageFileScope(struct {
		WorkspacePath           string
		RepoRoot                string
		WorkspaceInRepoPath     string
		CurrentSessionFilePaths []string
	}{workspacePath, repoRoot, workspaceInRepoPath, currentSessionFilePaths})
	out := make([]GitFileChange, 0, len(files))
	for _, file := range files {
		if isCommitMessageFileInScope(file, scope) {
			out = append(out, file)
		}
	}
	return out
}
