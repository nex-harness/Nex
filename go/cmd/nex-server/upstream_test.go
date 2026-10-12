package main

// upstream_test.go pins the one piece of wiring that is load-bearing and invisible: the Node
// upstream must NOT share this server's data base.
//
// WHY, because this is the least obvious decision in the entry point. entry-http.ts always passes
// `sessionsFilePath: join(getAppConfigDir(), "web-sessions.json")` when a token is set, so an
// upstream sharing the data base would open the very file this front just wrote a browser's session
// into — under a DIFFERENT token, because internal/adapters/node generates one per start. The
// TypeScript store discards every session whose fingerprint does not match its own ("token 变更
// （指纹不同）时丢弃全部会话"), which is correct for a rotation and catastrophic here: the upstream's
// next write would log every user out, at a moment unrelated to anything they did.
//
// The symptom was found by the acceptance list, not by reasoning: item 3 ("a restart keeps
// sessions") failed intermittently, and the sessions file's idHash CHANGED across the restart,
// which is only possible if another process wrote it.

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestUpstreamDataDirIsSeparateFromTheServersOwn is the assertion: the upstream's data base is a
// subdirectory, never the data base itself.
func TestUpstreamDataDirIsSeparateFromTheServersOwn(t *testing.T) {
	base := t.TempDir()
	t.Setenv("NEX_DATA_BASE_DIR", base)

	dir, err := upstreamDataDir()
	if err != nil {
		t.Fatalf("upstreamDataDir() = %v", err)
	}
	if dir == base {
		t.Fatal("upstreamDataDir() returned the server's own data base; the two processes would " +
			"write one web-sessions.json under two different tokens")
	}
	if !strings.HasPrefix(dir, base) {
		t.Errorf("upstreamDataDir() = %q, want it under the configured data base %q", dir, base)
	}
	if info, err := os.Stat(dir); err != nil || !info.IsDir() {
		t.Errorf("upstreamDataDir() did not create %q: %v", dir, err)
	}
	// 0700: the upstream's config dir holds a materialized provider config, which is the same
	// material the front's own config dir holds and gets the same treatment.
	info, err := os.Stat(dir)
	if err == nil && info.Mode().Perm() != 0o700 {
		t.Errorf("upstream data dir mode = %v, want 0700", info.Mode().Perm())
	}
}

// TestUpstreamDataDirIsStableAcrossCalls: the path must not contain a PID or a timestamp. A path
// that changed per start would leave a new directory behind on every restart, and an operator
// looking for the upstream's state would have to guess which one is live.
func TestUpstreamDataDirIsStableAcrossCalls(t *testing.T) {
	t.Setenv("NEX_DATA_BASE_DIR", t.TempDir())
	first, err := upstreamDataDir()
	if err != nil {
		t.Fatal(err)
	}
	second, err := upstreamDataDir()
	if err != nil {
		t.Fatal(err)
	}
	if first != second {
		t.Errorf("upstreamDataDir() returned %q then %q; the path must be stable", first, second)
	}
}

// TestUpstreamDataDirIsNotTheSessionsFile: the whole point is that the upstream's data base cannot
// BE the sessions file, so assert the two paths are unrelated — different names, and the upstream's
// is strictly below the front's config dir so no configuration of the front can name it.
func TestUpstreamDataDirIsNotTheSessionsFile(t *testing.T) {
	base := t.TempDir()
	t.Setenv("NEX_DATA_BASE_DIR", base)

	dir, err := upstreamDataDir()
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Base(dir) == "web-sessions.json" {
		t.Fatalf("upstreamDataDir() = %q, which is the sessions file itself", dir)
	}
	if filepath.Dir(dir) == dir {
		t.Fatalf("upstreamDataDir() = %q is its own parent", dir)
	}
	if !strings.HasPrefix(filepath.Dir(dir), base) {
		t.Errorf("the upstream data base %q is not directly under %q", dir, base)
	}
}
