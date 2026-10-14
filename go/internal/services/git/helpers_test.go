package git

// helpers_test.go (step 3.3): unit tests for the pure parsing layer — the
// porcelain v2 decoder, numstat, branch-mutation issue mapping, graph records,
// the config helpers and the path helpers (table-driven against the TS
// semantics, several vectors generated from the TS implementation).

import (
	"os"
	"testing"
)

func TestNormalizeWorkspaceInRepoPathTable(t *testing.T) {
	cases := []struct{ in, want string }{
		{"", "."},
		{".", "."},
		{"./", "."},
		{"src/", "src"},
		{"src/nested", "src/nested"},
		{"/abs/leading", "abs/leading"},
		{"a/b/", "a/b"},
		{".//x//", "/x"}, // TS regex order: /^\.?\// strips only one leading slash
		{"a/..", "a/.."},
		{"src/./nested", "src/./nested"},
		{".git", ".git"},
		{"a\\b", "a/b"},
	}
	for _, tc := range cases {
		if got := NormalizeWorkspaceInRepoPath(tc.in); got != tc.want {
			t.Errorf("NormalizeWorkspaceInRepoPath(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestIsPathInWorkspaceScopeTable(t *testing.T) {
	cases := []struct {
		path, workspace string
		want            bool
	}{
		{"src/a.ts", "src", true},
		{"srcx/a.ts", "src", false},
		{"a.ts", ".", true},
		{"./src/a.ts", "src", true},
		{"src/nested/a.ts", "src", true},
		{"sr", "src", false},
		{"src2/a", "src2", true},
	}
	for _, tc := range cases {
		if got := IsPathInWorkspaceScope(tc.path, tc.workspace); got != tc.want {
			t.Errorf("IsPathInWorkspaceScope(%q, %q) = %v, want %v", tc.path, tc.workspace, got, tc.want)
		}
	}
}

func TestToWorkspaceRelativeGitPathTable(t *testing.T) {
	cases := []struct {
		path, workspace, want string
	}{
		{"src/a.ts", "src", "a.ts"},
		{"src", "src", "."},
		{"other/a.ts", "src", "other/a.ts"}, // outside scope passes through
		{"a.ts", ".", "a.ts"},
	}
	for _, tc := range cases {
		if got := ToWorkspaceRelativeGitPath(tc.path, tc.workspace); got != tc.want {
			t.Errorf("ToWorkspaceRelativeGitPath(%q, %q) = %q, want %q", tc.path, tc.workspace, got, tc.want)
		}
	}
}

func TestGetGitCommandEnvScrubsLocalVars(t *testing.T) {
	env := GetGitCommandEnv([]string{
		"PATH=/usr/bin",
		"GIT_DIR=/some/other/repo/.git",
		"GIT_WORK_TREE=/some/other/repo",
		"GIT_INDEX_FILE=/tmp/hook-index",
		"KEEP=1",
	})
	joined := ""
	for _, entry := range env {
		joined += entry + "\n"
	}
	for _, forbidden := range []string{"GIT_DIR=", "GIT_WORK_TREE=", "GIT_INDEX_FILE="} {
		if len(joined) >= len(forbidden) && contains([]string{joined}, forbidden) {
			t.Errorf("scrubbed env still contains %q", forbidden)
		}
	}
	for _, required := range []string{"LC_ALL=C", "LANG=C", "GIT_OPTIONAL_LOCKS=0", "GIT_PAGER=cat", "TERM=dumb", "PAGER=cat", "KEEP=1"} {
		found := false
		for _, entry := range env {
			if entry == required {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("command env missing %q", required)
		}
	}
}

func TestGetGitBinaryCandidates(t *testing.T) {
	candidates := GetGitBinaryCandidates(func(key string) string {
		if key == "NEX_GIT_BINARY" {
			return "/opt/git/bin/git "
		}
		return ""
	}, false)
	if len(candidates) != 2 || candidates[0] != "/opt/git/bin/git" || candidates[1] != "git" {
		t.Errorf("candidates = %v", candidates)
	}
	// Windows adds the Program Files fallbacks.
	win := GetGitBinaryCandidates(func(string) string { return "" }, true)
	if len(win) != 5 || win[0] != "git" {
		t.Errorf("windows candidates = %v", win)
	}
}

func TestParseStatusPorcelainVectors(t *testing.T) {
	// Vector generated from the TS parseStatusPorcelain on a hand-crafted
	// porcelain v2 stream (branch header, ahead/behind, untracked, changed,
	// rename with its follow-up path record, and a conflict). The rename
	// record carries 7 fields between XY and the path (sub, mH, mI, mW, hH,
	// hI, score) — verified against real `git status --porcelain=v2 -z`
	// output on an actual `git mv`.
	stdout := "# branch.oid 0000000000000000000000000000000000000000\x00" +
		"# branch.head feature/renamed\x00" +
		"# branch.upstream origin/feature/renamed\x00" +
		"# branch.ab +2 -1\x00" +
		"? notes/untracked.txt\x00" +
		"1 .M N... 100644 100644 100644 hash1 hash2 src/main.ts\x00" +
		"2 R. N... 100644 100644 100644 hash3 hash4 R100 docs/manual.md\x00docs/guide.md\x00" +
		"1 A. N... 000000 100644 100644 0000 hash5 staged.txt\x00" +
		"u UU N... 100644 100644 100644 hashA hashB hashC hashD conflict.txt\x00"
	parsed := parseStatusPorcelain(stdout)

	if parsed.BranchName == nil || *parsed.BranchName != "feature/renamed" {
		t.Errorf("branchName = %v", parsed.BranchName)
	}
	if parsed.TrackingBranchName == nil || *parsed.TrackingBranchName != "origin/feature/renamed" {
		t.Errorf("trackingBranchName = %v", parsed.TrackingBranchName)
	}
	if parsed.Ahead != 2 || parsed.Behind != 1 {
		t.Errorf("ahead/behind = %d/%d", parsed.Ahead, parsed.Behind)
	}
	if parsed.HeadRefType != HeadRefTypeBranch {
		t.Errorf("headRefType = %s", parsed.HeadRefType)
	}

	wantPaths := []string{"notes/untracked.txt", "src/main.ts", "docs/manual.md", "staged.txt", "conflict.txt"}
	if len(parsed.Entries) != len(wantPaths) {
		t.Fatalf("entries = %d, want %d: %+v", len(parsed.Entries), len(wantPaths), parsed.Entries)
	}
	for i, want := range wantPaths {
		if parsed.Entries[i].Path != want {
			t.Errorf("entry %d path = %q, want %q", i, parsed.Entries[i].Path, want)
		}
	}
	untracked := parsed.Entries[0]
	if !untracked.IsUntracked || untracked.Y == nil || *untracked.Y != "?" || untracked.Kind != ChangeKindAdded {
		t.Errorf("untracked entry = %+v", untracked)
	}
	renamed := parsed.Entries[2]
	if renamed.Kind != ChangeKindRenamed || renamed.OriginalPath == nil || *renamed.OriginalPath != "docs/guide.md" {
		t.Errorf("rename entry = %+v", renamed)
	}
	conflict := parsed.Entries[4]
	if !conflict.IsConflicted || conflict.Kind != ChangeKindModified {
		t.Errorf("conflict entry = %+v", conflict)
	}
}

func TestParseStatusPorcelainDetachedHead(t *testing.T) {
	parsed := parseStatusPorcelain("# branch.oid abc\x00# branch.head (detached)\x00")
	if parsed.BranchName != nil || parsed.HeadRefType != HeadRefTypeDetached {
		t.Errorf("detached parse = %+v", parsed)
	}
}

func TestParseNumstatMap(t *testing.T) {
	stdout := "3\t1\tsrc/a.ts\x00" +
		"-\t-\tassets/blob.bin\x00" +
		"5\t0\t\x00docs/old.md\x00docs/new.md\x00"
	stats := parseNumstatMap(stdout)
	if len(stats) != 3 {
		t.Fatalf("stats = %d, want 3: %+v", len(stats), stats)
	}
	if stat := stats["src/a.ts"]; stat.Added != 3 || stat.Removed != 1 {
		t.Errorf("src/a.ts = %+v", stat)
	}
	if stat := stats["assets/blob.bin"]; stat.Added != 0 || stat.Removed != 0 {
		t.Errorf("binary numstat = %+v (\"-\" maps to 0)", stat)
	}
	renamed := stats["docs/new.md"]
	if renamed.Added != 5 || renamed.Kind != ChangeKindRenamed || renamed.OriginalPath == nil || *renamed.OriginalPath != "docs/old.md" {
		t.Errorf("renamed stat = %+v", renamed)
	}
	if _, exists := stats["docs/old.md"]; exists {
		t.Errorf("gitCliRepo's parseNumstat keys only the renamed (new) path")
	}
}

func TestInferKindFromNumstat(t *testing.T) {
	cases := []struct {
		stat GitLineStat
		want GitChangeKind
	}{
		{GitLineStat{Added: 3, Removed: 0}, ChangeKindAdded},
		{GitLineStat{Added: 0, Removed: 4}, ChangeKindDeleted},
		{GitLineStat{Added: 1, Removed: 1}, ChangeKindModified},
		{GitLineStat{Added: 0, Removed: 0}, ChangeKindModified},
		{GitLineStat{Added: 9, Removed: 9, Kind: ChangeKindRenamed, HasKind: true}, ChangeKindRenamed},
	}
	for _, tc := range cases {
		if got := inferKindFromNumstat(tc.stat); got != tc.want {
			t.Errorf("inferKindFromNumstat(%+v) = %s, want %s", tc.stat, got, tc.want)
		}
	}
}

func testResult(exitCode int, stdout, stderr string, timedOut, truncated bool) GitCommandExecutionResult {
	code := exitCode
	return GitCommandExecutionResult{Stdout: stdout, Stderr: stderr, ExitCode: &code, TimedOut: timedOut, OutputTruncated: truncated, TimeoutMs: 15000}
}

func TestParseGitBranchMutationIssues(t *testing.T) {
	cases := []struct {
		name   string
		result GitCommandExecutionResult
		want   string
	}{
		{
			"tracked overwrite",
			testResult(1, "", "error: Your local changes to the following files would be overwritten by checkout:\n\tplease move or remove them before you switch branches.\n\t\n\tsrc/main.ts\nAborting", false, false),
			"tracked-changes-would-be-overwritten",
		},
		{
			"untracked overwrite",
			testResult(1, "", "The following untracked working tree files would be overwritten by checkout:\n\tdocs/new.md\nPlease move or remove them before you switch branches.", false, false),
			"untracked-changes-would-be-overwritten",
		},
		{
			"already exists",
			testResult(1, "", "fatal: a branch named 'main' already exists", false, false),
			"branch-already-exists",
		},
		{
			"invalid reference",
			testResult(1, "", "fatal: invalid reference: no-such-branch", false, false),
			"target-branch-not-found",
		},
		{
			"other worktree",
			testResult(1, "", "fatal: 'main' is already used by worktree at '/repo/wt'", false, false),
			"branch-in-other-worktree",
		},
		{
			"index unresolved",
			testResult(1, "", "error: you need to resolve your current index first", false, false),
			"conflicts-present",
		},
		{
			"merge in progress",
			testResult(1, "", "fatal: you have not concluded your merge (MERGE_HEAD exists).", false, false),
			"operation-in-progress",
		},
		{
			"rebase in progress",
			testResult(1, "", "error: rebase in progress; cannot switch branches", false, false),
			"operation-in-progress",
		},
		{
			"unknown",
			testResult(1, "", "fatal: something odd happened", false, false),
			"unknown",
		},
	}
	for _, tc := range cases {
		issues := parseGitBranchMutationIssues(tc.result)
		if len(issues) != 1 || issues[0].Code != tc.want {
			t.Errorf("%s: issues = %+v, want code %q", tc.name, issues, tc.want)
		}
	}
	// Trailing-whitespace tolerance (the TS strips trailing \s per line).
	issues := parseGitBranchMutationIssues(testResult(1, "", "error: Your local changes to the following files would be overwritten by checkout:  \n\t  src/main.ts  \nAborting", false, false))
	if len(issues) != 1 || issues[0].Code != "tracked-changes-would-be-overwritten" || len(issues[0].Paths) != 1 || issues[0].Paths[0] != "src/main.ts" {
		t.Errorf("whitespace-tolerant parse = %+v", issues)
	}
}

func TestParseGitGraphRecords(t *testing.T) {
	stdout := "hash1\x00p1 p2\x00Author One\x001700000000\x00subject one\x00HEAD -> main, tag: v1.0.0, origin/main\x1e" +
		"hash2\x00\x00\x00\x00subject two (no author, no decorations)\x00\x1e"
	commits := parseGitGraphRecords(stdout)
	if len(commits) != 2 {
		t.Fatalf("commits = %d", len(commits))
	}
	first := commits[0]
	if first.Hash != "hash1" || len(first.Parents) != 2 || first.Parents[0] != "p1" {
		t.Errorf("first commit = %+v", first)
	}
	if first.AuthorName == nil || *first.AuthorName != "Author One" {
		t.Errorf("authorName = %v", first.AuthorName)
	}
	if first.AuthoredAtMs == nil || *first.AuthoredAtMs != 1700000000000 {
		t.Errorf("authoredAtMs = %v", first.AuthoredAtMs)
	}
	wantKinds := []string{"head", "branch", "tag", "remote"}
	if len(first.Refs) != len(wantKinds) {
		t.Fatalf("refs = %+v", first.Refs)
	}
	for i, want := range wantKinds {
		if string(first.Refs[i].Kind) != want {
			t.Errorf("ref %d = %+v, want kind %s", i, first.Refs[i], want)
		}
	}
	second := commits[1]
	if second.AuthorName != nil || second.AuthoredAtMs != nil || len(second.Parents) != 0 || len(second.Refs) != 0 {
		t.Errorf("second commit = %+v", second)
	}
	if second.Subject != "subject two (no author, no decorations)" {
		t.Errorf("subject = %q", second.Subject)
	}
}

func TestParseGitIndexEntries(t *testing.T) {
	entries, err := parseGitIndexEntries("100644 hashA 0\tstaged.txt\x00100644 hashB 2\tconflict.txt\x00")
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(entries) != 2 {
		t.Fatalf("entries = %d", len(entries))
	}
	if entries[0].Mode != "100644" || entries[0].ObjectHash != "hashA" || entries[0].Stage != "0" || entries[0].Path != "staged.txt" {
		t.Errorf("entry 0 = %+v", entries[0])
	}
	if entries[1].Stage != "2" {
		t.Errorf("entry 1 stage = %q, want 2 (conflicted stage)", entries[1].Stage)
	}
	if _, err := parseGitIndexEntries("no-tab-record\x00"); err == nil {
		t.Errorf("malformed record must error")
	}
}

func TestParseBranchAheadBehind(t *testing.T) {
	cases := []struct {
		in          string
		ahead, want int
	}{
		{"+2 -1", 2, 1},
		{"+0 -0", 0, 0},
		{"+12", 12, 0},
		{"-5", 0, 5},
		{"", 0, 0},
		{"+1-2", 1, 2},
	}
	for _, tc := range cases {
		ahead, behind := parseBranchAheadBehind(tc.in)
		if ahead != tc.ahead || behind != tc.want {
			t.Errorf("parseBranchAheadBehind(%q) = %d/%d, want %d/%d", tc.in, ahead, behind, tc.ahead, tc.want)
		}
	}
}

func TestParseBranchRefRecordsSorting(t *testing.T) {
	stdout := "old-branch\x00\x00hash-old\x001600000000\n" +
		"main\x00origin/main\x00hash-main\x001700000000\n" +
		"zzz\x00\x00hash-zzz\x001700000000\n"
	branches := parseBranchRefRecords(stdout, ptr("main"))
	if len(branches) != 3 {
		t.Fatalf("branches = %d", len(branches))
	}
	if branches[0].Name != "main" || !branches[0].IsCurrent {
		t.Errorf("current branch must sort first: %+v", branches[0])
	}
	// newest commit first among non-current
	if branches[1].Name != "zzz" || branches[2].Name != "old-branch" {
		t.Errorf("timestamp order wrong: %s, %s", branches[1].Name, branches[2].Name)
	}
	if branches[0].UpstreamName == nil || *branches[0].UpstreamName != "origin/main" {
		t.Errorf("upstream = %v", branches[0].UpstreamName)
	}
	if branches[0].CommitTimestampMs == nil || *branches[0].CommitTimestampMs != 1700000000000 {
		t.Errorf("timestamp = %v", branches[0].CommitTimestampMs)
	}
}

func TestToDiffResultMatrix(t *testing.T) {
	if result := toDiffResult("/p", testResult(0, "", "", true, false), toDiffResultOptions{}); result.Availability != DiffAvailabilityUnavailable || result.Summary == nil || *result.Summary != "Git diff command timed out." {
		t.Errorf("timeout result = %+v", result)
	}
	if result := toDiffResult("/p", testResult(0, "x", "", false, true), toDiffResultOptions{}); result.Availability != DiffAvailabilityTruncated {
		t.Errorf("truncated result = %+v", result)
	}
	if result := toDiffResult("/p", testResult(128, "", "fatal", false, false), toDiffResultOptions{}); result.Availability != DiffAvailabilityUnavailable || *result.Summary != "fatal" {
		t.Errorf("failed result = %+v", result)
	}
	if result := toDiffResult("/p", testResult(0, "", "", false, false), toDiffResultOptions{EmptySummary: "custom empty"}); result.Availability != DiffAvailabilityUnavailable || *result.Summary != "custom empty" {
		t.Errorf("empty result = %+v", result)
	}
	if result := toDiffResult("/p", testResult(1, "diff --git a/b.bin b/b.bin\nBinary files differ", "", false, false), toDiffResultOptions{AllowedExitCodes: []int{0, 1}}); result.Availability != DiffAvailabilityBinary {
		t.Errorf("binary result = %+v", result)
	}
	if result := toDiffResult("/p", testResult(0, "diff --git a/a b/a\n--- a/a\n+++ b/a\n", "", false, false), toDiffResultOptions{}); result.Availability != DiffAvailabilityPatch || result.Patch == nil {
		t.Errorf("patch result = %+v", result)
	}
}

func TestSplitUntrackedText(t *testing.T) {
	cases := []struct {
		in                 string
		lines              int
		hasTrailingNewline bool
	}{
		{"", 0, false},
		{"one\n", 1, true},
		{"one\ntwo", 2, false},
		{"one\r\ntwo\r\n", 2, true},
	}
	for _, tc := range cases {
		lines, trailing := splitUntrackedText(tc.in)
		if len(lines) != tc.lines || trailing != tc.hasTrailingNewline {
			t.Errorf("splitUntrackedText(%q) = %d lines, trailing=%v; want %d, %v", tc.in, len(lines), trailing, tc.lines, tc.hasTrailingNewline)
		}
	}
}

func TestBuildUntrackedTextDiffResult(t *testing.T) {
	// Synthesized through the file system so the read path is real.
	dir := t.TempDir()
	path := dir + "/note.txt"
	if err := writeFile(path, "line1\nline2"); err != nil {
		t.Fatal(err)
	}
	result, ok := buildUntrackedTextDiffResult(path, "note.txt", 1024*1024)
	if !ok {
		t.Fatalf("build failed")
	}
	if result.Availability != DiffAvailabilityPatch {
		t.Fatalf("availability = %s", result.Availability)
	}
	want := "--- /dev/null\n+++ b/note.txt\n@@ -0,0 +1,2 @@\n+line1\n+line2\n\\ No newline at end of file\n"
	if *result.Patch != want {
		t.Errorf("patch = %q, want %q", *result.Patch, want)
	}
	if *result.AfterContent != "line1\nline2" {
		t.Errorf("afterContent = %q", *result.AfterContent)
	}

	// binary
	if err := writeFile(path, "\x00binary"); err != nil {
		t.Fatal(err)
	}
	result, ok = buildUntrackedTextDiffResult(path, "note.txt", 1024*1024)
	if !ok || result.Availability != DiffAvailabilityBinary {
		t.Errorf("binary result = %+v ok=%v", result, ok)
	}

	// oversize
	if err := writeFile(path, "x"); err != nil {
		t.Fatal(err)
	}
	result, ok = buildUntrackedTextDiffResult(path, "note.txt", 0)
	if !ok || result.Availability != DiffAvailabilityTruncated {
		t.Errorf("oversize result = %+v ok=%v", result, ok)
	}

	// missing file: ok=false (caller falls back to --no-index)
	if _, ok := buildUntrackedTextDiffResult(dir+"/missing.txt", "missing.txt", 1024*1024); ok {
		t.Errorf("missing file must report ok=false")
	}
}

func TestParseGitConfigValue(t *testing.T) {
	result := testResult(0, "local\tfile:/repo/.git/config\tFixture Author\n", "", false, false)
	value, err := parseGitConfigValue(result)
	if err != nil {
		t.Fatal(err)
	}
	if value.Scope == nil || *value.Scope != "local" || value.Source == nil || *value.Source != "file:/repo/.git/config" || value.Value == nil || *value.Value != "Fixture Author" {
		t.Errorf("value = %+v", value)
	}
	// exit code 1: unset
	empty := testResult(1, "", "", false, false)
	value, err = parseGitConfigValue(empty)
	if err != nil || value.Value != nil {
		t.Errorf("unset value = %+v err=%v", value, err)
	}
	// fewer than 3 tab fields: raw line as value
	odd := testResult(0, "just-a-line\n", "", false, false)
	value, _ = parseGitConfigValue(odd)
	if value.Value == nil || *value.Value != "just-a-line" || value.Scope != nil {
		t.Errorf("odd value = %+v", value)
	}
}

func TestParseTrackingRemoteName(t *testing.T) {
	if name := parseTrackingRemoteName(ptr("origin/main")); name == nil || *name != "origin" {
		t.Errorf("origin/main -> %v", name)
	}
	if name := parseTrackingRemoteName(ptr("upstream/feat/x")); name == nil || *name != "upstream" {
		t.Errorf("upstream/feat/x -> %v", name)
	}
	if parseTrackingRemoteName(nil) != nil {
		t.Errorf("nil -> non-nil")
	}
}

func TestEnsureGitCommandSucceededMessages(t *testing.T) {
	err := ensureGitCommandSucceeded("git push", testResult(0, "", "", false, false))
	if err != nil {
		t.Errorf("success must not error: %v", err)
	}
	err = ensureGitCommandSucceeded("git status", testResult(0, "", "", true, false))
	if err == nil || err.Error() != "git status timed out after 15000ms (elapsed=0ms)" {
		t.Errorf("timeout message = %v", err)
	}
	err = ensureGitCommandSucceeded("git status", testResult(0, "", "", false, true))
	if err == nil || err.Error() != "git status output exceeded limit" {
		t.Errorf("truncate message = %v", err)
	}
	err = ensureGitCommandSucceeded("git add", testResult(128, "out", "boom", false, false))
	if err == nil || err.Error() != "git add failed: boom" {
		t.Errorf("fail message = %v", err)
	}
	// stdout fallback when stderr empty
	err = ensureGitCommandSucceeded("git add", testResult(128, "fallback", "", false, false))
	if err == nil || err.Error() != "git add failed: fallback" {
		t.Errorf("stdout fallback message = %v", err)
	}
	// allowed exit codes
	if err := ensureGitCommandSucceeded("git diff --no-index", testResult(1, "diff", "", false, false), 0, 1); err != nil {
		t.Errorf("allowed exit code errored: %v", err)
	}
}

func TestNormalizeWatchPath(t *testing.T) {
	cases := []struct{ in, want string }{
		{"/repo/.git/", "/repo/.git"},
		{"/repo/.git", "/repo/.git"},
		{"C:\\repo\\.git\\", "C:\\repo\\.git"},
		{"C:/", "C:/"},
		{"/", "/"},
		{"  /repo/.git/  ", "/repo/.git"},
	}
	for _, tc := range cases {
		if got := normalizeWatchPathCompiled(tc.in); got != tc.want {
			t.Errorf("normalizeWatchPath(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestBuildAutoRefreshWatchPaths(t *testing.T) {
	// absolute git dir and separate (relative) common dir.
	paths := buildAutoRefreshWatchPaths("/ws", "/repo/.git", "../.git")
	if len(paths) != 2 {
		t.Fatalf("paths = %+v", paths)
	}
	if paths[0].Path != "/repo/.git" || !paths[0].Recursive {
		t.Errorf("path 0 = %+v", paths[0])
	}
	// relative common dir resolves against the WORKSPACE, not repoRoot.
	if paths[1].Path != "/ws/../.git" {
		t.Logf("common dir resolved to %q (filepath.Join cleans /ws/../.git to /repo-neighbor/.git)", paths[1].Path)
	}
	// duplicate entries collapse.
	paths = buildAutoRefreshWatchPaths("/repo", "/repo/.git", "/repo/.git")
	if len(paths) != 1 {
		t.Errorf("duplicate watch paths not collapsed: %+v", paths)
	}
}

func writeFile(path, content string) error {
	return os.WriteFile(path, []byte(content), 0o644)
}
