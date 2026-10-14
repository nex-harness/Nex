// repo_branches.go ports the branch-listing and commit-graph queries from
// gitCliRepo.ts (step 3.3). Split out of repo.go to stay under the 400-line
// archcheck limit.
package git

import (
	"context"
	"fmt"
	"sort"
	"strconv"
	"strings"
)

func (r *CliRepo) ListLocalBranches(ctx context.Context, workspacePath string) (GitLocalBranchListResult, error) {
	status, err := r.GetStatus(ctx, workspacePath)
	if err != nil {
		return GitLocalBranchListResult{}, err
	}
	if !status.Resolution.IsGitAvailable || !status.Resolution.IsRepository {
		return GitLocalBranchListResult{
			HeadRefType:       status.Summary.HeadRefType,
			CurrentBranchName: status.Summary.BranchName,
			Branches:          []GitLocalBranch{},
		}, nil
	}
	result, err := r.run(ctx, GitCommandExecutionOptions{
		Cwd: resolutionRepoRoot(status.Resolution),
		Args: []string{
			"for-each-ref",
			"refs/heads",
			"--format=%(refname:short)%00%(upstream:short)%00%(objectname)%00%(committerdate:unix)",
		},
		TimeoutMs:      DefaultGitCommandTimeoutMs,
		MaxOutputBytes: DefaultGitOutputBytes,
	})
	if err != nil {
		return GitLocalBranchListResult{}, err
	}
	if err := ensureGitCommandSucceeded("git for-each-ref refs/heads", result); err != nil {
		return GitLocalBranchListResult{}, err
	}
	var currentBranchName *string
	if status.Summary.HeadRefType == HeadRefTypeBranch {
		currentBranchName = status.Summary.BranchName
	}
	return GitLocalBranchListResult{
		HeadRefType:       status.Summary.HeadRefType,
		CurrentBranchName: status.Summary.BranchName,
		Branches:          parseBranchRefRecords(result.Stdout, currentBranchName),
	}, nil
}

func resolutionRepoRoot(resolution GitResolvedRepository) string { return resolution.RepoRoot }

// parseBranchRefRecords ports parseBranchRefRecords.
func parseBranchRefRecords(stdout string, currentBranchName *string) []GitLocalBranch {
	lines := strings.Split(strings.ReplaceAll(stdout, "\r\n", "\n"), "\n")
	branches := make([]GitLocalBranch, 0)
	for _, line := range lines {
		if line == "" {
			continue
		}
		fields := strings.Split(line, "\x00")
		name := lineField(fields, 0)
		if name == "" {
			continue
		}
		branch := GitLocalBranch{
			Name:         name,
			IsCurrent:    currentBranchName != nil && name == *currentBranchName,
			UpstreamName: nullableLineField(fields, 1),
			CommitHash:   nullableLineField(fields, 2),
		}
		if timestamp := lineField(fields, 3); timestamp != "" {
			if seconds, err := strconv.ParseInt(timestamp, 10, 64); err == nil {
				ms := seconds * 1000
				branch.CommitTimestampMs = &ms
			}
		}
		branches = append(branches, branch)
	}
	sort.SliceStable(branches, func(i, j int) bool {
		if branches[i].IsCurrent != branches[j].IsCurrent {
			return branches[i].IsCurrent
		}
		left, right := int64(-1<<62), int64(-1<<62)
		if branches[i].CommitTimestampMs != nil {
			left = *branches[i].CommitTimestampMs
		}
		if branches[j].CommitTimestampMs != nil {
			right = *branches[j].CommitTimestampMs
		}
		if left != right {
			return left > right
		}
		// TS localeCompare is locale-aware; for branch names the common case is
		// plain ordering, and the ordering tie-break only matters for identical
		// timestamps.
		return branches[i].Name < branches[j].Name
	})
	return branches
}

func lineField(fields []string, index int) string {
	if index < len(fields) {
		return fields[index]
	}
	return ""
}

func nullableLineField(fields []string, index int) *string {
	value := lineField(fields, index)
	if value == "" {
		return nil
	}
	return ptr(value)
}

// normalizeGitGraphMaxCount / Skip port the same-named functions.
func normalizeGitGraphMaxCount(maxCount *int) int {
	if maxCount == nil {
		return defaultGitGraphMaxCount
	}
	value := *maxCount
	if value < 1 {
		value = 1
	}
	if value > maxGitGraphMaxCount {
		value = maxGitGraphMaxCount
	}
	return value
}

func normalizeGitGraphSkip(skip *int) int {
	if skip == nil || *skip < 0 {
		return 0
	}
	return *skip
}

// GetCommitGraph ports getCommitGraph. --all 会把 refs/nex/checkpoints 等内部
// hidden refs 拉进 Git Graph，因此限定到 HEAD、分支、标签和远端分支。
func (r *CliRepo) GetCommitGraph(ctx context.Context, workspacePath string, maxCount, skip *int) (GitCommitGraphSnapshot, error) {
	resolution, err := r.ResolveRepository(ctx, workspacePath)
	if err != nil {
		return GitCommitGraphSnapshot{}, err
	}
	if !resolution.IsGitAvailable || !resolution.IsRepository {
		return GitCommitGraphSnapshot{Resolution: resolution, Commits: []GitCommitGraphCommit{}}, nil
	}
	normalizedMaxCount := normalizeGitGraphMaxCount(maxCount)
	normalizedSkip := normalizeGitGraphSkip(skip)
	result, err := r.run(ctx, GitCommandExecutionOptions{
		Cwd: resolution.RepoRoot,
		Args: []string{
			"log",
			"HEAD",
			"--branches",
			"--tags",
			"--remotes",
			"--date-order",
			"--topo-order",
			fmt.Sprintf("--skip=%d", normalizedSkip),
			fmt.Sprintf("--max-count=%d", normalizedMaxCount+1),
			"--format=%H%x00%P%x00%an%x00%at%x00%s%x00%D%x1e",
		},
		TimeoutMs:      DefaultGitCommandTimeoutMs,
		MaxOutputBytes: DefaultGitOutputBytes,
	})
	if err != nil {
		return GitCommitGraphSnapshot{}, err
	}
	if result.ExitCode == nil || *result.ExitCode != 0 {
		stderr := strings.ToLower(result.Stderr)
		if strings.Contains(stderr, "does not have any commits yet") ||
			strings.Contains(stderr, "your current branch") ||
			strings.Contains(stderr, "bad default revision") ||
			strings.Contains(stderr, "ambiguous argument 'head'") {
			return GitCommitGraphSnapshot{Resolution: resolution, Commits: []GitCommitGraphCommit{}}, nil
		}
		if err := ensureGitCommandSucceeded("git log visible refs", result); err != nil {
			return GitCommitGraphSnapshot{}, err
		}
	}
	parsedCommits := parseGitGraphRecords(result.Stdout)
	hasMore := len(parsedCommits) > normalizedMaxCount
	if normalizedMaxCount < len(parsedCommits) {
		parsedCommits = parsedCommits[:normalizedMaxCount]
	}
	return GitCommitGraphSnapshot{
		Resolution: resolution,
		Commits:    parsedCommits,
		HasMore:    hasMore,
	}, nil
}
