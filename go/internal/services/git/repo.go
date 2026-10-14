// repo.go ports packages/services/src/git/repo/gitCliRepo.ts, part 1 (step
// 3.3): repository resolution, workspace info, the status snapshot with
// in-flight deduplication, ignored paths, branches and the commit graph.
//
// 与 TS 一致的关键语义：
//   - repositoryResolutionRequests / workspaceRepositoryInfoRequests /
//     statusRequests 三个 in-flight map：一轮 UI 刷新里并发的 summary/changes/
//     branch 调用合并成一轮 git status；
//   - 未跟踪目录折叠（collapsedUntrackedRepoRoots）在输出超限后按 repoRoot
//     记住，避免每次自动刷新重复执行必然超限的详细命令；
//   - switch/commit/push 等改动后 invalidate 缓存。
package git

import (
	"context"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
)

// GitResolvedRepository ports GitResolvedRepository.
type GitResolvedRepository struct {
	WorkspacePath         string
	RepoRoot              string
	WorkspaceInRepoPath   string
	AutoRefreshWatchPaths []GitRepositoryAutoRefreshWatchPath
	IsGitAvailable        bool
	IsRepository          bool
}

// GitStatusSnapshot ports GitStatusSnapshot.
type GitStatusSnapshot struct {
	Resolution     GitResolvedRepository
	Summary        GitRepositorySummary
	Entries        []GitStatusEntry
	StagedStats    map[string]GitLineStat
	UnstagedStats  map[string]GitLineStat
	UntrackedStats map[string]GitLineStat
}

// GitBranchComparisonChange ports GitBranchComparisonChange.
type GitBranchComparisonChange struct {
	Path         string
	OriginalPath *string
	Kind         GitChangeKind
	Added        int
	Removed      int
}

// GitBranchComparisonSnapshot ports GitBranchComparisonSnapshot.
type GitBranchComparisonSnapshot struct {
	Resolution      GitResolvedRepository
	BaseRef         *string
	HeadRef         *string
	ComparisonLabel *string
	Changes         []GitBranchComparisonChange
}

// GitCommitGraphSnapshot ports GitCommitGraphSnapshot.
type GitCommitGraphSnapshot struct {
	Resolution GitResolvedRepository
	Commits    []GitCommitGraphCommit
	HasMore    bool
}

// createEmptySummary ports createEmptySummary.
func createEmptySummary(resolution GitResolvedRepository) GitRepositorySummary {
	return GitRepositorySummary{
		WorkspacePath:         resolution.WorkspacePath,
		RepoRoot:              resolution.RepoRoot,
		WorkspaceInRepoPath:   resolution.WorkspaceInRepoPath,
		AutoRefreshWatchPaths: resolution.AutoRefreshWatchPaths,
		BranchName:            nil,
		TrackingBranchName:    nil,
		HeadRefType:           HeadRefTypeBranch,
		Ahead:                 0,
		Behind:                0,
		IsDirty:               false,
		IsGitAvailable:        resolution.IsGitAvailable,
		IsRepository:          resolution.IsRepository,
	}
}

// gitOperationMarkers ports GIT_OPERATION_MARKERS.
var gitOperationMarkers = []string{
	"MERGE_HEAD", "CHERRY_PICK_HEAD", "REVERT_HEAD", "REBASE_HEAD",
	"rebase-merge", "rebase-apply", "BISECT_LOG",
}

const (
	defaultGitGraphMaxCount = 100
	maxGitGraphMaxCount     = 200
	gitGraphRecordSeparator = "\x1e"
	gitGraphFieldSeparator  = "\x00"
)

// CliRepo ports GitCliRepo (the interface of createGitCliRepo's return value).
type CliRepo struct {
	provider GitCommandProvider

	mu sync.Mutex
	// In-flight request dedup caches, keyed by workspacePath.
	resolutionRequests map[string]*resolutionCall
	infoRequests       map[string]*infoCall
	statusRequests     map[string]*statusCall
	collapsedUntracked map[string]bool
}

type resolutionCall struct {
	once sync.Once
	wg   sync.WaitGroup
	res  GitResolvedRepository
}

type infoCall struct {
	once sync.Once
	wg   sync.WaitGroup
	res  GitWorkspaceRepositoryInfo
}

type statusCall struct {
	once sync.Once
	wg   sync.WaitGroup
	res  GitStatusSnapshot
}

// NewCliRepo ports createGitCliRepo.
func NewCliRepo(provider GitCommandProvider) *CliRepo {
	if provider == nil {
		provider = NewCommandProvider(CommandProviderOptions{})
	}
	return &CliRepo{
		provider:           provider,
		resolutionRequests: map[string]*resolutionCall{},
		infoRequests:       map[string]*infoCall{},
		statusRequests:     map[string]*statusCall{},
		collapsedUntracked: map[string]bool{},
	}
}

func (r *CliRepo) run(ctx context.Context, options GitCommandExecutionOptions) (GitCommandExecutionResult, error) {
	return r.provider.Run(ctx, options)
}

// invalidate ports invalidate.
func (r *CliRepo) invalidate(workspacePath string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.resolutionRequests, workspacePath)
	delete(r.infoRequests, workspacePath)
	delete(r.statusRequests, workspacePath)
}

// normalizeWatchPath ports normalizeWatchPath.
func normalizeWatchPath(path string) string {
	trimmed := strings.TrimSpace(path)
	if trimmed == "/" || regexp.MustCompile(`^[A-Za-z]:[\\/]?$`).MatchString(trimmed) {
		return trimmed
	}
	return regexp.MustCompile(`[\\/]+$`).ReplaceAllString(trimmed, "")
}

var (
	driveLetterRe   = regexp.MustCompile(`^[A-Za-z]:[\\/]?$`)
	trailingSlashRe = regexp.MustCompile(`[\\/]+$`)
)

func normalizeWatchPathCompiled(path string) string {
	trimmed := strings.TrimSpace(path)
	if trimmed == "/" || driveLetterRe.MatchString(trimmed) {
		return trimmed
	}
	return trailingSlashRe.ReplaceAllString(trimmed, "")
}

// addAutoRefreshWatchPath ports addAutoRefreshWatchPath.
func addAutoRefreshWatchPath(paths []GitRepositoryAutoRefreshWatchPath, path string, recursive bool) []GitRepositoryAutoRefreshWatchPath {
	normalizedPath := normalizeWatchPathCompiled(path)
	if normalizedPath == "" {
		return paths
	}
	for _, entry := range paths {
		if entry.Path == normalizedPath {
			return paths
		}
	}
	return append(paths, GitRepositoryAutoRefreshWatchPath{Path: normalizedPath, Recursive: recursive})
}

// buildAutoRefreshWatchPaths ports buildAutoRefreshWatchPaths. Git 元数据可能
// 在 linked worktree 或 separate git-dir 中位于 repoRoot 之外；用 Git 自身解析
// 出的目录作为刷新边界。
func buildAutoRefreshWatchPaths(workspacePath, absoluteGitDir, gitCommonDir string) []GitRepositoryAutoRefreshWatchPath {
	paths := make([]GitRepositoryAutoRefreshWatchPath, 0, 2)
	paths = addAutoRefreshWatchPath(paths, absoluteGitDir, true)
	resolvedCommonDir := absoluteGitDir
	if gitCommonDir != "" {
		if filepath.IsAbs(gitCommonDir) {
			resolvedCommonDir = gitCommonDir
		} else {
			// `git rev-parse --git-common-dir` 的相对结果以命令 cwd 为基准，
			// 子目录 workspace 若误用 repoRoot 会把 `/root` + `../.git` 解析成 `/.git`。
			resolvedCommonDir = filepath.Join(workspacePath, gitCommonDir)
		}
	}
	paths = addAutoRefreshWatchPath(paths, resolvedCommonDir, true)
	return paths
}

// unavailableResolution is the shared "not a repo" resolution shape.
func unavailableResolution(workspacePath string, gitAvailable bool) GitResolvedRepository {
	return GitResolvedRepository{
		WorkspacePath:       workspacePath,
		RepoRoot:            workspacePath,
		WorkspaceInRepoPath: ".",
		IsGitAvailable:      gitAvailable,
		IsRepository:        false,
	}
}

// ResolveRepository ports resolveRepository.
func (r *CliRepo) GetIgnoredPaths(ctx context.Context, workspacePath string, paths []string) ([]string, error) {
	if len(paths) == 0 {
		return []string{}, nil
	}
	resolution, err := r.ResolveRepository(ctx, workspacePath)
	if err != nil {
		return nil, err
	}
	if !resolution.IsGitAvailable || !resolution.IsRepository {
		return []string{}, nil
	}
	type inputPair struct {
		absolutePath     string
		repoRelativePath string
	}
	var validPairs []inputPair
	for _, path := range paths {
		absolutePath := path
		if !filepath.IsAbs(path) {
			absolutePath = filepath.Join(resolution.WorkspacePath, strings.ReplaceAll(path, "/", string(filepath.Separator)))
		}
		repoRelativePath, err := normalizeInputPath(resolution, path)
		if err != nil {
			continue
		}
		validPairs = append(validPairs, inputPair{absolutePath: absolutePath, repoRelativePath: repoRelativePath})
	}
	if len(validPairs) == 0 {
		return []string{}, nil
	}
	args := []string{"check-ignore", "--"}
	for _, pair := range validPairs {
		args = append(args, pair.repoRelativePath)
	}
	ignoredResult, err := r.run(ctx, GitCommandExecutionOptions{
		Cwd:            resolution.RepoRoot,
		Args:           args,
		TimeoutMs:      DefaultGitCommandTimeoutMs,
		MaxOutputBytes: DefaultGitOutputBytes,
	})
	if err != nil {
		return nil, err
	}
	if ignoredResult.ExitCode != nil && *ignoredResult.ExitCode == 1 {
		return []string{}, nil
	}
	if err := ensureGitCommandSucceeded("git check-ignore", ignoredResult); err != nil {
		return nil, err
	}
	ignored := map[string]bool{}
	for _, line := range regexp.MustCompile(`\r?\n`).Split(ignoredResult.Stdout, -1) {
		if line == "" {
			continue
		}
		ignored[strings.ReplaceAll(line, "\\", "/")] = true
	}
	var out []string
	for _, pair := range validPairs {
		if ignored[pair.repoRelativePath] {
			out = append(out, pair.absolutePath)
		}
	}
	if out == nil {
		out = []string{}
	}
	return out, nil
}

// ---------------------------------------------------------------------------
// branches and graph
// ---------------------------------------------------------------------------

// ListLocalBranches ports listLocalBranches.
