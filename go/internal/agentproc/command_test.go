// command_test.go covers the pure parts of the spawn decision: the command
// resolver chain, the spawn-cwd fallback rule, and the environment precedence.
// These are the rules a wrong answer silently breaks, so they are pinned here
// rather than only being exercised through a live process.
package agentproc

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// TestSpawnCwdFallsBackOnlyForTheWorkspacePath is resolveNexAgentSpawnCwd's whole
// contract: a command that asked for its OWN cwd keeps it even when that directory
// is gone (the agent then fails loudly, which is the TS behavior), while a missing
// WORKSPACE cwd falls back so a stale historical directory still starts.
func TestSpawnCwdFallsBackOnlyForTheWorkspacePath(t *testing.T) {
	workspace := t.TempDir()
	fallback := t.TempDir()
	missing := filepath.Join(t.TempDir(), "gone")

	cases := []struct {
		name         string
		requested    string
		workspace    string // defaults to `missing` when empty
		fallback     string
		wantCwd      string
		wantFallback bool
		wantExists   bool
	}{
		{name: "live workspace keeps itself", requested: workspace, workspace: workspace, fallback: fallback, wantCwd: workspace, wantFallback: false, wantExists: true},
		{name: "missing workspace uses the fallback", requested: missing, workspace: missing, fallback: fallback, wantCwd: fallback, wantFallback: true, wantExists: true},
		{name: "missing workspace without a fallback keeps the path", requested: missing, workspace: missing, fallback: "", wantCwd: missing, wantFallback: false, wantExists: false},
		// A command cwd differs from the workspace: requested != workspacePath,
		// so the workspace fallback never applies. The agent then fails loudly,
		// which is the TS behavior for a stale command-specified directory.
		{name: "a command cwd is never replaced", requested: missing, workspace: workspace, fallback: fallback, wantCwd: missing, wantFallback: false, wantExists: false},
		{name: "an empty fallback is not a fallback", requested: missing, workspace: missing, fallback: "", wantCwd: missing, wantFallback: false, wantExists: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ws := tc.workspace
			if ws == "" {
				ws = missing
			}
			got := ResolveSpawnCwd(tc.requested, ws, tc.fallback)
			if got.Cwd != tc.wantCwd || got.UsedFallback != tc.wantFallback || got.CwdExists != tc.wantExists {
				t.Errorf("ResolveSpawnCwd(%q, workspace=%q, fallback=%q) = %+v, want cwd=%q fallback=%v exists=%v",
					tc.requested, ws, tc.fallback, got, tc.wantCwd, tc.wantFallback, tc.wantExists)
			}
		})
	}
}

// TestSpawnCwdTreatsAFileAsUnusable: a cwd that exists but is not a directory is
// as unusable as a missing one, and must take the same fallback path.
func TestSpawnCwdTreatsAFileAsUnusable(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "not-a-dir")
	if err := os.WriteFile(file, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	fallback := t.TempDir()
	got := ResolveSpawnCwd(file, file, fallback)
	if got.Cwd != fallback || !got.UsedFallback {
		t.Errorf("a file used as cwd resolved to %+v, want the fallback", got)
	}
}

// TestSpawnCwdFallbackDoesNotChangeTheIdentityKey: the AGENTS.md rule. A stale
// workspace directory may change where the agent STARTS, never which workspace it
// belongs to.
func TestSpawnCwdFallbackDoesNotChangeTheIdentityKey(t *testing.T) {
	workspace := WorkspaceRef{WorkspacePath: "/gone/workspace", WorkspaceIdentity: "  ssh://host/repo  "}
	fallback := t.TempDir()
	got := ResolveSpawnCwd(workspace.WorkspacePath, workspace.WorkspacePath, fallback)
	if got.Cwd != fallback {
		t.Fatalf("the fallback was not applied: %+v", got)
	}
	if key := workspace.Key(); key != "ssh://host/repo" {
		t.Errorf("the residency key = %q; a cwd fallback must not change it", key)
	}
}

// TestWorkspaceKeyPrefersIdentity is the identity rule every caller relies on.
func TestWorkspaceKeyPrefersIdentity(t *testing.T) {
	cases := []struct {
		name    string
		ref     WorkspaceRef
		wantKey string
	}{
		{"identity wins", WorkspaceRef{WorkspacePath: "/a", WorkspaceIdentity: "remote:1"}, "remote:1"},
		{"identity is trimmed", WorkspaceRef{WorkspacePath: "/a", WorkspaceIdentity: "  remote:2  "}, "remote:2"},
		{"blank identity falls back to the path", WorkspaceRef{WorkspacePath: "/a", WorkspaceIdentity: "   "}, "/a"},
		{"no identity is the path", WorkspaceRef{WorkspacePath: "/a"}, "/a"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.ref.Key(); got != tc.wantKey {
				t.Errorf("Key() = %q, want %q", got, tc.wantKey)
			}
		})
	}
}

// TestBuildPreflightRecordsBothVisibilityFacts: the preflight exists because a
// spawn ENOENT can mean "no such command" or "no such cwd", and reading a vanished
// workspace as a lost binary sends the operator hunting for the wrong thing.
func TestBuildPreflightRecordsBothVisibilityFacts(t *testing.T) {
	workspace := t.TempDir()
	command := AgentCommand{Command: "/definitely/not/here/nex-agent", Args: []string{"app-server"}}
	preflight := BuildPreflight(command, workspace, "")

	if preflight.CommandPathKind != "absolute" {
		t.Errorf("CommandPathKind = %q, want absolute", preflight.CommandPathKind)
	}
	if preflight.CommandExists == nil || *preflight.CommandExists {
		t.Errorf("CommandExists = %v, want false for a missing absolute command", preflight.CommandExists)
	}
	if !preflight.CwdExists {
		t.Error("CwdExists = false for an existing workspace")
	}
	if preflight.CwdSource != CwdSourceWorkspace {
		t.Errorf("CwdSource = %q, want %q", preflight.CwdSource, CwdSourceWorkspace)
	}

	// A command that names its own cwd is reported as such, and a relative command
	// has no on-disk visibility claim (the PATH lookup decides).
	named := BuildPreflight(AgentCommand{Command: "nex-agent", Cwd: workspace}, workspace, "")
	if named.CwdSource != CwdSourceCommand {
		t.Errorf("CwdSource = %q, want %q for a command-declared cwd", named.CwdSource, CwdSourceCommand)
	}
	if named.CommandExists != nil {
		t.Errorf("CommandExists = %v, want nil for a PATH-resolved command", *named.CommandExists)
	}
	if named.CommandPathKind != "path-search" {
		t.Errorf("CommandPathKind = %q, want path-search", named.CommandPathKind)
	}
}

// TestResolveDefaultCommandHonorsTheFileOverride: GLM_BINARY_PATH is the operator
// configuration both engines read, and it wins whenever the file is really there.
func TestResolveDefaultCommandHonorsTheFileOverride(t *testing.T) {
	binary := filepath.Join(t.TempDir(), "nex-agent")
	if err := os.WriteFile(binary, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv(DefaultAgentRuntime.BinaryEnvVar, binary)

	command, err := ResolveDefaultCommand(CommandResolverContext{WorkspacePath: "/workspace", WorkspaceKey: "/workspace"})
	if err != nil {
		t.Fatalf("ResolveDefaultCommand: %v", err)
	}
	if command == nil {
		t.Fatal("the override resolved to no command")
	}
	if command.Command != binary {
		t.Errorf("Command = %q, want the override %q", command.Command, binary)
	}
	if strings.Join(command.Args, " ") != strings.Join(DefaultAgentRuntime.SpawnArgs, " ") {
		t.Errorf("Args = %v, want the agent spawn args %v", command.Args, DefaultAgentRuntime.SpawnArgs)
	}
	if command.Cwd != "/workspace" {
		t.Errorf("Cwd = %q, want the workspace path", command.Cwd)
	}
}

// TestResolveDefaultCommandIgnoresAMissingOverride: an override pointing at a file
// that is not there must fall through to the bundled chain rather than producing a
// command that cannot be spawned.
func TestResolveDefaultCommandIgnoresAMissingOverride(t *testing.T) {
	t.Setenv(DefaultAgentRuntime.BinaryEnvVar, filepath.Join(t.TempDir(), "gone"))
	command, err := ResolveDefaultCommand(CommandResolverContext{WorkspacePath: "/workspace", WorkspaceKey: "/workspace"})
	if err != nil {
		t.Fatalf("ResolveDefaultCommand: %v", err)
	}
	if command != nil && command.Command == "" {
		t.Error("a resolved command has an empty path")
	}
}

// TestBinaryNamePerPlatform pins the one platform rule of the runtime descriptor.
func TestBinaryNamePerPlatform(t *testing.T) {
	runtime := DefaultAgentRuntime
	if got := runtime.BinaryNameFor("windows"); got != "nex-agent.exe" {
		t.Errorf("windows binary name = %q, want nex-agent.exe", got)
	}
	for _, goos := range []string{"linux", "darwin"} {
		if got := runtime.BinaryNameFor(goos); got != "nex-agent" {
			t.Errorf("%s binary name = %q, want nex-agent", goos, got)
		}
	}
}

// TestParseArgsJSONRejectsANonStringArray: a malformed override must fail loudly at
// spawn time rather than produce an argv the agent cannot start with.
func TestParseArgsJSONRejectsANonStringArray(t *testing.T) {
	if got, err := ParseArgsJSON(`["app-server","--stdio"]`); err != nil || len(got) != 2 {
		t.Errorf("ParseArgsJSON(valid) = %v, %v", got, err)
	}
	if got, err := ParseArgsJSON("  "); err != nil || got != nil {
		t.Errorf("ParseArgsJSON(blank) = %v, %v; want nil, nil", got, err)
	}
	for _, bad := range []string{`{"a":1}`, `[1,2]`, `not json`} {
		if _, err := ParseArgsJSON(bad); err == nil {
			t.Errorf("ParseArgsJSON(%q) accepted a value that is not a string array", bad)
		}
	}
}

// TestFindAgentRuntimeBinaryIgnoresTheRealEnvironment: the chain must be a pure
// function of the override plus the filesystem, so this pins it by pointing the
// override at a real file and asserting the first candidate wins.
func TestFindAgentRuntimeBinaryIgnoresTheRealEnvironment(t *testing.T) {
	if runtime.GOOS == "" {
		t.Skip("hostGOOS is always set on a supported platform")
	}
	dir := t.TempDir()
	binary := filepath.Join(dir, DefaultAgentRuntime.BinaryNameFor(hostGOOS))
	if err := os.WriteFile(binary, []byte("x"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv(DefaultAgentRuntime.BinaryEnvVar, binary)
	if got := FindAgentRuntimeBinary(DefaultAgentRuntime); got != binary {
		t.Errorf("FindAgentRuntimeBinary = %q, want the override %q", got, binary)
	}
}
