package runnerupdate

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/nex-harness/nex/server/internal/runners"
)

// writeBuild stages one build.json for a platform and returns its path.
func writeBuild(t *testing.T, dir, platform, buildID string) string {
	t.Helper()
	target := filepath.Join(dir, platform)
	if err := os.MkdirAll(target, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	file := filepath.Join(target, "build.json")
	body := `{"buildId":"` + buildID + `","version":"1.2.3","builtAt":"2026-01-01T00:00:00Z"}`
	if err := os.WriteFile(file, []byte(body), 0o644); err != nil {
		t.Fatalf("write build.json: %v", err)
	}
	return file
}

func TestLatestReadsAndReportsMissing(t *testing.T) {
	dir := t.TempDir()
	bundles := NewBundles(dir)

	if _, ok := bundles.Latest("linux-x64"); ok {
		t.Fatal("missing build.json must report not known")
	}

	writeBuild(t, dir, "linux-x64", "abc123")
	buildID, ok := bundles.Latest("linux-x64")
	if !ok || buildID != "abc123" {
		t.Fatalf("Latest = %q,%v, want abc123,true", buildID, ok)
	}

	// Unknown platforms never touch the filesystem.
	if _, ok := bundles.Latest("../etc"); ok {
		t.Fatal("unknown platform must report not known")
	}
	// An unset dir disables lookups entirely.
	if _, ok := NewBundles("").Latest("linux-x64"); ok {
		t.Fatal("empty dir must report not known")
	}
}

// The cache is keyed by mtime+size: identical bytes are served from memory, and
// a rewritten file (new mtime) is re-read.
func TestLatestCachesByMtime(t *testing.T) {
	dir := t.TempDir()
	file := writeBuild(t, dir, "darwin-arm64", "old-build")
	info, err := os.Stat(file)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	// Pin a distinctive mtime so the rewrite below is unambiguous.
	pinned := time.Unix(1_700_000_000, 0)
	if err := os.Chtimes(file, pinned, pinned); err != nil {
		t.Fatalf("chtimes: %v", err)
	}
	info, err = os.Stat(file)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}

	bundles := NewBundles(dir)
	if buildID, ok := bundles.Latest("darwin-arm64"); !ok || buildID != "old-build" {
		t.Fatalf("Latest = %q,%v", buildID, ok)
	}

	// Same size + same mtime: the cached value wins even though the bytes changed.
	sameSize := `{"buildId":"new-build","version":"1.2.3","builtAt":"2026-01-01T00:00:00Z"}`
	if len(sameSize) != int(info.Size()) {
		t.Fatalf("test fixture changed size: %d vs %d", len(sameSize), info.Size())
	}
	if err := os.WriteFile(file, []byte(sameSize), 0o644); err != nil {
		t.Fatalf("rewrite: %v", err)
	}
	if err := os.Chtimes(file, pinned, pinned); err != nil {
		t.Fatalf("chtimes: %v", err)
	}
	if buildID, _ := bundles.Latest("darwin-arm64"); buildID != "old-build" {
		t.Fatalf("cache miss: got %q, want the cached old-build", buildID)
	}

	// A deploy changes the mtime: the new build must be visible immediately.
	later := pinned.Add(2 * time.Second)
	if err := os.Chtimes(file, later, later); err != nil {
		t.Fatalf("chtimes: %v", err)
	}
	if buildID, ok := bundles.Latest("darwin-arm64"); !ok || buildID != "new-build" {
		t.Fatalf("Latest after rewrite = %q,%v, want new-build,true", buildID, ok)
	}
}

func TestLatestTreatsDevAsUnknown(t *testing.T) {
	dir := t.TempDir()
	writeBuild(t, dir, "linux-arm64", "dev")
	if _, ok := NewBundles(dir).Latest("linux-arm64"); ok {
		t.Fatal("a dev build is never a comparable latest build")
	}
}

func TestPlatformOf(t *testing.T) {
	cases := []struct{ osName, arch, want string }{
		{"linux", "x64", "linux-x64"},
		{"linux", "arm64", "linux-arm64"},
		{"darwin", "arm64", "darwin-arm64"},
		{"darwin", "x64", "darwin-x64"},
		{"win32", "x64", "win32-x64"},
		{"win32", "arm64", "win32-arm64"},
		// process.platform/arch spellings and casing are normalized.
		{"Darwin", "ARM64", "darwin-arm64"},
		{"windows", "x64", "win32-x64"},
		{"linux", "amd64", "linux-x64"},
		{"linux", "aarch64", "linux-arm64"},
		// Unknown combinations have no bundle.
		{"freebsd", "x64", ""},
		{"linux", "riscv64", ""},
		{"", "", ""},
	}
	for _, testCase := range cases {
		if got := PlatformOf(testCase.osName, testCase.arch); got != testCase.want {
			t.Fatalf("PlatformOf(%q,%q) = %q, want %q", testCase.osName, testCase.arch, got, testCase.want)
		}
	}
}

func TestOutdatedRules(t *testing.T) {
	dir := t.TempDir()
	writeBuild(t, dir, "linux-x64", "new")
	bundles := NewBundles(dir)

	info := func(buildID string) runners.Info {
		return runners.Info{RunnerID: "r1", OS: "linux", Arch: "x64", BuildID: buildID}
	}

	if !bundles.Outdated(info("old")) {
		t.Fatal("a different build must be outdated")
	}
	if bundles.Outdated(info("new")) {
		t.Fatal("the staged build must not be outdated")
	}
	if bundles.Outdated(info("")) {
		t.Fatal("a legacy runner without buildId must not be outdated")
	}
	if bundles.Outdated(info("dev")) {
		t.Fatal("a dev build must not be outdated")
	}
	if bundles.Outdated(runners.Info{RunnerID: "r1", OS: "freebsd", Arch: "x64", BuildID: "old"}) {
		t.Fatal("an unknown platform must not be outdated")
	}
	// No staged bundle for this platform: nothing to compare against.
	if NewBundles(t.TempDir()).Outdated(info("old")) {
		t.Fatal("a platform without build.json must not be outdated")
	}
}

func TestHasCapability(t *testing.T) {
	caps := []string{"runner-workspaces", " self-update "}
	if !HasCapability(caps, CapabilitySelfUpdate) {
		t.Fatal("capability with surrounding spaces must match")
	}
	if HasCapability(caps, "search") {
		t.Fatal("missing capability matched")
	}
	if HasCapability(nil, CapabilitySelfUpdate) {
		t.Fatal("nil capabilities matched")
	}
}
