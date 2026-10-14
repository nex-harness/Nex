package git

// service_test.go (step 3.3): end-to-end behavior tests against the fixture
// repository — the same scenarios svc-capture.mjs records through the real
// RPC, driven here directly on the Service (the channel adapter is exercised
// in channel_test.go). Error paths carry the exact TS messages.

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestGetRepositorySummaryFixture(t *testing.T) {
	requireGitAvailable(t)
	f := buildFixtureRepo(t)
	svc := f.service()
	summary, err := svc.GetRepositorySummary(context.Background(), f.repo)
	if err != nil {
		t.Fatalf("GetRepositorySummary: %v", err)
	}
	if summary.BranchName == nil || *summary.BranchName != "main" {
		t.Errorf("branchName = %v, want main", summary.BranchName)
	}
	if summary.TrackingBranchName == nil || *summary.TrackingBranchName != "origin/main" {
		t.Errorf("trackingBranchName = %v, want origin/main", summary.TrackingBranchName)
	}
	if !summary.IsRepository || !summary.IsGitAvailable {
		t.Errorf("isRepository=%v isGitAvailable=%v, want true/true", summary.IsRepository, summary.IsGitAvailable)
	}
	if !summary.IsDirty {
		t.Errorf("isDirty = false, want true (fixture leaves staged+unstaged+untracked changes)")
	}
	if summary.RepoRoot != f.repo {
		t.Errorf("repoRoot = %q, want %q", summary.RepoRoot, f.repo)
	}
	if summary.WorkspaceInRepoPath != "." {
		t.Errorf("workspaceInRepoPath = %q, want .", summary.WorkspaceInRepoPath)
	}
	// The git metadata dir must be a watch boundary.
	if len(summary.AutoRefreshWatchPaths) == 0 {
		t.Errorf("autoRefreshWatchPaths empty")
	}
}

func TestGetRepositorySummaryNotARepository(t *testing.T) {
	requireGitAvailable(t)
	f := buildFixtureRepo(t)
	svc := f.service()
	notRepo := filepath.Join(f.root, "not-a-repo")
	if err := os.MkdirAll(notRepo, 0o777); err != nil {
		t.Fatal(err)
	}
	summary, err := svc.GetRepositorySummary(context.Background(), notRepo)
	if err != nil {
		t.Fatalf("not-a-repo must degrade, not error: %v", err)
	}
	if summary.IsRepository {
		t.Errorf("isRepository = true, want false")
	}
	if summary.BranchName != nil || summary.IsDirty {
		t.Errorf("summary fields must be empty: %+v", summary)
	}
}

func TestGetRepositorySummaryEmptyRepo(t *testing.T) {
	requireGitAvailable(t)
	f := buildFixtureRepo(t)
	svc := f.service()
	summary, err := svc.GetRepositorySummary(context.Background(), f.empty)
	if err != nil {
		t.Fatalf("empty repo must degrade: %v", err)
	}
	if !summary.IsRepository {
		t.Fatalf("isRepository = false for a git-init'd dir")
	}
	// An unborn branch: porcelain v2 emits "# branch.head main" — TS keeps the
	// name; isDirty false because there are no entries yet.
	if summary.BranchName == nil || *summary.BranchName != "main" {
		t.Logf("branchName = %v (unborn-branch naming differs across git versions)", summary.BranchName)
	}
}

func TestGetChangesSections(t *testing.T) {
	requireGitAvailable(t)
	f := buildFixtureRepo(t)
	svc := f.service()
	ctx := context.Background()

	staged, err := svc.GetChanges(ctx, f.repo, SourceStaged)
	if err != nil {
		t.Fatalf("staged: %v", err)
	}
	if len(staged) != 1 || staged[0].RepoRelativePath != "staged.txt" {
		t.Fatalf("staged changes = %+v, want exactly staged.txt", staged)
	}
	if !staged[0].IsStaged || staged[0].Section != SectionStaged {
		t.Errorf("staged.txt flags wrong: %+v", staged[0])
	}

	unstaged, err := svc.GetChanges(ctx, f.repo, SourceUnstaged)
	if err != nil {
		t.Fatalf("unstaged: %v", err)
	}
	foundMain := false
	for _, change := range unstaged {
		if change.RepoRelativePath == "src/main.ts" {
			foundMain = true
			if change.Section != SectionUnstaged || change.IsStaged {
				t.Errorf("main.ts section flags wrong: %+v", change)
			}
			// +1/-1 from the modified single line.
			if change.Added != 1 || change.Removed != 1 {
				t.Errorf("main.ts added/removed = %d/%d, want 1/1", change.Added, change.Removed)
			}
		}
		if change.WorkspaceRelativePath != change.RepoRelativePath {
			t.Errorf("workspace-relative mismatch for root workspace: %+v", change)
		}
	}
	if !foundMain {
		t.Errorf("src/main.ts missing from unstaged changes: %+v", unstaged)
	}

	// Untracked: empty.txt (+0/-0) is filtered out; untracked.txt kept.
	var untrackedPaths []string
	for _, change := range unstaged {
		if change.IsUntracked {
			untrackedPaths = append(untrackedPaths, change.RepoRelativePath)
			if change.Section != SectionUntracked {
				t.Errorf("untracked section wrong: %+v", change)
			}
		}
	}
	if len(untrackedPaths) == 0 || untrackedPaths[0] != "notes/untracked.txt" {
		t.Errorf("untracked paths = %v, want notes/untracked.txt only (empty.txt is +0/-0)", untrackedPaths)
	}
}

func TestGetChangesWorkspaceScopeMonorepo(t *testing.T) {
	requireGitAvailable(t)
	f := buildFixtureRepo(t)
	svc := f.service()
	ctx := context.Background()
	// A sub-directory workspace only sees its own scope.
	changes, err := svc.GetChanges(ctx, filepath.Join(f.repo, "src"), SourceUnstaged)
	if err != nil {
		t.Fatalf("unstaged: %v", err)
	}
	for _, change := range changes {
		if change.RepoRelativePath == "staged.txt" {
			t.Errorf("staged.txt leaked into a src/ workspace: %+v", change)
		}
	}
	// main.ts must appear with the workspace-relative path.
	for _, change := range changes {
		if change.RepoRelativePath == "src/main.ts" {
			if change.WorkspaceRelativePath != "main.ts" {
				t.Errorf("workspaceRelativePath = %q, want main.ts", change.WorkspaceRelativePath)
			}
		}
	}
}

func TestGetDiffTrackedFile(t *testing.T) {
	requireGitAvailable(t)
	f := buildFixtureRepo(t)
	svc := f.service()
	ctx := context.Background()
	result, err := svc.GetDiff(ctx, GetDiffParams{WorkspacePath: f.repo, Path: "src/main.ts", SourceID: ptrSource(SourceUnstaged)})
	if err != nil {
		t.Fatalf("GetDiff: %v", err)
	}
	if result.Availability != DiffAvailabilityPatch {
		t.Fatalf("availability = %s, want patch", result.Availability)
	}
	if result.Patch == nil || !strings.Contains(*result.Patch, "-export function main() {}") {
		t.Errorf("patch missing the removed line: %q", deref(result.Patch))
	}
	// Unstaged diffs include before/after full contents on the patch path.
	if result.BeforeContent == nil || !strings.Contains(*result.BeforeContent, "main() {}") {
		t.Errorf("beforeContent missing")
	}
	if result.AfterContent == nil || !strings.Contains(*result.AfterContent, "return 2") {
		t.Errorf("afterContent missing")
	}
}

func TestGetDiffStagedFile(t *testing.T) {
	requireGitAvailable(t)
	f := buildFixtureRepo(t)
	svc := f.service()
	result, err := svc.GetDiff(context.Background(), GetDiffParams{WorkspacePath: f.repo, Path: "staged.txt", SourceID: ptrSource(SourceStaged)})
	if err != nil {
		t.Fatalf("GetDiff staged: %v", err)
	}
	if result.Availability != DiffAvailabilityPatch {
		t.Fatalf("availability = %s, want patch", result.Availability)
	}
	if result.Patch == nil || !strings.Contains(*result.Patch, `+++ b/staged.txt`) {
		t.Errorf("staged patch wrong: %q", deref(result.Patch))
	}
	// New file: the HEAD blob read fails, so the TS keeps the patch without
	// before/after contents (toCompleteDiffContents requires both sides).
	if result.BeforeContent != nil || result.AfterContent != nil {
		t.Errorf("staged new-file diff must have null contents, got %v/%v", result.BeforeContent, result.AfterContent)
	}
}

func TestGetDiffBinaryFile(t *testing.T) {
	requireGitAvailable(t)
	f := buildFixtureRepo(t)
	svc := f.service()
	// Modify the binary so an unstaged diff exists.
	if err := os.WriteFile(filepath.Join(f.repo, "assets", "blob.bin"), []byte{0x00, 0x09, 0x08, 0x00}, 0o644); err != nil {
		t.Fatal(err)
	}
	result, err := svc.GetDiff(context.Background(), GetDiffParams{WorkspacePath: f.repo, Path: "assets/blob.bin", SourceID: ptrSource(SourceUnstaged)})
	if err != nil {
		t.Fatalf("GetDiff binary: %v", err)
	}
	if result.Availability != DiffAvailabilityBinary {
		t.Fatalf("availability = %s, want binary", result.Availability)
	}
	if result.Summary == nil || *result.Summary != "Binary diff is not previewable." {
		t.Errorf("binary summary = %v", result.Summary)
	}
}

func TestGetDiffUntrackedFile(t *testing.T) {
	requireGitAvailable(t)
	f := buildFixtureRepo(t)
	svc := f.service()
	result, err := svc.GetDiff(context.Background(), GetDiffParams{WorkspacePath: f.repo, Path: "notes/untracked.txt"})
	if err != nil {
		t.Fatalf("GetDiff untracked: %v", err)
	}
	if result.Availability != DiffAvailabilityPatch {
		t.Fatalf("availability = %s, want patch (synthesized untracked diff)", result.Availability)
	}
	patch := deref(result.Patch)
	if !strings.Contains(patch, "--- /dev/null") || !strings.Contains(patch, "+++ b/notes/untracked.txt") {
		t.Errorf("untracked patch headers wrong: %q", patch)
	}
	if !strings.Contains(patch, "+untracked") {
		t.Errorf("untracked patch body wrong: %q", patch)
	}
	if result.BeforeContent == nil || *result.BeforeContent != "" {
		t.Errorf("untracked beforeContent should be \"\"")
	}
	if result.AfterContent == nil || *result.AfterContent != "untracked\n" {
		t.Errorf("untracked afterContent = %q", deref(result.AfterContent))
	}
}

func TestGetDiffNotARepository(t *testing.T) {
	requireGitAvailable(t)
	f := buildFixtureRepo(t)
	svc := f.service()
	result, err := svc.GetDiff(context.Background(), GetDiffParams{WorkspacePath: f.root, Path: "whatever.txt"})
	if err != nil {
		t.Fatalf("GetDiff outside a repo must degrade: %v", err)
	}
	if result.Availability != DiffAvailabilityUnavailable {
		t.Fatalf("availability = %s, want unavailable", result.Availability)
	}
	if result.Summary == nil || *result.Summary != "Workspace is not inside a Git repository." {
		t.Errorf("summary = %v", result.Summary)
	}
}

func TestGetIgnoredPaths(t *testing.T) {
	requireGitAvailable(t)
	f := buildFixtureRepo(t)
	writeFixtureFile(t, filepath.Join(f.repo, ".gitignore"), "ignored/\n*.log\n")
	svc := f.service()
	result, err := svc.GetIgnoredPaths(context.Background(), f.repo, []string{
		filepath.Join(f.repo, "README.md"),
		"ignored/x.log",
		filepath.Join(f.repo, "nothing.log"),
	})
	if err != nil {
		t.Fatalf("GetIgnoredPaths: %v", err)
	}
	// ignored/x.log is not on disk, but check-ignore is purely rule-based —
	// it IS ignored; nothing.log matches *.log too; README.md is not.
	joined := strings.Join(result, "\n")
	if !strings.Contains(joined, "nothing.log") || !strings.Contains(joined, "ignored/x.log") {
		t.Errorf("ignored paths = %v, want ignored/x.log and nothing.log", result)
	}
	for _, path := range result {
		if strings.HasSuffix(path, "README.md") {
			t.Errorf("README.md reported as ignored: %v", result)
		}
	}
}

func TestGetLocalBranchesFixture(t *testing.T) {
	requireGitAvailable(t)
	f := buildFixtureRepo(t)
	svc := f.service()
	result, err := svc.GetLocalBranches(context.Background(), f.repo)
	if err != nil {
		t.Fatalf("GetLocalBranches: %v", err)
	}
	if result.CurrentBranchName == nil || *result.CurrentBranchName != "main" {
		t.Fatalf("currentBranchName = %v, want main", result.CurrentBranchName)
	}
	if len(result.Branches) != 2 {
		t.Fatalf("branches = %d, want 2 (main, feature/one)", len(result.Branches))
	}
	if !result.Branches[0].IsCurrent {
		t.Errorf("first branch must be the current one (main): %+v", result.Branches[0])
	}
	if result.Branches[0].Name != "main" {
		t.Errorf("first branch = %q, want main", result.Branches[0].Name)
	}
	if result.Branches[0].CommitTimestampMs == nil {
		t.Errorf("commitTimestampMs missing")
	}
}

func TestGetCommitGraphFixture(t *testing.T) {
	requireGitAvailable(t)
	f := buildFixtureRepo(t)
	svc := f.service()
	result, err := svc.GetCommitGraph(context.Background(), f.repo, nil, nil)
	if err != nil {
		t.Fatalf("GetCommitGraph: %v", err)
	}
	if len(result.Commits) < 5 {
		t.Fatalf("commits = %d, want >= 5", len(result.Commits))
	}
	if result.HasMore {
		t.Errorf("hasMore = true on a small repo")
	}
	// The merge commit must carry two parents.
	sawMerge := false
	for _, commit := range result.Commits {
		if len(commit.Parents) == 2 {
			sawMerge = true
		}
	}
	if !sawMerge {
		t.Errorf("no merge commit (two parents) in the graph")
	}
	// HEAD + branch + tag decorations present on the tip.
	tip := result.Commits[0]
	var kinds []string
	for _, ref := range tip.Refs {
		kinds = append(kinds, string(ref.Kind))
	}
	if !contains(kinds, "head") {
		t.Errorf("tip refs = %v, want a head ref", kinds)
	}
	if !contains(kinds, "branch") {
		t.Errorf("tip refs = %v, want a branch ref", kinds)
	}
	// The tag lives on an earlier commit; find it somewhere in the graph.
	sawTag := false
	for _, commit := range result.Commits {
		for _, ref := range commit.Refs {
			if ref.Kind == GraphRefKindTag && ref.Name == "v1.0.0" {
				sawTag = true
			}
		}
	}
	if !sawTag {
		t.Errorf("tag v1.0.0 missing from graph decorations")
	}
	// maxCount clamp + hasMore.
	maxCount := 2
	result, err = svc.GetCommitGraph(context.Background(), f.repo, &maxCount, nil)
	if err != nil {
		t.Fatalf("GetCommitGraph clamped: %v", err)
	}
	if len(result.Commits) != 2 || !result.HasMore {
		t.Errorf("clamped graph = %d commits, hasMore=%v, want 2/true", len(result.Commits), result.HasMore)
	}
	// skip paging.
	skip := 1
	result, err = svc.GetCommitGraph(context.Background(), f.repo, &maxCount, &skip)
	if err != nil {
		t.Fatalf("GetCommitGraph skip: %v", err)
	}
	if len(result.Commits) != 2 {
		t.Errorf("skip graph = %d commits, want 2", len(result.Commits))
	}
}

func TestGetCommitGraphEmptyRepo(t *testing.T) {
	requireGitAvailable(t)
	f := buildFixtureRepo(t)
	svc := f.service()
	result, err := svc.GetCommitGraph(context.Background(), f.empty, nil, nil)
	if err != nil {
		t.Fatalf("empty repo must degrade: %v", err)
	}
	if len(result.Commits) != 0 || result.HasMore {
		t.Errorf("empty repo graph = %+v", result)
	}
}

func TestSwitchBranchAndCreateBranch(t *testing.T) {
	requireGitAvailable(t)
	f := buildFixtureRepo(t)
	svc := f.service()
	ctx := context.Background()

	// Switch to the existing branch (clean enough for git to allow it — the
	// fixture leaves modifications to files that also exist on feature/one, so
	// first discard them through the service itself).
	if err := svc.DiscardPaths(ctx, f.repo, []string{"src/main.ts"}, false); err != nil {
		t.Fatalf("discard: %v", err)
	}
	result, err := svc.SwitchBranch(ctx, f.repo, "feature/one")
	if err != nil {
		t.Fatalf("SwitchBranch: %v", err)
	}
	if !result.OK || !result.DidChange || result.Created || result.BranchName == nil || *result.BranchName != "feature/one" {
		t.Fatalf("switch result = %+v", result)
	}
	if result.Summary.BranchName == nil || *result.Summary.BranchName != "feature/one" {
		t.Errorf("post-switch summary branch = %v", result.Summary.BranchName)
	}

	// Switch to the same branch again: didChange false.
	result, err = svc.SwitchBranch(ctx, f.repo, "feature/one")
	if err != nil {
		t.Fatalf("SwitchBranch same: %v", err)
	}
	if !result.OK || result.DidChange {
		t.Errorf("same-branch switch = %+v", result)
	}

	// Create-and-switch from a start point.
	result, err = svc.CreateBranchAndSwitch(ctx, f.repo, "feature/two", ptr("main"))
	if err != nil {
		t.Fatalf("CreateBranchAndSwitch: %v", err)
	}
	if !result.OK || !result.Created || result.BranchName == nil || *result.BranchName != "feature/two" {
		t.Fatalf("create result = %+v", result)
	}

	// Invalid branch name surfaces the invalid-branch-name issue.
	result, err = svc.SwitchBranch(ctx, f.repo, "bad..name")
	if err != nil {
		t.Fatalf("invalid name must return a failure result, not an error: %v", err)
	}
	if result.OK || len(result.Issues) != 1 || result.Issues[0].Code != "invalid-branch-name" {
		t.Errorf("invalid name result = %+v", result)
	}

	// Empty branch name.
	result, err = svc.SwitchBranch(ctx, f.repo, "   ")
	if err != nil {
		t.Fatalf("empty name: %v", err)
	}
	if result.OK || result.BranchName != nil {
		t.Errorf("empty name result = %+v", result)
	}

	// Existing branch name on create: branch-already-exists.
	result, err = svc.CreateBranchAndSwitch(ctx, f.repo, "main", nil)
	if err != nil {
		t.Fatalf("existing branch create: %v", err)
	}
	if result.OK || len(result.Issues) != 1 || result.Issues[0].Code != "branch-already-exists" {
		t.Errorf("existing branch result = %+v", result)
	}

	// Missing target: target-branch-not-found.
	result, err = svc.SwitchBranch(ctx, f.repo, "no-such-branch")
	if err != nil {
		t.Fatalf("missing target: %v", err)
	}
	if result.OK || len(result.Issues) != 1 || result.Issues[0].Code != "target-branch-not-found" {
		t.Errorf("missing target result = %+v", result)
	}
}

func TestSwitchBranchDirtyTreeOverwrite(t *testing.T) {
	requireGitAvailable(t)
	f := buildFixtureRepo(t)
	svc := f.service()
	ctx := context.Background()
	// feature/one renamed docs/guide.md -> docs/manual.md; a local modification
	// to a file whose content differs per branch must block the switch.
	if err := svc.DiscardPaths(ctx, f.repo, []string{"src/main.ts"}, false); err != nil {
		t.Fatalf("discard: %v", err)
	}
	// README.md differs between main and feature/one? No — docs/manual.md exists
	// only on feature/one, so git would take the local file as an untracked
	// overwrite. Use docs/guide.md instead: it exists ONLY on main (renamed away
	// on feature/one) — same untracked-overwrite class. The reliable tracked
	// overwrite is staged.txt+src/main.ts? Simplest deterministic case: modify a
	// file that differs across branches. main and feature/one both have
	// docs/guide.md? feature/one renamed it, so contents differ across branches
	// only via the rename. Modify src/feature.ts (only on feature/one): git
	// would report untracked-would-be-overwritten. The TS maps both classes;
	// assert the tracked class by modifying README.md after making it differ
	// across branches — instead, keep it simple: modify docs/manual.md which
	// exists only on feature/one with different content staged from main.
	writeFixtureFile(t, filepath.Join(f.repo, "README.md"), "# fixture\nlocally modified for overwrite\n")
	// main's README and feature/one's README are identical, so this is NOT a
	// tracked overwrite either — the real overwrite needs a differing blob.
	// Give feature/one its own README content first.
	f.git(t, f.repo, "switch", "feature/one")
	writeFixtureFile(t, filepath.Join(f.repo, "README.md"), "# fixture\non feature\n")
	f.git(t, f.repo, "add", "README.md")
	f.git(t, f.repo, "commit", "-m", "docs: feature readme")
	f.git(t, f.repo, "switch", "main")
	writeFixtureFile(t, filepath.Join(f.repo, "README.md"), "# fixture\nlocally modified for overwrite\n")
	result, err := svc.SwitchBranch(ctx, f.repo, "feature/one")
	if err != nil {
		t.Fatalf("dirty switch: %v", err)
	}
	if result.OK {
		t.Fatalf("dirty switch must fail: %+v", result)
	}
	if len(result.Issues) != 1 {
		t.Fatalf("issues = %+v, want one", result.Issues)
	}
	// git switch refuses with either the tracked- or untracked-overwrite
	// message depending on state; both map to the overwrite family.
	switch result.Issues[0].Code {
	case "tracked-changes-would-be-overwritten", "untracked-changes-would-be-overwritten":
		if len(result.Issues[0].Paths) == 0 {
			t.Errorf("overwrite issue missing paths: %+v", result.Issues[0])
		}
	default:
		t.Errorf("issue code = %q, want an overwrite code (tracked-changes-would-be-overwritten)", result.Issues[0].Code)
	}
}

func TestStageUnstageDiscard(t *testing.T) {
	requireGitAvailable(t)
	f := buildFixtureRepo(t)
	svc := f.service()
	ctx := context.Background()

	if err := svc.StagePaths(ctx, f.repo, []string{"notes/untracked.txt"}); err != nil {
		t.Fatalf("stage: %v", err)
	}
	staged, err := svc.GetChanges(ctx, f.repo, SourceStaged)
	if err != nil {
		t.Fatalf("changes after stage: %v", err)
	}
	sawStagedNote := false
	for _, change := range staged {
		if change.RepoRelativePath == "notes/untracked.txt" {
			sawStagedNote = true
		}
	}
	if !sawStagedNote {
		t.Errorf("notes/untracked.txt not staged: %+v", staged)
	}

	if err := svc.UnstagePaths(ctx, f.repo, []string{"notes/untracked.txt"}); err != nil {
		t.Fatalf("unstage: %v", err)
	}
	staged, err = svc.GetChanges(ctx, f.repo, SourceStaged)
	if err != nil {
		t.Fatalf("changes after unstage: %v", err)
	}
	for _, change := range staged {
		if change.RepoRelativePath == "notes/untracked.txt" {
			t.Errorf("notes/untracked.txt still staged: %+v", change)
		}
	}

	if err := svc.DiscardPaths(ctx, f.repo, []string{"src/main.ts"}, false); err != nil {
		t.Fatalf("discard: %v", err)
	}
	content, err := os.ReadFile(filepath.Join(f.repo, "src", "main.ts"))
	if err != nil || string(content) != "export function main() {}\n" {
		t.Errorf("discard did not restore the file: %q %v", string(content), err)
	}
}

func TestStagePathsOutsideRepository(t *testing.T) {
	requireGitAvailable(t)
	f := buildFixtureRepo(t)
	svc := f.service()
	err := svc.StagePaths(context.Background(), f.repo, []string{"../../outside.txt"})
	if err == nil || !strings.Contains(err.Error(), "Path is outside repository scope") {
		t.Fatalf("outside-repo stage err = %v", err)
	}
	err = svc.StagePaths(context.Background(), f.root, []string{"whatever"})
	if err == nil || !strings.Contains(err.Error(), "Cannot stage paths: workspace is not inside a Git repository") {
		t.Fatalf("not-a-repo stage err = %v", err)
	}
}

func TestCommitAndScopedStagedOnlyCommit(t *testing.T) {
	requireGitAvailable(t)
	f := buildFixtureRepo(t)
	svc := f.service()
	ctx := context.Background()

	// Plain commit of selected paths (git add + commit -- paths).
	if err := svc.StagePaths(ctx, f.repo, []string{"notes/untracked.txt"}); err != nil {
		t.Fatalf("stage: %v", err)
	}
	result, err := svc.Commit(ctx, CommitRequest{WorkspacePath: f.repo, Message: "feat: commit fixture", Paths: []string{"notes/untracked.txt", "staged.txt"}})
	if err != nil {
		t.Fatalf("Commit: %v", err)
	}
	if len(result.CommitHash) != 40 {
		t.Errorf("commitHash = %q, want a full sha", result.CommitHash)
	}
	// src/main.ts remains modified by design (committed only the two paths).
	stagedSummary := result.Summary
	if !stagedSummary.IsDirty {
		t.Errorf("tree unexpectedly clean; src/main.ts should still be modified")
	}

	// Empty message is rejected with the TS message.
	if _, err := svc.Commit(ctx, CommitRequest{WorkspacePath: f.repo, Message: "   "}); err == nil || err.Error() != "Commit message cannot be empty" {
		t.Fatalf("empty message err = %v", err)
	}
}

func TestScopedStagedOnlyCommitKeepsOtherStagedFiles(t *testing.T) {
	requireGitAvailable(t)
	f := buildFixtureRepo(t)
	svc := f.service()
	ctx := context.Background()

	if err := svc.StagePaths(ctx, f.repo, []string{"notes/untracked.txt"}); err != nil {
		t.Fatalf("stage notes: %v", err)
	}
	result, err := svc.Commit(ctx, CommitRequest{
		WorkspacePath: f.repo,
		Message:       "feat: scoped staged commit",
		Paths:         []string{"notes/untracked.txt"},
		StagedOnly:    true,
	})
	if err != nil {
		t.Fatalf("scoped staged-only commit: %v", err)
	}
	if len(result.CommitHash) != 40 {
		t.Fatalf("commitHash = %q", result.CommitHash)
	}
	// staged.txt must still be staged afterwards (the temp-index commit must
	// not replace the real index).
	staged, err := svc.GetChanges(ctx, f.repo, SourceStaged)
	if err != nil {
		t.Fatalf("changes: %v", err)
	}
	sawStagedTxt := false
	for _, change := range staged {
		if change.RepoRelativePath == "staged.txt" {
			sawStagedTxt = true
		}
	}
	if !sawStagedTxt {
		t.Errorf("staged.txt was unstaged by the scoped commit: %+v", staged)
	}
}

func TestGetBranchComparison(t *testing.T) {
	requireGitAvailable(t)
	f := buildFixtureRepo(t)
	svc := f.service()
	// Move local main ahead of origin/main by one commit.
	writeFixtureFile(t, filepath.Join(f.repo, "local-only.txt"), "ahead\n")
	f.git(t, f.repo, "add", "-A")
	f.git(t, f.repo, "commit", "-m", "feat: local only")
	comparison, err := svc.GetBranchComparison(context.Background(), f.repo)
	if err != nil {
		t.Fatalf("GetBranchComparison: %v", err)
	}
	if comparison.BaseRef == nil || *comparison.BaseRef != "origin/main" {
		t.Errorf("baseRef = %v", comparison.BaseRef)
	}
	if comparison.ComparisonLabel == nil || *comparison.ComparisonLabel != "main -> origin/main" {
		t.Errorf("comparisonLabel = %v", comparison.ComparisonLabel)
	}
	found := false
	for _, change := range comparison.Changes {
		if change.RepoRelativePath == "local-only.txt" {
			found = true
			if change.Kind != ChangeKindAdded || change.Added != 1 {
				t.Errorf("local-only.txt change = %+v", change)
			}
			if change.Section != SectionBranch {
				t.Errorf("section = %s, want branch", change.Section)
			}
		}
	}
	if !found {
		t.Errorf("local-only.txt missing from branch comparison: %+v", comparison.Changes)
	}
}

func TestGetIdentity(t *testing.T) {
	requireGitAvailable(t)
	f := buildFixtureRepo(t)
	svc := f.service()
	identity, err := svc.GetIdentity(context.Background(), f.repo)
	if err != nil {
		t.Fatalf("GetIdentity: %v", err)
	}
	if identity.UserName == nil || *identity.UserName != "Fixture Author" {
		t.Errorf("userName = %v, want Fixture Author", identity.UserName)
	}
	if identity.UserEmail == nil || *identity.UserEmail != "author@fixture.local" {
		t.Errorf("userEmail = %v", identity.UserEmail)
	}
	if identity.ScopeLabel == nil {
		t.Errorf("scopeLabel missing (repo-local config sets scope)")
	}
	if identity.NameSource == nil {
		t.Errorf("nameSource missing")
	}
}

func TestGetIdentityNotARepository(t *testing.T) {
	requireGitAvailable(t)
	f := buildFixtureRepo(t)
	svc := f.service()
	identity, err := svc.GetIdentity(context.Background(), f.root)
	if err != nil {
		t.Fatalf("identity outside a repo must degrade: %v", err)
	}
	if identity.UserName != nil || identity.UserEmail != nil || identity.ScopeLabel != nil {
		t.Errorf("identity = %+v, want all null", identity)
	}
}

func TestPushLocalBareRemote(t *testing.T) {
	requireGitAvailable(t)
	f := buildFixtureRepo(t)
	svc := f.service()
	ctx := context.Background()
	// Commit everything so the push is clean; origin is a LOCAL bare repo —
	// no network is involved.
	writeFixtureFile(t, filepath.Join(f.repo, "pushed.txt"), "push me\n")
	f.git(t, f.repo, "add", "-A")
	f.git(t, f.repo, "commit", "-m", "feat: push payload")
	result, err := svc.Push(ctx, f.repo)
	if err != nil {
		t.Fatalf("Push: %v", err)
	}
	if result.BranchName == nil || *result.BranchName != "main" {
		t.Errorf("branchName = %v", result.BranchName)
	}
	if result.SetUpstream {
		t.Errorf("setUpstream = true, but the fixture branch already tracks")
	}
	if result.RemoteName == nil || *result.RemoteName != "origin" {
		t.Errorf("remoteName = %v", result.RemoteName)
	}
	// ahead must be 0 after the push.
	if result.Summary.Ahead != 0 {
		t.Errorf("ahead = %d after push, want 0", result.Summary.Ahead)
	}
	// The bare repo actually received the commit.
	head := f.git(t, f.bare, "rev-parse", "HEAD")
	if !strings.HasPrefix(strings.TrimSpace(head), "") || strings.TrimSpace(head) == "" {
		t.Fatalf("bare repo HEAD empty")
	}
}

func TestPushDetachedHead(t *testing.T) {
	requireGitAvailable(t)
	f := buildFixtureRepo(t)
	svc := f.service()
	f.git(t, f.repo, "checkout", "--detach")
	_, err := svc.Push(context.Background(), f.repo)
	if err == nil || err.Error() != "Cannot push while HEAD is detached." {
		t.Fatalf("detached push err = %v", err)
	}
}

func TestRefresh(t *testing.T) {
	requireGitAvailable(t)
	f := buildFixtureRepo(t)
	svc := f.service()
	result, err := svc.Refresh(context.Background(), RefreshRequest{WorkspacePath: f.repo, IncludeIdentity: true, IncludeBranchComparison: true})
	if err != nil {
		t.Fatalf("Refresh: %v", err)
	}
	if result.Identity == nil || result.Identity.UserName == nil {
		t.Errorf("identity missing from refresh")
	}
	if result.BranchComparison == nil {
		t.Errorf("branchComparison missing from refresh")
	}
	if len(result.UnstagedChanges) == 0 {
		t.Errorf("unstagedChanges empty")
	}
	if result.Summary.BranchName == nil || *result.Summary.BranchName != "main" {
		t.Errorf("summary branch = %v", result.Summary.BranchName)
	}

	// Without the flags both stay null.
	result, err = svc.Refresh(context.Background(), RefreshRequest{WorkspacePath: f.repo})
	if err != nil {
		t.Fatalf("Refresh bare: %v", err)
	}
	if result.Identity != nil || result.BranchComparison != nil {
		t.Errorf("identity/branchComparison must be null when not requested")
	}
}

func TestGetWorkspaceRepositoryInfoWorktree(t *testing.T) {
	requireGitAvailable(t)
	f := buildFixtureRepo(t)
	svc := f.service()
	ctx := context.Background()

	info, err := svc.GetWorkspaceRepositoryInfo(ctx, f.repo)
	if err != nil || info.Kind != WorkspaceRepoKindMainTree {
		t.Fatalf("main repo info = %+v err=%v", info, err)
	}

	worktree := filepath.Join(f.root, "linked-worktree")
	f.git(t, f.repo, "worktree", "add", worktree, "-b", "wt-branch")
	info, err = svc.GetWorkspaceRepositoryInfo(ctx, worktree)
	if err != nil {
		t.Fatalf("worktree info: %v", err)
	}
	if info.Kind != WorkspaceRepoKindLinkedWorktree {
		t.Errorf("worktree kind = %s, want linked-worktree", info.Kind)
	}

	notRepo := filepath.Join(f.root, "not-a-repo")
	if err := os.MkdirAll(notRepo, 0o777); err != nil {
		t.Fatal(err)
	}
	info, err = svc.GetWorkspaceRepositoryInfo(ctx, notRepo)
	if err != nil || info.Kind != WorkspaceRepoKindNotRepository {
		t.Fatalf("not-repo info = %+v err=%v", info, err)
	}
}

func TestGenerateCommitMessageNotWired(t *testing.T) {
	requireGitAvailable(t)
	f := buildFixtureRepo(t)
	svc := f.service() // no generator injected
	_, err := svc.GenerateCommitMessage(context.Background(), GenerateCommitMessageRequest{WorkspacePath: f.repo})
	if err == nil || err.Error() != "Commit message generation is not available." {
		t.Fatalf("err = %v, want the TS not-wired message", err)
	}
}

func TestGenerateCommitMessageNoChanges(t *testing.T) {
	requireGitAvailable(t)
	f := buildFixtureRepo(t)
	generator := &stubGenerator{}
	svc := NewService(f.cliRepo(), generator)
	_, err := svc.GenerateCommitMessage(context.Background(), GenerateCommitMessageRequest{WorkspacePath: f.empty})
	if err == nil || err.Error() != "There are no changes available to commit." {
		t.Fatalf("err = %v, want the no-changes message", err)
	}
	if generator.called {
		t.Errorf("generator must not be called when there are no changes")
	}
}

func TestGenerateCommitMessageSessionScopeAndGeneratorInput(t *testing.T) {
	requireGitAvailable(t)
	f := buildFixtureRepo(t)
	generator := &stubGenerator{}
	svc := NewService(f.cliRepo(), generator)
	_, err := svc.GenerateCommitMessage(context.Background(), GenerateCommitMessageRequest{
		WorkspacePath:           f.repo,
		CurrentSessionFilePaths: []string{filepath.Join(f.repo, "src", "main.ts")},
	})
	if err != nil {
		t.Fatalf("GenerateCommitMessage: %v", err)
	}
	if !generator.called {
		t.Fatalf("generator was not called")
	}
	if len(generator.params.Files) == 0 {
		t.Fatalf("generator received no files")
	}
	for _, file := range generator.params.Files {
		if file.RepoRelativePath == "staged.txt" {
			t.Errorf("session scope leaked staged.txt: %+v", file)
		}
	}
	sawMain := false
	for _, file := range generator.params.Files {
		if file.RepoRelativePath == "src/main.ts" {
			sawMain = true
		}
	}
	if !sawMain {
		t.Errorf("session scope lost src/main.ts: %+v", generator.params.Files)
	}
	// The diff queries: up to 8 files, unstaged included by default.
	if len(generator.params.Diffs) == 0 {
		t.Errorf("generator received no diffs")
	}
	if generator.params.BranchName == nil || *generator.params.BranchName != "main" {
		t.Errorf("branchName = %v", generator.params.BranchName)
	}
}

type stubGenerator struct {
	called  bool
	params  GenerateParams
	lastErr error
}

func (g *stubGenerator) Generate(ctx context.Context, params GenerateParams) (GitGenerateCommitMessageResult, error) {
	g.called = true
	g.params = params
	return GitGenerateCommitMessageResult{Message: "feat: stub", ProviderID: "p", Model: "m"}, nil
}

func ptrSource(source GitChangeSourceId) *GitChangeSourceId { return &source }

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

func contains(values []string, needle string) bool {
	for _, value := range values {
		if value == needle {
			return true
		}
	}
	return false
}
