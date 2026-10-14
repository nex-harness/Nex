// repo_push.go ports the push and identity half of gitCliRepo.ts (step 3.3):
// remote resolution, push with the tracking-remote chain, and GetIdentity.
// Split out of repo_commit.go to stay under the 400-line archcheck limit.
package git

import (
	"context"
	"errors"
	"fmt"
	"strings"
)

func (r *CliRepo) readOptionalGitConfig(ctx context.Context, resolution GitResolvedRepository, key string) (*string, error) {
	result, err := r.run(ctx, GitCommandExecutionOptions{
		Cwd:       resolution.RepoRoot,
		Args:      []string{"config", "--get", key},
		TimeoutMs: DefaultGitCommandTimeoutMs,
	})
	if err != nil {
		return nil, err
	}
	if result.TimedOut || result.OutputTruncated {
		if err := ensureGitCommandSucceeded(fmt.Sprintf("git config --get %s", key), result); err != nil {
			return nil, err
		}
	}
	if result.ExitCode != nil && *result.ExitCode == 0 {
		if value := strings.TrimSpace(result.Stdout); value != "" {
			return ptr(value), nil
		}
		return nil, nil
	}
	if result.ExitCode != nil && *result.ExitCode == 1 {
		return nil, nil
	}
	if err := ensureGitCommandSucceeded(fmt.Sprintf("git config --get %s", key), result); err != nil {
		return nil, err
	}
	return nil, nil
}

// listRemotes ports listRemotes.
func (r *CliRepo) listRemotes(ctx context.Context, resolution GitResolvedRepository) ([]string, error) {
	result, err := r.run(ctx, GitCommandExecutionOptions{
		Cwd:            resolution.RepoRoot,
		Args:           []string{"remote"},
		TimeoutMs:      DefaultGitCommandTimeoutMs,
		MaxOutputBytes: DefaultGitOutputBytes,
	})
	if err != nil {
		return nil, err
	}
	if err := ensureGitCommandSucceeded("git remote", result); err != nil {
		return nil, err
	}
	var remotes []string
	for _, line := range strings.Split(strings.ReplaceAll(result.Stdout, "\r\n", "\n"), "\n") {
		if line = strings.TrimSpace(line); line != "" {
			remotes = append(remotes, line)
		}
	}
	return remotes, nil
}

// parseTrackingRemoteName ports parseTrackingRemoteName.
func parseTrackingRemoteName(trackingBranchName *string) *string {
	if trackingBranchName == nil {
		return nil
	}
	remoteName := strings.TrimSpace(strings.SplitN(*trackingBranchName, "/", 2)[0])
	if remoteName == "" {
		return nil
	}
	return ptr(remoteName)
}

// resolvePushRemote ports resolvePushRemote: branch remote > remote.pushDefault
// > origin > the single remote; error on none or ambiguity.
func (r *CliRepo) resolvePushRemote(ctx context.Context, status GitStatusSnapshot) (string, error) {
	resolution, err := ensureRepositoryAvailable(status.Resolution, "push changes")
	if err != nil {
		return "", err
	}
	branchName := ""
	if status.Summary.BranchName != nil {
		branchName = strings.TrimSpace(*status.Summary.BranchName)
	}
	if branchName == "" {
		return "", errors.New("Cannot resolve a Git push remote without a current branch.")
	}
	if branchRemote, err := r.readOptionalGitConfig(ctx, resolution, fmt.Sprintf("branch.%s.remote", branchName)); err != nil {
		return "", err
	} else if branchRemote != nil {
		return *branchRemote, nil
	}
	if pushDefault, err := r.readOptionalGitConfig(ctx, resolution, "remote.pushDefault"); err != nil {
		return "", err
	} else if pushDefault != nil {
		return *pushDefault, nil
	}
	remotes, err := r.listRemotes(ctx, resolution)
	if err != nil {
		return "", err
	}
	for _, remote := range remotes {
		if remote == "origin" {
			return "origin", nil
		}
	}
	if len(remotes) == 1 {
		return remotes[0], nil
	}
	if len(remotes) == 0 {
		return "", errors.New("No Git remote is configured for the current repository.")
	}
	return "", errors.New("Multiple Git remotes are configured. Configure branch.<name>.remote or remote.pushDefault first.")
}

// Push ports push. push 是显式用户动作且可能被 pre-push hook 拉长：单独使用
// 更长超时和更大输出配额，避免测试脚本尚未跑完就被误判成失败。
func (r *CliRepo) Push(ctx context.Context, workspacePath string) (GitPushResult, error) {
	status, err := r.GetStatus(ctx, workspacePath)
	if err != nil {
		return GitPushResult{}, err
	}
	resolution, err := ensureRepositoryAvailable(status.Resolution, "push changes")
	if err != nil {
		return GitPushResult{}, err
	}
	branchName := ""
	if status.Summary.BranchName != nil {
		branchName = strings.TrimSpace(*status.Summary.BranchName)
	}
	if status.Summary.HeadRefType != HeadRefTypeBranch || branchName == "" {
		return GitPushResult{}, errors.New("Cannot push while HEAD is detached.")
	}

	hasTrackingBranch := status.Summary.TrackingBranchName != nil
	remoteName := ""
	if hasTrackingBranch {
		if name := parseTrackingRemoteName(status.Summary.TrackingBranchName); name != nil {
			remoteName = *name
		}
	} else {
		remoteName, err = r.resolvePushRemote(ctx, status)
		if err != nil {
			return GitPushResult{}, err
		}
	}
	var args []string
	if hasTrackingBranch {
		args = []string{"push"}
	} else {
		target := remoteName
		if target == "" {
			target = "origin"
		}
		args = []string{"push", "--set-upstream", target, branchName}
	}
	pushResult, err := r.run(ctx, GitCommandExecutionOptions{
		Cwd:            resolution.RepoRoot,
		Args:           args,
		TimeoutMs:      DefaultGitPushTimeoutMs,
		MaxOutputBytes: DefaultGitPushOutputBytes,
	})
	if err != nil {
		return GitPushResult{}, err
	}
	if err := ensureGitCommandSucceeded("git push", pushResult); err != nil {
		return GitPushResult{}, err
	}
	r.invalidate(workspacePath)

	nextStatus, err := r.GetStatus(ctx, workspacePath)
	if err != nil {
		return GitPushResult{}, err
	}
	resultRemote := remoteName
	if resultRemote == "" {
		if name := parseTrackingRemoteName(nextStatus.Summary.TrackingBranchName); name != nil {
			resultRemote = *name
		}
	}
	var remotePtr *string
	if resultRemote != "" {
		remotePtr = ptr(resultRemote)
	}
	var branchPtr *string
	if branchName != "" {
		branchPtr = ptr(branchName)
	}
	return GitPushResult{
		BranchName:         branchPtr,
		TrackingBranchName: nextStatus.Summary.TrackingBranchName,
		RemoteName:         remotePtr,
		SetUpstream:        !hasTrackingBranch,
		Summary:            nextStatus.Summary,
	}, nil
}

// ---------------------------------------------------------------------------
// identity
// ---------------------------------------------------------------------------

// GetIdentity ports getIdentity.
func (r *CliRepo) GetIdentity(ctx context.Context, workspacePath string) (GitIdentity, error) {
	resolution, err := r.ResolveRepository(ctx, workspacePath)
	if err != nil {
		return GitIdentity{}, err
	}
	if !resolution.IsGitAvailable || !resolution.IsRepository {
		return GitIdentity{}, nil
	}
	nameResult, err := r.run(ctx, GitCommandExecutionOptions{
		Cwd:       resolution.RepoRoot,
		Args:      []string{"config", "--show-scope", "--show-origin", "--get", "user.name"},
		TimeoutMs: DefaultGitCommandTimeoutMs,
	})
	if err != nil {
		return GitIdentity{}, err
	}
	emailResult, err := r.run(ctx, GitCommandExecutionOptions{
		Cwd:       resolution.RepoRoot,
		Args:      []string{"config", "--show-scope", "--show-origin", "--get", "user.email"},
		TimeoutMs: DefaultGitCommandTimeoutMs,
	})
	if err != nil {
		return GitIdentity{}, err
	}
	name, err := parseGitConfigValue(nameResult)
	if err != nil {
		return GitIdentity{}, err
	}
	email, err := parseGitConfigValue(emailResult)
	if err != nil {
		return GitIdentity{}, err
	}
	var scopeLabel *string
	if name.Scope != nil {
		scopeLabel = name.Scope
	} else if email.Scope != nil {
		scopeLabel = email.Scope
	}
	return GitIdentity{
		UserName:    name.Value,
		UserEmail:   email.Value,
		NameSource:  name.Source,
		EmailSource: email.Source,
		ScopeLabel:  scopeLabel,
	}, nil
}
