// Package agentproc is the Go agent process manager (step 3.10.1), the port of
// packages/services/src/nex-agent/nexAgentProcessManager.ts together with the
// spawn-cwd rule (nexAgentSpawnCwd.ts), the spawn env construction
// (runtime-tools/agentProxyEnv.ts) and the single-line stderr diagnostic contract
// (packages/shared/src/process-diagnostic.ts).
//
// The boundaries it keeps:
//
//   - One agent process per workspace, keyed by the AGENTS.md identity rule
//     `workspaceIdentity?.trim() || workspacePath` (paths.WorkspaceKey). The
//     workspace path is only ever used for cwd and file operations; isolation
//     keys never fall back to it when an identity is present.
//   - Spawning and killing go through internal/adapters/exec, the single process
//     adapter. Nothing here re-implements process management: Setpgid/Job Object,
//     output collection and the SIGTERM→SIGKILL escalation belong to that
//     package, and this one only decides *when* a tree dies.
//   - The runtime epoch (generation + instance id) is host-owned and increases on
//     every spawn, so a subscriber that reconnects after a crash can tell the old
//     runtime's frames from the new one's.
//   - The first cleanup reason is the root-cause fact and is recorded once
//     (recordTerminationIntent), so a later idempotent reclaim can never rewrite
//     a crash into an "expected" exit.
package agentproc

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// AgentRuntime ports the NEX_AGENT_RUNTIME descriptor
// (packages/shared/src/nex-agent-runtime.ts). SpawnArgs is what the agent needs
// to speak the protocol over stdio: `app-server --stdio`.
type AgentRuntime struct {
	// BinaryEnvVar is the file-based override an operator sets. It is the same
	// GLM_BINARY_PATH the Node host reads, so one deployment configures both
	// engines.
	BinaryEnvVar string
	// BundledResourceDir is the per-platform resource directory under a
	// bundled-agents root.
	BundledResourceDir string
	// BinaryName is the platform-neutral binary file name; Windows appends .exe.
	BinaryName string
	// SpawnArgs is the argv tail every spawn gets.
	SpawnArgs []string
}

// DefaultAgentRuntime is NEX_AGENT_RUNTIME for the Go agent.
var DefaultAgentRuntime = AgentRuntime{
	BinaryEnvVar:       "GLM_BINARY_PATH",
	BundledResourceDir: "glm",
	BinaryName:         "nex-agent",
	SpawnArgs:          []string{"app-server", "--stdio"},
}

// Provider is NEX_AGENT_PROVIDER (packages/shared/src/nex-agent-policy.ts): the
// provider label every process-lifecycle record carries.
const Provider = "glm"

// BinaryNameFor returns the platform binary file name, porting
// resolvePlatformBinaryName.
func (r AgentRuntime) BinaryNameFor(goos string) string {
	if goos == "windows" {
		return r.BinaryName + ".exe"
	}
	return r.BinaryName
}

// AgentCommand is the NexAgentCommand port: what to spawn and with which extra
// env. Args/Cwd are pointers of meaning here — a nil Cwd means "use the
// workspace path", which the preflight resolves.
type AgentCommand struct {
	Command string
	Args    []string
	// Cwd overrides the workspace path. Empty means the workspace path.
	Cwd string
	// Env is the deployment-specific env patch; it has the highest priority of
	// every env source (after the inherited sanitized environment).
	Env map[string]string
	// SupportsStorageStartup marks a command from this deployment that announces
	// storage migrations before serving. A custom command keeps the old protocol.
	SupportsStorageStartup bool
}

// CommandResolverContext is NexAgentCommandResolverContext.
type CommandResolverContext struct {
	WorkspacePath     string
	WorkspaceIdentity string
	WorkspaceKey      string
}

// CommandResolver is NexAgentCommandResolver. Returning (nil, nil) means "no
// agent command is configured", which the manager reports as such instead of
// spawning something arbitrary.
type CommandResolver func(CommandResolverContext) (*AgentCommand, error)

// ErrCommandNotConfigured is the TS "Nex agent server command is not
// configured." error. It is a value so a caller can branch on it with
// errors.Is instead of matching a string.
var ErrCommandNotConfigured = errors.New(
	"Nex agent server command is not configured. Set GLM_BINARY_PATH before integration.")

// ResolveDefaultCommand ports resolveDefaultNexAgentCommand's Phase-3 form: the
// env/file override first (so an operator configuration is always honored), then
// the bundled-agents candidate chain findNexAgentRuntimeBinary walks.
//
// The JS-bundle branches of the TS resolver (Electron Node runtime running
// nex.cjs, the monorepo tsx source entry) are Node-host concepts and do not
// exist in the Go server: the deployed binary is the Go agent, and the
// development source entry is served by `go run` / the built binary through
// GLM_BINARY_PATH.
func ResolveDefaultCommand(ctx CommandResolverContext) (*AgentCommand, error) {
	binary := FindAgentRuntimeBinary(DefaultAgentRuntime)
	if binary == "" {
		return nil, nil
	}
	return &AgentCommand{
		Command: binary,
		Args:    DefaultAgentRuntime.SpawnArgs,
		Cwd:     ctx.WorkspacePath,
		// The native binary in this deployment is the agent that ships with it, so
		// it announces storage startup. A command found through the chain is the
		// bundled runtime, never an arbitrary third-party script.
		SupportsStorageStartup: true,
	}, nil
}

// FindAgentRuntimeBinary ports findNexAgentRuntimeBinary's candidate chain
// (providerRuntimeResolver.ts): the GLM_BINARY_PATH override when the file
// exists, then the packaged resources, the ~/.nex/server/agents deployment, and
// every platform-scoped bundled-agents root (repository root, desktop package,
// and the sibling directories a pnpm-filtered dev server runs from).
//
// It returns "" when nothing exists, matching the TS null.
func FindAgentRuntimeBinary(runtime AgentRuntime) string {
	if envPath := strings.TrimSpace(os.Getenv(runtime.BinaryEnvVar)); envPath != "" && fileExists(envPath) {
		return envPath
	}
	entry := runtime.BinaryNameFor(hostGOOS)
	resourceSegments := append([]string{runtime.BundledResourceDir}, entry)
	platformKey := hostGOOS + "-" + hostGOARCH

	candidates := []string{filepath.Join(homeDir(), ".nex", "server", "agents", filepath.Join(resourceSegments...))}
	for _, root := range platformScopedRoots(platformKey) {
		if root != "" {
			candidates = append(candidates, filepath.Join(root, filepath.Join(resourceSegments...)))
		}
	}
	for _, root := range legacyBundledResourceRoots() {
		if root != "" {
			candidates = append(candidates, filepath.Join(root, filepath.Join(resourceSegments...)))
		}
	}
	return firstExistingPath(candidates)
}

// platformScopedRoots ports resolvePlatformScopedBundledAgentRoots.
func platformScopedRoots(platformKey string) []string {
	cwd, err := os.Getwd()
	if err != nil {
		cwd = ""
	}
	roots := []string{
		filepath.Join(cwd, "bundled-agents", platformKey),
		filepath.Join(cwd, "packages", "desktop", "bundled-agents", platformKey),
		filepath.Join(cwd, "..", "desktop", "bundled-agents", platformKey),
	}
	return roots
}

// legacyBundledResourceRoots ports resolveLegacyBundledResourceRoots.
func legacyBundledResourceRoots() []string {
	cwd, err := os.Getwd()
	if err != nil {
		cwd = ""
	}
	return []string{
		filepath.Join(cwd, "bundled-resources"),
		filepath.Join(cwd, "packages", "desktop", "bundled-resources"),
		filepath.Join(cwd, "..", "desktop", "bundled-resources"),
	}
}

// ParseArgsJSON ports parseArgsJson: a JSON string array or an error, never a
// partially parsed one.
func ParseArgsJSON(raw string) ([]string, error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return nil, nil
	}
	var parsed []string
	if err := json.Unmarshal([]byte(trimmed), &parsed); err != nil {
		return nil, errors.New("NEX_AGENT_SERVER_ARGS_JSON must be a JSON string array")
	}
	return parsed, nil
}

// ResolveSpawnCwd ports resolveNexAgentSpawnCwd: the requested cwd wins whenever
// it is a usable directory; the fallback applies only when the request *was* the
// workspace path and that directory is gone. A stale historical directory must
// keep starting the agent, and ENOTDIR/EACCES must count as "not usable" the
// same way a missing path does.
//
// The fallback never changes the workspace identity: business keying stays on
// WorkspaceKey.
func ResolveSpawnCwd(requestedCwd, workspacePath, spawnFallbackCwd string) SpawnCwdResult {
	requestedExists := isDirectory(requestedCwd)
	if requestedCwd == workspacePath && !requestedExists && isDirectory(spawnFallbackCwd) {
		return SpawnCwdResult{Cwd: spawnFallbackCwd, UsedFallback: true, CwdExists: true}
	}
	return SpawnCwdResult{Cwd: requestedCwd, CwdExists: requestedExists}
}

// SpawnCwdResult is the resolveNexAgentSpawnCwd result.
type SpawnCwdResult struct {
	Cwd          string
	UsedFallback bool
	CwdExists    bool
}

// CwdSource names where the effective cwd came from, for the preflight log.
type CwdSource string

// The three cwd sources the TS preflight distinguishes.
const (
	CwdSourceCommand         CwdSource = "command"
	CwdSourceWorkspace       CwdSource = "workspace"
	CwdSourceWorkspaceFallbk CwdSource = "workspace-fallback"
)

// Preflight is NexAgentSpawnPreflight: everything the spawn log and the error
// path need to explain a failure without guessing. Node's ENOENT on spawn can
// mean "no such command" *or* "no such cwd"; recording both before spawning is
// what keeps a vanished workspace from being read as a lost binary.
type Preflight struct {
	Command         string
	Args            []string
	RequestedCwd    string
	Cwd             string
	CwdSource       CwdSource
	CommandPathKind string // "absolute" or "path-search"
	CommandExists   *bool
	CwdExists       bool
}

// BuildPreflight ports buildNexAgentSpawnPreflight.
func BuildPreflight(command AgentCommand, workspacePath, spawnFallbackCwd string) Preflight {
	requestedCwd := command.Cwd
	if requestedCwd == "" {
		requestedCwd = workspacePath
	}
	resolved := ResolveSpawnCwd(requestedCwd, workspacePath, spawnFallbackCwd)
	source := CwdSourceWorkspace
	switch {
	case resolved.UsedFallback:
		source = CwdSourceWorkspaceFallbk
	case command.Cwd != "":
		source = CwdSourceCommand
	}
	kind := "path-search"
	if filepath.IsAbs(command.Command) {
		kind = "absolute"
	}
	preflight := Preflight{
		Command:         command.Command,
		Args:            command.Args,
		RequestedCwd:    requestedCwd,
		Cwd:             resolved.Cwd,
		CwdSource:       source,
		CommandPathKind: kind,
		CwdExists:       resolved.CwdExists,
	}
	if kind == "absolute" {
		exists := fileExists(command.Command)
		preflight.CommandExists = &exists
	}
	return preflight
}

// ErrDisposed is the "manager is disposed" error the TS throws from getClient
// and startClient.
var ErrDisposed = errors.New("Nex agent process manager is disposed")

// ErrStartCancelled is the admission-abort error a start sees when its
// workspace was disposed or restarted while the start was still resolving.
var ErrStartCancelled = errors.New("Nex agent process start was cancelled")

// ErrRuntimeUnavailable is the "runtime identity is unavailable" error.
var ErrRuntimeUnavailable = errors.New("Nex agent runtime identity is unavailable")

func fileExists(path string) bool {
	if path == "" {
		return false
	}
	_, err := os.Stat(path)
	return err == nil
}

func isDirectory(path string) bool {
	if path == "" {
		return false
	}
	info, err := os.Stat(path)
	if err != nil {
		return false
	}
	return info.IsDir()
}

func firstExistingPath(candidates []string) string {
	for _, candidate := range candidates {
		if fileExists(candidate) {
			return candidate
		}
	}
	return ""
}

// fmtCommand renders a command for a log line without leaking its env.
func fmtCommand(command AgentCommand) string {
	return fmt.Sprintf("%s %s", command.Command, strings.Join(command.Args, " "))
}
