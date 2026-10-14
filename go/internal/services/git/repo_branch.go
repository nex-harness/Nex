// repo_branch.go ports the branch-mutation half of gitCliRepo.ts (step 3.3):
// switch, create-and-switch, branch-name validation and the in-progress guard.
// Split out of repo_mutations.go to stay under the 400-line archcheck limit.
package git

import (
	"context"
	"path/filepath"
	"strings"
)

func (r *CliRepo) hasOperationInProgress(ctx context.Context, resolution GitResolvedRepository) (bool, error) {
	args := []string{"rev-parse"}
	for _, marker := range gitOperationMarkers {
		args = append(args, "--git-path", marker)
	}
	result, err := r.run(ctx, GitCommandExecutionOptions{
		Cwd:       resolution.RepoRoot,
		Args:      args,
		TimeoutMs: DefaultGitCommandTimeoutMs,
	})
	if err != nil {
		return false, err
	}
	if err := ensureGitCommandSucceeded("git rev-parse --git-path", result); err != nil {
		return false, err
	}
	for _, line := range strings.Split(strings.ReplaceAll(result.Stdout, "\r\n", "\n"), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		candidate := line
		if !filepath.IsAbs(candidate) {
			candidate = filepath.Join(resolution.RepoRoot, candidate)
		}
		if fileExists(candidate) {
			return true, nil
		}
	}
	return false, nil
}

// validateBranchName ports validateBranchName.
func (r *CliRepo) validateBranchName(ctx context.Context, resolution GitResolvedRepository, branchName string) (*GitBranchMutationIssue, error) {
	result, err := r.run(ctx, GitCommandExecutionOptions{
		Cwd:       resolution.RepoRoot,
		Args:      []string{"check-ref-format", "--branch", branchName},
		TimeoutMs: DefaultGitCommandTimeoutMs,
	})
	if err != nil {
		return nil, err
	}
	if result.TimedOut || result.OutputTruncated {
		if err := ensureGitCommandSucceeded("git check-ref-format --branch", result); err != nil {
			return nil, err
		}
	}
	if result.ExitCode != nil && *result.ExitCode == 0 {
		return nil, nil
	}
	issue := toInvalidBranchNameIssue(result.Stderr)
	return &issue, nil
}

func toBranchMutationFailure(action GitBranchMutationAction, branchName *string, created bool, summary GitRepositorySummary, issues []GitBranchMutationIssue) GitBranchMutationResult {
	return GitBranchMutationResult{
		OK:         false,
		Action:     action,
		BranchName: branchName,
		DidChange:  false,
		Created:    created,
		Summary:    summary,
		Issues:     issues,
	}
}

func toBranchMutationSuccess(action GitBranchMutationAction, branchName string, didChange, created bool, summary GitRepositorySummary) GitBranchMutationResult {
	return GitBranchMutationResult{
		OK:         true,
		Action:     action,
		BranchName: ptr(branchName),
		DidChange:  didChange,
		Created:    created,
		Summary:    summary,
		Issues:     []GitBranchMutationIssue{},
	}
}

// SwitchBranch ports switchBranch.
func (r *CliRepo) SwitchBranch(ctx context.Context, workspacePath, targetBranchName string) (GitBranchMutationResult, error) {
	status, err := r.GetStatus(ctx, workspacePath)
	if err != nil {
		return GitBranchMutationResult{}, err
	}
	resolution, err := ensureRepositoryAvailable(status.Resolution, "switch branches")
	if err != nil {
		return GitBranchMutationResult{}, err
	}
	normalizedBranchName := strings.TrimSpace(targetBranchName)
	if normalizedBranchName == "" {
		return toBranchMutationFailure(MutationActionSwitch, nil, false, status.Summary, []GitBranchMutationIssue{toInvalidBranchNameIssue("")}), nil
	}
	if status.Summary.HeadRefType == HeadRefTypeBranch && status.Summary.BranchName != nil && *status.Summary.BranchName == normalizedBranchName {
		return toBranchMutationSuccess(MutationActionSwitch, normalizedBranchName, false, false, status.Summary), nil
	}
	// 优先返回当前仓库已知的阻塞状态，避免 UI 只能看到一条模糊的 Git 原生错误。
	if hasConflictedEntry(status.Entries) {
		return toBranchMutationFailure(MutationActionSwitch, ptr(normalizedBranchName), false, status.Summary, []GitBranchMutationIssue{{
			Code:    "conflicts-present",
			Message: "Repository still has unresolved conflicts.",
		}}), nil
	}
	if inProgress, err := r.hasOperationInProgress(ctx, resolution); err != nil {
		return GitBranchMutationResult{}, err
	} else if inProgress {
		return toBranchMutationFailure(MutationActionSwitch, ptr(normalizedBranchName), false, status.Summary, []GitBranchMutationIssue{{
			Code:    "operation-in-progress",
			Message: "Another Git operation is still in progress.",
		}}), nil
	}
	invalidIssue, err := r.validateBranchName(ctx, resolution, normalizedBranchName)
	if err != nil {
		return GitBranchMutationResult{}, err
	}
	if invalidIssue != nil {
		return toBranchMutationFailure(MutationActionSwitch, ptr(normalizedBranchName), false, status.Summary, []GitBranchMutationIssue{*invalidIssue}), nil
	}
	result, err := r.run(ctx, GitCommandExecutionOptions{
		Cwd:            resolution.RepoRoot,
		Args:           []string{"switch", "--no-guess", normalizedBranchName},
		TimeoutMs:      DefaultGitCommandTimeoutMs,
		MaxOutputBytes: DefaultGitOutputBytes,
	})
	if err != nil {
		return GitBranchMutationResult{}, err
	}
	if result.ExitCode == nil || *result.ExitCode != 0 {
		return toBranchMutationFailure(MutationActionSwitch, ptr(normalizedBranchName), false, status.Summary, parseGitBranchMutationIssues(result)), nil
	}
	r.invalidate(workspacePath)
	nextStatus, err := r.GetStatus(ctx, workspacePath)
	if err != nil {
		return GitBranchMutationResult{}, err
	}
	return toBranchMutationSuccess(MutationActionSwitch, normalizedBranchName, true, false, nextStatus.Summary), nil
}

// CreateBranchAndSwitch ports createBranchAndSwitch.
func (r *CliRepo) CreateBranchAndSwitch(ctx context.Context, workspacePath string, branchName string, startPoint *string) (GitBranchMutationResult, error) {
	status, err := r.GetStatus(ctx, workspacePath)
	if err != nil {
		return GitBranchMutationResult{}, err
	}
	resolution, err := ensureRepositoryAvailable(status.Resolution, "create and switch branches")
	if err != nil {
		return GitBranchMutationResult{}, err
	}
	normalizedBranchName := strings.TrimSpace(branchName)
	if normalizedBranchName == "" {
		return toBranchMutationFailure(MutationActionCreateAndSwitch, nil, false, status.Summary, []GitBranchMutationIssue{toInvalidBranchNameIssue("")}), nil
	}
	if hasConflictedEntry(status.Entries) {
		return toBranchMutationFailure(MutationActionCreateAndSwitch, ptr(normalizedBranchName), false, status.Summary, []GitBranchMutationIssue{{
			Code:    "conflicts-present",
			Message: "Repository still has unresolved conflicts.",
		}}), nil
	}
	if inProgress, err := r.hasOperationInProgress(ctx, resolution); err != nil {
		return GitBranchMutationResult{}, err
	} else if inProgress {
		return toBranchMutationFailure(MutationActionCreateAndSwitch, ptr(normalizedBranchName), false, status.Summary, []GitBranchMutationIssue{{
			Code:    "operation-in-progress",
			Message: "Another Git operation is still in progress.",
		}}), nil
	}
	invalidIssue, err := r.validateBranchName(ctx, resolution, normalizedBranchName)
	if err != nil {
		return GitBranchMutationResult{}, err
	}
	if invalidIssue != nil {
		return toBranchMutationFailure(MutationActionCreateAndSwitch, ptr(normalizedBranchName), false, status.Summary, []GitBranchMutationIssue{*invalidIssue}), nil
	}
	var args []string
	normalizedStartPoint := ""
	if startPoint != nil {
		normalizedStartPoint = strings.TrimSpace(*startPoint)
	}
	if normalizedStartPoint != "" {
		// startPoint 可能来自外部输入；以 `-` 开头时会被当成 switch 的选项解析。
		// 显式插入 `--` 终止选项解析，确保后面的值始终按位置参数处理。
		args = []string{"switch", "--no-guess", "-c", normalizedBranchName, "--", normalizedStartPoint}
	} else {
		args = []string{"switch", "--no-guess", "-c", normalizedBranchName}
	}
	result, err := r.run(ctx, GitCommandExecutionOptions{
		Cwd:            resolution.RepoRoot,
		Args:           args,
		TimeoutMs:      DefaultGitCommandTimeoutMs,
		MaxOutputBytes: DefaultGitOutputBytes,
	})
	if err != nil {
		return GitBranchMutationResult{}, err
	}
	if result.ExitCode == nil || *result.ExitCode != 0 {
		return toBranchMutationFailure(MutationActionCreateAndSwitch, ptr(normalizedBranchName), false, status.Summary, parseGitBranchMutationIssues(result)), nil
	}
	r.invalidate(workspacePath)
	nextStatus, err := r.GetStatus(ctx, workspacePath)
	if err != nil {
		return GitBranchMutationResult{}, err
	}
	return toBranchMutationSuccess(MutationActionCreateAndSwitch, normalizedBranchName, true, true, nextStatus.Summary), nil
}
