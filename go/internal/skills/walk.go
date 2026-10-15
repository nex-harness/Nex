// walk.go: the bounded SKILL.md discovery walk, porting
// walkSkillMarkdownPaths (skillDiscoveryWalk.ts) with the shared scan policy
// (skill-scan-policy.ts) applied through ShouldWalkDirectoryEntry.
package skills

import (
	"io/fs"
	"os"
	"path/filepath"
	"sort"
)

// WalkError reports a failed readdir/stat; the caller decides whether to
// collect it as a diagnostic (the TS onError callback).
type WalkError func(path string, err error)

// WalkSkillMarkdownPaths walks from rootPath depth-first and returns every
// SKILL.md absolute path, deduped and sorted (the TS yields a Set, then
// callers sort; this returns the sorted slice directly — the sorted order is
// what every consumer uses).
//
// Policy: skip node_modules-style content directories and dot directories
// (except .system); depth capped at MaxScanDepth; symlink directories are
// deduped by realpath so Windows junctions and cycles cannot duplicate or
// hang the scan. Regular directory trees cannot cycle, so the hot path does
// not realpath them.
func WalkSkillMarkdownPaths(rootPath string, onError WalkError) []string {
	type frame struct {
		dir   string
		depth int
	}
	stack := []frame{{rootPath, 0}}
	visitedSymlinkTargets := map[string]bool{}
	discovered := map[string]bool{}
	var out []string

	for len(stack) > 0 {
		current := stack[len(stack)-1]
		stack = stack[:len(stack)-1]

		entries, err := os.ReadDir(current.dir)
		if err != nil {
			if onError != nil {
				onError(current.dir, err)
			}
			continue
		}

		hasSkillFile := false
		var childDirs, childSymlinks []string
		for _, entry := range entries {
			// A regular file OR a symlink named SKILL.md is a skill definition.
			if entry.Name() == FileName && !entry.IsDir() {
				hasSkillFile = true
				continue
			}
			if !ShouldWalkDirectoryEntry(entry.Name()) {
				continue
			}
			entryPath := filepath.Join(current.dir, entry.Name())
			if entry.IsDir() {
				childDirs = append(childDirs, entryPath)
			} else if entry.Type()&os.ModeSymlink != 0 {
				childSymlinks = append(childSymlinks, entryPath)
			}
		}

		if hasSkillFile {
			skillPath := filepath.Join(current.dir, FileName)
			if !discovered[skillPath] {
				discovered[skillPath] = true
				out = append(out, skillPath)
			}
		}

		if current.depth >= MaxScanDepth {
			continue
		}

		for _, dir := range childDirs {
			stack = append(stack, frame{dir, current.depth + 1})
		}
		for _, symlink := range childSymlinks {
			info, err := os.Stat(symlink)
			if err != nil {
				if onError != nil {
					onError(symlink, err)
				}
				continue
			}
			if !info.IsDir() {
				continue
			}
			canonical, err := filepath.EvalSymlinks(symlink)
			if err != nil {
				canonical = symlink
			}
			if visitedSymlinkTargets[canonical] {
				continue
			}
			visitedSymlinkTargets[canonical] = true
			stack = append(stack, frame{symlink, current.depth + 1})
		}
	}
	sortStrings(out)
	return out
}

// IsDirEntry reports whether the entry is a directory (or a symlink that
// resolves to a directory) — the `entry.isDirectory() ||
// (entry.isSymbolicLink() && stat(...)?.isDirectory())` shape from
// skillSyncService.ts.
func IsDirEntry(entry fs.DirEntry, path string) bool {
	if entry.IsDir() {
		return true
	}
	if entry.Type()&os.ModeSymlink == 0 {
		return false
	}
	info, err := os.Stat(path)
	return err == nil && info.IsDir()
}

func sortStrings(items []string) { sort.Strings(items) }

func containsString(items []string, value string) bool {
	for _, item := range items {
		if item == value {
			return true
		}
	}
	return false
}
