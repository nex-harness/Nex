// Package webroot is the filesystem adapter for the nex-server static file handler.
//
// It exists so go/internal/server can resolve and read files without importing "os"
// (enforced by internal/archcheck). The interface is deliberately the minimum
// resolveStaticFile in packages/server/src/http.ts needs: join, stat, read. Path
// semantics — including the inside-the-root guard — stay in the server package,
// because they are the security-relevant logic rather than the I/O.
package webroot

import (
	"os"
	"path/filepath"
)

// Root is a resolved static root. Every path it hands back has been joined and cleaned
// with the same rules node:path's resolve() applies.
type Root struct {
	path string
}

// New resolves root the way `resolve(staticRoot)` does: a relative path is resolved
// against the process working directory, and "." components are collapsed. An empty or
// blank root is rejected because the TypeScript server skips static serving entirely in
// that case.
func New(root string) (Root, bool) {
	if root == "" || isBlank(root) {
		return Root{}, false
	}
	return Root{path: filepath.Clean(root)}, true
}

// Path returns the cleaned absolute-or-relative root.
func (r Root) Path() string { return r.path }

// Join resolves a root-relative path exactly like path.resolve(root, rel): an absolute rel
// replaces the root, otherwise it is appended and the whole thing cleaned. Callers must
// still check that the result stays inside the root.
func (r Root) Join(rel string) string {
	if filepath.IsAbs(rel) {
		return filepath.Clean(rel)
	}
	return filepath.Clean(filepath.Join(r.path, rel))
}

// Kind reports what lives at path.
type Kind int

const (
	KindMissing Kind = iota
	KindFile
	KindDirectory
	KindOther
)

// Stat classifies path. A stat error other than "not exists" (a permission problem, for
// instance) is reported as KindMissing, matching the TypeScript, whose try/catch around
// stat() treats every failure as a cache miss and falls through to the SPA fallback.
func Stat(path string) Kind {
	info, err := os.Stat(path)
	if err != nil {
		return KindMissing
	}
	if info.IsDir() {
		return KindDirectory
	}
	if info.Mode().IsRegular() {
		return KindFile
	}
	return KindOther
}

// Read returns the whole file. The TypeScript reads the entire buffer with readFile and
// then sets Content-Length from it, so there is no streaming or range support to mirror.
func Read(path string) ([]byte, error) {
	return os.ReadFile(path) //nolint:gosec // path is validated by the caller's inside-root guard
}

// isBlank reports a string of only whitespace. path.Clean would turn "   " into "   " and
// the TypeScript's `staticRoot?.trim()` test would have skipped serving entirely, so the
// check happens before any filesystem call.
func isBlank(value string) bool {
	for _, r := range value {
		switch r {
		case ' ', '\t', '\n', '\r', '\v', '\f':
		default:
			return false
		}
	}
	return true
}
