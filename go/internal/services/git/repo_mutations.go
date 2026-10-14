// repo_mutations.go ports the staging half of gitCliRepo.ts (step 3.3): path
// normalization, stage, unstage, discard. Split from the branch/diff/graph
// halves to stay under the 400-line archcheck limit.
package git

import (
	"context"
)

func (r *CliRepo) normalizeRepoPaths(ctx context.Context, resolution GitResolvedRepository, paths []string) ([]string, error) {
	seen := map[string]bool{}
	var out []string
	for _, path := range paths {
		repoPath, err := normalizeInputPath(resolution, path)
		if err != nil {
			return nil, err
		}
		if !seen[repoPath] {
			seen[repoPath] = true
			out = append(out, repoPath)
		}
	}
	return out, nil
}

// Stage ports stage.
func (r *CliRepo) Stage(ctx context.Context, workspacePath string, paths []string) error {
	resolution, err := ensureRepositoryAvailableSync(ctx, r, workspacePath, "stage paths")
	if err != nil {
		return err
	}
	repoPaths, err := r.normalizeRepoPaths(ctx, resolution, paths)
	if err != nil {
		return err
	}
	if len(repoPaths) == 0 {
		return nil
	}
	result, err := r.run(ctx, GitCommandExecutionOptions{
		Cwd:       resolution.RepoRoot,
		Args:      append([]string{"add", "--"}, repoPaths...),
		TimeoutMs: DefaultGitCommandTimeoutMs,
	})
	if err != nil {
		return err
	}
	if err := ensureGitCommandSucceeded("git add", result); err != nil {
		return err
	}
	r.invalidate(workspacePath)
	return nil
}

// Unstage ports unstage.
func (r *CliRepo) Unstage(ctx context.Context, workspacePath string, paths []string) error {
	resolution, err := ensureRepositoryAvailableSync(ctx, r, workspacePath, "unstage paths")
	if err != nil {
		return err
	}
	repoPaths, err := r.normalizeRepoPaths(ctx, resolution, paths)
	if err != nil {
		return err
	}
	if len(repoPaths) == 0 {
		return nil
	}
	result, err := r.run(ctx, GitCommandExecutionOptions{
		Cwd:       resolution.RepoRoot,
		Args:      append([]string{"restore", "--staged", "--"}, repoPaths...),
		TimeoutMs: DefaultGitCommandTimeoutMs,
	})
	if err != nil {
		return err
	}
	if err := ensureGitCommandSucceeded("git restore --staged", result); err != nil {
		return err
	}
	r.invalidate(workspacePath)
	return nil
}

// Discard ports discard.
func (r *CliRepo) Discard(ctx context.Context, workspacePath string, paths []string, staged bool) error {
	resolution, err := ensureRepositoryAvailableSync(ctx, r, workspacePath, "discard paths")
	if err != nil {
		return err
	}
	repoPaths, err := r.normalizeRepoPaths(ctx, resolution, paths)
	if err != nil {
		return err
	}
	if len(repoPaths) == 0 {
		return nil
	}
	var args []string
	if staged {
		args = append([]string{"restore", "--source=HEAD", "--staged", "--worktree", "--"}, repoPaths...)
	} else {
		args = append([]string{"restore", "--worktree", "--"}, repoPaths...)
	}
	result, err := r.run(ctx, GitCommandExecutionOptions{
		Cwd:       resolution.RepoRoot,
		Args:      args,
		TimeoutMs: DefaultGitCommandTimeoutMs,
	})
	if err != nil {
		return err
	}
	if err := ensureGitCommandSucceeded("git restore", result); err != nil {
		return err
	}
	r.invalidate(workspacePath)
	return nil
}

func ensureRepositoryAvailableSync(ctx context.Context, repo *CliRepo, workspacePath, label string) (GitResolvedRepository, error) {
	resolution, err := repo.ResolveRepository(ctx, workspacePath)
	if err != nil {
		return GitResolvedRepository{}, err
	}
	return ensureRepositoryAvailable(resolution, label)
}
