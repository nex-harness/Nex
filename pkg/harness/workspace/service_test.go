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
