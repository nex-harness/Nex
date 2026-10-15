// Package hooktrust is the workspace hook trust core (step 3.7): the
// declaration/bundle digest algorithms, the review monotonicity verdict, the
// trust-store file codec (byte-compatible with the Node writer — the agent
// reads the same file), and the hooks config discovery.
//
// Sources: packages/shared/src/workspace-hook-{digest,config,trust-store-file,
// review-monotonicity,mutation}.ts, packages/services/src/hooks/*.ts.
//
// The digest payloads are JSON.stringify shapes — the Go side must emit the
// same JSON byte sequence (no spaces, JS number formatting) as Node, or
// every existing trust record would invalidate. canonicalJSON below is that
// serializer.
package hooktrust

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
)

// Digest schema constants (workspace-hook-config.ts).
const (
	DigestSchemaVersion   = 1
	DefaultTimeoutMs      = 60_000
	DefaultMaxOutputBytes = 32_768
)

// EventNames ports WORKSPACE_HOOK_EVENT_NAMES.
var EventNames = []string{
	"SessionStart",
	"UserPromptSubmit",
	"PreToolUse",
	"PermissionRequest",
	"PostToolUse",
	"PostToolUseFailure",
	"Stop",
}

// IsEventName reports whether the value is one of the seven hook events.
func IsEventName(value string) bool {
	for _, name := range EventNames {
		if name == value {
			return true
		}
	}
	return false
}

// Definition is one hook declaration (WorkspaceHookDefinition — the union of
// the process and command shapes; Type discriminates).
type Definition struct {
	Type          string   `json:"type"` // command | process
	Command       string   `json:"command"`
	Enabled       *bool    `json:"enabled,omitempty"`
	Args          []string `json:"args,omitempty"`
	Async         *bool    `json:"async,omitempty"`
	Shell         any      `json:"shell,omitempty"` // true | string | absent
	TimeoutMs     *float64 `json:"timeoutMs,omitempty"`
	Timeout       *float64 `json:"timeout,omitempty"`
	StatusMessage string   `json:"statusMessage,omitempty"`
}

// HooksConfig is the hooks root of a config file (WorkspaceHooksConfig).
type HooksConfig struct {
	Enabled        *bool                      `json:"enabled,omitempty"`
	TimeoutMs      *float64                   `json:"timeoutMs,omitempty"`
	MaxOutputBytes *float64                   `json:"maxOutputBytes,omitempty"`
	Events         map[string][]MatcherConfig `json:"events,omitempty"`
}

// MatcherConfig is one event matcher (WorkspaceHookMatcherConfig).
type MatcherConfig struct {
	Matcher string       `json:"matcher,omitempty"`
	Hooks   []Definition `json:"hooks"`
}

// RuntimeRoot ports WorkspaceHookRuntimeRoot.
type RuntimeRoot struct {
	Enabled        bool
	TimeoutMs      int64
	MaxOutputBytes int64
}

// ResolveTimeoutMs ports resolveWorkspaceHookTimeoutMs: timeoutMs ?? (command
// ? timeout*1000 : default), floored at 1ms.
func ResolveTimeoutMs(hook *Definition, defaultTimeoutMs int64) int64 {
	var timeoutMs float64
	switch {
	case hook.TimeoutMs != nil:
		timeoutMs = *hook.TimeoutMs
	case hook.Type == "command" && hook.Timeout != nil:
		timeoutMs = *hook.Timeout * 1000
	default:
		timeoutMs = float64(defaultTimeoutMs)
	}
	if timeoutMs < 1 {
		return 1
	}
	return int64(timeoutMs + 0.5)
}

// ResolveMaxOutputBytes ports resolveWorkspaceHookMaxOutputBytes.
func ResolveMaxOutputBytes(maxOutputBytes int64) int64 {
	if maxOutputBytes < 1 {
		return 1
	}
	return maxOutputBytes
}

// ResolveRuntimeRoot ports resolveWorkspaceHookRuntimeRoot: any root with
// enabled=true turns it on; the last timeout/max wins.
func ResolveRuntimeRoot(roots []*HooksConfig) RuntimeRoot {
	root := RuntimeRoot{Enabled: false, TimeoutMs: DefaultTimeoutMs, MaxOutputBytes: DefaultMaxOutputBytes}
	for _, candidate := range roots {
		if candidate == nil {
			continue
		}
		if candidate.Enabled != nil && *candidate.Enabled {
			root.Enabled = true
		}
		if candidate.TimeoutMs != nil {
			root.TimeoutMs = clampPositive(int64(*candidate.TimeoutMs))
		}
		if candidate.MaxOutputBytes != nil {
			root.MaxOutputBytes = ResolveMaxOutputBytes(int64(*candidate.MaxOutputBytes))
		}
	}
	root.TimeoutMs = clampPositive(root.TimeoutMs)
	root.MaxOutputBytes = ResolveMaxOutputBytes(root.MaxOutputBytes)
	return root
}

func clampPositive(value int64) int64 {
	if value < 1 {
		return 1
	}
	return value
}

// ConfiguredGates ports resolveWorkspaceHookConfiguredGates.
type ConfiguredGates struct {
	SourceRootEnabled   bool
	DeclarationEnabled  bool
	RuntimeHooksEnabled bool
	ConfiguredEnabled   bool
}

// ResolveConfiguredGates ports resolveWorkspaceHookConfiguredGates.
func ResolveConfiguredGates(sourceEnabled, declarationEnabled *bool, runtimeHooksEnabled bool) ConfiguredGates {
	sourceRootEnabled := sourceEnabled == nil || *sourceEnabled
	declEnabled := declarationEnabled == nil || *declarationEnabled
	return ConfiguredGates{
		SourceRootEnabled:   sourceRootEnabled,
		DeclarationEnabled:  declEnabled,
		RuntimeHooksEnabled: runtimeHooksEnabled,
		ConfiguredEnabled:   sourceRootEnabled && declEnabled && runtimeHooksEnabled,
	}
}

// CreateDeclarationDigest ports createWorkspaceHookDeclarationDigest: the
// sha256 of the canonical JSON payload. Any byte difference invalidates
// every existing trust record, so the payload construction is frozen.
func CreateDeclarationDigest(input DeclarationDigestInput) string {
	execution := canonicalDeclarationExecution(input.Hook)
	payload := []any{
		"workspace-hook-declaration",
		DigestSchemaVersion,
		input.SourceRelativePath,
		input.SourceDiscoveryOrder,
		input.Event,
		input.Matcher, // string or nil
		input.MatcherIndex,
		input.HookIndex,
		execution,
		ResolveTimeoutMs(&input.Hook, input.DefaultTimeoutMs),
		input.ResolvedMaxOutputBytes,
	}
	return sha256JSON(payload)
}

// DeclarationDigestInput mirrors the TS input.
type DeclarationDigestInput struct {
	SourceRelativePath     string
	SourceDiscoveryOrder   int
	Event                  string
	Matcher                *string
	MatcherIndex           int
	HookIndex              int
	Hook                   Definition
	DefaultTimeoutMs       int64
	ResolvedMaxOutputBytes int64
}

func canonicalDeclarationExecution(hook Definition) []any {
	if hook.Type == "process" {
		args := []any{}
		for _, arg := range hook.Args {
			args = append(args, arg)
		}
		if hook.Args == nil {
			args = []any{}
		}
		return []any{"process", hook.Command, args}
	}
	var shell any
	switch value := hook.Shell.(type) {
	case nil:
		shell = []any{"unset"}
	case bool:
		if value {
			shell = []any{"true"}
		} else {
			shell = []any{"unset"}
		}
	default:
		shell = []any{"string", value}
	}
	async := hook.Async != nil && *hook.Async
	return []any{"command", hook.Command, async, shell}
}

// BundleDigestInput mirrors buildWorkspaceHookBundleSnapshot's payload.
type BundleDigestInput struct {
	WorkspaceIdentity string
	WorkspacePath     string
	Sources           []SourceInput
	RuntimeRoot       RuntimeRoot
}

// SourceInput ports WorkspaceHookSourceInput.
type SourceInput struct {
	CanonicalPath         string
	BaseDir               string
	DiscoveryOrder        int
	ConfigFileKind        string // nex.json | .nex/config.json | explicit
	ExplicitProjectConfig bool
	Editable              bool
	Hooks                 *HooksConfig
}

// CreateBundleDigest ports the bundlePayload sha256. Returns "" when there
// are no hook entries (buildWorkspaceHookBundleSnapshot returns undefined).
func CreateBundleDigest(input BundleDigestInput) string {
	hasHooks := false
	for _, source := range input.Sources {
		if source.Hooks != nil && len(source.Hooks.Events) > 0 {
			for _, matchers := range source.Hooks.Events {
				if len(matchers) > 0 {
					hasHooks = true
				}
			}
		}
	}
	if !hasHooks {
		return ""
	}
	var sourcePayloads []any
	for _, source := range input.Sources {
		sourcePayloads = append(sourcePayloads, []any{
			normalizeRelativeSourcePath(input.WorkspacePath, source.CanonicalPath),
			source.DiscoveryOrder,
			source.ConfigFileKind,
			source.ExplicitProjectConfig,
			canonicalOptional(source.Hooks),
		})
	}
	// Hooks list: each entry contributes its digest + gates.
	var hookPayloads []any
	for _, source := range input.Sources {
		if source.Hooks == nil {
			continue
		}
		for _, event := range EventNames {
			for matcherIndex, matcher := range source.Hooks.Events[event] {
				for hookIndex, hook := range matcher.Hooks {
					digest := CreateDeclarationDigest(DeclarationDigestInput{
						SourceRelativePath:     normalizeRelativeSourcePath(input.WorkspacePath, source.CanonicalPath),
						SourceDiscoveryOrder:   source.DiscoveryOrder,
						Event:                  event,
						Matcher:                matcherPointer(matcher.Matcher),
						MatcherIndex:           matcherIndex,
						HookIndex:              hookIndex,
						Hook:                   hook,
						DefaultTimeoutMs:       input.RuntimeRoot.TimeoutMs,
						ResolvedMaxOutputBytes: input.RuntimeRoot.MaxOutputBytes,
					})
					gates := ResolveConfiguredGates(source.Hooks.Enabled, hook.Enabled, input.RuntimeRoot.Enabled)
					hookPayloads = append(hookPayloads, []any{
						digest,
						gates.SourceRootEnabled,
						gates.DeclarationEnabled,
						gates.RuntimeHooksEnabled,
						gates.ConfiguredEnabled,
					})
				}
			}
		}
	}
	payload := []any{
		"workspace-hook-bundle",
		DigestSchemaVersion,
		sourcePayloads,
		hookPayloads,
	}
	return sha256JSON(payload)
}

func matcherPointer(matcher string) *string {
	if matcher == "" {
		return nil
	}
	return &matcher
}

// canonicalOptional ports canonicalOptional for the source hooks root:
// enabled/timeoutMs/maxOutputBytes each become ["set", v] or ["unset"].
func canonicalOptional(hooks *HooksConfig) []any {
	return []any{
		optionalValue(hooks != nil && hooks.Enabled != nil, boolPointerValue(hooks, "enabled")),
		optionalValue(hooks != nil && hooks.TimeoutMs != nil, numberPointerValue(hooks, "timeoutMs")),
		optionalValue(hooks != nil && hooks.MaxOutputBytes != nil, numberPointerValue(hooks, "maxOutputBytes")),
	}
}

func optionalValue(set bool, value any) []any {
	if !set {
		return []any{"unset"}
	}
	return []any{"set", value}
}

func boolPointerValue(hooks *HooksConfig, key string) any {
	if hooks == nil {
		return nil
	}
	switch key {
	case "enabled":
		if hooks.Enabled != nil {
			return *hooks.Enabled
		}
	case "timeoutMs":
		if hooks.TimeoutMs != nil {
			return jsNumber(*hooks.TimeoutMs)
		}
	case "maxOutputBytes":
		if hooks.MaxOutputBytes != nil {
			return jsNumber(*hooks.MaxOutputBytes)
		}
	}
	return nil
}

func numberPointerValue(hooks *HooksConfig, key string) any { return boolPointerValue(hooks, key) }

// normalizeRelativeSourcePath ports normalizeRelativeSourcePath.
func normalizeRelativeSourcePath(workspacePath, sourcePath string) string {
	absoluteWorkspace, err := absPath(workspacePath)
	if err != nil {
		absoluteWorkspace = workspacePath
	}
	absoluteSource, err := absPath(sourcePath)
	if err != nil {
		absoluteSource = sourcePath
	}
	relative, err := relPath(absoluteWorkspace, absoluteSource)
	if err != nil || relative == "" || relative == "." {
		return baseName(sourcePath)
	}
	return strings.ReplaceAll(relative, "\\", "/")
}

// sha256JSON hashes the canonical JSON encoding of value.
func sha256JSON(value any) string {
	sum := sha256.Sum256([]byte(canonicalJSON(value)))
	return hex.EncodeToString(sum[:])
}

func absPath(path string) (string, error) { return absClean(path) }
func baseName(path string) string {
	if index := strings.LastIndexAny(path, "/\\"); index >= 0 {
		return path[index+1:]
	}
	return path
}
