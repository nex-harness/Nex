// checkpoint_restore.go ports RestoreBetweenCheckpoints and DeleteCheckpoint
// from gitCheckpointRepo.ts (step 3.3). Split out of checkpoint_repo.go to
// stay under the 400-line archcheck limit.
package git

import (
	"context"
)

func (r *CheckpointRepo) RestoreBetweenCheckpoints(ctx context.Context, workspacePath string, from, to GitCheckpointMeta, force bool) (GitCheckpointRestoreResult, error) {
	resolution, err := r.ensureRepository(ctx, workspacePath)
	if err != nil {
		return GitCheckpointRestoreResult{}, err
	}
	diff, err := r.computeCheckpointDiff(ctx, workspacePath, from, to)
	if err != nil {
		return GitCheckpointRestoreResult{}, err
	}
	affectedRepoPaths := make([]string, 0, len(diff.Files))
	seen := map[string]bool{}
	for _, path := range buildAffectedRepoPaths(diff.Files) {
		normalized := normalizeAffectedRepoPath(resolution.RepoRoot, path)
		if !seen[normalized] {
			seen[normalized] = true
			affectedRepoPaths = append(affectedRepoPaths, normalized)
		}
	}
	if len(affectedRepoPaths) == 0 {
		return GitCheckpointRestoreResult{Success: true, RestoredPaths: []string{}}, nil
	}

	conflicts, err := r.collectWorkspaceConflicts(ctx, resolution.RepoRoot, resolution.WorkspaceInRepoPath, from, affectedRepoPaths)
	if err != nil {
		return GitCheckpointRestoreResult{}, err
	}
	if len(conflicts) > 0 && !force {
		// 默认模式：一旦相关路径偏离 fromCheckpoint 就拒绝写入，把冲突抛给用户。
		return GitCheckpointRestoreResult{Success: false, Conflicts: conflicts}, nil
	}

	var restoreRepoPaths []string
	for _, file := range diff.Files {
		if file.Kind != ChangeKindDeleted {
			restoreRepoPaths = append(restoreRepoPaths, file.RepoRelativePath)
		}
	}
	if len(restoreRepoPaths) > 0 {
		// restore 阶段只改 worktree 不改真实 index：回滚是"文件状态恢复"，
		// 不是偷偷改用户暂存区的破坏性操作。
		restoreResult, err := r.run(ctx, GitCommandExecutionOptions{
			Cwd:  resolution.RepoRoot,
			Args: append([]string{"restore", "--source=" + to.CommitOid, "--worktree", "--"}, restoreRepoPaths...),
		})
		if err != nil {
			return GitCheckpointRestoreResult{}, err
		}
		if err := ensureGitCommandSucceeded("git restore checkpoint", restoreResult); err != nil {
			return GitCheckpointRestoreResult{}, err
		}
	}

	deleteAbsolutePaths := map[string]bool{}
	for _, file := range diff.Files {
		if file.Kind == ChangeKindDeleted {
			// toCheckpoint 不再包含这些路径，restore 后要删掉 live worktree 残留。
			deleteAbsolutePaths[file.Path] = true
			continue
		}
		if file.Kind == ChangeKindRenamed && file.OriginalPath != nil {
			// rename 场景下 git restore 只恢复新路径；旧路径显式删除避免并存。
			deleteAbsolutePaths[*file.OriginalPath] = true
		}
	}
	for path := range deleteAbsolutePaths {
		if err := removeFileIfExists(path); err != nil {
			return GitCheckpointRestoreResult{}, err
		}
	}

	verifyConflicts, err := r.collectWorkspaceConflicts(ctx, resolution.RepoRoot, resolution.WorkspaceInRepoPath, to, affectedRepoPaths)
	if err != nil {
		return GitCheckpointRestoreResult{}, err
	}
	if len(verifyConflicts) > 0 {
		// 理论上不应再有偏差；还有说明 restore 没把磁盘真正带到目标状态，
		// 返回 success 只会把错误状态固化，因此直接抛错让调用方感知。
		return GitCheckpointRestoreResult{}, errCheckpointVerification
	}
	restoredPaths := make([]string, 0, len(diff.Files))
	for _, file := range diff.Files {
		restoredPaths = append(restoredPaths, file.Path)
	}
	return GitCheckpointRestoreResult{Success: true, RestoredPaths: restoredPaths}, nil
}

// DeleteCheckpoint ports deleteCheckpoint: 只删 hidden ref；manifest 由上层
// store/service 负责。repo 层专注 Git 对象引用管理。
func (r *CheckpointRepo) DeleteCheckpoint(ctx context.Context, workspacePath string, checkpoint GitCheckpointMeta) error {
	resolution, err := r.ensureRepository(ctx, workspacePath)
	if err != nil {
		return err
	}
	result, err := r.run(ctx, GitCommandExecutionOptions{
		Cwd:  resolution.RepoRoot,
		Args: []string{"update-ref", "-d", checkpoint.RefName},
	})
	if err != nil {
		return err
	}
	return ensureGitCommandSucceeded("git update-ref -d checkpoint", result, 0, 1)
}

func stringsTrimSpace(s string) string {
	for len(s) > 0 && (s[0] == ' ' || s[0] == '\t' || s[0] == '\n' || s[0] == '\r') {
		s = s[1:]
	}
	for len(s) > 0 && (s[len(s)-1] == ' ' || s[len(s)-1] == '\t' || s[len(s)-1] == '\n' || s[len(s)-1] == '\r') {
		s = s[:len(s)-1]
	}
	return s
}

func sortConflicts(conflicts []GitCheckpointConflict) {
	// Deterministic order for the RPC payload (Go map iteration is random;
	// the TS iterates the dedupe Map in insertion order — affected-path order).
	for i := 1; i < len(conflicts); i++ {
		for j := i; j > 0 && conflicts[j].RepoRelativePath < conflicts[j-1].RepoRelativePath; j-- {
			conflicts[j], conflicts[j-1] = conflicts[j-1], conflicts[j]
		}
	}
}
