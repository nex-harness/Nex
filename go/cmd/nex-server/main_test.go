package main

// Tests for the entry point. Two things are worth pinning here and neither is covered by the
// library packages' own tests:
//
//   - the CLI surface (`--version`, `--help`, unknown argument), because build-server.sh's smoke
//     test and every release script read `--version` and a silent format change would break them;
//   - nodeCommand / searchDirs, because "which Node process did this deployment pick" is a
//     question an operator has to answer from a startup log, and getting it wrong produces an
//     upstream that never becomes ready.

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tylinux/nex/go/internal/server"
)

func TestVersionFlagPrintsTheInjectedVersion(t *testing.T) {
	var stderr bytes.Buffer
	if code := run([]string{"--version"}, &stderr); code != 0 {
		t.Fatalf("run(--version) = %d, want 0 (stderr %q)", code, stderr.String())
	}
	got := strings.TrimSpace(stderr.String())
	// The value internal/server reports through GET /api/server-info is the same variable, so a
	// release that pinned a version in -ldflags cannot report two different ones.
	if want := "nex-server " + server.Version; got != want {
		t.Errorf("--version printed %q, want %q", got, want)
	}
}

// TestVersionFlagIsNotBakedIn asserts the default is the development one, which is what a build
// without -ldflags must say. If someone hardcoded a version here, build-server.sh's "Go version
// equals the package.json version" check would keep passing while nothing was being injected.
func TestVersionFlagIsNotBakedIn(t *testing.T) {
	if server.Version != "0.0.0-dev" {
		t.Errorf("server.Version = %q, want the unversioned default 0.0.0-dev", server.Version)
	}
}

func TestHelpListsEveryEnvironmentVariable(t *testing.T) {
	var stderr bytes.Buffer
	if code := run([]string{"--help"}, &stderr); code != 0 {
		t.Fatalf("run(--help) = %d, want 0", code)
	}
	// Every variable listed here must exist in the TypeScript server too (D7). The list is the
	// documentation; docs/specs/go-rewrite/phase-2-usage.md is the prose version, and
	// TestUsageAndUsageDocAgree keeps the two from drifting.
	for _, name := range []string{
		"PORT",
		"NEX_SERVER_HOST",
		"HOST",
		"NEX_WEB_STATIC_ROOT",
		"NEX_SERVER_AUTH_TOKEN",
		"NEX_DATA_BASE_DIR",
		"NEX_SERVER_WORKSPACE",
		"NEX_SERVER_ID",
		"NEX_SERVER_NAME",
	} {
		if !strings.Contains(stderr.String(), name) {
			t.Errorf("--help does not document %s", name)
		}
	}
	// NEX_SERVER_ROOT is how the Node entry is found, which is worth stating on --help because it
	// is the one variable an operator may need to set on a release that is not running from its
	// own directory.
	if !strings.Contains(stderr.String(), "NEX_SERVER_ROOT") {
		t.Error("--help does not document NEX_SERVER_ROOT")
	}
}

func TestUnknownArgumentIsRejected(t *testing.T) {
	var stderr bytes.Buffer
	if code := run([]string{"--nope"}, &stderr); code != 2 {
		t.Fatalf("run(--nope) = %d, want 2 (usage error)", code)
	}
	if !strings.Contains(stderr.String(), "usage: nex-server") {
		t.Errorf("unknown argument did not print usage: %q", stderr.String())
	}
}

func TestNodeCommandPutsNodeInFrontOfABundleOnly(t *testing.T) {
	js := nodeCommand("/opt/nex/server/entry-http.js")
	if len(js) != 2 || !strings.HasSuffix(js[0], "node") && !strings.HasSuffix(js[0], "node.exe") {
		t.Errorf("nodeCommand(js) = %v, want [<node>, <entry>]", js)
	}
	if js[1] != "/opt/nex/server/entry-http.js" {
		t.Errorf("nodeCommand(js) lost the entry: %v", js)
	}
	// A SEA binary is its own interpreter, so putting `node` in front of it would fail to exec it.
	sea := nodeCommand("/opt/nex/runtime/nex-server")
	if len(sea) != 1 || sea[0] != "/opt/nex/runtime/nex-server" {
		t.Errorf("nodeCommand(sea) = %v, want the binary alone", sea)
	}
}

// TestSearchDirsStartWithTheServerRoot pins the ORDER of the search: an explicit NEX_SERVER_ROOT
// wins over whatever happens to sit next to the executable. Getting this backwards would run a
// developer's checkout against a release's binary.
func TestSearchDirsStartWithTheServerRoot(t *testing.T) {
	root := t.TempDir()
	t.Setenv("NEX_SERVER_ROOT", root)
	dirs := searchDirs()
	if len(dirs) == 0 || dirs[0] != filepath.Join(root, "runtime") {
		t.Fatalf("searchDirs() = %v, want %s first", dirs, filepath.Join(root, "runtime"))
	}
	// The root itself is second so a layout that puts the entry directly under the root is found.
	if len(dirs) < 2 || dirs[1] != root {
		t.Errorf("searchDirs() = %v, want %s second", dirs, root)
	}
}

func TestSearchDirsAreDeduplicated(t *testing.T) {
	root := t.TempDir()
	t.Setenv("NEX_SERVER_ROOT", root)
	dirs := searchDirs()
	seen := map[string]bool{}
	for _, dir := range dirs {
		if seen[dir] {
			t.Fatalf("searchDirs() repeated %s: %v", dir, dirs)
		}
		seen[dir] = true
	}
}

// TestResolveNodeEntryFindsTheLayoutItWasGiven installs each layout in a throwaway directory and
// requires the resolver to find it. Without this, a layout change in scripts/build-nex.mjs would
// only surface as a startup timeout on a machine with no Go tree.
func TestResolveNodeEntryFindsTheLayoutItWasGiven(t *testing.T) {
	for _, name := range []string{"entry-http.js", "server-core.js", "nex-server-http.cjs"} {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			runtimeDir := filepath.Join(dir, "runtime")
			mustMkdir(t, runtimeDir)
			want := filepath.Join(runtimeDir, name)
			mustWrite(t, want, "// node entry\n")
			t.Setenv("NEX_SERVER_ROOT", dir)

			got := resolveNodeEntry()
			if got != want {
				t.Fatalf("resolveNodeEntry() = %q, want %q", got, want)
			}
			if _, err := resolveNodeEntryChecked(got); err != nil {
				t.Fatalf("resolveNodeEntryChecked(%q) = %v, want nil", got, err)
			}
		})
	}
}

// TestResolveNodeEntryIgnoresADirectoryNamedLikeTheBinary: a layout with a per-binary directory
// (`runtime/nex-server/`) must not be mistaken for the SEA binary, or the child would be spawned
// on a directory and fail with an exec error that names no file.
//
// The assertion is "never the directory" rather than "nothing found", because searchDirs also
// looks next to the executable and at the repository root: in a checkout with @nex/server built,
// those legitimately find an entry, and that is the behaviour the previous test pins.
func TestResolveNodeEntryIgnoresADirectoryNamedLikeTheBinary(t *testing.T) {
	dir := t.TempDir()
	decoy := filepath.Join(dir, "runtime", "nex-server")
	mustMkdir(t, decoy)
	t.Setenv("NEX_SERVER_ROOT", dir)
	if got := resolveNodeEntry(); got == decoy {
		t.Fatalf("resolveNodeEntry() = %q, which is the directory %q", got, decoy)
	}
}

// TestResolveNodeEntryCheckedNamesWhatItSearched: the failure is the message an operator sees
// when the Go front cannot find its upstream, so it has to say where it looked.
func TestResolveNodeEntryCheckedNamesWhatItSearched(t *testing.T) {
	t.Setenv("NEX_SERVER_ROOT", filepath.Join(t.TempDir(), "empty-root"))
	_, err := resolveNodeEntryChecked("")
	if err == nil {
		t.Fatal("resolveNodeEntryChecked(\"\") = nil, want an error")
	}
	message := err.Error()
	for _, want := range []string{"no node upstream entry found", "empty-root", "entry-http.js"} {
		if !strings.Contains(message, want) {
			t.Errorf("error %q does not mention %q", message, want)
		}
	}
}

func TestListenAddrBracketsIPv6(t *testing.T) {
	cfg := config{host: "::1", port: 3030}
	if got := cfg.listenAddr(); got != "[::1]:3030" {
		t.Errorf("listenAddr() = %q, want [::1]:3030", got)
	}
	if got := (config{port: 80}).listenAddr(); got != ":80" {
		t.Errorf("listenAddr() with no host = %q, want :80", got)
	}
}

func mustMkdir(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(path, 0o755); err != nil {
		t.Fatal(err)
	}
}

func mustWrite(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o755); err != nil {
		t.Fatal(err)
	}
}
