package node

// Shared fixture for the supervisor tests: build the fake upstream ONCE per test
// binary and hand out the path. Building it per test would add ~1 s of compile time
// to every case, and the crash-budget tests spin up several children.
//
// The fake is a real process, so every assertion below is about real process
// behavior (pid, exit status, a real TCP port) rather than about a mock.

import (
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"testing"
)

var (
	fakeOnce sync.Once
	fakePath string
	fakeErr  error
)

// fakeUpstreamBinary compiles internal/adapters/node/fakeupstream once and returns
// the executable path.
func fakeUpstreamBinary(t *testing.T) string {
	t.Helper()
	fakeOnce.Do(func() {
		dir, err := os.MkdirTemp("", "nex-fake-upstream")
		if err != nil {
			fakeErr = err
			return
		}
		fakePath = filepath.Join(dir, "fakeupstream")
		if err := os.WriteFile(fakePath, []byte("#!/bin/sh\nexit 1\n"), 0o755); err != nil {
			// The placeholder only exists so a failed build cannot leave a stale
			// binary from a previous run behind and be mistaken for the real one.
			fakeErr = err
			return
		}
		cmd := exec.Command("go", "build", "-o", fakePath, "./fakeupstream")
		cmd.Env = os.Environ()
		if output, err := cmd.CombinedOutput(); err != nil {
			fakeErr = err
			t.Logf("build fakeupstream: %v\n%s", err, output)
		}
	})
	if fakeErr != nil {
		t.Fatalf("build the fake upstream: %v", fakeErr)
	}
	return fakePath
}

// nodeExecutable returns the `node` binary path, or skips when Node is not installed.
func nodeExecutable(t *testing.T) string {
	t.Helper()
	path, err := exec.LookPath("node")
	if err != nil {
		t.Skipf("node is not installed: %v", err)
	}
	return path
}

// repoRoot walks up to the repository root (the directory holding pnpm-workspace.yaml).
func repoRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatalf("working directory: %v", err)
	}
	for i := 0; i < 8; i++ {
		if _, statErr := os.Stat(filepath.Join(dir, "pnpm-workspace.yaml")); statErr == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}
	t.Fatalf("repository root not found above %s", dir)
	return ""
}
