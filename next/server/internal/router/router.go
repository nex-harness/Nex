// Package router decides whether a browser channel request is answered by a
// server-local channel or forwarded to a runner.
//
// Forwarded channels route by the server-built `runner:<runnerId>:<path>`
// workspaceIdentity when the argument carries one, and by workspacePath only
// as the legacy fallback (see docs/next/multi-runner.md "Routing (server)").
// The registry resolves the target and reports `runner offline` / ambiguity.
package router

import (
	"encoding/json"
	"strings"

	"github.com/nex-harness/nex/server/internal/rpcwire"
	"github.com/nex-harness/nex/server/internal/runners"
)

// TargetKind describes who answers a request.
type TargetKind int

// Target kinds.
const (
	// TargetLocal is answered by a server-local channel implementation.
	TargetLocal TargetKind = iota
	// TargetWorkspace forwards to the runner selected by WorkspacePath /
	// WorkspaceIdentity.
	TargetWorkspace
	// TargetFirstRunner forwards to the first connected runner (channels with
	// no workspace in their argument, e.g. the model catalog without a target).
	TargetFirstRunner
	// TargetUnknownChannel rejects immediately with the ChannelServer error.
	TargetUnknownChannel
	// TargetLocalUnsupported is a local channel/method the server does not
	// implement; it returns a MethodNotFound error.
	TargetLocalUnsupported
	// TargetRunner forwards to the runner named by RunnerID (config export and
	// other server-initiated runner facts).
	TargetRunner
)

// Route is the resolved destination for one channel request.
type Route struct {
	Kind TargetKind
	// WorkspacePath is set for TargetWorkspace.
	WorkspacePath string
	// WorkspaceIdentity is set for TargetWorkspace when the argument carried a
	// runner identity; the registry prefers it over WorkspacePath.
	WorkspaceIdentity string
	// RunnerID is set for TargetRunner.
	RunnerID string
}

// Target combines the route's workspace fields into a registry target.
func (r Route) Target() runners.Target {
	return runners.Target{WorkspacePath: r.WorkspacePath, WorkspaceIdentity: r.WorkspaceIdentity}
}

// Channel names with special routing.
const (
	ChannelNexAgent         = "nex-agent"
	ChannelNexSession       = "nex-session"
	ChannelNexTask          = "nex-task"
	ChannelModelSelection   = "model-selection"
	ChannelProviderSettings = "provider-settings"
	// ChannelMcpSync is the MCP settings channel: load/save are server-owned
	// (the config doc); statuses come from the owning runner's live sessions.
	ChannelMcpSync = "mcp-sync"
	// ChannelSkills is the Settings → Skills channel (doc bundles).
	ChannelSkills = "skills"
	// ChannelConfig is the server-owned desired configuration (docs/next/
	// config-mcp-skills.md). provider-settings/model-selection are answered from
	// it by the server; the rest of the channel is served locally too.
	ChannelConfig = "config"
)

// workspaceTargetMethods enumerates forwarded nex-agent methods that carry a
// workspace target at the top level of their argument.
var workspaceTargetMethods = map[string]struct{}{
	"subscribeConversationV4":               {},
	"resyncConversationV4":                  {},
	"unsubscribeConversationV4":             {},
	"conversationRowsRangeV4":               {},
	"conversationPlansV4":                   {},
	"conversationWorkflowRunEventsV4":       {},
	"conversationWorkflowRunsV4":            {},
	"conversationWorkflowRunArtifactsV4":    {},
	"conversationWorkflowRunArtifactDataV4": {},
	"conversationWorkflowRunArtifactReadV4": {},
	"conversationWorkflowRunWorkspaceV4":    {},
	"conversationWorkflowRunNodeResultV4":   {},
	"conversationFileChangesV4":             {},
	"conversationFileRewindPreviewV4":       {},
	"sendConversationCommandV4":             {},
	"queryConversationCommandsV4":           {},
	"attachmentBeginV4":                     {},
	"attachmentChunkV4":                     {},
	"attachmentCommitV4":                    {},
	"attachmentAbortV4":                     {},
	"attachmentPreviewSourceV4":             {},
	"attachmentReadV4":                      {},
	"conversationAttachmentReadV4":          {},
	"conversationAttachmentStatV4":          {},
	"backgroundBashOutputV4":                {},
	"subscribeSessionsIndexV4":              {},
	"resyncSessionsIndexV4":                 {},
	"unsubscribeSessionsIndexV4":            {},
	"subscribeWorkspaceConfigV4":            {},
	"resyncWorkspaceConfigV4":               {},
	"unsubscribeWorkspaceConfigV4":          {},
	"setConnectionFlowStateV4":              {},
	// Legacy/auxiliary methods also take NexAgentWorkspaceTarget.
	"prepareStorage":                   {},
	"initialize":                       {},
	"createSession":                    {},
	"resumeSession":                    {},
	"listSessions":                     {},
	"readSession":                      {},
	"readSessionDebug":                 {},
	"readSessionPromptContext":         {},
	"readSessionEvents":                {},
	"listPlugins":                      {},
	"getPluginsOverview":               {},
	"installPlugin":                    {},
	"uninstallPlugin":                  {},
	"updatePlugin":                     {},
	"configurePlugin":                  {},
	"validatePlugin":                   {},
	"describePlugin":                   {},
	"setPluginEnabled":                 {},
	"listAutomations":                  {},
	"createAutomation":                 {},
	"updateAutomation":                 {},
	"deleteAutomation":                 {},
	"setAutomationEnabled":             {},
	"restartAutomation":                {},
	"runAutomationNow":                 {},
	"listAutomationRuns":               {},
	"deleteAutomationRun":              {},
	"listSessionSubagents":             {},
	"sendPrompt":                       {},
	"compactSession":                   {},
	"goalSession":                      {},
	"closeSession":                     {},
	"setModel":                         {},
	"setThoughtLevel":                  {},
	"setMode":                          {},
	"respondSessionRuntimePreferences": {},
	"disposeWorkspace":                 {},
	"getSkillReferenceCatalog":         {},
	"getPluginReferenceCatalog":        {},
	"resolveSuggestedPluginReference":  {},
	"getTaskTokenUsage":                {},
	"deliverSessionMessage":            {},
}

// eventWorkspaceMethods enumerates dynamic events whose listen argument
// carries the workspace target.
var eventWorkspaceMethods = map[string]struct{}{
	"onDynamicConversationFrame":                {},
	"onDynamicSessionsIndexFrame":               {},
	"onDynamicWorkspaceConfigFrame":             {},
	"onDynamicConversationTelemetryFact":        {},
	"onDynamicLocalTtftFacts":                   {},
	"onDynamicSessionRuntimePreferencesRequest": {},
	"onDynamicSessionEvent":                     {},
	"onDynamicWorkspaceEvent":                   {},
}

// nexusTaskGlobalMethods are nex-task methods with no workspace placeholder.
var nexusTaskGlobalMethods = map[string]struct{}{
	"listPinnedTaskIds": {},
}

// Resolve decides the destination of one request (promise or listen).
//
// local reports whether the channel name is registered as a server-local
// channel. The arg may be nil for calls without arguments.
func Resolve(channel, method string, arg rpcwire.Value, local func(string) bool) Route {
	if route, ok := resolveSpecial(channel, method, arg); ok {
		return route
	}
	if local(channel) {
		return Route{Kind: TargetLocal}
	}
	return Route{Kind: TargetUnknownChannel}
}

// resolveSpecial handles the channels that are either forwarded or have a
// fixed local table, regardless of the local registry.
func resolveSpecial(channel, method string, arg rpcwire.Value) (Route, bool) {
	switch channel {
	case ChannelNexAgent:
		if _, ok := workspaceTargetMethods[method]; ok {
			return workspaceRoute(arg)
		}
		// nex-agent methods without a workspace are answered from a fixed
		// local table (helloConversationV4 etc.) by the browser dispatcher.
		return Route{Kind: TargetLocalUnsupported}, true

	case ChannelMcpSync:
		// Live server statuses belong to the machine running the session.
		// Everything else (load/save of the doc's servers) is server-owned.
		if method == "listWorkspaceMcpServerStatuses" || method == "listMcpServerStatuses" {
			// The status set is the machine's live sessions; without a workspace in
			// the argument there is one candidate: the first connected runner.
			if target := workspaceTarget(arg); target != (runners.Target{}) {
				return workspaceRoute(arg)
			}
			return Route{Kind: TargetFirstRunner}, true
		}
		return Route{Kind: TargetLocal}, true

	case ChannelSkills:
		// list/setEnabled/deleteSkill read and write the doc; copy/remove of the
		// shared store are no-ops in this release (all server-owned).
		return Route{Kind: TargetLocal}, true

	case ChannelNexSession, ChannelNexTask:
		// Every workspace-bound method on these channels carries the target at
		// the top level of its argument.
		if target := workspaceTarget(arg); target != (runners.Target{}) {
			return Route{
				Kind:              TargetWorkspace,
				WorkspacePath:     target.WorkspacePath,
				WorkspaceIdentity: target.WorkspaceIdentity,
			}, true
		}
		if channel == ChannelNexTask {
			if _, ok := nexusTaskGlobalMethods[method]; ok {
				// listPinnedTaskIds has no workspace in its args; MVP answers
				// from the first connected runner.
				return Route{Kind: TargetFirstRunner}, true
			}
		}
		return Route{Kind: TargetLocalUnsupported}, true

	case ChannelModelSelection, ChannelProviderSettings:
		// The server serves these channels from the config doc plus runner facts
		// (docs/next/config-mcp-skills.md decision 6), in both modes: while the doc
		// has no providers the view is compiled from the runner's own catalog as
		// reported in `configApplied.facts` (fallback), so the browser never needs
		// a machine-dependent forward for the composer. An older runner that
		// reports no catalog gets the stable "no runner connected" error instead
		// of an unanswered forward. Writes are always server-owned.
		return Route{Kind: TargetLocal}, true

	case ChannelConfig:
		// config.exportLocal reads a runner's local ~/.pi/agent; the runner id is
		// explicit (no workspace in the arg). Everything else is server-local.
		if method == "exportLocal" {
			if runnerID := stringFieldOf(positionalArgument(arg), "runnerId"); runnerID != "" {
				return Route{Kind: TargetRunner, RunnerID: runnerID}, true
			}
			return Route{Kind: TargetFirstRunner}, true
		}
		return Route{Kind: TargetLocal}, true
	}
	return Route{}, false
}

// ResolveEvent decides the destination of one event listen.
func ResolveEvent(channel, event string, arg rpcwire.Value, local func(string) bool) Route {
	if route, ok := resolveSpecialEvent(channel, event, arg); ok {
		return route
	}
	if local(channel) {
		return Route{Kind: TargetLocal}
	}
	return Route{Kind: TargetUnknownChannel}
}

func resolveSpecialEvent(channel, event string, arg rpcwire.Value) (Route, bool) {
	switch channel {
	case ChannelNexAgent, ChannelNexTask:
		if _, ok := eventWorkspaceMethods[event]; ok {
			return workspaceRoute(arg)
		}
		return Route{Kind: TargetLocalUnsupported}, true
	case ChannelModelSelection, ChannelProviderSettings:
		// onDidChange follows getView: server-owned in both modes (a config
		// version or a runner's fresh facts re-broadcast the view).
		return Route{Kind: TargetLocal}, true
	case ChannelConfig, ChannelMcpSync, ChannelSkills:
		// mcp-sync/skills events carry no live state; a listen stays local (and
		// never fires), so the browser's always-truthy proxy methods stay quiet.
		return Route{Kind: TargetLocal}, true
	}
	return Route{}, false
}

func workspaceRoute(arg rpcwire.Value) (Route, bool) {
	target := workspaceTarget(arg)
	if target == (runners.Target{}) {
		return Route{Kind: TargetLocalUnsupported}, true
	}
	return Route{
		Kind:              TargetWorkspace,
		WorkspacePath:     target.WorkspacePath,
		WorkspaceIdentity: target.WorkspaceIdentity,
	}, true
}

// workspaceTarget reads the workspace identity and path from the RPC argument.
//
// The web client reaches the server through ProxyChannel.toService, which
// always sends the method arguments as an array (`call(name, args)`), so the
// real shape is `[{workspacePath: "...", workspaceIdentity: "..."}]`. A bare
// object is also accepted so hand-written clients and tests can call without
// the wrapper. Only top-level fields of the first argument object are used:
// `workspaceScopes` (nex-task listTaskList) is a list of scopes, not a routing
// key.
func workspaceTarget(arg rpcwire.Value) runners.Target {
	switch typed := arg.(type) {
	case []any:
		for _, element := range typed {
			if target := workspaceTarget(element); target != (runners.Target{}) {
				return target
			}
		}
		return runners.Target{}
	case json.RawMessage:
		return workspaceTargetFromRaw(typed)
	case map[string]any:
		path, _ := typed["workspacePath"].(string)
		identity, _ := typed["workspaceIdentity"].(string)
		return normalizeTarget(path, identity)
	}
	return runners.Target{}
}

func workspaceTargetFromRaw(raw json.RawMessage) runners.Target {
	trimmed := strings.TrimSpace(string(raw))
	if strings.HasPrefix(trimmed, "[") {
		var elements []json.RawMessage
		if err := json.Unmarshal(raw, &elements); err != nil {
			return runners.Target{}
		}
		for _, element := range elements {
			if target := workspaceTargetFromRaw(element); target != (runners.Target{}) {
				return target
			}
		}
		return runners.Target{}
	}
	var probe struct {
		WorkspacePath     string `json:"workspacePath"`
		WorkspaceIdentity string `json:"workspaceIdentity"`
	}
	if err := json.Unmarshal(raw, &probe); err != nil {
		return runners.Target{}
	}
	return normalizeTarget(probe.WorkspacePath, probe.WorkspaceIdentity)
}

// stringFieldOf reads one string property of an argument object (arrays are
// searched element-wise so the caller may pass a positional envelope).
func stringFieldOf(arg rpcwire.Value, key string) string {
	switch typed := arg.(type) {
	case []any:
		for _, element := range typed {
			if value := stringFieldOf(element, key); value != "" {
				return value
			}
		}
	case json.RawMessage:
		trimmed := strings.TrimSpace(string(typed))
		// The undef sentinel (`{"$undef":true}`) is the client's undefined value,
		// not an argument object.
		if trimmed == "" || strings.Contains(trimmed, `"$undef"`) {
			return ""
		}
		var probe map[string]any
		if json.Unmarshal(typed, &probe) == nil {
			if text, ok := probe[key].(string); ok {
				return strings.TrimSpace(text)
			}
		}
	case map[string]any:
		if text, ok := typed[key].(string); ok {
			return strings.TrimSpace(text)
		}
	}
	return ""
}

// positionalArgument returns the first element of a `[arg]` call envelope.
//
// ProxyChannel.toService always sends `call(name, [...args])`, so a method's
// argument is wrapped in a one-element array; a bare object (hand-written
// client or test) is accepted as well. A `{"$undef":true}` element is the
// client's undefined value and yields no argument.
func positionalArgument(arg rpcwire.Value) rpcwire.Value {
	switch typed := arg.(type) {
	case []any:
		if len(typed) == 0 {
			return nil
		}
		return positionalArgument(typed[0])
	case json.RawMessage:
		trimmed := strings.TrimSpace(string(typed))
		if strings.HasPrefix(trimmed, "[") {
			var elements []json.RawMessage
			if json.Unmarshal(typed, &elements) != nil || len(elements) == 0 {
				return nil
			}
			return positionalArgument(elements[0])
		}
		if trimmed == "" || strings.Contains(trimmed, `"$undef"`) {
			return nil
		}
	}
	return arg
}

func normalizeTarget(path, identity string) runners.Target {
	path = strings.TrimSpace(path)
	identity = strings.TrimSpace(identity)
	// A non-runner identity (desktop `remote:` values) is not a server routing
	// key; the registry falls back to the path for it as well, so dropping it
	// here keeps Route equality checks simple.
	if _, _, ok := runners.ParseIdentity(identity); !ok {
		identity = ""
	}
	return runners.Target{WorkspacePath: path, WorkspaceIdentity: identity}
}
