// Package router decides whether a browser channel request is answered by a
// server-local channel or forwarded to the runner that owns a workspace.
//
// Routing is by workspacePath only. The UI treats any non-empty
// workspaceIdentity as a remote workspace, so the server never synthesizes
// one (docs/next/README.md "Workspace identity").
package router

import (
	"encoding/json"
	"strings"

	"github.com/tylinux/nex-next/server/internal/rpcwire"
)

// TargetKind describes who answers a request.
type TargetKind int

// Target kinds.
const (
	// TargetLocal is answered by a server-local channel implementation.
	TargetLocal TargetKind = iota
	// TargetWorkspace forwards to the runner owning WorkspacePath.
	TargetWorkspace
	// TargetFirstRunner forwards to the first connected runner (channels with
	// no workspace in their argument, e.g. the model catalog).
	TargetFirstRunner
	// TargetUnknownChannel rejects immediately with the ChannelServer error.
	TargetUnknownChannel
	// TargetLocalUnsupported is a local channel/method the server does not
	// implement; it returns a MethodNotFound error.
	TargetLocalUnsupported
)

// Route is the resolved destination for one channel request.
type Route struct {
	Kind TargetKind
	// WorkspacePath is set for TargetWorkspace.
	WorkspacePath string
}

// Channel names with special routing.
const (
	ChannelNexAgent         = "nex-agent"
	ChannelNexSession       = "nex-session"
	ChannelNexTask          = "nex-task"
	ChannelModelSelection   = "model-selection"
	ChannelProviderSettings = "provider-settings"
)

// workspaceTargetMethods enumerates forwarded nex-agent methods that carry a
// workspace path at the top level of their argument.
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
	"listMcpServerStatuses":            {},
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
// carries the workspace path.
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
			if path := workspacePathFromArg(arg); path != "" {
				return Route{Kind: TargetWorkspace, WorkspacePath: path}, true
			}
			return Route{Kind: TargetLocalUnsupported}, true
		}
		// nex-agent methods without a workspace are answered from a fixed
		// local table (helloConversationV4 etc.) by the browser dispatcher.
		return Route{Kind: TargetLocalUnsupported}, true

	case ChannelNexSession, ChannelNexTask:
		if path := workspacePathFromArg(arg); path != "" {
			return Route{Kind: TargetWorkspace, WorkspacePath: path}, true
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
		return Route{Kind: TargetFirstRunner}, true
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
			if path := workspacePathFromArg(arg); path != "" {
				return Route{Kind: TargetWorkspace, WorkspacePath: path}, true
			}
			return Route{Kind: TargetLocalUnsupported}, true
		}
		return Route{Kind: TargetLocalUnsupported}, true
	case ChannelModelSelection, ChannelProviderSettings:
		// Their onDidChange events are plain listens with no workspace; forward
		// to the same runner that serves getView.
		return Route{Kind: TargetFirstRunner}, true
	}
	return Route{}, false
}

// workspacePathFromArg reads the workspacePath field from the RPC argument.
//
// The web client reaches the server through ProxyChannel.toService, which
// always sends the method arguments as an array (`call(name, args)`), so the
// real shape is `[{workspacePath: "..."}]`. A bare object is also accepted so
// hand-written clients and tests can call without the wrapper.
func workspacePathFromArg(arg rpcwire.Value) string {
	switch typed := arg.(type) {
	case []any:
		for _, element := range typed {
			if path := workspacePathFromArg(element); path != "" {
				return path
			}
		}
		return ""
	case json.RawMessage:
		return workspacePathFromRaw(typed)
	case map[string]any:
		if value, ok := typed["workspacePath"].(string); ok {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func workspacePathFromRaw(raw json.RawMessage) string {
	trimmed := strings.TrimSpace(string(raw))
	if strings.HasPrefix(trimmed, "[") {
		var elements []json.RawMessage
		if err := json.Unmarshal(raw, &elements); err != nil {
			return ""
		}
		for _, element := range elements {
			if path := workspacePathFromRaw(element); path != "" {
				return path
			}
		}
		return ""
	}
	var probe struct {
		WorkspacePath string `json:"workspacePath"`
	}
	if err := json.Unmarshal(raw, &probe); err != nil {
		return ""
	}
	return strings.TrimSpace(probe.WorkspacePath)
}
