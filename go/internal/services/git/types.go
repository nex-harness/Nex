// types.go is the Go transcription of the git types in
// packages/shared/src/git.ts — the shapes both the git and gitcheckpoint
// services exchange over RPC. JSON tags match the TS field names exactly; the
// TS's `x?: string` optional fields use pointer-free strings with omitempty
// only where the TS serializes them as absent (undefined), not null.
package git

// GitHeadRefType is GitHeadRefType: "branch" | "detached".
type GitHeadRefType string

const (
	HeadRefTypeBranch   GitHeadRefType = "branch"
	HeadRefTypeDetached GitHeadRefType = "detached"
)

// GitChangeKind is GitChangeKind.
type GitChangeKind string

const (
	ChangeKindModified GitChangeKind = "modified"
	ChangeKindAdded    GitChangeKind = "added"
	ChangeKindDeleted  GitChangeKind = "deleted"
	ChangeKindRenamed  GitChangeKind = "renamed"
)

// GitChangeSectionId is GitChangeSectionId.
type GitChangeSectionId string

const (
	SectionStaged     GitChangeSectionId = "staged"
	SectionUnstaged   GitChangeSectionId = "unstaged"
	SectionUntracked  GitChangeSectionId = "untracked"
	SectionConflicted GitChangeSectionId = "conflicted"
	SectionBranch     GitChangeSectionId = "branch"
	SectionLastTurn   GitChangeSectionId = "last-turn"
)

// GitChangeSourceId is GitChangeSourceId.
type GitChangeSourceId string

const (
	SourceUnstaged GitChangeSourceId = "unstaged"
	SourceStaged   GitChangeSourceId = "staged"
	SourceBranch   GitChangeSourceId = "branch"
	SourceLastTurn GitChangeSourceId = "last-turn"
)

// GitDiffAvailability is GitDiffAvailability.
type GitDiffAvailability string

const (
	DiffAvailabilityPatch       GitDiffAvailability = "patch"
	DiffAvailabilityBinary      GitDiffAvailability = "binary"
	DiffAvailabilityTruncated   GitDiffAvailability = "truncated"
	DiffAvailabilityUnavailable GitDiffAvailability = "unavailable"
)

// GitRepositoryAutoRefreshWatchPath is GitRepositoryAutoRefreshWatchPath.
type GitRepositoryAutoRefreshWatchPath struct {
	Path      string `json:"path"`
	Recursive bool   `json:"recursive"`
}

// GitRepositorySummary is GitRepositorySummary. Nullable fields use pointers so
// the RPC payload carries JSON null exactly like the TS.
type GitRepositorySummary struct {
	WorkspacePath         string                              `json:"workspacePath"`
	RepoRoot              string                              `json:"repoRoot"`
	WorkspaceInRepoPath   string                              `json:"workspaceInRepoPath"`
	AutoRefreshWatchPaths []GitRepositoryAutoRefreshWatchPath `json:"autoRefreshWatchPaths"`
	BranchName            *string                             `json:"branchName"`
	TrackingBranchName    *string                             `json:"trackingBranchName"`
	HeadRefType           GitHeadRefType                      `json:"headRefType"`
	Ahead                 int                                 `json:"ahead"`
	Behind                int                                 `json:"behind"`
	IsDirty               bool                                `json:"isDirty"`
	IsGitAvailable        bool                                `json:"isGitAvailable"`
	IsRepository          bool                                `json:"isRepository"`
}

// GitFileChange is GitFileChange. X/Y are the TS's optional `x?: string` — the
// TS service copies `entry.x ?? undefined`, so absent means the KEY is absent
// on the wire, which is what omitempty on a string gives.
type GitFileChange struct {
	Path                  string             `json:"path"`
	RepoRelativePath      string             `json:"repoRelativePath"`
	WorkspaceRelativePath string             `json:"workspaceRelativePath"`
	X                     string             `json:"x,omitempty"`
	Y                     string             `json:"y,omitempty"`
	Kind                  GitChangeKind      `json:"kind"`
	Section               GitChangeSectionId `json:"section"`
	Added                 int                `json:"added"`
	Removed               int                `json:"removed"`
	IsStaged              bool               `json:"isStaged"`
	IsUntracked           bool               `json:"isUntracked"`
	IsConflicted          bool               `json:"isConflicted"`
}

// GitDiffResult is GitDiffResult.
type GitDiffResult struct {
	Path          string              `json:"path"`
	Availability  GitDiffAvailability `json:"availability"`
	Patch         *string             `json:"patch"`
	BeforeContent *string             `json:"beforeContent"`
	AfterContent  *string             `json:"afterContent"`
	// Summary is `summary?: string | null` — the TS always includes it on the
	// error paths and omits the success path only in buildUntrackedTextDiffResult
	// (summary: null); keep it always-present with omitempty for the absent case.
	Summary *string `json:"summary,omitempty"`
}

// GitIdentity is GitIdentity.
type GitIdentity struct {
	UserName    *string `json:"userName"`
	UserEmail   *string `json:"userEmail"`
	NameSource  *string `json:"nameSource"`
	EmailSource *string `json:"emailSource"`
	// ScopeLabel is `scopeLabel?: string | null`.
	ScopeLabel *string `json:"scopeLabel,omitempty"`
}

// GitWorkspaceRepositoryKind is GitWorkspaceRepositoryKind.
type GitWorkspaceRepositoryKind string

const (
	WorkspaceRepoKindNotRepository  GitWorkspaceRepositoryKind = "not-repository"
	WorkspaceRepoKindMainTree       GitWorkspaceRepositoryKind = "main-tree"
	WorkspaceRepoKindLinkedWorktree GitWorkspaceRepositoryKind = "linked-worktree"
)

// GitWorkspaceRepositoryInfo is GitWorkspaceRepositoryInfo.
type GitWorkspaceRepositoryInfo struct {
	WorkspacePath  string                     `json:"workspacePath"`
	Kind           GitWorkspaceRepositoryKind `json:"kind"`
	IsGitAvailable bool                       `json:"isGitAvailable"`
}

// GitCommitGraphRefKind is GitCommitGraphRefKind.
type GitCommitGraphRefKind string

const (
	GraphRefKindBranch GitCommitGraphRefKind = "branch"
	GraphRefKindRemote GitCommitGraphRefKind = "remote"
	GraphRefKindTag    GitCommitGraphRefKind = "tag"
	GraphRefKindHead   GitCommitGraphRefKind = "head"
)

// GitCommitGraphRef is GitCommitGraphRef.
type GitCommitGraphRef struct {
	Name string                `json:"name"`
	Kind GitCommitGraphRefKind `json:"kind"`
}

// GitCommitGraphCommit is GitCommitGraphCommit.
type GitCommitGraphCommit struct {
	Hash         string              `json:"hash"`
	Parents      []string            `json:"parents"`
	Refs         []GitCommitGraphRef `json:"refs"`
	Subject      string              `json:"subject"`
	AuthorName   *string             `json:"authorName"`
	AuthoredAtMs *int64              `json:"authoredAtMs"`
}

// GitCommitGraphResult is GitCommitGraphResult.
type GitCommitGraphResult struct {
	Commits []GitCommitGraphCommit `json:"commits"`
	HasMore bool                   `json:"hasMore"`
}

// GitBranchComparison is GitBranchComparison.
type GitBranchComparison struct {
	BaseRef         *string         `json:"baseRef"`
	HeadRef         *string         `json:"headRef"`
	ComparisonLabel *string         `json:"comparisonLabel"`
	Changes         []GitFileChange `json:"changes"`
}

// GitLocalBranch is GitLocalBranch.
type GitLocalBranch struct {
	Name              string  `json:"name"`
	IsCurrent         bool    `json:"isCurrent"`
	UpstreamName      *string `json:"upstreamName"`
	CommitHash        *string `json:"commitHash"`
	CommitTimestampMs *int64  `json:"commitTimestampMs"`
}

// GitLocalBranchListResult is GitLocalBranchListResult.
type GitLocalBranchListResult struct {
	HeadRefType       GitHeadRefType   `json:"headRefType"`
	CurrentBranchName *string          `json:"currentBranchName"`
	Branches          []GitLocalBranch `json:"branches"`
}

// GitBranchMutationAction is GitBranchMutationAction.
type GitBranchMutationAction string

const (
	MutationActionSwitch          GitBranchMutationAction = "switch"
	MutationActionCreateAndSwitch GitBranchMutationAction = "create-and-switch"
)

// GitBranchMutationIssue is GitBranchMutationIssue.
type GitBranchMutationIssue struct {
	Code    string `json:"code"`
	Message string `json:"message"`
	// Paths is `paths?: string[]`; Detail is `detail?: string | null`.
	Paths  []string `json:"paths,omitempty"`
	Detail *string  `json:"detail,omitempty"`
}

// GitBranchMutationResult is GitBranchMutationResult.
type GitBranchMutationResult struct {
	OK         bool                     `json:"ok"`
	Action     GitBranchMutationAction  `json:"action"`
	BranchName *string                  `json:"branchName"`
	DidChange  bool                     `json:"didChange"`
	Created    bool                     `json:"created"`
	Summary    GitRepositorySummary     `json:"summary"`
	Issues     []GitBranchMutationIssue `json:"issues"`
}

// GitCommitResult is GitCommitResult.
type GitCommitResult struct {
	CommitHash string               `json:"commitHash"`
	Summary    GitRepositorySummary `json:"summary"`
}

// GitCommitMessageConversationMessage is the context message shape.
type GitCommitMessageConversationMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

// GitCommitMessageConversationContext is GitCommitMessageConversationContext.
type GitCommitMessageConversationContext struct {
	SessionID           string                                `json:"sessionId,omitempty"`
	OmittedMessageCount int                                   `json:"omittedMessageCount,omitempty"`
	Messages            []GitCommitMessageConversationMessage `json:"messages"`
}

// GitGenerateCommitMessageResult is GitGenerateCommitMessageResult.
type GitGenerateCommitMessageResult struct {
	Message    string `json:"message"`
	ProviderID string `json:"providerId"`
	Model      string `json:"model"`
}

// GitPushResult is GitPushResult.
type GitPushResult struct {
	BranchName         *string              `json:"branchName"`
	TrackingBranchName *string              `json:"trackingBranchName"`
	RemoteName         *string              `json:"remoteName"`
	SetUpstream        bool                 `json:"setUpstream"`
	Summary            GitRepositorySummary `json:"summary"`
}

// GitRefreshResult is GitRefreshResult.
type GitRefreshResult struct {
	Summary          GitRepositorySummary `json:"summary"`
	Identity         *GitIdentity         `json:"identity"`
	UnstagedChanges  []GitFileChange      `json:"unstagedChanges"`
	StagedChanges    []GitFileChange      `json:"stagedChanges"`
	BranchComparison *GitBranchComparison `json:"branchComparison"`
}

// ---------------------------------------------------------------------------
// checkpoint types
// ---------------------------------------------------------------------------

// GitCheckpointMeta is GitCheckpointMeta.
type GitCheckpointMeta struct {
	CheckpointID        string `json:"checkpointId"`
	WorkspacePath       string `json:"workspacePath"`
	RepoRoot            string `json:"repoRoot"`
	WorkspaceInRepoPath string `json:"workspaceInRepoPath"`
	CreatedAt           int64  `json:"createdAt"`
	RefName             string `json:"refName"`
	CommitOid           string `json:"commitOid"`
	Scope               string `json:"scope"`
}

// GitCheckpointFileDiff is GitCheckpointFileDiff.
type GitCheckpointFileDiff struct {
	Path                  string        `json:"path"`
	RepoRelativePath      string        `json:"repoRelativePath"`
	WorkspaceRelativePath string        `json:"workspaceRelativePath"`
	OriginalPath          *string       `json:"originalPath,omitempty"`
	Kind                  GitChangeKind `json:"kind"`
	Added                 int           `json:"added"`
	Removed               int           `json:"removed"`
}

// GitCheckpointDiff is GitCheckpointDiff.
type GitCheckpointDiff struct {
	FromCheckpointID string                  `json:"fromCheckpointId"`
	ToCheckpointID   string                  `json:"toCheckpointId"`
	Files            []GitCheckpointFileDiff `json:"files"`
}

// GitCheckpointConflictReason is GitCheckpointConflictReason.
type GitCheckpointConflictReason string

const (
	CheckpointConflictContentMismatch   GitCheckpointConflictReason = "content-mismatch"
	CheckpointConflictMissingInWorktree GitCheckpointConflictReason = "missing-in-worktree"
	CheckpointConflictUnexpectedFile    GitCheckpointConflictReason = "unexpected-file-in-worktree"
	CheckpointConflictTypeMismatch      GitCheckpointConflictReason = "type-mismatch"
)

// GitCheckpointConflict is GitCheckpointConflict.
type GitCheckpointConflict struct {
	Path                  string                      `json:"path"`
	RepoRelativePath      string                      `json:"repoRelativePath"`
	WorkspaceRelativePath string                      `json:"workspaceRelativePath"`
	Reason                GitCheckpointConflictReason `json:"reason"`
}

// GitCheckpointRestoreResult is GitCheckpointRestoreResult.
type GitCheckpointRestoreResult struct {
	Success       bool                    `json:"success"`
	Conflicts     []GitCheckpointConflict `json:"conflicts,omitempty"`
	RestoredPaths []string                `json:"restoredPaths,omitempty"`
}
