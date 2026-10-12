package main

// nodeentry.go answers one question: which Node process does this Go server put behind itself?
//
// The answer is NOT an environment variable. D7 forbids adding one, and adding one would also be
// the wrong shape: the Node upstream is not a per-deployment setting the operator should have to
// restate on every machine that runs a release, it is a property of the release layout. The
// TypeScript `nex-server-cli` already decides it from a directory (`NEX_SERVER_ROOT`, written by
// the launcher in packages/nex-server-cli/src/runtime/stableLauncher.ts, plus the release's
// `runtime/` tree), so the Go front reads the SAME layout and the two agree by construction.
//
// The candidates, in order, each with the reason it is where it is:
//
//  1. NEX_SERVER_ROOT/runtime/server-core.js … a release's own tree.
//  2. <dir of this executable>/server/entry-http.js … the distribution layout scripts/build-nex.mjs
//     produces (server/, agent/, web/ next to each other).
//  3. <repo>/packages/server/dist/entry-http.js … a developer checkout, so `go run ./cmd/nex-server`
//     works after `pnpm --filter @nex/server build`.
//
// A SEA binary (`nex-server` with node baked in) is found by the same walk, because a SEA binary and
// the ESM bundle are both "an executable that boots the HTTP entry"; the only difference is whether
// `node` has to be put in front of it, which nodeCommand decides from the extension.
//
// If nothing is found the error says which paths were tried. Silently falling back to "node with no
// script" would start a REPL as the upstream and time out on the health probe with a message that
// points nowhere near the real problem.

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
)

// nodeEntryCandidates are the basenames a Node HTTP entry has been shipped under. The SEA build
// writes `nex-server` (scripts/build-sea.mjs); the tsup build writes `entry-http.js`
// (packages/server/tsup.config.ts); the distribution copies the latter into `server/`.
var nodeEntryCandidates = []string{"nex-server-http.cjs", "server-core.js", "entry-http.js"}

// resolveNodeEntry returns the first candidate that exists, or "" when none does. It never fails:
// the caller decides whether a missing entry is fatal (it is, at startup), so a probe that only
// resolves a path stays testable.
func resolveNodeEntry() string {
	for _, dir := range searchDirs() {
		for _, name := range nodeEntryCandidates {
			candidate := filepath.Join(dir, name)
			if isExecutableFile(candidate) {
				return candidate
			}
		}
		// A SEA binary is the release's own name, so it is checked as a plain file in the runtime
		// and distribution directories rather than as a fixed basename.
		for _, name := range []string{"nex-server", "nex-server.exe"} {
			candidate := filepath.Join(dir, name)
			if isExecutableFile(candidate) {
				return candidate
			}
		}
	}
	return ""
}

// resolveNodeEntryChecked is resolveNodeEntry with the failure message attached. The message names
// every directory that was searched, because "the Node upstream is missing" is only actionable if
// the operator can see what the Go front looked at.
func resolveNodeEntryChecked(found string) (string, error) {
	if found != "" {
		return found, nil
	}
	dirs := searchDirs()
	return "", fmt.Errorf(
		"no node upstream entry found (searched %s, names %s); "+
			"build @nex/server (pnpm --filter @nex/server build) or point NEX_SERVER_ROOT at a release",
		strings.Join(dirs, ", "),
		strings.Join(nodeEntryCandidates, ", "),
	)
}

// searchDirs is the ordered list of directories the entry may live in. It is a function rather
// than a package-level slice so a test can pin the working directory and the environment instead of
// mutating process state that another test reads.
func searchDirs() []string {
	var dirs []string
	add := func(dir string) {
		if dir == "" {
			return
		}
		dirs = appendUnique(dirs, filepath.Clean(dir))
	}

	// 1. A release: NEX_SERVER_ROOT is written by the launcher script the TypeScript CLI generates
	//    for systemd/launchd/Windows, so a machine already running a release has it set in the
	//    service unit. runtime/ is where releaseInstaller.ts puts the Node runtime and the server
	//    entry.
	if root := strings.TrimSpace(os.Getenv("NEX_SERVER_ROOT")); root != "" {
		add(filepath.Join(root, "runtime"))
		add(root)
	}
	// 2. The distribution layout: this binary sits next to server/, agent/ and web/.
	if exe, err := os.Executable(); err == nil {
		dir := filepath.Dir(exe)
		add(filepath.Join(dir, "server"))
		add(dir)
		// ../../server for a bin/ layout (bin/nex-server next to server/entry-http.js).
		add(filepath.Join(dir, "..", "server"))
	}
	// 3. A developer checkout: go run ./cmd/nex-server from go/ still resolves, because the repo
	//    root is two directories up from the built binary in a `go build -o go/dist/...` layout too.
	if repo := repoRoot(); repo != "" {
		add(filepath.Join(repo, "packages", "server", "dist"))
	}
	return dirs
}

// repoRoot walks up from the working directory looking for pnpm-workspace.yaml, which is the one
// file every layout of this repository has at its root. A -trimmed walk of four levels covers both
// `cd go && go run` and `go build` into go/dist from the repo root.
func repoRoot() string {
	dir, err := os.Getwd()
	if err != nil {
		return ""
	}
	for i := 0; i < 4; i++ {
		if _, statErr := os.Stat(filepath.Join(dir, "pnpm-workspace.yaml")); statErr == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}
	return ""
}

// isExecutableFile reports whether path is a regular file. A directory named `nex-server` (which a
// layout with a per-binary subdirectory has) must not be mistaken for the binary.
func isExecutableFile(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.Mode().IsRegular()
}

// appendUnique keeps the first occurrence, so the order searchDirs documents is the order the
// candidates are tried in and a duplicate cannot shadow an earlier hit.
func appendUnique(items []string, value string) []string {
	for _, item := range items {
		if item == value {
			return items
		}
	}
	return append(items, value)
}

// runtimeIsWindows exists so nodeCommand's fallback naming is one line and does not import runtime
// at the call site; it also gives a test a seam.
func runtimeIsWindows() bool { return runtime.GOOS == "windows" }

// execLookPath is exec.LookPath, aliased so a test can install a fake PATH-driven node without the
// process's real PATH being part of the assertion.
var execLookPath = exec.LookPath
