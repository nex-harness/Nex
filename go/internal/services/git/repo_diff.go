// repo_diff.go ports the diff half of gitCliRepo.ts (step 3.3): working-tree /
// staged / branch diff contents, GetDiff, GetBranchComparison.
// Split out of repo_mutations.go to stay under the 400-line archcheck limit.
package git

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

func hasConflictedEntry(entries []GitStatusEntry) bool {
	for _, entry := range entries {
		if entry.IsConflicted {
			return true
		}
	}
	return false
}

// ---------------------------------------------------------------------------
// diff
// ---------------------------------------------------------------------------

// readWorkingTreePreviewContent ports readWorkingTreePreviewContent. 文件删除和
// 原子保存窗口都会让 stat/readFile 失败；这里不能猜成合法空文件。
func readWorkingTreePreviewContent(absolutePath string) *string {
	info, err := os.Stat(absolutePath)
	if err != nil || info.IsDir() || info.Size() > DefaultGitDiffBytes {
		return nil
	}
	content, err := os.ReadFile(absolutePath)
	if err != nil {
		return nil
	}
	text := string(content)
	if !isPreviewableText(text) {
		return nil
	}
	return &text
}

// readGitBlobPreviewContent ports readGitBlobPreviewContent.
func (r *CliRepo) readGitBlobPreviewContent(ctx context.Context, repoRoot, ref, repoRelativePath string) *string {
	result, err := r.run(ctx, GitCommandExecutionOptions{
		Cwd:            repoRoot,
		Args:           []string{"show", fmt.Sprintf("%s:%s", ref, repoRelativePath)},
		TimeoutMs:      DefaultGitDiffTimeoutMs,
		MaxOutputBytes: DefaultGitDiffBytes,
	})
	if err != nil {
		return nil
	}
	if result.TimedOut || result.OutputTruncated || result.ExitCode == nil || *result.ExitCode != 0 || !isPreviewableText(result.Stdout) {
		return nil
	}
	return ptr(result.Stdout)
}

// gitDiffContents ports GitDiffContents; nil means "either side failed".
type gitDiffContents struct {
	before string
	after  string
}

// toCompleteDiffContents ports toCompleteDiffContents: 完整 diff 的任一侧读取
// 失败后若被补成空字符串，UI 会把"不可读"误判成"文件为空"，所以必须一起成功或
// 一起降级。
func toCompleteDiffContents(before, after *string) *gitDiffContents {
	if before == nil || after == nil {
		return nil
	}
	return &gitDiffContents{before: *before, after: *after}
}

func (r *CliRepo) readBranchDiffContents(ctx context.Context, repoRoot, repoRelativePath, trackingBranchName string) *gitDiffContents {
	mergeBaseResult, err := r.run(ctx, GitCommandExecutionOptions{
		Cwd:            repoRoot,
		Args:           []string{"merge-base", trackingBranchName, "HEAD"},
		TimeoutMs:      DefaultGitCommandTimeoutMs,
		MaxOutputBytes: DefaultGitOutputBytes,
	})
	mergeBase := ""
	if err == nil && mergeBaseResult.ExitCode != nil && *mergeBaseResult.ExitCode == 0 {
		mergeBase = strings.TrimSpace(mergeBaseResult.Stdout)
	}
	beforeRef := trackingBranchName
	if mergeBase != "" {
		beforeRef = mergeBase
	}
	beforeContent := r.readGitBlobPreviewContent(ctx, repoRoot, beforeRef, repoRelativePath)
	afterContent := r.readGitBlobPreviewContent(ctx, repoRoot, "HEAD", repoRelativePath)
	return toCompleteDiffContents(beforeContent, afterContent)
}

func (r *CliRepo) readStagedDiffContents(ctx context.Context, repoRoot, repoRelativePath string) *gitDiffContents {
	beforeContent := r.readGitBlobPreviewContent(ctx, repoRoot, "HEAD", repoRelativePath)
	afterContent := r.readGitBlobPreviewContent(ctx, repoRoot, "", repoRelativePath)
	return toCompleteDiffContents(beforeContent, afterContent)
}

func (r *CliRepo) readUnstagedDiffContents(ctx context.Context, absolutePath, repoRoot, repoRelativePath string) *gitDiffContents {
	beforeContent := r.readGitBlobPreviewContent(ctx, repoRoot, "", repoRelativePath)
	afterContent := readWorkingTreePreviewContent(absolutePath)
	return toCompleteDiffContents(beforeContent, afterContent)
}

// withDiffContents ports withDiffContents: patch 已成功生成时，全文预览失败只应
// 关闭 MultiFileDiff，不能把正确 patch 一并丢弃。
func withDiffContents(diff GitDiffResult, contents *gitDiffContents) GitDiffResult {
	if diff.Availability != DiffAvailabilityPatch {
		return diff
	}
	if contents == nil {
		return diff
	}
	diff.BeforeContent = ptr(contents.before)
	diff.AfterContent = ptr(contents.after)
	return diff
}

// GetDiffParams is the Go shape of GitDiffQuery.
type GetDiffParams struct {
	WorkspacePath string
	Path          string
	Staged        *bool
	SourceID      *GitChangeSourceId
}

// GetDiff ports getDiff.
func (r *CliRepo) GetDiff(ctx context.Context, params GetDiffParams) (GitDiffResult, error) {
	resolution, err := r.ResolveRepository(ctx, params.WorkspacePath)
	if err != nil {
		return GitDiffResult{}, err
	}
	absolutePath := params.Path
	if !filepath.IsAbs(params.Path) {
		absolutePath = filepath.Join(params.WorkspacePath, strings.ReplaceAll(params.Path, "/", string(filepath.Separator)))
	}
	if !resolution.IsGitAvailable {
		return unavailableDiff(absolutePath, "Git binary is not available in the current environment."), nil
	}
	if !resolution.IsRepository {
		return unavailableDiff(absolutePath, "Workspace is not inside a Git repository."), nil
	}
	repoRelativePath, err := normalizeInputPath(resolution, params.Path)
	if err != nil {
		return GitDiffResult{}, err
	}

	if params.SourceID != nil && *params.SourceID == SourceBranch {
		status, err := r.GetStatus(ctx, params.WorkspacePath)
		if err != nil {
			return GitDiffResult{}, err
		}
		if status.Summary.TrackingBranchName == nil {
			return unavailableDiff(absolutePath, "Current branch does not have an upstream branch."), nil
		}
		trackingBranchName := *status.Summary.TrackingBranchName
		branchDiffResult, err := r.run(ctx, GitCommandExecutionOptions{
			Cwd: resolution.RepoRoot,
			Args: []string{
				"diff", "--no-ext-diff", "--no-color", "--binary", "--find-renames",
				trackingBranchName + "...HEAD", "--", repoRelativePath,
			},
			TimeoutMs:      DefaultGitDiffTimeoutMs,
			MaxOutputBytes: DefaultGitDiffBytes,
		})
		if err != nil {
			return GitDiffResult{}, err
		}
		parsed := toDiffResult(absolutePath, branchDiffResult, toDiffResultOptions{
			EmptySummary: "No branch comparison diff is available for this file.",
		})
		return withDiffContents(parsed, r.readBranchDiffContents(ctx, resolution.RepoRoot, repoRelativePath, trackingBranchName)), nil
	}

	staged := params.Staged != nil && *params.Staged || params.SourceID != nil && *params.SourceID == SourceStaged
	var diffResult GitCommandExecutionResult
	if staged {
		diffResult, err = r.run(ctx, GitCommandExecutionOptions{
			Cwd:            resolution.RepoRoot,
			Args:           []string{"diff", "--cached", "--no-ext-diff", "--no-color", "--binary", "--", repoRelativePath},
			TimeoutMs:      DefaultGitDiffTimeoutMs,
			MaxOutputBytes: DefaultGitDiffBytes,
		})
	} else {
		diffResult, err = r.run(ctx, GitCommandExecutionOptions{
			Cwd:            resolution.RepoRoot,
			Args:           []string{"diff", "--no-ext-diff", "--no-color", "--binary", "--", repoRelativePath},
			TimeoutMs:      DefaultGitDiffTimeoutMs,
			MaxOutputBytes: DefaultGitDiffBytes,
		})
	}
	if err != nil {
		return GitDiffResult{}, err
	}
	parsedDiff := toDiffResult(absolutePath, diffResult, toDiffResultOptions{
		EmptySummary: "No Git diff is available for this file.",
	})
	if parsedDiff.Availability != DiffAvailabilityUnavailable || staged {
		var contents *gitDiffContents
		if staged {
			contents = r.readStagedDiffContents(ctx, resolution.RepoRoot, repoRelativePath)
		} else {
			contents = r.readUnstagedDiffContents(ctx, absolutePath, resolution.RepoRoot, repoRelativePath)
		}
		return withDiffContents(parsedDiff, contents), nil
	}

	// 未跟踪文件不会出现在 `git diff` 里：先稳定单文件 patch，二进制再退回 --no-index。
	if !fileExists(absolutePath) {
		return parsedDiff, nil
	}
	if untrackedDiff, ok := buildUntrackedTextDiffResult(absolutePath, repoRelativePath, DefaultGitDiffBytes); ok {
		return untrackedDiff, nil
	}
	noIndexResult, err := r.run(ctx, GitCommandExecutionOptions{
		Cwd: resolution.RepoRoot,
		Args: []string{
			"diff", "--no-index", "--no-ext-diff", "--no-color", "--binary",
			GetGitNullDevicePath(IsWindowsOS()), absolutePath,
		},
		TimeoutMs:      DefaultGitDiffTimeoutMs,
		MaxOutputBytes: DefaultGitDiffBytes,
	})
	if err != nil {
		return GitDiffResult{}, err
	}
	return toDiffResult(absolutePath, noIndexResult, toDiffResultOptions{
		AllowedExitCodes: []int{0, 1},
		EmptySummary:     "No previewable diff is available for this file.",
	}), nil
}

// GetBranchComparison ports getBranchComparison.
func (r *CliRepo) GetBranchComparison(ctx context.Context, workspacePath string) (GitBranchComparisonSnapshot, error) {
	status, err := r.GetStatus(ctx, workspacePath)
	if err != nil {
		return GitBranchComparisonSnapshot{}, err
	}
	headRef := ptr("HEAD")
	if status.Summary.BranchName != nil {
		headRef = status.Summary.BranchName
	}
	if !status.Resolution.IsGitAvailable || !status.Resolution.IsRepository || status.Summary.TrackingBranchName == nil {
		return GitBranchComparisonSnapshot{
			Resolution:      status.Resolution,
			BaseRef:         status.Summary.TrackingBranchName,
			HeadRef:         headRef,
			ComparisonLabel: nil,
			Changes:         []GitBranchComparisonChange{},
		}, nil
	}
	trackingBranchName := *status.Summary.TrackingBranchName
	result, err := r.run(ctx, GitCommandExecutionOptions{
		Cwd: status.Resolution.RepoRoot,
		Args: []string{
			"diff", "--numstat", "-z", "--find-renames",
			trackingBranchName + "...HEAD", "--",
		},
		TimeoutMs:      DefaultGitCommandTimeoutMs,
		MaxOutputBytes: DefaultGitOutputBytes,
	})
	if err != nil {
		return GitBranchComparisonSnapshot{}, err
	}
	if err := ensureGitCommandSucceeded("git diff --numstat upstream...HEAD", result); err != nil {
		return GitBranchComparisonSnapshot{}, err
	}
	numstat := parseNumstatMap(result.Stdout)
	changes := make([]GitBranchComparisonChange, 0, len(numstat))
	for _, path := range sortedNumstatKeys(numstat) {
		stat := numstat[path]
		changes = append(changes, GitBranchComparisonChange{
			Path:         path,
			OriginalPath: stat.OriginalPath,
			Kind:         inferKindFromNumstat(stat),
			Added:        stat.Added,
			Removed:      stat.Removed,
		})
	}
	var comparisonLabel *string
	if status.Summary.BranchName != nil {
		comparisonLabel = ptr(fmt.Sprintf("%s -> %s", *status.Summary.BranchName, trackingBranchName))
	} else {
		comparisonLabel = ptr(fmt.Sprintf("HEAD -> %s", trackingBranchName))
	}
	return GitBranchComparisonSnapshot{
		Resolution:      status.Resolution,
		BaseRef:         status.Summary.TrackingBranchName,
		HeadRef:         headRef,
		ComparisonLabel: comparisonLabel,
		Changes:         changes,
	}, nil
}

func sortedNumstatKeys(stats map[string]GitLineStat) []string {
	keys := make([]string, 0, len(stats))
	for key := range stats {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

// ---------------------------------------------------------------------------
// stage / unstage / discard
// ---------------------------------------------------------------------------
