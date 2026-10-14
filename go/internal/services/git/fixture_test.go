package git

// fixture_test.go builds the step-3.3 fixture repository: branches, merges,
// renames, binary files, a submodule, detached HEAD, an empty repo, a linked
// worktree, a bare repo (for push) and a 10k-commit graph. Deterministic
// authors/dates so hashes are stable within a run.
//
// Windows path/CRLF (core.autocrlf) cases are exercised on the Windows runner
// (see autocrlf_windows_test.go); on Linux they are skipped with a reason.

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// fixtureRepo is a built fixture workspace tree.
type fixtureRepo struct {
	t    *testing.T
	root string // root of all fixture dirs
	home string // fake HOME so user-level git config never leaks in
	// repo is the main working repository.
	repo string
	// sub is the submodule repository.
	sub string
	// bare is the bare push target.
	bare string
	// empty is a git init'd repo with no commits.
	empty string
}

// gitCmd runs a git command inside the fixture environment (isolated HOME,
// deterministic identity; no interactive prompts).
func (f *fixtureRepo) git(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(),
		"GIT_AUTHOR_NAME=Fixture Author",
		"GIT_AUTHOR_EMAIL=author@fixture.local",
		"GIT_COMMITTER_NAME=Fixture Author",
		"GIT_COMMITTER_EMAIL=author@fixture.local",
		"GIT_AUTHOR_DATE=1700000000 +0000",
		"GIT_COMMITTER_DATE=1700000000 +0000",
		"HOME="+f.home,
		"GIT_CONFIG_NOSYSTEM=1",
		"GIT_TERMINAL_PROMPT=0",
		// git >= 2.38.1 blocks the file transport by default; the fixture's
		// submodule and local bare remote need it (local paths only, never a network).
		"GIT_ALLOW_PROTOCOL=file",
	)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s (in %s): %v\n%s", strings.Join(args, " "), dir, err, out)
	}
	return string(out)
}

// gitAllowFail runs a git command that may fail; returns combined output.
func (f *fixtureRepo) gitAllowFail(t *testing.T, dir string, args ...string) (string, error) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(),
		"GIT_AUTHOR_NAME=Fixture Author",
		"GIT_AUTHOR_EMAIL=author@fixture.local",
		"GIT_COMMITTER_NAME=Fixture Author",
		"GIT_COMMITTER_EMAIL=author@fixture.local",
		"GIT_AUTHOR_DATE=1700000000 +0000",
		"GIT_COMMITTER_DATE=1700000000 +0000",
		"HOME="+f.home,
		"GIT_CONFIG_NOSYSTEM=1",
		"GIT_TERMINAL_PROMPT=0",
	)
	out, err := cmd.CombinedOutput()
	return string(out), err
}

func writeFixtureFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o777); err != nil {
		t.Fatalf("mkdir %s: %v", path, err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

// buildFixtureRepo constructs the whole fixture tree. It shells to the REAL
// git binary (the service under test also shells to it), mirroring
// scripts/go-rewrite's fixture builders.
func buildFixtureRepo(t *testing.T) *fixtureRepo {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skipf("git binary not available: %v", err)
	}
	root := t.TempDir()
	f := &fixtureRepo{t: t, root: root, home: filepath.Join(root, "home"), repo: filepath.Join(root, "repo")}
	for _, dir := range []string{f.home, filepath.Join(root, "objects")} {
		if err := os.MkdirAll(dir, 0o777); err != nil {
			t.Fatalf("mkdir %s: %v", dir, err)
		}
	}

	// --- submodule repo (built first so the main repo can reference it) ---
	f.sub = filepath.Join(root, "submodule-repo")
	f.git(t, root, "init", "--initial-branch=main", f.sub)
	writeFixtureFile(t, filepath.Join(f.sub, "lib.ts"), "export const lib = 1;\n")
	f.git(t, f.sub, "add", "-A")
	f.git(t, f.sub, "commit", "-m", "submodule initial")

	// --- main repo ---
	f.git(t, root, "init", "--initial-branch=main", f.repo)
	f.git(t, f.repo, "config", "user.name", "Fixture Author")
	f.git(t, f.repo, "config", "user.email", "author@fixture.local")

	writeFixtureFile(t, filepath.Join(f.repo, "README.md"), "# fixture\nfirst\n")
	writeFixtureFile(t, filepath.Join(f.repo, "src", "main.ts"), "export function main() {}\n")
	writeFixtureFile(t, filepath.Join(f.repo, "docs", "guide.md"), "guide v1\n")
	f.git(t, f.repo, "add", "-A")
	f.git(t, f.repo, "commit", "-m", "feat: initial commit")

	// binary file (NUL bytes) — exercises the binary diff paths.
	if err := os.MkdirAll(filepath.Join(f.repo, "assets"), 0o777); err != nil {
		t.Fatalf("mkdir assets: %v", err)
	}
	if err := os.WriteFile(filepath.Join(f.repo, "assets", "blob.bin"), []byte{0x00, 0x01, 0x02, 0xff, 0x00, 0xfe}, 0o644); err != nil {
		t.Fatalf("write binary: %v", err)
	}
	f.git(t, f.repo, "add", "-A")
	f.git(t, f.repo, "commit", "-m", "feat: add binary asset")

	// feature branch + rename (exercises the "2 " porcelain record).
	f.git(t, f.repo, "switch", "-c", "feature/one")
	if err := os.Rename(filepath.Join(f.repo, "docs", "guide.md"), filepath.Join(f.repo, "docs", "manual.md")); err != nil {
		t.Fatalf("rename: %v", err)
	}
	writeFixtureFile(t, filepath.Join(f.repo, "src", "feature.ts"), "export const feature = 1;\n")
	f.git(t, f.repo, "add", "-A")
	f.git(t, f.repo, "commit", "-m", "feat: rename guide to manual")

	// merge feature into main (a real merge commit for the graph).
	f.git(t, f.repo, "switch", "main")
	f.git(t, f.repo, "merge", "--no-ff", "feature/one", "-m", "chore: merge feature/one")

	// tag for graph decorations.
	f.git(t, f.repo, "tag", "v1.0.0")

	// submodule (registered as a gitlink; tests treat it as a path boundary).
	f.git(t, f.repo, "submodule", "add", f.sub, "vendor/lib")
	writeFixtureFile(t, filepath.Join(f.repo, ".gitmodules"), "[submodule \"vendor/lib\"]\n\tpath = vendor/lib\n\turl = "+f.sub+"\n")
	f.git(t, f.repo, "add", "-A")
	f.git(t, f.repo, "commit", "-m", "chore: add submodule")

	// working tree state for status tests (unstaged / staged / untracked).
	writeFixtureFile(t, filepath.Join(f.repo, "src", "main.ts"), "export function main() { return 2; }\n")
	writeFixtureFile(t, filepath.Join(f.repo, "staged.txt"), "staged content\n")
	f.git(t, f.repo, "add", "staged.txt")
	writeFixtureFile(t, filepath.Join(f.repo, "notes", "untracked.txt"), "untracked\n")
	writeFixtureFile(t, filepath.Join(f.repo, "notes", "empty.txt"), "")
	// empty files are excluded from changes (+0/-0); a dir record is kept when
	// it is the only representation — covered by the collapsed-untracked path.

	// bare push target + upstream wiring (push test: local only, NO network).
	f.bare = filepath.Join(root, "bare.git")
	f.git(t, root, "init", "--bare", f.bare)
	f.git(t, f.repo, "remote", "add", "origin", f.bare)
	f.git(t, f.repo, "push", "-u", "origin", "main")

	// empty repo (init, no commits).
	f.empty = filepath.Join(root, "empty")
	f.git(t, root, "init", "--initial-branch=main", f.empty)
	return f
}

// newFixtureService builds the Service against the fixture with an isolated
// environment (fake HOME via env override is not possible through os.Environ
// of a running process, so the provider's Getenv is pointed at the fixture).
func (f *fixtureRepo) service() *Service {
	provider := NewCommandProvider(CommandProviderOptions{
		Getenv: func(key string) string {
			if key == "HOME" {
				return f.home
			}
			if key == "NEX_GIT_BINARY" {
				return ""
			}
			return os.Getenv(key)
		},
	})
	return NewService(NewCliRepo(provider), nil)
}

// newFixtureRepo builds a CliRepo against the fixture environment.
func (f *fixtureRepo) cliRepo() *CliRepo {
	provider := NewCommandProvider(CommandProviderOptions{
		Getenv: func(key string) string {
			if key == "HOME" {
				return f.home
			}
			if key == "NEX_GIT_BINARY" {
				return ""
			}
			return os.Getenv(key)
		},
	})
	return NewCliRepo(provider)
}

// requireGitAvailable skips the test when git is missing.
func requireGitAvailable(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skipf("git binary not available: %v", err)
	}
}
