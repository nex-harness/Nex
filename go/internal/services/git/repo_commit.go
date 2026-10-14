// repo_commit.go ports gitCliRepo.ts, part 3 (step 3.3): commit (plain and the
// staged-only scoped path with a temporary GIT_INDEX_FILE), push (with the
// remote resolution chain) and identity.
package git

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
)

// GitIndexEntry ports GitIndexEntry.
type GitIndexEntry struct {
	Mode       string
	ObjectHash string
	Stage      string
	Path       string
}

// parseGitIndexEntries ports parseGitIndexEntries.
func parseGitIndexEntries(stdout string) ([]GitIndexEntry, error) {
	var entries []GitIndexEntry
	for _, record := range strings.Split(stdout, "\x00") {
		if record == "" {
			continue
		}
		tabIndex := strings.IndexByte(record, '\t')
		if tabIndex < 0 {
			return nil, errors.New("Failed to parse staged Git index entry.")
		}
		header := strings.Fields(record[:tabIndex])
		if len(header) < 3 {
			return nil, errors.New("Failed to parse staged Git index entry.")
		}
		mode, objectHash, stage := header[0], header[1], header[2]
		path := NormalizeGitPath(record[tabIndex+1:])
		if mode == "" || objectHash == "" || stage == "" || path == "" {
			return nil, errors.New("Failed to parse staged Git index entry.")
		}
		entries = append(entries, GitIndexEntry{Mode: mode, ObjectHash: objectHash, Stage: stage, Path: path})
	}
	return entries, nil
}

// CommitOptions ports the commit options bag.
type CommitOptions struct {
	StagedOnly bool
}

// Commit ports commit. The stagedOnly+paths path builds a temporary index from
// HEAD, applies only the staged entries for the selected paths, commits with
// that index, and finally resets ONLY the committed paths in the real index —
// 提交当前会话文件时不能把真实 index 整体替换成临时 index，其它已暂存文件要
// 继续等待用户手动提交。
func (r *CliRepo) Commit(ctx context.Context, workspacePath, message string, paths []string, options CommitOptions) (string, error) {
	resolution, err := ensureRepositoryAvailableSync(ctx, r, workspacePath, "commit changes")
	if err != nil {
		return "", err
	}
	trimmedMessage := strings.TrimSpace(message)
	if trimmedMessage == "" {
		return "", errors.New("Commit message cannot be empty")
	}
	var repoPaths []string
	if len(paths) > 0 {
		repoPaths, err = r.normalizeRepoPaths(ctx, resolution, paths)
		if err != nil {
			return "", err
		}
	}

	if options.StagedOnly && len(repoPaths) > 0 {
		return r.commitStagedOnly(ctx, workspacePath, resolution, trimmedMessage, repoPaths)
	}

	var args []string
	if len(repoPaths) > 0 {
		args = append([]string{"commit", "-m", trimmedMessage, "--"}, repoPaths...)
	} else {
		args = []string{"commit", "-m", trimmedMessage}
	}
	commitResult, err := r.run(ctx, GitCommandExecutionOptions{
		Cwd:            resolution.RepoRoot,
		Args:           args,
		TimeoutMs:      DefaultGitCommandTimeoutMs,
		MaxOutputBytes: DefaultGitOutputBytes,
	})
	if err != nil {
		return "", err
	}
	if err := ensureGitCommandSucceeded("git commit", commitResult); err != nil {
		return "", err
	}
	r.invalidate(workspacePath)
	return r.revParseHead(ctx, resolution.RepoRoot, "git rev-parse HEAD")
}

func (r *CliRepo) commitStagedOnly(ctx context.Context, workspacePath string, resolution GitResolvedRepository, trimmedMessage string, repoPaths []string) (string, error) {
	scopedStatusResult, err := r.run(ctx, GitCommandExecutionOptions{
		Cwd:            resolution.RepoRoot,
		Args:           append([]string{"status", "--porcelain=v2", "-z", "--"}, repoPaths...),
		TimeoutMs:      DefaultGitCommandTimeoutMs,
		MaxOutputBytes: DefaultGitOutputBytes,
	})
	if err != nil {
		return "", err
	}
	if err := ensureGitCommandSucceeded("git status selected paths", scopedStatusResult); err != nil {
		return "", err
	}
	// cleanupRepoPaths: the requested paths plus the pre-rename paths of any
	// matched rename entries (a staged rename must be removed from the old path).
	cleanupSet := map[string]bool{}
	for _, path := range repoPaths {
		cleanupSet[path] = true
	}
	for _, entry := range parseStatusPorcelain(scopedStatusResult.Stdout).Entries {
		matched := false
		for _, path := range repoPaths {
			if path == entry.Path {
				matched = true
				break
			}
		}
		if matched && entry.OriginalPath != nil {
			cleanupSet[*entry.OriginalPath] = true
		}
	}
	cleanupRepoPaths := make([]string, 0, len(cleanupSet))
	for _, path := range repoPaths {
		if cleanupSet[path] {
			cleanupRepoPaths = append(cleanupRepoPaths, path)
			delete(cleanupSet, path)
		}
	}
	for path := range cleanupSet {
		cleanupRepoPaths = append(cleanupRepoPaths, path)
	}

	stagedEntriesResult, err := r.run(ctx, GitCommandExecutionOptions{
		Cwd:            resolution.RepoRoot,
		Args:           append([]string{"ls-files", "--stage", "-z", "--"}, repoPaths...),
		TimeoutMs:      DefaultGitCommandTimeoutMs,
		MaxOutputBytes: DefaultGitOutputBytes,
	})
	if err != nil {
		return "", err
	}
	if err := ensureGitCommandSucceeded("git ls-files selected staged entries", stagedEntriesResult); err != nil {
		return "", err
	}
	stagedEntries, err := parseGitIndexEntries(stagedEntriesResult.Stdout)
	if err != nil {
		return "", err
	}
	for _, entry := range stagedEntries {
		if entry.Stage != "0" {
			return "", errors.New("Cannot commit selected staged paths while index conflicts exist.")
		}
	}

	headResult, err := r.run(ctx, GitCommandExecutionOptions{
		Cwd:       resolution.RepoRoot,
		Args:      []string{"rev-parse", "--verify", "HEAD"},
		TimeoutMs: DefaultGitCommandTimeoutMs,
	})
	if err != nil {
		return "", err
	}
	parentHash := ""
	if headResult.ExitCode != nil && *headResult.ExitCode == 0 {
		parentHash = strings.TrimSpace(headResult.Stdout)
	}

	tempIndexDir, err := os.MkdirTemp("", "nex-git-index-")
	if err != nil {
		return "", err
	}
	defer func() { _ = os.RemoveAll(tempIndexDir) }()
	tempIndexPath := filepath.Join(tempIndexDir, "index")
	tempIndexEnv := map[string]string{"GIT_INDEX_FILE": tempIndexPath}

	var readTreeArgs []string
	if parentHash != "" {
		readTreeArgs = []string{"read-tree", parentHash}
	} else {
		readTreeArgs = []string{"read-tree", "--empty"}
	}
	readTreeResult, err := r.run(ctx, GitCommandExecutionOptions{
		Cwd:       resolution.RepoRoot,
		Args:      readTreeArgs,
		Env:       tempIndexEnv,
		TimeoutMs: DefaultGitCommandTimeoutMs,
	})
	if err != nil {
		return "", err
	}
	if err := ensureGitCommandSucceeded("git read-tree selected commit base", readTreeResult); err != nil {
		return "", err
	}

	if len(cleanupRepoPaths) > 0 {
		removeResult, err := r.run(ctx, GitCommandExecutionOptions{
			Cwd:       resolution.RepoRoot,
			Args:      append([]string{"update-index", "--force-remove", "--"}, cleanupRepoPaths...),
			Env:       tempIndexEnv,
			TimeoutMs: DefaultGitCommandTimeoutMs,
		})
		if err != nil {
			return "", err
		}
		if err := ensureGitCommandSucceeded("git update-index remove selected paths", removeResult); err != nil {
			return "", err
		}
	}

	for _, entry := range stagedEntries {
		addResult, err := r.run(ctx, GitCommandExecutionOptions{
			Cwd:       resolution.RepoRoot,
			Args:      []string{"update-index", "--add", "--cacheinfo", entry.Mode, entry.ObjectHash, entry.Path},
			Env:       tempIndexEnv,
			TimeoutMs: DefaultGitCommandTimeoutMs,
		})
		if err != nil {
			return "", err
		}
		if err := ensureGitCommandSucceeded("git update-index add selected paths", addResult); err != nil {
			return "", err
		}
	}

	scopedCommitResult, err := r.run(ctx, GitCommandExecutionOptions{
		Cwd:            resolution.RepoRoot,
		Args:           []string{"commit", "-m", trimmedMessage},
		Env:            tempIndexEnv,
		TimeoutMs:      DefaultGitCommandTimeoutMs,
		MaxOutputBytes: DefaultGitOutputBytes,
	})
	if err != nil {
		return "", err
	}
	if err := ensureGitCommandSucceeded("git commit selected staged paths", scopedCommitResult); err != nil {
		return "", err
	}
	commitHash, err := r.revParseHead(ctx, resolution.RepoRoot, "git rev-parse selected commit HEAD")
	if err != nil {
		return "", err
	}

	// 只把已提交的路径同步到新 HEAD，保留其它已暂存文件继续等待用户手动提交。
	resetSelectedResult, err := r.run(ctx, GitCommandExecutionOptions{
		Cwd:       resolution.RepoRoot,
		Args:      append([]string{"reset", "--quiet", "HEAD", "--"}, cleanupRepoPaths...),
		TimeoutMs: DefaultGitCommandTimeoutMs,
	})
	if err != nil {
		return "", err
	}
	if err := ensureGitCommandSucceeded("git reset selected committed paths", resetSelectedResult); err != nil {
		return "", err
	}
	r.invalidate(workspacePath)
	return commitHash, nil
}

func (r *CliRepo) revParseHead(ctx context.Context, repoRoot, label string) (string, error) {
	hashResult, err := r.run(ctx, GitCommandExecutionOptions{
		Cwd:       repoRoot,
		Args:      []string{"rev-parse", "HEAD"},
		TimeoutMs: DefaultGitCommandTimeoutMs,
	})
	if err != nil {
		return "", err
	}
	if err := ensureGitCommandSucceeded(label, hashResult); err != nil {
		return "", err
	}
	return strings.TrimSpace(hashResult.Stdout), nil
}

// ---------------------------------------------------------------------------
// push
// ---------------------------------------------------------------------------

// readOptionalGitConfig ports readOptionalGitConfig.
