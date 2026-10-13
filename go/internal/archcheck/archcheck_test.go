// Package archcheck enforces the repository's architecture rules on the Go tree
// as ordinary tests, so they run with `go test ./...` and need no extra linter.
//
// Rules (from AGENTS.md and apps/nex-cli/AGENTS.md):
//   - external I/O is confined to adapters and entry points;
//   - a source file stays under 400 lines.
package archcheck

import (
	"bufio"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

const maxFileLines = 400

// ioPackages are the standard-library packages that reach outside the process.
// Business packages must depend on interfaces instead.
var ioPackages = map[string]bool{
	"os/exec": true, "net": true, "net/http": true, "database/sql": true,
}

// ioPathAllowed reports whether a package path may import ioPackages.
// internal/protocol is allowed none of them on purpose: it works on io.Reader
// and io.Writer so it stays testable; only cmd/ wires real streams.
//
// internal/server and its subpackages are the other boundary: they *are* the HTTP
// shell (static files, GET /api/server-info, web authentication), so net/http is their
// job rather than something to abstract away. Their filesystem and environment access
// still goes through adapters (internal/adapters/webroot, internal/adapters/serverenv,
// internal/adapters/webauthfs), which is why they are absent from
// osAllowedOutsideAdapters.
//
// internal/gateway is the same kind of boundary as internal/server (step 2.8): it dials the
// Node upstream, reverse-proxies the routes Go does not own, and reads the routing table.
// Wrapping those in interfaces would only move the net/http calls somewhere less honest.
// Its channels/ subpackage stays pure — it is a name list — and internal/rpc remains
// transport-agnostic, which is what gate G2.11 checks separately.
func ioPathAllowed(relDir string) bool {
	return strings.HasPrefix(relDir, "cmd/") ||
		strings.HasPrefix(relDir, "internal/adapters/") ||
		relDir == "internal/server" ||
		strings.HasPrefix(relDir, "internal/server/") ||
		relDir == "internal/gateway"
}

// osAllowedOutsideAdapters lists packages that may import "os" even though they
// are not adapters, with the reason. Keep this list short.
var osAllowedOutsideAdapters = map[string]string{
	"internal/archcheck": "reads the source tree to check it",
	// internal/gateway reads the gateway-routes.json routing table itself (step 2.8). The table
	// is the only persistent state the gateway owns and it is read exactly once at startup, so an
	// adapter would be a wrapper around os.ReadFile with one call site.
	"internal/gateway": "reads the gateway-routes.json routing table",
	// internal/services/paths is the paths.ts port (step 3.1): its whole job is
	// resolving NEX_DATA_BASE_DIR/HOME into directories, captured once at
	// construction (NewFromProcess) exactly like the TS module-level consts.
	// Reading env/home is the package's contract, not a side effect to hide
	// behind an adapter; New(env, home) keeps the logic pure for tests.
	"internal/services/paths": "owns the paths.ts env/home resolution",
}

func goFiles(t *testing.T, root string) []string {
	t.Helper()
	var files []string
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() && (d.Name() == "testdata" || d.Name() == "vendor") {
			return fs.SkipDir
		}
		if !d.IsDir() && strings.HasSuffix(path, ".go") {
			files = append(files, path)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return files
}

func moduleRoot(t *testing.T) string {
	t.Helper()
	dir, _ := os.Getwd()
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("go.mod not found")
		}
		dir = parent
	}
}

func TestImportBoundaries(t *testing.T) {
	root := moduleRoot(t)
	fset := token.NewFileSet()
	for _, file := range goFiles(t, root) {
		rel, _ := filepath.Rel(root, file)
		relDir := filepath.ToSlash(filepath.Dir(rel))
		isTest := strings.HasSuffix(file, "_test.go")
		f, err := parser.ParseFile(fset, file, nil, parser.ImportsOnly)
		if err != nil {
			t.Fatalf("%s: %v", rel, err)
		}
		for _, imp := range f.Imports {
			path, _ := strconv.Unquote(imp.Path.Value)
			// Tests may spawn the binary under test and read fixtures.
			if isTest {
				continue
			}
			if ioPackages[path] && !ioPathAllowed(relDir) {
				t.Errorf("%s imports %q: external I/O belongs in internal/adapters or cmd", rel, path)
			}
			if path == "os" && !ioPathAllowed(relDir) {
				if _, ok := osAllowedOutsideAdapters[relDir]; !ok {
					t.Errorf("%s imports \"os\": route file/env access through an adapter", rel)
				}
			}
		}
	}
}

func TestFileLineLimit(t *testing.T) {
	root := moduleRoot(t)
	for _, file := range goFiles(t, root) {
		if strings.HasSuffix(file, "_test.go") || strings.HasSuffix(file, ".gen.go") {
			continue
		}
		fh, err := os.Open(file)
		if err != nil {
			t.Fatal(err)
		}
		n := 0
		sc := bufio.NewScanner(fh)
		sc.Buffer(make([]byte, 0, 1<<20), 16<<20)
		for sc.Scan() {
			n++
		}
		fh.Close()
		if n > maxFileLines {
			rel, _ := filepath.Rel(root, file)
			t.Errorf("%s has %d lines (limit %d): split it by responsibility", rel, n, maxFileLines)
		}
	}
}
