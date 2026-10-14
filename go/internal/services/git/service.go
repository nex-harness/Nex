// service.go ports packages/services/src/git/gitService.ts (step 3.3): the
// IGitService surface. workspace 可以是 monorepo 子目录，service 层统一按作用
// 域裁剪；repo 层继续只负责"把 Git 原始状态解析出来"。
//
// generateCommitMessage 依赖 Phase-1 的 model client + modelSelection（step
// 3.6 才有 Go 实现）。与 TS 的 `if (!options?.commitMessageGenerator) throw
// new Error("Commit message generation is not available.")` 一致：未注入
// generator 时返回同一个错误信息，路由层把该方法留在 Node。
package git

import (
	"context"
	"path/filepath"
	"strings"
	"sync"
)

// CommitMessageGenerator is the GitCommitMessageGenerator seam: the model call
// stays behind this interface until step 3.6 ports modelSelection.
type CommitMessageGenerator interface {
	Generate(ctx context.Context, params GenerateParams) (GitGenerateCommitMessageResult, error)
}

// GenerateParams is gitService's call into the generator.
type GenerateParams struct {
	WorkspacePath       string
	WorkspaceIdentity   string
	Locale              string
	BranchName          *string
	Files               []GitFileChange
	Diffs               []GitDiffResult
	ConversationContext *GitCommitMessageConversationContext
}

// GenerateCommitMessageRequest is GitGenerateCommitMessageRequest.
// IncludeUnstaged is `includeUnstaged?: boolean` in the TS (undefined = true),
// so the tri-state is carried by IncludeUnstagedSet.
type GenerateCommitMessageRequest struct {
	WorkspacePath           string
	WorkspaceIdentity       string
	Locale                  string
	IncludeUnstaged         bool
	IncludeUnstagedSet      bool
	CurrentSessionFilePaths []string
	ConversationContext     *GitCommitMessageConversationContext
}

// Service is the IGitService implementation.
type Service struct {
	repo        *CliRepo
	generator   CommitMessageGenerator
	generatorMu sync.RWMutex
}

// NewService ports createGitService.
func NewService(repo *CliRepo, generator CommitMessageGenerator) *Service {
	if repo == nil {
		repo = NewCliRepo(nil)
	}
	return &Service{repo: repo, generator: generator}
}

// SetCommitMessageGenerator lets a later step (3.6) wire the generator
// without rebuilding the service; the TS replaces the whole instance instead.
func (s *Service) SetCommitMessageGenerator(generator CommitMessageGenerator) {
	s.generatorMu.Lock()
	defer s.generatorMu.Unlock()
	s.generator = generator
}

func (s *Service) commitGenerator() CommitMessageGenerator {
	s.generatorMu.RLock()
	defer s.generatorMu.RUnlock()
	return s.generator
}

// GetRepositorySummary ports getRepositorySummary.
func (s *Service) GetRepositorySummary(ctx context.Context, workspacePath string) (GitRepositorySummary, error) {
	status, err := s.repo.GetStatus(ctx, workspacePath)
	if err != nil {
		return GitRepositorySummary{}, err
	}
	return status.Summary, nil
}

// GetWorkspaceRepositoryInfo ports getWorkspaceRepositoryInfo.
func (s *Service) GetWorkspaceRepositoryInfo(ctx context.Context, workspacePath string) (GitWorkspaceRepositoryInfo, error) {
	return s.repo.GetWorkspaceRepositoryInfo(ctx, workspacePath)
}

// GetLocalBranches ports getLocalBranches.
func (s *Service) GetLocalBranches(ctx context.Context, workspacePath string) (GitLocalBranchListResult, error) {
	return s.repo.ListLocalBranches(ctx, workspacePath)
}

// GetCommitGraph ports getCommitGraph.
func (s *Service) GetCommitGraph(ctx context.Context, workspacePath string, maxCount, skip *int) (GitCommitGraphResult, error) {
	snapshot, err := s.repo.GetCommitGraph(ctx, workspacePath, maxCount, skip)
	if err != nil {
		return GitCommitGraphResult{}, err
	}
	if snapshot.Commits == nil {
		snapshot.Commits = []GitCommitGraphCommit{}
	}
	return GitCommitGraphResult{Commits: snapshot.Commits, HasMore: snapshot.HasMore}, nil
}

// SwitchBranch ports switchBranch.
func (s *Service) SwitchBranch(ctx context.Context, workspacePath, targetBranchName string) (GitBranchMutationResult, error) {
	return s.repo.SwitchBranch(ctx, workspacePath, targetBranchName)
}

// CreateBranchAndSwitch ports createBranchAndSwitch.
func (s *Service) CreateBranchAndSwitch(ctx context.Context, workspacePath, branchName string, startPoint *string) (GitBranchMutationResult, error) {
	return s.repo.CreateBranchAndSwitch(ctx, workspacePath, branchName, startPoint)
}

// GetChanges ports getChanges.
func (s *Service) GetChanges(ctx context.Context, workspacePath string, sourceId GitChangeSourceId) ([]GitFileChange, error) {
	status, err := s.repo.GetStatus(ctx, workspacePath)
	if err != nil {
		return nil, err
	}
	return getChangesForSource(status, sourceId), nil
}

// GetIgnoredPaths ports getIgnoredPaths.
func (s *Service) GetIgnoredPaths(ctx context.Context, workspacePath string, paths []string) ([]string, error) {
	return s.repo.GetIgnoredPaths(ctx, workspacePath, paths)
}

// GetDiffParams is the service-level diff query (GitDiffQuery).
type GetDiffParams2 = GetDiffParams

// GetDiff ports getDiff.
func (s *Service) GetDiff(ctx context.Context, params GetDiffParams) (GitDiffResult, error) {
	return s.repo.GetDiff(ctx, params)
}

// GetBranchComparison ports getBranchComparison.
func (s *Service) GetBranchComparison(ctx context.Context, workspacePath string) (GitBranchComparison, error) {
	snapshot, err := s.repo.GetBranchComparison(ctx, workspacePath)
	if err != nil {
		return GitBranchComparison{}, err
	}
	changes := make([]GitFileChange, 0, len(snapshot.Changes))
	for _, change := range snapshot.Changes {
		if converted, ok := toBranchComparisonChange(snapshot, change); ok {
			changes = append(changes, converted)
		}
	}
	return GitBranchComparison{
		BaseRef:         snapshot.BaseRef,
		HeadRef:         snapshot.HeadRef,
		ComparisonLabel: snapshot.ComparisonLabel,
		Changes:         changes,
	}, nil
}

// StagePaths / UnstagePaths / DiscardPaths port the mutations.
func (s *Service) StagePaths(ctx context.Context, workspacePath string, paths []string) error {
	return s.repo.Stage(ctx, workspacePath, paths)
}

func (s *Service) UnstagePaths(ctx context.Context, workspacePath string, paths []string) error {
	return s.repo.Unstage(ctx, workspacePath, paths)
}

func (s *Service) DiscardPaths(ctx context.Context, workspacePath string, paths []string, staged bool) error {
	return s.repo.Discard(ctx, workspacePath, paths, staged)
}

const commitMessageDiffFileLimit = 8

// GenerateCommitMessage ports generateCommitMessage.
func toAbsolutePath(repoRoot, repoRelativePath string) string {
	parts := strings.Split(NormalizeGitPath(repoRelativePath), "/")
	return filepath.Join(append([]string{repoRoot}, parts...)...)
}

// matchesWorkspaceScope ports matchesWorkspaceScope.
func matchesWorkspaceScope(workspaceInRepoPath string, entry GitStatusEntry) bool {
	if IsPathInWorkspaceScope(entry.Path, workspaceInRepoPath) {
		return true
	}
	return entry.OriginalPath != nil && IsPathInWorkspaceScope(*entry.OriginalPath, workspaceInRepoPath)
}

// buildFileChange ports buildFileChange.
func buildFileChange(snapshot GitStatusSnapshot, entry GitStatusEntry, section GitChangeSectionId, added, removed int) GitFileChange {
	change := GitFileChange{
		Path:                  toAbsolutePath(snapshot.Resolution.RepoRoot, entry.Path),
		RepoRelativePath:      entry.Path,
		WorkspaceRelativePath: ToWorkspaceRelativeGitPath(entry.Path, snapshot.Summary.WorkspaceInRepoPath),
		Kind:                  entry.Kind,
		Section:               section,
		Added:                 added,
		Removed:               removed,
		IsStaged:              section == SectionStaged,
		IsUntracked:           section == SectionUntracked,
		IsConflicted:          section == SectionConflicted,
	}
	if entry.X != nil {
		change.X = *entry.X
	}
	if entry.Y != nil {
		change.Y = *entry.Y
	}
	return change
}

// shouldIncludeUntrackedChange ports shouldIncludeUntrackedChange: 构建目录里
// 常见大量未跟踪的空文件/失效链接，没有可审查的文本增删，之前会以 +0/-0 挤进
// Review 面板。但目录记录（末尾 `/`）必须保留为可 stage/discard 的降级入口。
func shouldIncludeUntrackedChange(entry GitStatusEntry, stat GitLineStat) bool {
	return strings.HasSuffix(entry.Path, "/") || stat.Added > 0 || stat.Removed > 0
}

func lineStatOrZero(stats map[string]GitLineStat, path string) GitLineStat {
	if stat, ok := stats[path]; ok {
		return stat
	}
	return GitLineStat{}
}

// toChangeForSource ports toChangeForSource.
func toChangeForSource(snapshot GitStatusSnapshot, entry GitStatusEntry, sourceId GitChangeSourceId) (GitFileChange, bool) {
	if !matchesWorkspaceScope(snapshot.Summary.WorkspaceInRepoPath, entry) {
		return GitFileChange{}, false
	}
	if sourceId == SourceStaged {
		if entry.IsUntracked || entry.IsConflicted || entry.X == nil || *entry.X == "." {
			return GitFileChange{}, false
		}
		stat := lineStatOrZero(snapshot.StagedStats, entry.Path)
		return buildFileChange(snapshot, entry, SectionStaged, stat.Added, stat.Removed), true
	}
	if entry.IsConflicted {
		return buildFileChange(snapshot, entry, SectionConflicted, 0, 0), true
	}
	if entry.IsUntracked {
		stat := lineStatOrZero(snapshot.UntrackedStats, entry.Path)
		if !shouldIncludeUntrackedChange(entry, stat) {
			return GitFileChange{}, false
		}
		return buildFileChange(snapshot, entry, SectionUntracked, stat.Added, stat.Removed), true
	}
	if entry.Y == nil || *entry.Y == "." {
		return GitFileChange{}, false
	}
	stat := lineStatOrZero(snapshot.UnstagedStats, entry.Path)
	return buildFileChange(snapshot, entry, SectionUnstaged, stat.Added, stat.Removed), true
}

// getChangesForSource ports getChangesForSource.
func getChangesForSource(snapshot GitStatusSnapshot, sourceId GitChangeSourceId) []GitFileChange {
	out := make([]GitFileChange, 0, len(snapshot.Entries))
	for _, entry := range snapshot.Entries {
		if change, ok := toChangeForSource(snapshot, entry, sourceId); ok {
			out = append(out, change)
		}
	}
	return out
}

// toBranchComparisonChange ports toBranchComparisonChange.
func toBranchComparisonChange(snapshot GitBranchComparisonSnapshot, change GitBranchComparisonChange) (GitFileChange, bool) {
	if !IsPathInWorkspaceScope(change.Path, snapshot.Resolution.WorkspaceInRepoPath) &&
		!(change.OriginalPath != nil && IsPathInWorkspaceScope(*change.OriginalPath, snapshot.Resolution.WorkspaceInRepoPath)) {
		return GitFileChange{}, false
	}
	return GitFileChange{
		Path:                  toAbsolutePath(snapshot.Resolution.RepoRoot, change.Path),
		RepoRelativePath:      change.Path,
		WorkspaceRelativePath: ToWorkspaceRelativeGitPath(change.Path, snapshot.Resolution.WorkspaceInRepoPath),
		Kind:                  change.Kind,
		Section:               SectionBranch,
		Added:                 change.Added,
		Removed:               change.Removed,
		IsStaged:              false,
		IsUntracked:           false,
		IsConflicted:          false,
	}, true
}
