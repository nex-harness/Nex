// helpers_checkpoint.go ports packages/services/src/git/repo/gitCheckpointHelpers.ts
// (step 3.3): the name-status / ls-tree parsers, the diff merge, the affected
// path folding and the checkpoint env.
package git

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/tylinux/nex/go/internal/services/paths"
)

// gitCheckpointNameStatusEntry ports GitCheckpointNameStatusEntry.
type gitCheckpointNameStatusEntry struct {
	Kind         GitChangeKind
	Path         string
	OriginalPath *string
}

// gitTreeEntry ports GitTreeEntry.
type gitTreeEntry struct {
	Mode     string
	Type     string
	ObjectID string
	Path     string
}

// GetWorkspacePathspec ports getWorkspacePathspec.
func GetWorkspacePathspec(workspaceInRepoPath string) string {
	return workspaceInRepoPath
}

// GetCheckpointRefName ports getCheckpointRefName: refs/nex/checkpoints/<hash>/<id>.
func GetCheckpointRefName(workspacePath, checkpointID string) string {
	return "refs/nex/checkpoints/" + paths.WorkspaceHash(workspacePath, "") + "/" + checkpointID
}

// mapNameStatusKind ports mapNameStatusKind.
func mapNameStatusKind(status string) GitChangeKind {
	normalized := "M"
	if status != "" {
		normalized = string(status[0])
	}
	switch normalized {
	case "A":
		return ChangeKindAdded
	case "D":
		return ChangeKindDeleted
	case "R", "C":
		return ChangeKindRenamed
	default:
		return ChangeKindModified
	}
}

// parseNameStatus ports parseNameStatus (`git diff --name-status -z`).
func parseNameStatus(stdout string) []gitCheckpointNameStatusEntry {
	records := strings.Split(stdout, "\x00")
	var entries []gitCheckpointNameStatusEntry
	for index := 0; index < len(records); index++ {
		status := records[index]
		if status == "" && index == len(records)-1 {
			break
		}
		kind := mapNameStatusKind(status)
		if kind == ChangeKindRenamed {
			var originalPath, path string
			if index+1 < len(records) {
				originalPath = records[index+1]
			}
			if index+2 < len(records) {
				path = records[index+2]
			}
			index += 2
			if originalPath == "" || path == "" {
				continue
			}
			entries = append(entries, gitCheckpointNameStatusEntry{
				Kind:         kind,
				OriginalPath: ptr(NormalizeGitPath(originalPath)),
				Path:         NormalizeGitPath(path),
			})
			continue
		}
		var path string
		if index+1 < len(records) {
			path = records[index+1]
		}
		index++
		if path == "" {
			continue
		}
		entries = append(entries, gitCheckpointNameStatusEntry{
			Kind: kind,
			Path: NormalizeGitPath(path),
		})
	}
	return entries
}

// parseNumstatCheckpoint ports the checkpoint variant of parseNumstat: the
// renamed record keys BOTH the new and the original path.
func parseNumstatCheckpoint(stdout string) map[string]gitCheckpointLineStat {
	type stat struct {
		added   int
		removed int
	}
	records := strings.Split(stdout, "\x00")
	stats := map[string]stat{}
	for index := 0; index < len(records); index++ {
		record := records[index]
		if record == "" {
			continue
		}
		fields := strings.Split(record, "\t")
		if len(fields) < 3 {
			continue
		}
		added := 0
		if fields[0] != "-" {
			if n, err := strconv.Atoi(fields[0]); err == nil {
				added = n
			}
		}
		removed := 0
		if fields[1] != "-" {
			if n, err := strconv.Atoi(fields[1]); err == nil {
				removed = n
			}
		}
		pathField := strings.Join(fields[2:], "\t")
		if pathField != "" {
			stats[NormalizeGitPath(pathField)] = stat{added: added, removed: removed}
			continue
		}
		var originalPath, renamedPath string
		if index+1 < len(records) {
			originalPath = records[index+1]
		}
		if index+2 < len(records) {
			renamedPath = records[index+2]
		}
		index += 2
		if renamedPath == "" {
			continue
		}
		stats[NormalizeGitPath(renamedPath)] = stat{added: added, removed: removed}
		if originalPath != "" {
			stats[NormalizeGitPath(originalPath)] = stat{added: added, removed: removed}
		}
	}
	// Convert to the exported map once (stable key set).
	out := make(map[string]gitCheckpointLineStat, len(stats))
	for key, value := range stats {
		out[key] = gitCheckpointLineStat{Added: value.added, Removed: value.removed}
	}
	return out
}

type gitCheckpointLineStat struct {
	Added   int
	Removed int
}

// parseLsTree ports parseLsTree.
func parseLsTree(stdout string) map[string]gitTreeEntry {
	records := strings.Split(stdout, "\x00")
	entries := map[string]gitTreeEntry{}
	for _, record := range records {
		if record == "" {
			continue
		}
		tabIndex := strings.IndexByte(record, '\t')
		if tabIndex < 0 {
			continue
		}
		header := strings.Fields(record[:tabIndex])
		if len(header) < 3 {
			continue
		}
		path := NormalizeGitPath(record[tabIndex+1:])
		entries[path] = gitTreeEntry{
			Mode:     header[0],
			Type:     header[1],
			ObjectID: header[2],
			Path:     path,
		}
	}
	return entries
}

// mergeCheckpointDiff ports mergeCheckpointDiff: name-status 决定"改了哪些文件、
// 是什么类型的改动"，numstat 提供 added/removed 统计，拼成结构化 diff。
func mergeCheckpointDiff(repoRoot, workspaceInRepoPath, fromCheckpointID, toCheckpointID string, nameStatusEntries []gitCheckpointNameStatusEntry, numstat map[string]gitCheckpointLineStat) GitCheckpointDiff {
	files := make([]GitCheckpointFileDiff, 0, len(nameStatusEntries))
	for _, entry := range nameStatusEntries {
		stat, ok := numstat[entry.Path]
		if !ok && entry.OriginalPath != nil {
			stat, ok = numstat[*entry.OriginalPath]
		}
		if !ok {
			stat = gitCheckpointLineStat{}
		}
		file := GitCheckpointFileDiff{
			Path:                  toAbsolutePath(repoRoot, entry.Path),
			RepoRelativePath:      entry.Path,
			WorkspaceRelativePath: ToWorkspaceRelativeGitPath(entry.Path, workspaceInRepoPath),
			Kind:                  entry.Kind,
			Added:                 stat.Added,
			Removed:               stat.Removed,
		}
		if entry.OriginalPath != nil {
			original := toAbsolutePath(repoRoot, *entry.OriginalPath)
			file.OriginalPath = &original
		}
		files = append(files, file)
	}
	return GitCheckpointDiff{
		FromCheckpointID: fromCheckpointID,
		ToCheckpointID:   toCheckpointID,
		Files:            files,
	}
}

// buildAffectedRepoPaths ports buildAffectedRepoPaths: diff 里既可能有当前路径，
// 也可能有 rename 前的旧路径，统一折叠成受影响 path 集。
func buildAffectedRepoPaths(files []GitCheckpointFileDiff) []string {
	values := map[string]bool{}
	for _, file := range files {
		values[file.RepoRelativePath] = true
		if file.OriginalPath != nil {
			// OriginalPath here is absolute (mergeCheckpointDiff absolutized it).
			values[normalizeAffectedRepoPath("", *file.OriginalPath)] = true
		}
	}
	out := make([]string, 0, len(values))
	for value := range values {
		out = append(out, value)
	}
	return out
}

// buildCheckpointEnv ports buildCheckpointEnv.
func buildCheckpointEnv(tempIndexPath string) map[string]string {
	return map[string]string{
		"GIT_INDEX_FILE":      tempIndexPath,
		"GIT_AUTHOR_NAME":     "Nex Checkpoint",
		"GIT_AUTHOR_EMAIL":    "checkpoint@nex.local",
		"GIT_COMMITTER_NAME":  "Nex Checkpoint",
		"GIT_COMMITTER_EMAIL": "checkpoint@nex.local",
	}
}

// removeFileIfExists ports removeFileIfExists (rm -rf semantics).
func removeFileIfExists(path string) error {
	return os.RemoveAll(path)
}

// normalizeAffectedRepoPath ports normalizeAffectedRepoPath.
func normalizeAffectedRepoPath(repoRoot, path string) string {
	if filepath.IsAbs(path) {
		rel := relPath(repoRoot, path)
		return NormalizeGitPath(rel)
	}
	return NormalizeGitPath(path)
}

// pathExistsCheckpoint ports pathExists (lstat-based, includes dangling symlinks).
func pathExistsCheckpoint(path string) bool {
	_, err := os.Lstat(path)
	return err == nil
}

// hashObjectNoFilters runs `git hash-object --no-filters <path>`.
func (r *CheckpointRepo) hashObjectNoFilters(ctx context.Context, repoRoot, absolutePath string) (string, error) {
	result, err := r.run(ctx, GitCommandExecutionOptions{
		Cwd:       repoRoot,
		Args:      []string{"hash-object", "--no-filters", absolutePath},
		TimeoutMs: DefaultGitCommandTimeoutMs,
	})
	if err != nil {
		return "", err
	}
	if err := ensureGitCommandSucceeded("git hash-object checkpoint verify", result); err != nil {
		return "", err
	}
	return strings.TrimSpace(result.Stdout), nil
}

var errCheckpointVerification = errors.New("Checkpoint restore verification failed.")
