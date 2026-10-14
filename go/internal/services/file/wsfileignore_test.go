package file

// wsfileignore_test.go: the .nexignore single-source-of-truth behaviors from
// workspaceFileIgnore.ts — auto-create from .gitignore copy or template,
// fail-open degradation, marker sectioning, sync/reset transforms, atomic
// write, and the settings-page read/preview split.

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeStr(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestLoadIgnoreRulesAutoCreatesFromGitignoreCopy(t *testing.T) {
	dir := t.TempDir()
	writeStr(t, filepath.Join(dir, ".gitignore"), "dist/\n*.log\n")

	rules, err := loadIgnoreRules(dir, noopLogger{})
	if err != nil {
		t.Fatal(err)
	}
	if rules.Source != "created-from-gitignore" {
		t.Fatalf("source: %s", rules.Source)
	}
	content, err := os.ReadFile(filepath.Join(dir, IgnoreFileName))
	if err != nil {
		t.Fatalf("auto-create did not write .nexignore: %v", err)
	}
	// gitignore 拷贝区保留原文（含尾换行）
	if !strings.HasPrefix(string(content), "dist/\n*.log\n") {
		t.Fatalf("gitignore section: %q", string(content))
	}
	// 默认段去重：.gitignore 未声明 node_modules/ 等目录
	if !strings.Contains(string(content), "node_modules/") {
		t.Fatalf("builtin defaults section missing: %q", string(content))
	}
	// 双标记分区存在
	if !strings.Contains(string(content), syncMarker) || !strings.Contains(string(content), defaultsMarker) {
		t.Fatalf("markers missing: %q", string(content))
	}
}

func TestLoadIgnoreRulesAutoCreatesTemplateWithoutGitignore(t *testing.T) {
	dir := t.TempDir()
	rules, err := loadIgnoreRules(dir, noopLogger{})
	if err != nil {
		t.Fatal(err)
	}
	if rules.Source != "created-from-template" {
		t.Fatalf("source: %s", rules.Source)
	}
	if !rules.isPathIgnored("node_modules", "directory") {
		t.Fatalf("template defaults must ignore node_modules/")
	}
}

func TestLoadIgnoreRulesUsesExistingFile(t *testing.T) {
	dir := t.TempDir()
	// 用户删除默认段的 node_modules/ 后应恢复搜索（单一真相源）
	writeStr(t, filepath.Join(dir, IgnoreFileName), "custom/\n")
	rules, err := loadIgnoreRules(dir, noopLogger{})
	if err != nil {
		t.Fatal(err)
	}
	if rules.Source != "file" {
		t.Fatalf("source: %s", rules.Source)
	}
	if !rules.isPathIgnored("custom", "directory") {
		t.Fatalf("custom rule must apply")
	}
	if rules.isPathIgnored("node_modules", "directory") {
		t.Fatalf("deleted builtin rule must not apply")
	}
}

func TestIsPathIgnoredDirectoryProbeUsesTrailingSlash(t *testing.T) {
	// gitignore dirOnly 规则只匹配目录：目录必须传带尾斜杠的路径
	m := newMatcher().Add("build/")
	rules := &IgnoreRules{matcher: m, Source: "file"}
	if !rules.isPathIgnored("build", "directory") {
		t.Errorf("directory probe must append /")
	}
	if rules.isPathIgnored("build", "file") {
		t.Errorf("dirOnly rule must not match a file")
	}
}

func TestReadIgnoreFileTemplatePreviewDoesNotWrite(t *testing.T) {
	dir := t.TempDir()
	content, source, err := readIgnoreFile(dir)
	if err != nil {
		t.Fatal(err)
	}
	if source != "template" {
		t.Fatalf("source: %s", source)
	}
	if !strings.Contains(content, defaultsMarker) {
		t.Fatalf("template content: %q", content)
	}
	if _, err := os.Stat(filepath.Join(dir, IgnoreFileName)); !os.IsNotExist(err) {
		t.Fatalf("template preview must not create the file")
	}
}

func TestTransformSyncGitignoreRewritesOnlySyncSection(t *testing.T) {
	dir := t.TempDir()
	writeStr(t, filepath.Join(dir, ".gitignore"), "new1/\nnew2/\n")
	writeStr(t, filepath.Join(dir, IgnoreFileName), "old/\n"+syncMarker+"\nkeepdefaults/\n"+defaultsMarker+"\nmycustom/\n")

	content, err := transformIgnoreFile(dir, "sync-gitignore")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(content, "new1/\nnew2/\n") {
		t.Fatalf("sync section rewritten: %q", content)
	}
	if !strings.Contains(content, "keepdefaults/") || !strings.Contains(content, "mycustom/") {
		t.Fatalf("default/custom sections preserved: %q", content)
	}
	if strings.Contains(content, "old/") {
		t.Fatalf("old gitignore section must be replaced: %q", content)
	}
}

func TestTransformResetDefaultsDedupesAgainstGitignoreSection(t *testing.T) {
	dir := t.TempDir()
	// gitignore 区已声明 node_modules/：恢复默认时不重复写回
	writeStr(t, filepath.Join(dir, IgnoreFileName), "node_modules/\n"+syncMarker+"\nuserremoved/\n"+defaultsMarker+"\n")
	content, err := transformIgnoreFile(dir, "reset-defaults")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Count(content, "node_modules/") != 1 {
		t.Fatalf("dedup failed: %q", content)
	}
	// 若用户曾从 .gitignore 删掉 node_modules 等，恢复时默认段会把它补回（兜底回归）
	if !strings.Contains(content, "coverage/") {
		t.Fatalf("missing builtin lines: %q", content)
	}
}

func TestWriteIgnoreFileAtomicReplace(t *testing.T) {
	dir := t.TempDir()
	if err := writeIgnoreFile(dir, "one/\n"); err != nil {
		t.Fatal(err)
	}
	if err := writeIgnoreFile(dir, "two/\n"); err != nil {
		t.Fatal(err)
	}
	content, _ := os.ReadFile(filepath.Join(dir, IgnoreFileName))
	if string(content) != "two/\n" {
		t.Fatalf("atomic replace: %q", content)
	}
	// no temp files left behind
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".tmp") {
			t.Fatalf("temp file leaked: %s", e.Name())
		}
	}
}

// The scan respects .nexignore over the builtin blacklist (ignoreRulesActive).
func TestScanRespectsNexignoreOverBuiltinBlacklist(t *testing.T) {
	dir := t.TempDir()
	mustWrite(t, filepath.Join(dir, "node_modules", "dep", "x.js"), "x")
	mustWrite(t, filepath.Join(dir, "src", "main.ts"), "x")
	s := newTestService(t)
	ctx := t.Context()

	packed, err := s.ListWorkspaceFilesRange(ctx, dir, 0, 1_000_000)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(packed, "node_modules") {
		t.Fatalf("default .nexignore must prune node_modules: %q", packed)
	}
	if !strings.Contains(packed, "src/main.ts") {
		t.Fatalf("src/main.ts missing: %q", packed)
	}
	// hidden directories: 不占候选但保留遍历
	if !strings.Contains(packed, "dep/x.js") {
		// node_modules is itself ignored here, so the child never appears; the
		// hidden-directory traversal is asserted via a .github-style dir below
		_ = packed
	}
}

func TestScanHiddenDirectoriesTraverseButNotListed(t *testing.T) {
	dir := t.TempDir()
	// 遇到任意隐藏目录就整棵剪枝会让 .github 等目录里的有效文件无法通过名称搜索：
	// 隐藏目录不占候选，但其中的普通文件进入索引。
	mustWrite(t, filepath.Join(dir, ".github", "workflows", "ci.yml"), "x")
	mustWrite(t, filepath.Join(dir, ".github", "nested"), "x")
	s := newTestService(t)
	ctx := t.Context()

	packed, err := s.ListWorkspaceFilesRange(ctx, dir, 0, 1_000_000)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(packed, ".github/workflows/ci.yml") {
		t.Fatalf("files inside hidden dirs must be indexed: %q", packed)
	}
	if strings.Contains(packed, "directory\t.github") {
		t.Fatalf("hidden directories must not occupy candidates: %q", packed)
	}
}
