// repo_resolve.go ports repository resolution and workspace info from
// gitCliRepo.ts (step 3.3): the cached ResolveRepository path, the uncached
// resolver, and GetWorkspaceRepositoryInfo with its cache.
// Split out of repo.go to stay under the 400-line archcheck limit.
package git

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

func (r *CliRepo) ResolveRepository(ctx context.Context, workspacePath string) (GitResolvedRepository, error) {
	r.mu.Lock()
	call := r.resolutionRequests[workspacePath]
	if call == nil {
		call = &resolutionCall{}
		call.wg.Add(1)
		r.resolutionRequests[workspacePath] = call
		r.mu.Unlock()
		res, err := r.resolveRepositoryUncached(ctx, workspacePath)
		call.res = res
		if err != nil {
			// 失败不缓存：下一次调用重试（TS 的 promise 删除行为）。
			r.mu.Lock()
			delete(r.resolutionRequests, workspacePath)
			r.mu.Unlock()
			call.wg.Done()
			return res, err
		}
		call.wg.Done()
		return res, nil
	}
	r.mu.Unlock()
	call.wg.Wait()
	return call.res, nil
}

func (r *CliRepo) resolveRepositoryUncached(ctx context.Context, workspacePath string) (GitResolvedRepository, error) {
	gitBinary, err := r.provider.ResolveGitBinary(ctx)
	if err != nil {
		return GitResolvedRepository{}, err
	}
	if gitBinary == "" {
		return unavailableResolution(workspacePath, false), nil
	}
	result, err := r.run(ctx, GitCommandExecutionOptions{
		Cwd: workspacePath,
		Args: []string{
			"rev-parse",
			"--show-toplevel",
			"--show-prefix",
			"--absolute-git-dir",
			"--git-common-dir",
		},
		TimeoutMs: DefaultGitCommandTimeoutMs,
	})
	if err != nil {
		return unavailableResolution(workspacePath, true), nil
	}
	if result.ExitCode == nil || *result.ExitCode != 0 {
		// 测试/窗口切换时 workspace 目录可能在并发请求过程中被删除。目录缺失不属
		// 于"Git 协议失败"，按"当前非可用仓库"降级返回。
		if isMissingWorkingDirectoryResult(result) || isNotRepositoryResult(result) {
			return unavailableResolution(workspacePath, true), nil
		}
		if err := ensureGitCommandSucceeded("git rev-parse", result); err != nil {
			return GitResolvedRepository{}, err
		}
	}
	lines := strings.Split(strings.ReplaceAll(result.Stdout, "\r\n", "\n"), "\n")
	repoRoot := strings.TrimSpace(firstLineOrEmpty(lines))
	if repoRoot == "" {
		return GitResolvedRepository{}, fmt.Errorf("Failed to resolve Git repository root")
	}
	return GitResolvedRepository{
		WorkspacePath:         workspacePath,
		RepoRoot:              repoRoot,
		WorkspaceInRepoPath:   NormalizeWorkspaceInRepoPath(secondLineOrEmpty(lines)),
		AutoRefreshWatchPaths: buildAutoRefreshWatchPaths(workspacePath, thirdLineOrEmpty(lines), fourthLineOrEmpty(lines)),
		IsGitAvailable:        true,
		IsRepository:          true,
	}, nil
}

func firstLineOrEmpty(lines []string) string  { return lineOrEmpty(lines, 0) }
func secondLineOrEmpty(lines []string) string { return lineOrEmpty(lines, 1) }
func thirdLineOrEmpty(lines []string) string  { return lineOrEmpty(lines, 2) }
func fourthLineOrEmpty(lines []string) string { return lineOrEmpty(lines, 3) }

func lineOrEmpty(lines []string, index int) string {
	if index < len(lines) {
		return lines[index]
	}
	return ""
}

// GetWorkspaceRepositoryInfo ports getWorkspaceRepositoryInfo: linked-worktree
// detection via the `.git` FILE's gitdir pointer (`/.git/worktrees/` 结构),
// fail-open 当成 main-tree（迁移过滤不是强依赖，宁可少过滤也不要误杀）。
func (r *CliRepo) GetWorkspaceRepositoryInfo(ctx context.Context, workspacePath string) (GitWorkspaceRepositoryInfo, error) {
	r.mu.Lock()
	call := r.infoRequests[workspacePath]
	if call == nil {
		call = &infoCall{}
		call.wg.Add(1)
		r.infoRequests[workspacePath] = call
		r.mu.Unlock()
		res, err := r.workspaceInfoUncached(ctx, workspacePath)
		if err != nil {
			r.mu.Lock()
			delete(r.infoRequests, workspacePath)
			r.mu.Unlock()
			call.wg.Done()
			return GitWorkspaceRepositoryInfo{}, err
		}
		call.res = res
		call.wg.Done()
		return res, nil
	}
	r.mu.Unlock()
	call.wg.Wait()
	return call.res, nil
}

func (r *CliRepo) workspaceInfoUncached(ctx context.Context, workspacePath string) (GitWorkspaceRepositoryInfo, error) {
	resolution, err := r.ResolveRepository(ctx, workspacePath)
	if err != nil {
		return GitWorkspaceRepositoryInfo{}, err
	}
	if !resolution.IsGitAvailable || !resolution.IsRepository {
		return GitWorkspaceRepositoryInfo{
			WorkspacePath:  workspacePath,
			Kind:           WorkspaceRepoKindNotRepository,
			IsGitAvailable: resolution.IsGitAvailable,
		}, nil
	}
	gitEntryPath := filepath.Join(resolution.RepoRoot, ".git")
	if info, err := os.Stat(gitEntryPath); err == nil {
		if info.IsDir() {
			return GitWorkspaceRepositoryInfo{WorkspacePath: workspacePath, Kind: WorkspaceRepoKindMainTree, IsGitAvailable: true}, nil
		}
		if info.Mode().IsRegular() {
			content, err := os.ReadFile(gitEntryPath)
			if err == nil {
				contentLines := strings.Split(strings.ReplaceAll(string(content), "\r\n", "\n"), "\n")
				firstLine := strings.TrimSpace(firstLineOrEmpty(contentLines))
				const gitDirPrefix = "gitdir:"
				if strings.HasPrefix(firstLine, gitDirPrefix) {
					rawGitDir := strings.TrimSpace(firstLine[len(gitDirPrefix):])
					resolvedGitDir := ""
					if rawGitDir != "" {
						if filepath.IsAbs(rawGitDir) {
							resolvedGitDir = rawGitDir
						} else {
							resolvedGitDir = filepath.Join(resolution.RepoRoot, rawGitDir)
						}
					}
					normalizedGitDir := strings.ReplaceAll(resolvedGitDir, "\\", "/")
					// linked worktree 的 `.git` 文件指向 `<main-tree>/.git/worktrees/<name>`；
					// 其它 `.git` 文件形态（submodule / separate-git-dir）一律按 main-tree 放行。
					if strings.Contains(normalizedGitDir, "/.git/worktrees/") {
						return GitWorkspaceRepositoryInfo{WorkspacePath: workspacePath, Kind: WorkspaceRepoKindLinkedWorktree, IsGitAvailable: true}, nil
					}
				}
			}
		}
	}
	return GitWorkspaceRepositoryInfo{WorkspacePath: workspacePath, Kind: WorkspaceRepoKindMainTree, IsGitAvailable: true}, nil
}

// executeGitStatus ports executeGitStatus.
