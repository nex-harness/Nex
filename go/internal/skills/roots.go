// roots.go: skill root resolution shared by the skills service and the agent.
// Ports resolveAncestorWorkspaceRoots + findWorktreeRoot (skillsService.ts
// lines 140–182) and the user-level root helpers, plus the nested-root
// filtering and realpath dedupe used before scanning.
package skills

import (
	"os"
	"path/filepath"
	"strings"
)

// RootDirs are the two workspace-level skill roots of one workspace
// directory: both .nex/skills and .agents/skills are always collected (the
// agent runtime merges both scans; the UI previously treated .agents as a
// fallback only, which split discovery semantics).
func RootDirs(workspacePath string) []string {
	return []string{
		filepath.Join(workspacePath, ".nex", "skills"),
		filepath.Join(workspacePath, ".agents", "skills"),
	}
}

// UserNexSkillRoot is the user-level .nex/skills root.
func UserNexSkillRoot(homeDir string) string {
	return filepath.Join(homeDir, ".nex", "skills")
}

// UserAgentsSkillRoot is the user-level .agents/skills compatibility root.
func UserAgentsSkillRoot(homeDir string) string {
	return filepath.Join(homeDir, ".agents", "skills")
}

// FindWorktreeRoot walks up until a .git marker (directory or file —
// worktrees use a file), or returns "" at the filesystem root.
func FindWorktreeRoot(workingDirectory string) string {
	current := workingDirectory
	for {
		if _, err := os.Stat(filepath.Join(current, ".git")); err == nil {
			return current
		}
		parent := filepath.Dir(current)
		if parent == current {
			return ""
		}
		current = parent
	}
}

// ResolveAncestorWorkspaceRoots walks from workspacePath up to the worktree
// root (inclusive), collecting .nex/skills and .agents/skills at every level.
// Without a .git marker it falls back to workspacePath itself.
func ResolveAncestorWorkspaceRoots(workspacePath string) []string {
	worktreeRoot := FindWorktreeRoot(workspacePath)
	var baseDirectories []string
	if worktreeRoot == "" {
		baseDirectories = append(baseDirectories, workspacePath)
	} else {
		current := workspacePath
		for {
			baseDirectories = append(baseDirectories, current)
			if current == worktreeRoot || current == filepath.Dir(current) {
				break
			}
			current = filepath.Dir(current)
		}
	}
	var roots []string
	for _, dir := range baseDirectories {
		roots = append(roots, RootDirs(dir)...)
	}
	return roots
}

// normalizeScanRootPath ports normalizeScanRootPath: backslashes to forward
// slashes, trailing slashes stripped.
func normalizeScanRootPath(path string) string {
	normalized := strings.ReplaceAll(path, "\\", "/")
	return strings.TrimRight(normalized, "/")
}

func pathExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

// FilterNestedScanRoots drops ancestors that a deeper existing scan root
// fully covers (the same SKILL.md would otherwise appear twice under
// different path spellings). Non-existent child paths keep their parent so
// non-standard layouts still work.
func FilterNestedScanRoots(paths []string) []string {
	existing := map[string]bool{}
	for _, path := range paths {
		if pathExists(path) {
			existing[normalizeScanRootPath(path)] = true
		}
	}
	normalized := make([]string, len(paths))
	for index, path := range paths {
		normalized[index] = normalizeScanRootPath(path)
	}
	out := make([]string, 0, len(paths))
	for index := range paths {
		current := normalized[index]
		if !existing[current] {
			out = append(out, paths[index])
			continue
		}
		covered := false
		for otherIndex, other := range normalized {
			if otherIndex == index || !existing[other] {
				continue
			}
			if strings.HasPrefix(other, current+"/") {
				covered = true
				break
			}
		}
		if !covered {
			out = append(out, paths[index])
		}
	}
	return out
}

// DedupeScanRootsByRealpath filters nested roots, then dedupes by realpath
// keeping the first occurrence (dedupeScanRootsByRealpath).
func DedupeScanRootsByRealpath(paths []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, path := range FilterNestedScanRoots(paths) {
		canonical := normalizeScanRootPath(path)
		if pathExists(path) {
			if resolved, err := filepath.EvalSymlinks(path); err == nil {
				canonical = normalizeScanRootPath(resolved)
			}
		}
		if seen[canonical] {
			continue
		}
		seen[canonical] = true
		out = append(out, path)
	}
	return out
}

// DedupeRoots keeps the first occurrence of each path string (dedupeRoots).
func DedupeRoots(paths []string) []string {
	seen := map[string]bool{}
	out := make([]string, 0, len(paths))
	for _, path := range paths {
		if seen[path] {
			continue
		}
		seen[path] = true
		out = append(out, path)
	}
	return out
}
