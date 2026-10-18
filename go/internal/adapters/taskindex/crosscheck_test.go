package taskindex_test

// crosscheck_test.go — the Go half of scripts/go-rewrite/crosscheck-taskindex.mjs.
// The script sets NEX_CROSSCHECK_MODE/DB/OUT and drives this package; the tests
// skip in normal runs because the env vars are absent.

import (
	"encoding/json"
	"os"
	"testing"
)

const (
	envMode = "NEX_CROSSCHECK_MODE"
	envDB   = "NEX_CROSSCHECK_DB"
	envOut  = "NEX_CROSSCHECK_OUT"
)

// TestCrosscheckGoReadsNode: Node wrote the rows; Go reads them back and prints
// the same normalised JSON the Node half prints, so the script can diff.
func TestCrosscheckGoReadsNode(t *testing.T) {
	if os.Getenv(envMode) != "go-reads-node" {
		t.Skip("crosscheck mode not set")
	}
	r := newRepo(t, os.Getenv(envDB))
	writeJSONFile(t, os.Getenv(envOut), readScenario(t, r))
}

// TestCrosscheckGoWritesNode: Go writes the scenario; Node must read it back.
func TestCrosscheckGoWritesNode(t *testing.T) {
	if os.Getenv(envMode) != "go-writes-node" {
		t.Skip("crosscheck mode not set")
	}
	r := newRepo(t, os.Getenv(envDB))
	writeScenario(t, r)
	writeJSONFile(t, os.Getenv(envOut), readScenario(t, r))
}

func writeJSONFile(t *testing.T, path string, v any) {
	t.Helper()
	if path == "" {
		t.Fatal("output path not set")
	}
	raw, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, raw, 0o644); err != nil {
		t.Fatal(err)
	}
}
