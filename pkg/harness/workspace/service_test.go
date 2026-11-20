package workspace

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestWorkspaceTreeSearchPreviewTraversalGuardAndGitDiff(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()

	// Initialize temporary git repo
	runCmd := func(args ...string) {
		t.Helper()
		c := exec.Command(args[0], args[1:]...)
		c.Dir = dir
		if out, err := c.CombinedOutput(); err != nil {
			t.Fatalf("%v failed: %v\n%s", args, err, string(out))
		}
	}
	runCmd("git", "init")
	runCmd("git", "config", "user.name", "Test")
	runCmd("git", "config", "user.email", "test@example.com")

	_ = os.WriteFile(filepath.Join(dir, ".gitignore"), []byte("dist/\n"), 0o644)
	_ = os.MkdirAll(filepath.Join(dir, "dist"), 0o755)
	_ = os.WriteFile(filepath.Join(dir, "dist", "bundle.js"), []byte("ignored"), 0o644)
	_ = os.MkdirAll(filepath.Join(dir, "pkg"), 0o755)
	_ = os.WriteFile(filepath.Join(dir, "pkg", "main.go"), []byte("package main\nfunc main() {}\n"), 0o644)
	_ = os.WriteFile(filepath.Join(dir, "README.md"), []byte("# Nex\n"), 0o644)
	_ = os.WriteFile(filepath.Join(dir, "icon.png"), []byte{0x89, 'P', 'N', 'G', '\r', '\n', 0x1a, '\n'}, 0o644)
	_ = os.WriteFile(filepath.Join(dir, "data.bin"), []byte{0x00, 0x01, 0x02, 0xFF}, 0o644)

	runCmd("git", "add", "pkg/main.go", "README.md", ".gitignore")
	runCmd("git", "commit", "--no-verify", "-m", "initial")

	// Modify tracked file and add untracked text file
	_ = os.WriteFile(filepath.Join(dir, "pkg", "main.go"), []byte("package main\nimport \"fmt\"\nfunc main() { fmt.Println(\"hi\") }\n"), 0o644)
	_ = os.WriteFile(filepath.Join(dir, "notes.txt"), []byte("line1\nline2\n"), 0o644)

	svc := NewService()

	// 1. Path traversal guard must block "../"
	if _, err := svc.ReadPreview(ctx, dir, "../../etc/passwd"); err == nil {
		t.Fatalf("expected path traversal rejection!")
	}

	// 2. Lazy directory listing & gitignore marking
	listMap, err := svc.ListDir(ctx, dir, "")
	if err != nil {
		t.Fatalf("ListDir failed: %v", err)
	}
	entries := listMap["entries"].([]DirEntry)
	foundDistIgnored := false
	for _, e := range entries {
		if e.Name == "dist" && e.GitIgnored {
			foundDistIgnored = true
		}
	}
	if !foundDistIgnored {
		t.Fatalf("expected dist/ marked as GitIgnored: %+v", entries)
	}

	// 3. Fast filename search
	rawSearch, err := svc.HandleRequest(ctx, dir, json.RawMessage(`{"action":"fs_search","query":"main"}`))
	if err != nil || !strings.Contains(string(rawSearch), "pkg/main.go") {
		t.Fatalf("fs_search failed: %s err=%v", string(rawSearch), err)
	}

	// 4. Multi-type New Tab File Previews (Go code, Markdown, PNG Image, Binary guard)
	codePrev, err := svc.ReadPreview(ctx, dir, "pkg/main.go")
	if err != nil || codePrev.PreviewAs != "text" || codePrev.Language != "go" {
		t.Fatalf("code preview mismatch: %+v err=%v", codePrev, err)
	}
	mdPrev, _ := svc.ReadPreview(ctx, dir, "README.md")
	if mdPrev.PreviewAs != "markdown" {
		t.Fatalf("expected markdown preview, got %+v", mdPrev)
	}
	imgPrev, _ := svc.ReadPreview(ctx, dir, "icon.png")
	if imgPrev.PreviewAs != "image" || imgPrev.MimeType != "image/png" {
		t.Fatalf("expected image preview, got %+v", imgPrev)
	}
	binPrev, _ := svc.ReadPreview(ctx, dir, "data.bin")
	if binPrev.PreviewAs != "binary" || binPrev.Content != "" {
		t.Fatalf("expected binary guard preview with empty content, got %+v", binPrev)
	}

	// 5. Git Summary & Review Diff
	gitSum, err := svc.GitSummary(ctx, dir, true, "")
	if err != nil || !gitSum.IsGitRepo || gitSum.Additions < 4 {
		t.Fatalf("git summary mismatch: %+v err=%v", gitSum, err)
	}
	foundGoDiff := false
	for _, f := range gitSum.Files {
		if f.RelPath == "pkg/main.go" && strings.Contains(f.Diff, "+import \"fmt\"") {
			foundGoDiff = true
		}
	}
	if !foundGoDiff {
		t.Fatalf("missing unified diff for pkg/main.go: %+v", gitSum.Files)
	}
}

func TestBrowseDirs(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	_ = os.MkdirAll(filepath.Join(root, "alpha"), 0o755)
	_ = os.MkdirAll(filepath.Join(root, "Beta"), 0o755)
	_ = os.MkdirAll(filepath.Join(root, ".hidden-dir"), 0o755)
	_ = os.WriteFile(filepath.Join(root, "plain.txt"), []byte("hi"), 0o644)

	linkTarget := filepath.Join(root, "alpha")
	_ = os.Symlink(linkTarget, filepath.Join(root, "alpha-link"))

	svc := NewService()

	// Default excludes hidden directories and files, keeps dirs and dir-symlinks sorted case-insensitively.
	res, err := svc.BrowseDirs(ctx, root, false)
	if err != nil {
		t.Fatalf("BrowseDirs failed: %v", err)
	}
	if res.CurrentPath == "" || res.ParentPath == "" {
		t.Fatalf("expected non-empty current/parent path: %+v", res)
	}
	if len(res.Entries) != 3 {
		t.Fatalf("expected 3 visible dir entries (alpha, alpha-link, Beta), got %+v", res.Entries)
	}
	if res.Entries[0].Name != "alpha" || res.Entries[1].Name != "alpha-link" || !res.Entries[1].IsSymlink || res.Entries[2].Name != "Beta" {
		t.Fatalf("unexpected entries ordering/flags: %+v", res.Entries)
	}

	// IncludeHidden=true reveals .hidden-dir via HandleRequest RPC path as well.
	raw, err := svc.HandleRequest(ctx, root, json.RawMessage(`{"action":"fs_browse_dirs","include_hidden":true}`))
	if err != nil {
		t.Fatalf("HandleRequest fs_browse_dirs failed: %v", err)
	}
	var rpcRes BrowseDirsResult
	if err := json.Unmarshal(raw, &rpcRes); err != nil {
		t.Fatalf("unmarshal BrowseDirsResult: %v", err)
	}
	foundHidden := false
	for _, e := range rpcRes.Entries {
		if e.Name == ".hidden-dir" {
			foundHidden = true
		}
	}
	if !foundHidden {
		t.Fatalf("expected .hidden-dir when include_hidden=true: %+v", rpcRes.Entries)
	}

	// Non-directory file path returns error.
	if _, err := svc.BrowseDirs(ctx, filepath.Join(root, "plain.txt"), false); err == nil {
		t.Fatalf("expected error for non-directory path")
	}
}

func TestGitStageUnstageDiscardCommitAndBranches(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	dir := t.TempDir()
	run := func(args ...string) {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(),
			"GIT_AUTHOR_NAME=Tester", "GIT_AUTHOR_EMAIL=t@example.com",
			"GIT_COMMITTER_NAME=Tester", "GIT_COMMITTER_EMAIL=t@example.com",
		)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v: %s", args, err, string(out))
		}
	}
	run("init", "-b", "main")
	run("config", "user.name", "Tester")
	run("config", "user.email", "t@example.com")
	_ = os.WriteFile(filepath.Join(dir, "a.txt"), []byte("line1\n"), 0o644)
	run("add", "a.txt")
	run("commit", "-m", "init")

	svc := NewService()
	ctx := context.Background()

	// Modify a.txt and create untracked b.txt
	_ = os.WriteFile(filepath.Join(dir, "a.txt"), []byte("line1\nline2\n"), 0o644)
	_ = os.WriteFile(filepath.Join(dir, "b.txt"), []byte("temp\n"), 0o644)

	// Stage a.txt -> verify Staged=true
	sum, err := svc.GitStage(ctx, dir, "a.txt")
	if err != nil {
		t.Fatalf("GitStage failed: %v", err)
	}
	foundStaged := false
	for _, f := range sum.Files {
		if f.RelPath == "a.txt" && f.Staged {
			foundStaged = true
		}
	}
	if !foundStaged {
		t.Fatalf("expected a.txt staged, got %+v", sum.Files)
	}

	// Unstage a.txt -> verify Staged=false
	sum, err = svc.GitUnstage(ctx, dir, "a.txt")
	if err != nil {
		t.Fatalf("GitUnstage failed: %v", err)
	}
	for _, f := range sum.Files {
		if f.RelPath == "a.txt" && f.Staged {
			t.Fatalf("expected a.txt unstaged, got %+v", f)
		}
	}

	// Discard untracked b.txt -> file removed
	sum, err = svc.GitDiscard(ctx, dir, "b.txt")
	if err != nil {
		t.Fatalf("GitDiscard untracked failed: %v", err)
	}
	if _, statErr := os.Stat(filepath.Join(dir, "b.txt")); !os.IsNotExist(statErr) {
		t.Fatalf("expected b.txt to be removed after discard")
	}

	// Stage all & Commit
	if _, err := svc.GitStage(ctx, dir, ""); err != nil {
		t.Fatal(err)
	}
	commitRes, err := svc.GitCommit(ctx, dir, "feat: add line2")
	if err != nil || commitRes["ok"] != true {
		t.Fatalf("GitCommit failed: %+v err=%v", commitRes, err)
	}

	// Create & checkout branch, list branches
	if _, err := svc.GitCheckout(ctx, dir, "feat/test-branch", true); err != nil {
		t.Fatalf("GitCheckout -b failed: %v", err)
	}
	brRes, err := svc.GitBranches(ctx, dir)
	if err != nil || brRes["current"] != "feat/test-branch" {
		t.Fatalf("unexpected GitBranches: %+v err=%v", brRes, err)
	}
}
