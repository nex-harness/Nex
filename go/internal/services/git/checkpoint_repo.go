// checkpoint_repo.go ports packages/services/src/git/repo/gitCheckpointRepo.ts
// (step 3.3): the immutable workspace snapshot engine — temp index, write-tree,
// commit-tree, hidden ref — plus diff / restore / delete.
//
// 关键约束（与 TS 相同）：
//   - 不污染用户真实 index（临时 GIT_INDEX_FILE）；
//   - 不产生用户可见分支或普通 commit（commit-tree + refs/nex/checkpoints/…）；
//   - scope 永远跟随当前 workspace，而不是整个 repo 无差别快照。
package git

import (
	"context"
	"errors"
	"fmt"
	"github.com/tylinux/nex/go/internal/services/paths"
	"os"
	"path/filepath"
	"time"
)

// nowMillis is the checkpoint createdAt clock (TS Date.now()).
func nowMillis() int64 { return time.Now().UnixMilli() }

// defaultCheckpointIndexRoot is getGitCheckpointIndexRootDir()
// (~/.nex/git-checkpoint-index).
func defaultCheckpointIndexRoot() string {
	return paths.NewFromProcess().GitCheckpointIndexRootDir()
}

// CheckpointRepo is the GitCheckpointRepo port.
type CheckpointRepo struct {
	provider GitCommandProvider
	repo     *CliRepo
	// IndexRootDir is getGitCheckpointIndexRootDir(); injected for tests so
	// suites never write into the real data dir.
	IndexRootDir string
}

// NewCheckpointRepo ports createGitCheckpointRepo.
func NewCheckpointRepo(provider GitCommandProvider, cliRepo *CliRepo, indexRootDir string) *CheckpointRepo {
	if provider == nil {
		provider = NewCommandProvider(CommandProviderOptions{})
	}
	if cliRepo == nil {
		cliRepo = NewCliRepo(provider)
	}
	if indexRootDir == "" {
		indexRootDir = defaultCheckpointIndexRoot()
	}
	return &CheckpointRepo{provider: provider, repo: cliRepo, IndexRootDir: indexRootDir}
}

func (r *CheckpointRepo) run(ctx context.Context, options GitCommandExecutionOptions) (GitCommandExecutionResult, error) {
	return r.provider.Run(ctx, options)
}

// ensureRepository ports the repo's shared guard: 所有 checkpoint 操作都必须建立
// 在"当前 workspace 对应一个真实 Git 仓库"这个前提上。
func (r *CheckpointRepo) ensureRepository(ctx context.Context, workspacePath string) (GitResolvedRepository, error) {
	resolution, err := r.repo.ResolveRepository(ctx, workspacePath)
	if err != nil {
		return GitResolvedRepository{}, err
	}
	if !resolution.IsGitAvailable {
		return GitResolvedRepository{}, errors.New("Git binary is not available in the current environment.")
	}
	if !resolution.IsRepository {
		return GitResolvedRepository{}, errors.New("Workspace is not inside a Git repository.")
	}
	return resolution, nil
}

// computeCheckpointDiff ports computeCheckpointDiff: 只针对当前 workspace scope
// 计算，monorepo 子目录打开时 restore / 摘要 / 冲突检测都收敛在工作区边界内。
func (r *CheckpointRepo) computeCheckpointDiff(ctx context.Context, workspacePath string, from, to GitCheckpointMeta) (GitCheckpointDiff, error) {
	resolution, err := r.ensureRepository(ctx, workspacePath)
	if err != nil {
		return GitCheckpointDiff{}, err
	}
	pathspec := GetWorkspacePathspec(resolution.WorkspaceInRepoPath)
	nameStatusResult, err := r.run(ctx, GitCommandExecutionOptions{
		Cwd:  resolution.RepoRoot,
		Args: []string{"diff", "--name-status", "--find-renames", "-z", from.CommitOid, to.CommitOid, "--", pathspec},
	})
	if err != nil {
		return GitCheckpointDiff{}, err
	}
	numstatResult, err := r.run(ctx, GitCommandExecutionOptions{
		Cwd:  resolution.RepoRoot,
		Args: []string{"diff", "--numstat", "--find-renames", "-z", from.CommitOid, to.CommitOid, "--", pathspec},
	})
	if err != nil {
		return GitCheckpointDiff{}, err
	}
	if err := ensureGitCommandSucceeded("git diff --name-status checkpoint", nameStatusResult); err != nil {
		return GitCheckpointDiff{}, err
	}
	if err := ensureGitCommandSucceeded("git diff --numstat checkpoint", numstatResult); err != nil {
		return GitCheckpointDiff{}, err
	}
	return mergeCheckpointDiff(
		resolution.RepoRoot,
		resolution.WorkspaceInRepoPath,
		from.CheckpointID,
		to.CheckpointID,
		parseNameStatus(nameStatusResult.Stdout),
		parseNumstatCheckpoint(numstatResult.Stdout),
	), nil
}

// collectWorkspaceConflicts ports collectWorkspaceConflicts: 冲突检测不是判断
// "整个工作区是否 dirty"，而是判断 restore 将触达的路径当前磁盘状态是否仍然
// 等于 fromCheckpoint —— 这样无关改动存在时依然安全工作。
func (r *CheckpointRepo) collectWorkspaceConflicts(ctx context.Context, repoRoot, workspaceInRepoPath string, from GitCheckpointMeta, affectedRepoPaths []string) ([]GitCheckpointConflict, error) {
	if len(affectedRepoPaths) == 0 {
		return []GitCheckpointConflict{}, nil
	}
	args := append([]string{"ls-tree", "-r", "-z", from.CommitOid, "--"}, affectedRepoPaths...)
	treeResult, err := r.run(ctx, GitCommandExecutionOptions{Cwd: repoRoot, Args: args})
	if err != nil {
		return nil, err
	}
	if err := ensureGitCommandSucceeded("git ls-tree checkpoint paths", treeResult); err != nil {
		return nil, err
	}
	treeEntries := parseLsTree(treeResult.Stdout)

	conflicts := map[string]GitCheckpointConflict{}
	addConflict := func(repoRelativePath, reason string) {
		if _, exists := conflicts[repoRelativePath]; exists {
			return
		}
		conflicts[repoRelativePath] = GitCheckpointConflict{
			Path:                  toAbsolutePath(repoRoot, repoRelativePath),
			RepoRelativePath:      repoRelativePath,
			WorkspaceRelativePath: ToWorkspaceRelativeGitPath(repoRelativePath, workspaceInRepoPath),
			Reason:                GitCheckpointConflictReason(reason),
		}
	}
	for _, repoRelativePath := range affectedRepoPaths {
		absolutePath := toAbsolutePath(repoRoot, repoRelativePath)
		expectedEntry, hasEntry := treeEntries[repoRelativePath]
		if !hasEntry {
			// fromCheckpoint 里不存在该路径：如果现在磁盘上却存在，说明用户在
			// checkpoint 之后新增了该文件，属于覆盖风险。
			if !pathExistsCheckpoint(absolutePath) {
				continue
			}
			addConflict(repoRelativePath, "unexpected-file-in-worktree")
			continue
		}
		info, err := os.Lstat(absolutePath)
		if err != nil {
			// fromCheckpoint 要求该文件存在但磁盘上已没有：恢复会覆盖掉"文件为何
			// 消失"的真实用户操作，先显式报告冲突。
			addConflict(repoRelativePath, "missing-in-worktree")
			continue
		}
		expectsSymlink := expectedEntry.Mode == "120000"
		if info.IsDir() || (expectsSymlink && info.Mode()&os.ModeSymlink == 0) {
			// checkpoint 期待的是文件/符号链接，磁盘却是目录或其它类型：类型冲突。
			addConflict(repoRelativePath, "type-mismatch")
			continue
		}
		hash, err := r.hashObjectNoFilters(ctx, repoRoot, absolutePath)
		if err != nil {
			return nil, err
		}
		if hash == expectedEntry.ObjectID {
			continue
		}
		// 不比较时间戳、大小等弱信号，直接比较 blob hash。
		addConflict(repoRelativePath, "content-mismatch")
	}
	out := make([]GitCheckpointConflict, 0, len(conflicts))
	for _, conflict := range conflicts {
		out = append(out, conflict)
	}
	sortConflicts(out)
	return out, nil
}

// CreateCheckpoint ports createCheckpoint: 预热临时 index（复制用户 index 优先，
// 降级 read-tree HEAD，最差空 index）后把当前 workspace 固化成隐藏 commit。
func (r *CheckpointRepo) CreateCheckpoint(ctx context.Context, workspacePath, checkpointID string) (GitCheckpointMeta, error) {
	resolution, err := r.ensureRepository(ctx, workspacePath)
	if err != nil {
		return GitCheckpointMeta{}, err
	}
	refName := GetCheckpointRefName(workspacePath, checkpointID)
	if err := os.MkdirAll(r.IndexRootDir, 0o777); err != nil {
		return GitCheckpointMeta{}, err
	}
	tempIndexDir, err := os.MkdirTemp(r.IndexRootDir, "index-")
	if err != nil {
		return GitCheckpointMeta{}, err
	}
	defer func() { _ = os.RemoveAll(tempIndexDir) }()
	tempIndexPath := filepath.Join(tempIndexDir, "index")
	env := buildCheckpointEnv(tempIndexPath)
	pathspec := GetWorkspacePathspec(resolution.WorkspaceInRepoPath)

	// 预热：空 index 下 git add -A 会全量 open/read/hash，Windows + Defender
	// 单文件开销 5-15ms；复用用户 index 的 stat cache 后未改动文件走 stat-match
	// 快速路径，且最终 tree 由 worktree 决定，语义完全等价。
	indexPathResult, err := r.run(ctx, GitCommandExecutionOptions{
		Cwd:  resolution.RepoRoot,
		Args: []string{"rev-parse", "--git-path", "index"},
	})
	if err != nil {
		return GitCheckpointMeta{}, err
	}
	primed := false
	if indexPathResult.ExitCode != nil && *indexPathResult.ExitCode == 0 {
		indexRelPath := stringsTrimSpace(indexPathResult.Stdout)
		if indexRelPath != "" {
			userIndexPath := filepath.Join(resolution.RepoRoot, indexRelPath)
			if data, readErr := os.ReadFile(userIndexPath); readErr == nil {
				if writeErr := os.WriteFile(tempIndexPath, data, 0o600); writeErr == nil {
					primed = true
				}
			}
		}
	}
	if !primed {
		// 没有可复制的 index 时用 HEAD tree 填充临时 index；HEAD 不存在（全新仓库）
		// 时允许静默回落到空 index。
		readTreeResult, err := r.run(ctx, GitCommandExecutionOptions{
			Cwd:  resolution.RepoRoot,
			Args: []string{"read-tree", "HEAD"},
			Env:  env,
		})
		if err != nil {
			return GitCheckpointMeta{}, err
		}
		if readTreeResult.ExitCode != nil && *readTreeResult.ExitCode == 0 {
			primed = true
		}
	}

	addResult, err := r.run(ctx, GitCommandExecutionOptions{
		Cwd:  resolution.RepoRoot,
		Args: []string{"add", "-A", "--", pathspec},
		Env:  env,
	})
	if err != nil {
		return GitCheckpointMeta{}, err
	}
	if err := ensureGitCommandSucceeded("git add checkpoint", addResult); err != nil {
		return GitCheckpointMeta{}, err
	}
	treeResult, err := r.run(ctx, GitCommandExecutionOptions{
		Cwd:  resolution.RepoRoot,
		Args: []string{"write-tree"},
		Env:  env,
	})
	if err != nil {
		return GitCheckpointMeta{}, err
	}
	if err := ensureGitCommandSucceeded("git write-tree checkpoint", treeResult); err != nil {
		return GitCheckpointMeta{}, err
	}
	commitResult, err := r.run(ctx, GitCommandExecutionOptions{
		Cwd:  resolution.RepoRoot,
		Args: []string{"commit-tree", stringsTrimSpace(treeResult.Stdout), "-m", fmt.Sprintf("nex checkpoint %s", checkpointID)},
		Env:  env,
	})
	if err != nil {
		return GitCheckpointMeta{}, err
	}
	if err := ensureGitCommandSucceeded("git commit-tree checkpoint", commitResult); err != nil {
		return GitCheckpointMeta{}, err
	}
	commitOid := stringsTrimSpace(commitResult.Stdout)

	// hidden ref 保证 Git 不会把这份对象当垃圾回收，后续 diff/restore/delete 都
	// 按 refName 稳定找回。
	updateRefResult, err := r.run(ctx, GitCommandExecutionOptions{
		Cwd:  resolution.RepoRoot,
		Args: []string{"update-ref", refName, commitOid},
	})
	if err != nil {
		return GitCheckpointMeta{}, err
	}
	if err := ensureGitCommandSucceeded("git update-ref checkpoint", updateRefResult); err != nil {
		return GitCheckpointMeta{}, err
	}
	return GitCheckpointMeta{
		CheckpointID:        checkpointID,
		WorkspacePath:       workspacePath,
		RepoRoot:            resolution.RepoRoot,
		WorkspaceInRepoPath: resolution.WorkspaceInRepoPath,
		CreatedAt:           nowMillis(),
		RefName:             refName,
		CommitOid:           commitOid,
		Scope:               "workspace",
	}, nil
}

// DiffCheckpoints ports diffCheckpoints (pure read, no side effects).
func (r *CheckpointRepo) DiffCheckpoints(ctx context.Context, workspacePath string, from, to GitCheckpointMeta) (GitCheckpointDiff, error) {
	return r.computeCheckpointDiff(ctx, workspacePath, from, to)
}

// RestoreBetweenCheckpoints ports restoreBetweenCheckpoints: 三方比较语义 ——
// 调用方声明磁盘应停留在 fromCheckpoint，目标是 toCheckpoint，现实可能已被改过。
// 有冲突默认拒绝写入；force=true 时覆盖；恢复后再次校验磁盘等于 toCheckpoint。
