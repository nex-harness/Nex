// Package runnerupdate decides whether a connected runner is out of date and
// tracks the self-update handshake (docs/next/runner-update.md decisions 4-5).
//
// Ownership: the server owns "what is latest", read from the per-platform
// bundle files. The runner owns its files and the swap; the supervisor owns the
// restart. This package only reads bundle metadata and records per-runner state.
package runnerupdate

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/nex-harness/nex/server/internal/runners"
)

// Platforms is the fixed set of bundle directory names. The bundle endpoint
// validates against this set, so a platform value never reaches the filesystem
// unchecked.
var Platforms = []string{
	"linux-x64",
	"linux-arm64",
	"darwin-arm64",
	"darwin-x64",
	"win32-x64",
	"win32-arm64",
}

var platformSet = func() map[string]struct{} {
	out := make(map[string]struct{}, len(Platforms))
	for _, platform := range Platforms {
		out[platform] = struct{}{}
	}
	return out
}()

// KnownPlatform reports whether platform is one of the staged bundle names.
func KnownPlatform(platform string) bool {
	_, ok := platformSet[platform]
	return ok
}

// PlatformOf maps a runner hello's os/arch to the bundle directory name.
// os is process.platform (linux | darwin | win32) and arch is process.arch
// (x64 | arm64). Unknown combinations return "" (no bundle can match).
func PlatformOf(osName, arch string) string {
	osName = strings.ToLower(strings.TrimSpace(osName))
	arch = strings.ToLower(strings.TrimSpace(arch))
	switch arch {
	case "x64", "x86_64", "amd64":
		arch = "x64"
	case "arm64", "aarch64":
		arch = "arm64"
	default:
		return ""
	}
	switch osName {
	case "windows":
		osName = "win32"
	case "linux", "darwin", "win32":
	default:
		return ""
	}
	platform := osName + "-" + arch
	if !KnownPlatform(platform) {
		return ""
	}
	return platform
}

// buildJSON is the staged metadata written by scripts/bundle.mjs.
type buildJSON struct {
	BuildID string `json:"buildId"`
	Version string `json:"version"`
	BuiltAt string `json:"builtAt"`
}

// cacheEntry is one parsed build.json keyed by the file's identity. The build
// file is rewritten by a deploy, which changes its mtime, so mtime+size is a
// sufficient cache key without a watcher.
type cacheEntry struct {
	modTime time.Time
	size    int64
	buildID string
}

// Bundles reads <Dir>/<platform>/build.json. Dir may be empty (no bundles
// staged); every lookup then reports "not known" instead of an error.
type Bundles struct {
	Dir string

	mu    sync.Mutex
	cache map[string]cacheEntry
}

// NewBundles returns a bundle source rooted at dir.
func NewBundles(dir string) *Bundles {
	return &Bundles{Dir: strings.TrimSpace(dir), cache: map[string]cacheEntry{}}
}

// Latest returns the current buildId staged for platform. It reports false when
// the directory is unset, the platform is unknown, or build.json is missing or
// unreadable. The result is cached until the file's mtime/size changes.
func (b *Bundles) Latest(platform string) (string, bool) {
	if b == nil || b.Dir == "" || !KnownPlatform(platform) {
		return "", false
	}
	file := filepath.Join(b.Dir, platform, "build.json")
	info, err := os.Stat(file)
	if err != nil || info.IsDir() {
		b.mu.Lock()
		delete(b.cache, platform)
		b.mu.Unlock()
		return "", false
	}

	b.mu.Lock()
	if cached, ok := b.cache[platform]; ok && cached.modTime.Equal(info.ModTime()) && cached.size == info.Size() {
		b.mu.Unlock()
		return cached.buildID, cached.buildID != ""
	}
	b.mu.Unlock()

	raw, err := os.ReadFile(file)
	if err != nil {
		return "", false
	}
	var parsed buildJSON
	if err := json.Unmarshal(raw, &parsed); err != nil {
		return "", false
	}
	buildID := strings.TrimSpace(parsed.BuildID)
	// A dev build is never a comparable target: it means the bundle was staged
	// from a working tree, so every runner would look outdated forever.
	if buildID == "dev" {
		buildID = ""
	}

	b.mu.Lock()
	if b.cache == nil {
		b.cache = map[string]cacheEntry{}
	}
	b.cache[platform] = cacheEntry{modTime: info.ModTime(), size: info.Size(), buildID: buildID}
	b.mu.Unlock()
	return buildID, buildID != ""
}

// LatestFor returns the latest build for the platform a runner reports.
func (b *Bundles) LatestFor(info runners.Info) (string, bool) {
	return b.Latest(PlatformOf(info.OS, info.Arch))
}

// Outdated reports whether a runner runs a different build than the bundle for
// its platform. A runner with no buildId (legacy), a "dev" build, an unknown
// platform or a platform with no staged bundle is never outdated — there is
// nothing to send it.
func (b *Bundles) Outdated(info runners.Info) bool {
	buildID := strings.TrimSpace(info.BuildID)
	if buildID == "" || buildID == "dev" {
		return false
	}
	latest, ok := b.LatestFor(info)
	return ok && latest != buildID
}

// HasCapability reports whether the runner advertised one hello capability.
func HasCapability(capabilities []string, name string) bool {
	for _, capability := range capabilities {
		if strings.TrimSpace(capability) == name {
			return true
		}
	}
	return false
}
