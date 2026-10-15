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
// ioAllowedOutsideAdapters lists the os-allowlisted packages that may ALSO
// import the net/net/http ioPackages, with the reason.
var ioAllowedOutsideAdapters = map[string]string{
	// internal/services/modelprovider (step 3.6): the models.dev catalog fetch
	// and the remote /models list are outbound HTTP the TS service itself
	// performs with fetch(); its local-file access is already justified in
	// osAllowedOutsideAdapters. Kept in the same tier as settingssync's
	// HTTP-facing services.
	"internal/services/modelprovider": "models.dev catalog + provider /models fetch (step 3.6)",
}

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
	// The step-3.2 tier-1 services ARE the I/O adapters (the phase-3 plan's own
	// title for them): IFileService/IFileWatcherService wrap the filesystem
	// for the RPC surface, ISystemService probes tcp/http. Wrapping their
	// os.Stat/ReadDir in a second adapter adds indirection with no second
	// implementation — the RPC channel adapters (channel.go in each package)
	// already isolate the transport. The system package's net/http stays
	// behind internal/adapters/probehttp (see system.go).
	"internal/services/file":        "tier-1 IFileService: its contract IS filesystem access (step 3.2)",
	"internal/services/filewatcher": "tier-1 IFileWatcherService: its contract IS filesystem watching (step 3.2)",
	"internal/services/system":      "tier-1 ISystemService: probes the OS (shells, tcp, http via probehttp) (step 3.2)",
	// The git services (step 3.3) shell out to the git binary and touch the
	// working tree: temp index dirs (git update-index --index-info), stat/read
	// of paths being staged, and the checkpoint store's atomic manifest
	// writes. os/exec itself still routes through internal/adapters/exec; the
	// os usage here is the same tier-1 "the service IS the I/O boundary" case.
	"internal/services/git":           "git services stage/restore working-tree files (step 3.3)",
	"internal/services/gitcheckpoint": "checkpoint store writes manifests atomically (step 3.3)",
	// The settings family (step 3.5) owns local state files whose paths ARE
	// the contract: settingService reads/quarantines setting.json and copies
	// the whole data directory on updateDataBaseDir; the onboarding record and
	// device telemetry-state files live under the data dir. Atomic writes
	// still route through internal/adapters/fsx (lock + rename); the raw os
	// usage is direct stat/read of the owned files, the same "service IS the
	// I/O boundary" case as file/git.
	"internal/services/setting":    "settings family owns setting.json reads/quarantines and the data-dir copy (step 3.5)",
	"internal/services/onboarding": "onboarding record file is the service's own state (step 3.5)",
	"internal/services/device":     "device identity file telemetry-state.json is the service's own state (step 3.5)",
	"internal/services/credential": "credentials.json store is the service's own state; private atomic writes route through fsx (step 3.5)",
	// settings-sync (step 3.5) copies/symlinks skill, command, plugin and MCP
	// config trees between agent directories and the Nex layout — its whole
	// contract is local filesystem migration (the plan's own reading of the
	// 2.4k-line TS: "local file scan/import, not a remote sync protocol").
	"internal/services/settingssync": "settings-sync migrates local agent config trees (step 3.5)",
	// The providers/model family (step 3.6) owns local state files whose
	// paths ARE the contract, the same tier-3 "service IS the I/O boundary"
	// case as setting/credential: provider_config.json + provisioning state
	// reads/writes route through fsx (lock + atomic), models.dev fetches its
	// catalog and the remote model list hits provider /models endpoints over
	// HTTP (the TS service itself calls fetch()). The agent-shared
	// internal/providerconfig stays pure (no os/net there).
	"internal/services/modelprovider": "providers family owns provider_config.json + provisioning state + models.dev/remote-model HTTP (step 3.6)",
	"internal/services/providers":     "provisioning target owns its state file; private atomic writes route through fsx (step 3.6)",
	// The dynamic-workflow snapshot reads the process env override before
	// any network action; env reads are its contract (HTTP stays behind the
	// injected client seam).
	"internal/services/codingplansubscription": "reads the NEX_DYNAMIC_WORKFLOW_MODE env override (step 3.5)",
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
				if _, ok := ioAllowedOutsideAdapters[relDir]; !ok {
					t.Errorf("%s imports %q: external I/O belongs in internal/adapters or cmd", rel, path)
				}
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
