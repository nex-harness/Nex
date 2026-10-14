// service_generate.go ports the GenerateCommitMessage and Refresh methods of
// gitService.ts (step 3.3): the commit-message generator plumbing and the
// refresh fan-out. Split out of service.go to stay under the 400-line
// archcheck limit.
package git

import (
	"context"
	"errors"
	"sync"
)

func (s *Service) GenerateCommitMessage(ctx context.Context, params GenerateCommitMessageRequest) (GitGenerateCommitMessageResult, error) {
	generator := s.commitGenerator()
	if generator == nil {
		return GitGenerateCommitMessageResult{}, errors.New("Commit message generation is not available.")
	}
	// TS: `(params as { includeUnstaged?: boolean }).includeUnstaged ?? true` —
	// absent means true; only an explicit false excludes unstaged changes.
	includeUnstaged := true
	if params.IncludeUnstagedSet {
		includeUnstaged = params.IncludeUnstaged
	}
	var unstagedChanges []GitFileChange
	var err error
	if includeUnstaged {
		if unstagedChanges, err = s.GetChanges(ctx, params.WorkspacePath, SourceUnstaged); err != nil {
			return GitGenerateCommitMessageResult{}, err
		}
	}
	stagedChanges, err := s.GetChanges(ctx, params.WorkspacePath, SourceStaged)
	if err != nil {
		return GitGenerateCommitMessageResult{}, err
	}
	files := append(append([]GitFileChange{}, unstagedChanges...), stagedChanges...)
	status, err := s.repo.GetStatus(ctx, params.WorkspacePath)
	if err != nil {
		return GitGenerateCommitMessageResult{}, err
	}
	files = FilterCommitMessageFilesByCurrentSession(files, params.WorkspacePath, status.Resolution.RepoRoot, status.Resolution.WorkspaceInRepoPath, params.CurrentSessionFilePaths)
	if len(files) == 0 {
		return GitGenerateCommitMessageResult{}, errors.New("There are no changes available to commit.")
	}

	// 与 TS 的 Promise.allSettled 一致：单个 diff 失败不阻塞整体，只是被丢弃。
	type diffOutcome struct {
		result GitDiffResult
		failed bool
	}
	queries := getCommitMessageDiffQueries(files, includeUnstaged)
	outcomes := make([]diffOutcome, len(queries))
	var wg sync.WaitGroup
	for i, query := range queries {
		wg.Add(1)
		go func(i int, query struct {
			path     string
			sourceId GitChangeSourceId
		}) {
			defer wg.Done()
			result, err := s.repo.GetDiff(ctx, GetDiffParams{
				WorkspacePath: params.WorkspacePath,
				Path:          query.path,
				SourceID:      &query.sourceId,
			})
			if err != nil {
				outcomes[i] = diffOutcome{failed: true}
				return
			}
			outcomes[i] = diffOutcome{result: result}
		}(i, query)
	}
	wg.Wait()
	var diffs []GitDiffResult
	for _, outcome := range outcomes {
		if outcome.failed {
			continue
		}
		if (outcome.result.Patch != nil && *outcome.result.Patch != "") || outcome.result.Summary != nil {
			diffs = append(diffs, outcome.result)
		}
	}
	return generator.Generate(ctx, GenerateParams{
		WorkspacePath:       params.WorkspacePath,
		WorkspaceIdentity:   params.WorkspaceIdentity,
		Locale:              params.Locale,
		BranchName:          status.Summary.BranchName,
		Files:               files,
		Diffs:               diffs,
		ConversationContext: params.ConversationContext,
	})
}

func getCommitMessageDiffQueries(files []GitFileChange, includeUnstaged bool) []struct {
	path     string
	sourceId GitChangeSourceId
} {
	type query = struct {
		path     string
		sourceId GitChangeSourceId
	}
	var queries []query
	limit := len(files)
	if limit > commitMessageDiffFileLimit {
		limit = commitMessageDiffFileLimit
	}
	for _, file := range files[:limit] {
		if file.Section == SectionStaged {
			queries = append(queries, query{path: file.Path, sourceId: SourceStaged})
			continue
		}
		if !includeUnstaged {
			continue
		}
		queries = append(queries, query{path: file.Path, sourceId: SourceUnstaged})
	}
	return queries
}

// CommitRequest is GitCommitRequest.
type CommitRequest struct {
	WorkspacePath string
	Message       string
	Paths         []string
	StagedOnly    bool
}

// Commit ports commit.
func (s *Service) Commit(ctx context.Context, params CommitRequest) (GitCommitResult, error) {
	commitHash, err := s.repo.Commit(ctx, params.WorkspacePath, params.Message, params.Paths, CommitOptions{StagedOnly: params.StagedOnly})
	if err != nil {
		return GitCommitResult{}, err
	}
	status, err := s.repo.GetStatus(ctx, params.WorkspacePath)
	if err != nil {
		return GitCommitResult{}, err
	}
	return GitCommitResult{CommitHash: commitHash, Summary: status.Summary}, nil
}

// Push ports push.
func (s *Service) Push(ctx context.Context, workspacePath string) (GitPushResult, error) {
	return s.repo.Push(ctx, workspacePath)
}

// GetIdentity ports getIdentity.
func (s *Service) GetIdentity(ctx context.Context, workspacePath string) (GitIdentity, error) {
	return s.repo.GetIdentity(ctx, workspacePath)
}

// RefreshRequest is GitRefreshRequest.
type RefreshRequest struct {
	WorkspacePath           string
	IncludeIdentity         bool
	IncludeBranchComparison bool
}

// Refresh ports refresh: UI 的自动刷新原本会并发调用 summary、unstaged、staged
// 三个 RPC，每个都重新执行一次 git status。refresh 在一次 status 快照里切出三份
// 数据。可选的 identity / branchComparison 并行取。
func (s *Service) Refresh(ctx context.Context, params RefreshRequest) (GitRefreshResult, error) {
	var (
		status        GitStatusSnapshot
		identity      GitIdentity
		comparison    GitBranchComparisonSnapshot
		errStatus     error
		errIdentity   error
		errComparison error
		wg            sync.WaitGroup
	)
	wg.Add(1)
	go func() {
		defer wg.Done()
		status, errStatus = s.repo.GetStatus(ctx, params.WorkspacePath)
	}()
	if params.IncludeIdentity {
		wg.Add(1)
		go func() {
			defer wg.Done()
			identity, errIdentity = s.repo.GetIdentity(ctx, params.WorkspacePath)
		}()
	}
	if params.IncludeBranchComparison {
		wg.Add(1)
		go func() {
			defer wg.Done()
			comparison, errComparison = s.repo.GetBranchComparison(ctx, params.WorkspacePath)
		}()
	}
	wg.Wait()
	if errStatus != nil {
		return GitRefreshResult{}, errStatus
	}
	if errIdentity != nil {
		return GitRefreshResult{}, errIdentity
	}
	if errComparison != nil {
		return GitRefreshResult{}, errComparison
	}
	var identityPtr *GitIdentity
	if params.IncludeIdentity {
		identityPtr = &identity
	}
	var branchComparison *GitBranchComparison
	if params.IncludeBranchComparison {
		changes := make([]GitFileChange, 0, len(comparison.Changes))
		for _, change := range comparison.Changes {
			if converted, ok := toBranchComparisonChange(comparison, change); ok {
				changes = append(changes, converted)
			}
		}
		branchComparison = &GitBranchComparison{
			BaseRef:         comparison.BaseRef,
			HeadRef:         comparison.HeadRef,
			ComparisonLabel: comparison.ComparisonLabel,
			Changes:         changes,
		}
	}
	return GitRefreshResult{
		Summary:          status.Summary,
		Identity:         identityPtr,
		UnstagedChanges:  getChangesForSource(status, SourceUnstaged),
		StagedChanges:    getChangesForSource(status, SourceStaged),
		BranchComparison: branchComparison,
	}, nil
}

// ---------------------------------------------------------------------------
// snapshot → GitFileChange (gitService.ts's private helpers)
// ---------------------------------------------------------------------------

// toAbsolutePath ports toAbsolutePath.
