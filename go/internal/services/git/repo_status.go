// repo_status.go ports the status queries from gitCliRepo.ts (step 3.3):
// git status execution, the cached GetStatus path, and ignored-path lookup.
// Split out of repo.go to stay under the 400-line archcheck limit.
package git

import (
	"context"
)

func (r *CliRepo) executeGitStatus(ctx context.Context, resolution GitResolvedRepository, untrackedMode string) (GitCommandExecutionResult, error) {
	return r.run(ctx, GitCommandExecutionOptions{
		Cwd:            resolution.RepoRoot,
		Args:           []string{"status", "--porcelain=v2", "--branch", "--untracked-files=" + untrackedMode, "-z"},
		TimeoutMs:      DefaultGitCommandTimeoutMs,
		MaxOutputBytes: DefaultGitOutputBytes,
	})
}

// runGitStatus ports runGitStatus with the collapsed-untracked fallback.
func (r *CliRepo) runGitStatus(ctx context.Context, resolution GitResolvedRepository) (GitCommandExecutionResult, error) {
	r.mu.Lock()
	useCollapsed := r.collapsedUntracked[resolution.RepoRoot]
	r.mu.Unlock()
	untrackedMode := "all"
	if useCollapsed {
		untrackedMode = "normal"
	}
	result, err := r.executeGitStatus(ctx, resolution, untrackedMode)
	if err != nil {
		return GitCommandExecutionResult{}, err
	}
	if useCollapsed || !result.OutputTruncated {
		return result, nil
	}
	// 大仓库的逐文件未跟踪状态可能超过输出上限；首次超限后按 repoRoot 记住目录
	// 折叠模式，避免每次自动刷新都重复执行一次必然失败的详细命令。
	r.mu.Lock()
	r.collapsedUntracked[resolution.RepoRoot] = true
	r.mu.Unlock()
	return r.executeGitStatus(ctx, resolution, "normal")
}

// GetStatus ports getStatus: staged / unstaged / summary / branch 比较都依赖
// 同一份状态快照，并发复用把一次渲染里的重复调用合并成一轮 Git CLI。
func (r *CliRepo) GetStatus(ctx context.Context, workspacePath string) (GitStatusSnapshot, error) {
	r.mu.Lock()
	call := r.statusRequests[workspacePath]
	if call == nil {
		call = &statusCall{}
		call.wg.Add(1)
		r.statusRequests[workspacePath] = call
		r.mu.Unlock()
		res, err := r.statusUncached(ctx, workspacePath)
		if err != nil {
			r.mu.Lock()
			delete(r.statusRequests, workspacePath)
			r.mu.Unlock()
			call.wg.Done()
			return GitStatusSnapshot{}, err
		}
		call.res = res
		call.wg.Done()
		return res, nil
	}
	r.mu.Unlock()
	call.wg.Wait()
	return call.res, nil
}

func (r *CliRepo) statusUncached(ctx context.Context, workspacePath string) (GitStatusSnapshot, error) {
	resolution, err := r.ResolveRepository(ctx, workspacePath)
	if err != nil {
		return GitStatusSnapshot{}, err
	}
	if !resolution.IsGitAvailable || !resolution.IsRepository {
		return GitStatusSnapshot{
			Resolution:     resolution,
			Summary:        createEmptySummary(resolution),
			Entries:        []GitStatusEntry{},
			StagedStats:    map[string]GitLineStat{},
			UnstagedStats:  map[string]GitLineStat{},
			UntrackedStats: map[string]GitLineStat{},
		}, nil
	}

	statusResult, statusErr := r.runGitStatus(ctx, resolution)
	stagedResult, stagedErr := r.run(ctx, GitCommandExecutionOptions{
		Cwd:            resolution.RepoRoot,
		Args:           []string{"diff", "--cached", "--numstat", "-z", "--find-renames", "--"},
		TimeoutMs:      DefaultGitCommandTimeoutMs,
		MaxOutputBytes: DefaultGitOutputBytes,
	})
	unstagedResult, unstagedErr := r.run(ctx, GitCommandExecutionOptions{
		Cwd:            resolution.RepoRoot,
		Args:           []string{"diff", "--numstat", "-z", "--find-renames", "--"},
		TimeoutMs:      DefaultGitCommandTimeoutMs,
		MaxOutputBytes: DefaultGitOutputBytes,
	})
	if statusErr != nil {
		return GitStatusSnapshot{}, statusErr
	}
	if stagedErr != nil {
		return GitStatusSnapshot{}, stagedErr
	}
	if unstagedErr != nil {
		return GitStatusSnapshot{}, unstagedErr
	}
	if err := ensureGitCommandSucceeded("git status", statusResult); err != nil {
		return GitStatusSnapshot{}, err
	}
	if err := ensureGitCommandSucceeded("git diff --cached --numstat", stagedResult); err != nil {
		return GitStatusSnapshot{}, err
	}
	if err := ensureGitCommandSucceeded("git diff --numstat", unstagedResult); err != nil {
		return GitStatusSnapshot{}, err
	}

	parsedStatus := parseStatusPorcelain(statusResult.Stdout)
	untrackedStats := buildUntrackedStats(resolution.RepoRoot, parsedStatus.Entries)

	summary := GitRepositorySummary{
		WorkspacePath:         resolution.WorkspacePath,
		RepoRoot:              resolution.RepoRoot,
		WorkspaceInRepoPath:   resolution.WorkspaceInRepoPath,
		AutoRefreshWatchPaths: resolution.AutoRefreshWatchPaths,
		BranchName:            parsedStatus.BranchName,
		TrackingBranchName:    parsedStatus.TrackingBranchName,
		HeadRefType:           parsedStatus.HeadRefType,
		Ahead:                 parsedStatus.Ahead,
		Behind:                parsedStatus.Behind,
		IsDirty:               len(parsedStatus.Entries) > 0,
		IsGitAvailable:        true,
		IsRepository:          true,
	}
	if summary.AutoRefreshWatchPaths == nil {
		summary.AutoRefreshWatchPaths = []GitRepositoryAutoRefreshWatchPath{}
	}
	return GitStatusSnapshot{
		Resolution:     resolution,
		Summary:        summary,
		Entries:        parsedStatus.Entries,
		StagedStats:    parseNumstatMap(stagedResult.Stdout),
		UnstagedStats:  parseNumstatMap(unstagedResult.Stdout),
		UntrackedStats: untrackedStats,
	}, nil
}

// GetIgnoredPaths ports getIgnoredPaths. `git check-ignore -z` 只能和 --stdin
// 一起使用；argv 传路径必须解析普通换行输出。
