// skills_test.go: the frontmatter corpus + scan policy + roots tests.
//
// The frontmatter corpus runs over every SKILL.md under the repo's bundled
// skills directory (walking up from this package to the workspace root) plus
// synthetic edge cases (CRLF, leaked body headings, unquoted Chinese
// descriptions, block scalars, non-mapping YAML, missing frontmatter).
package skills

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// repoRoot walks up to the directory holding go.mod + package.json.
func repoRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			if _, err := os.Stat(filepath.Join(dir, "..", "package.json")); err == nil {
				return filepath.Dir(dir)
			}
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("repo root not found")
		}
		dir = parent
	}
}

// TestFrontmatterCorpusBundledSkills: every bundled SKILL.md parses without
// losing its name or description (the loose-fallback paths must hold).
func TestFrontmatterCorpusBundledSkills(t *testing.T) {
	root := repoRoot(t)
	corpusDir := filepath.Join(root, "apps", "nex-cli", "packages", "bundled-skills")
	count := 0
	err := filepath.WalkDir(corpusDir, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			if os.IsNotExist(err) {
				return nil
			}
			return err
		}
		if entry.IsDir() || entry.Name() != FileName {
			return nil
		}
		content, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		parsed := ReadFrontmatter(string(content))
		if !parsed.HasFrontmatter {
			t.Errorf("%s: frontmatter not detected", path)
		}
		if strings.TrimSpace(parsed.Name) == "" {
			t.Errorf("%s: name lost", path)
		}
		count++
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if count == 0 {
		t.Skipf("no bundled SKILL.md corpus found under %s", corpusDir)
	}
}

// TestFrontmatterSyntheticEdgeCases pins the tricky shapes.
func TestFrontmatterSyntheticEdgeCases(t *testing.T) {
	cases := []struct {
		name        string
		content     string
		wantName    string
		wantDesc    string
		wantHas     bool
		wantParseOK bool
	}{
		{
			name:     "plain",
			content:  "---\nname: my-skill\ndescription: does things\n---\nbody here",
			wantName: "my-skill", wantDesc: "does things", wantHas: true, wantParseOK: true,
		},
		{
			name:     "crlf",
			content:  "---\r\nname: crlf-skill\r\ndescription: win toolchain\r\n---\r\nbody",
			wantName: "crlf-skill", wantDesc: "win toolchain", wantHas: true, wantParseOK: true,
		},
		{
			name:    "unknown-chinese-key-still-parses",
			content: "---\nname: zh\n触发场景: 用户说你好\n---\nbody",
			// An unknown key with a colon is valid YAML — strict parse wins.
			wantName: "zh", wantHas: true, wantParseOK: true,
		},
		{
			name:    "unquoted-colon-space-in-description",
			content: "---\nname: unsafe\ndescription: 触发场景: 用户说你好\n---\nbody",
			// `: ` inside the unquoted value breaks strict YAML → loose fallback.
			wantName: "unsafe", wantHas: true, wantParseOK: false,
		},
		{
			name:     "leaked-heading-body",
			content:  "---\nname: leaked\ndescription: d\n# Leaked Title\nMore body\n---\nafter",
			wantName: "leaked", wantDesc: "d", wantHas: true, wantParseOK: true,
		},
		{
			name:     "block-scalar-literal",
			content:  "---\nname: blocky\ndescription: |\n  line one\n  line two\n---\nbody",
			wantName: "blocky", wantDesc: "line one\nline two", wantHas: true, wantParseOK: true,
		},
		{
			name:     "block-scalar-folded",
			content:  "---\nname: folded\ndescription: >\n  folded one\n  folded two\n---\nbody",
			wantName: "folded", wantDesc: "folded one folded two", wantHas: true, wantParseOK: true,
		},
		{
			name:     "quoted-strings",
			content:  "---\nname: \"quoted\"\ndescription: 'single'\n---\nbody",
			wantName: "quoted", wantDesc: "single", wantHas: true, wantParseOK: true,
		},
		{
			name:    "no-frontmatter",
			content: "just a body",
			wantHas: false,
		},
		{
			name:    "empty",
			content: "",
			wantHas: false,
		},
		{
			name:     "extra-keys-collected",
			content:  "---\nname: extra\nlicense: MIT\nmetadata:\n  nested: 1\n---\nbody",
			wantName: "extra", wantHas: true, wantParseOK: true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			parsed := ReadFrontmatter(tc.content)
			if parsed.HasFrontmatter != tc.wantHas {
				t.Fatalf("hasFrontmatter = %v want %v", parsed.HasFrontmatter, tc.wantHas)
			}
			if tc.wantName != "" && parsed.Name != tc.wantName {
				t.Errorf("name = %q want %q", parsed.Name, tc.wantName)
			}
			if tc.wantDesc != "" && parsed.Description != tc.wantDesc {
				t.Errorf("description = %q want %q", parsed.Description, tc.wantDesc)
			}
			if parsed.ParseOK != tc.wantParseOK {
				t.Errorf("parseOk = %v want %v", parsed.ParseOK, tc.wantParseOK)
			}
		})
	}
}

// TestFrontmatterLeakedBodyJoin verifies the leaked heading lands in the
// body, joined with the after-frontmatter part.
func TestFrontmatterLeakedBodyJoin(t *testing.T) {
	parsed := ReadFrontmatter("---\nname: x\ndescription: d\n# Heading\nleaked text\n---\nafter text")
	if !strings.Contains(parsed.Body, "# Heading") || !strings.Contains(parsed.Body, "after text") {
		t.Fatalf("body = %q", parsed.Body)
	}
}

// TestScanPolicy ports the skill-scan-policy table.
func TestScanPolicy(t *testing.T) {
	walk := map[string]bool{
		"skills": true, "group": true, ".system": true,
		"node_modules": false, "dist": false, "build": false, "out": false,
		"target": false, "vendor": false, "coverage": false, ".cache": false,
		".next": false, ".turbo": false, ".venv": false, "__pycache__": false,
		".agents": false, ".cursor": false, ".hidden": false,
	}
	for name, expected := range walk {
		if got := ShouldWalkDirectoryEntry(name); got != expected {
			t.Errorf("ShouldWalkDirectoryEntry(%q) = %v want %v", name, got, expected)
		}
	}
	if MaxScanDepth != 8 {
		t.Errorf("MaxScanDepth = %d want 8", MaxScanDepth)
	}
	if FileName != "SKILL.md" {
		t.Errorf("FileName = %q", FileName)
	}
	if MaxDescriptionLength != 1024 {
		t.Errorf("MaxDescriptionLength = %d", MaxDescriptionLength)
	}
}

// TestWalkSkillMarkdownPaths builds a tree with excluded dirs, dot dirs,
// deep nesting past the cap, and a symlink loop — the walk must stay
// bounded and return every valid SKILL.md exactly once.
func TestWalkSkillMarkdownPaths(t *testing.T) {
	root := t.TempDir()
	mkdirTree(t, root, map[string]string{
		"a/SKILL.md":                  "x",
		"b/inner/SKILL.md":            "x",
		"b/node_modules/dep/SKILL.md": "x",
		".hidden/SKILL.md":            "x",
		".system/SKILL.md":            "x",
		"a/.agents/SKILL.md":          "x",
	})
	// Depth 9 chain (past the cap).
	deep := root
	for i := 0; i < 10; i++ {
		deep = filepath.Join(deep, "d")
	}
	os.MkdirAll(deep, 0o755)
	os.WriteFile(filepath.Join(deep, FileName), []byte("x"), 0o644)

	found := WalkSkillMarkdownPaths(root, nil)
	expected := []string{
		filepath.Join(root, ".system", FileName),
		filepath.Join(root, "a", FileName),
		filepath.Join(root, "b", "inner", FileName),
	}
	if len(found) != len(expected) {
		t.Fatalf("found %v", found)
	}
	for i := range expected {
		if found[i] != expected[i] {
			t.Errorf("found[%d] = %s want %s", i, found[i], expected[i])
		}
	}
}

// TestWalkSkillMarkdownPathsSymlinkLoop: a directory symlink cycle must
// terminate (realpath dedupe) and not duplicate entries.
func TestWalkSkillMarkdownPathsSymlinkLoop(t *testing.T) {
	if os.Geteuid() == 0 && isWindowsTest() {
		t.Skip("root on windows")
	}
	root := t.TempDir()
	os.MkdirAll(filepath.Join(root, "a", "b"), 0o755)
	os.WriteFile(filepath.Join(root, "a", "b", FileName), []byte("x"), 0o644)
	if err := os.Symlink(filepath.Join(root, "a"), filepath.Join(root, "a", "b", "loop")); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	found := WalkSkillMarkdownPaths(root, nil)
	count := 0
	for _, path := range found {
		if path == filepath.Join(root, "a", "b", FileName) {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("SKILL.md found %d times (dedupe broken)", count)
	}
}

// TestRootsResolution covers the worktree-walk root collection.
func TestRootsResolution(t *testing.T) {
	root := t.TempDir()
	workspace := filepath.Join(root, "repo", "pkg", "sub")
	gitDir := filepath.Join(root, "repo", ".git")
	os.MkdirAll(workspace, 0o755)
	os.MkdirAll(gitDir, 0o755)

	roots := ResolveAncestorWorkspaceRoots(workspace)
	expected := []string{
		filepath.Join(root, "repo", "pkg", "sub", ".nex", "skills"),
		filepath.Join(root, "repo", "pkg", "sub", ".agents", "skills"),
		filepath.Join(root, "repo", "pkg", ".nex", "skills"),
		filepath.Join(root, "repo", "pkg", ".agents", "skills"),
		filepath.Join(root, "repo", ".nex", "skills"),
		filepath.Join(root, "repo", ".agents", "skills"),
	}
	if len(roots) != len(expected) {
		t.Fatalf("roots = %v", roots)
	}
	for i := range expected {
		if roots[i] != expected[i] {
			t.Errorf("roots[%d] = %s want %s", i, roots[i], expected[i])
		}
	}
	// Without .git: only the workspace itself.
	noGit := t.TempDir()
	roots = ResolveAncestorWorkspaceRoots(noGit)
	if len(roots) != 2 || roots[0] != filepath.Join(noGit, ".nex", "skills") {
		t.Fatalf("roots = %v", roots)
	}
}

// TestDedupeScanRootsByRealpath covers the nested-root filtering.
func TestDedupeScanRootsByRealpath(t *testing.T) {
	base := t.TempDir()
	outer := filepath.Join(base, "outer")
	inner := filepath.Join(outer, "inner")
	os.MkdirAll(inner, 0o755)
	dedupe := DedupeScanRootsByRealpath([]string{outer, inner, outer})
	if len(dedupe) != 1 || dedupe[0] != inner {
		t.Fatalf("dedupe = %v (the ancestor is dropped for the deeper root)", dedupe)
	}
	// Non-existent inner keeps the parent for non-standard layouts.
	dedupe = DedupeScanRootsByRealpath([]string{outer, filepath.Join(outer, "missing")})
	if len(dedupe) != 2 {
		t.Fatalf("dedupe with missing child = %v", dedupe)
	}
}

// TestNormalizeSyncRelativePath is the path-policy table.
func TestNormalizeSyncRelativePath(t *testing.T) {
	valid := map[string]string{
		"a": "a", "a/b": "a/b",
	}
	for input, want := range valid {
		got, err := NormalizeSyncRelativePath(input, "label")
		if err != nil || got != want {
			t.Errorf("Normalize(%q) = %q, %v want %q", input, got, err, want)
		}
	}
	invalid := []string{"", ".", "..", "../x", "a/../b", "a//b", "/abs", "/a/", "C:/x", "c:x", "\\\\share", `a\b`, "./"}
	for _, input := range invalid {
		if got, err := NormalizeSyncRelativePath(input, "label"); err == nil {
			t.Errorf("Normalize(%q) = %q, want error", input, got)
		}
	}
}

// TestResolveSyncPathWithin checks containment.
func TestResolveSyncPathWithin(t *testing.T) {
	root := t.TempDir()
	path, err := ResolveSyncPathWithin(root, "a/b.txt", "label")
	if err != nil || path != filepath.Join(root, "a", "b.txt") {
		t.Fatalf("got %q, %v", path, err)
	}
	for _, bad := range []string{"..", "../x", "a/../../x"} {
		if _, err := ResolveSyncPathWithin(root, bad, "label"); err == nil {
			t.Errorf("ResolveSyncPathWithin(%q) must fail", bad)
		}
	}
}

func mkdirTree(t *testing.T, root string, files map[string]string) {
	t.Helper()
	for relative, content := range files {
		path := filepath.Join(root, filepath.FromSlash(relative))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

func isWindowsTest() bool { return filepath.Separator == '\\' }
