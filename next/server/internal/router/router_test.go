package router_test

import (
	"encoding/json"
	"testing"

	"github.com/nex-harness/nex/server/internal/router"
	"github.com/nex-harness/nex/server/internal/rpcwire"
	"github.com/nex-harness/nex/server/internal/runners"
)

func localNone(string) bool         { return false }
func localSetting(name string) bool { return name == "setting" }

func TestResolveLocalChannel(t *testing.T) {
	route := router.Resolve("setting", "get", rpcwire.Undefined{}, localSetting)
	if route.Kind != router.TargetLocal {
		t.Fatalf("route = %+v, want TargetLocal", route)
	}
}

func TestResolveForwardedNexAgentCall(t *testing.T) {
	cases := []struct {
		name string
		arg  rpcwire.Value
	}{
		{"bare object", json.RawMessage(`{"workspacePath":"/w","sessionId":"s"}`)},
		{"client args array", json.RawMessage(`[{"workspacePath":"/w","sessionId":"s"}]`)},
		{"decoded array", []any{json.RawMessage(`{"workspacePath":"/w"}`)}},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			route := router.Resolve("nex-agent", "subscribeConversationV4", testCase.arg, localNone)
			if route.Kind != router.TargetWorkspace || route.WorkspacePath != "/w" {
				t.Fatalf("route = %+v, want workspace /w", route)
			}
		})
	}
}

func TestResolveWorkspaceIdentityFromClientArgsArray(t *testing.T) {
	// The UI sends `[{workspacePath, workspaceIdentity, ...}]`; both fields must
	// survive the array unwrapping.
	route := router.Resolve("nex-agent", "subscribeConversationV4",
		json.RawMessage(`[{"workspacePath":"/w","workspaceIdentity":"runner:alpha:/w","sessionId":"s"}]`), localNone)
	if route.Kind != router.TargetWorkspace {
		t.Fatalf("route = %+v", route)
	}
	if route.WorkspaceIdentity != "runner:alpha:/w" || route.WorkspacePath != "/w" {
		t.Fatalf("route = %+v, want identity runner:alpha:/w and path /w", route)
	}

	eventRoute := router.ResolveEvent("nex-agent", "onDynamicConversationFrame",
		json.RawMessage(`[{"workspacePath":"/w","workspaceIdentity":"runner:alpha:/w"}]`), localNone)
	if eventRoute.Kind != router.TargetWorkspace || eventRoute.WorkspaceIdentity != "runner:alpha:/w" {
		t.Fatalf("event route = %+v", eventRoute)
	}
}

func TestResolveIdentityWithoutPathStillRoutes(t *testing.T) {
	// A runner identity alone is a complete routing key; the server ignores the
	// path for routing and the runner derives cwd from it.
	route := router.Resolve("nex-task", "listTasks",
		json.RawMessage(`[{"workspaceIdentity":"runner:alpha:C:\\Users\\me\\app"}]`), localNone)
	if route.Kind != router.TargetWorkspace {
		t.Fatalf("route = %+v", route)
	}
	if route.WorkspaceIdentity != `runner:alpha:C:\Users\me\app` {
		t.Fatalf("identity = %q", route.WorkspaceIdentity)
	}
}

func TestResolveIgnoresNonRunnerIdentityAndWorkspaceScopes(t *testing.T) {
	// A desktop `remote:` identity is not a server routing key. The path is used
	// as the legacy fallback.
	route := router.Resolve("nex-agent", "subscribeConversationV4",
		json.RawMessage(`[{"workspacePath":"/w","workspaceIdentity":"remote:ssh:host:22:me:/w"}]`), localNone)
	if route.Kind != router.TargetWorkspace || route.WorkspaceIdentity != "" || route.WorkspacePath != "/w" {
		t.Fatalf("route = %+v, want a path-only route", route)
	}

	// `workspaceScopes` is a list (nex-task.listTaskList), never a routing key.
	scopesOnly := router.ResolveEvent("nex-task", "onDynamicWorkspaceEvent",
		json.RawMessage(`[{"workspaceScopes":[{"workspacePath":"/other","workspaceIdentity":"runner:beta:/other"}]}]`), localNone)
	if scopesOnly.Kind != router.TargetLocalUnsupported {
		t.Fatalf("workspaceScopes must not route: %+v", scopesOnly)
	}
}

func TestResolveWorkspaceLessNexAgentMethods(t *testing.T) {
	for _, method := range []string{"helloConversationV4", "initializeConversationV4", "syncAppRuntimePreferences", "listAllAutomations"} {
		route := router.Resolve("nex-agent", method, rpcwire.Undefined{}, localNone)
		if route.Kind != router.TargetLocalUnsupported {
			t.Fatalf("%s route = %+v, want local fallback", method, route)
		}
	}
}

func TestResolveNexTask(t *testing.T) {
	route := router.Resolve("nex-task", "listTasks", json.RawMessage(`[{"workspacePath":"/w"}]`), localNone)
	if route.Kind != router.TargetWorkspace || route.WorkspacePath != "/w" {
		t.Fatalf("listTasks route = %+v", route)
	}
	route = router.Resolve("nex-task", "listPinnedTaskIds", rpcwire.Undefined{}, localNone)
	if route.Kind != router.TargetFirstRunner {
		t.Fatalf("listPinnedTaskIds route = %+v, want first runner", route)
	}
	route = router.ResolveEvent("nex-task", "onDynamicWorkspaceEvent",
		json.RawMessage(`[{"workspacePath":"/w"}]`), localNone)
	if route.Kind != router.TargetWorkspace || route.WorkspacePath != "/w" {
		t.Fatalf("onDynamicWorkspaceEvent route = %+v", route)
	}

	identityRoute := router.Resolve("nex-session", "readWorkspacePresentation",
		json.RawMessage(`[{"workspacePath":"/w","workspaceIdentity":"runner:alpha:/w"}]`), localNone)
	if identityRoute.Kind != router.TargetWorkspace || identityRoute.WorkspaceIdentity != "runner:alpha:/w" {
		t.Fatalf("nex-session route = %+v", identityRoute)
	}
}

func TestResolveModelCatalogChannels(t *testing.T) {
	// provider-settings/model-selection are answered by the server in both
	// modes: the doc plus runner facts, and in fallback the runner's own catalog
	// as reported in configApplied facts. A forward would leave an older runner
	// (or none) unanswered, so even a targeted getView stays local.
	for _, channel := range []string{"model-selection", "provider-settings"} {
		for name, arg := range map[string]rpcwire.Value{
			"undefined": rpcwire.Undefined{},
			"undef-arg": json.RawMessage(`[{"$undef":true}]`),
			"targeted":  json.RawMessage(`[{"selection":null,"workspacePath":"/w","workspaceIdentity":"runner:beta:/w"}]`),
		} {
			if route := router.Resolve(channel, "getView", arg, localNone); route.Kind != router.TargetLocal {
				t.Fatalf("%s.getView(%s) route = %+v, want local", channel, name, route)
			}
		}
		eventRoute := router.ResolveEvent(channel, "onDidChange", rpcwire.Undefined{}, localNone)
		if eventRoute.Kind != router.TargetLocal {
			t.Fatalf("%s event route = %+v, want local", channel, eventRoute)
		}
		if refresh := router.Resolve(channel, "refresh", json.RawMessage(`[{"reason":"manual"}]`), localNone); refresh.Kind != router.TargetLocal {
			t.Fatalf("%s.refresh route = %+v", channel, refresh)
		}
		// Writes are always server-owned.
		if route := router.Resolve(channel, "createPersonalProvider", rpcwire.Undefined{}, localNone); route.Kind != router.TargetLocal {
			t.Fatalf("%s write route = %+v, want local", channel, route)
		}
	}
}

func TestResolveConfigChannel(t *testing.T) {
	route := router.Resolve("config", "exportLocal", json.RawMessage(`[{"runnerId":"alpha"}]`), localNone)
	if route.Kind != router.TargetRunner || route.RunnerID != "alpha" {
		t.Fatalf("exportLocal route = %+v", route)
	}
	// The browser wraps a single argument in an array; the runnerId must be read
	// from inside it (exportLocal({runnerId}) otherwise fell back to the first
	// connected runner and imported the wrong machine).
	if wrapped := router.Resolve("config", "exportLocal", json.RawMessage(`[{"runnerId":"beta"}]`), localNone); wrapped.Kind != router.TargetRunner || wrapped.RunnerID != "beta" {
		t.Fatalf("wrapped exportLocal route = %+v", wrapped)
	}
	if fallback := router.Resolve("config", "exportLocal", rpcwire.Undefined{}, localNone); fallback.Kind != router.TargetFirstRunner {
		t.Fatalf("exportLocal without runnerId route = %+v", fallback)
	}
	if other := router.Resolve("config", "status", rpcwire.Undefined{}, localNone); other.Kind != router.TargetLocal {
		t.Fatalf("config.status route = %+v", other)
	}
}

func TestResolveUnknownChannel(t *testing.T) {
	route := router.Resolve("nope", "anything", rpcwire.Undefined{}, localNone)
	if route.Kind != router.TargetUnknownChannel {
		t.Fatalf("route = %+v, want unknown", route)
	}
}

func TestRouteTarget(t *testing.T) {
	route := router.Route{Kind: router.TargetWorkspace, WorkspacePath: "/w", WorkspaceIdentity: "runner:a:/w"}
	if got := route.Target(); got != (runners.Target{WorkspacePath: "/w", WorkspaceIdentity: "runner:a:/w"}) {
		t.Fatalf("Target() = %+v", got)
	}
}

func TestResolveFileChangesAndRewindPreviewForwardByWorkspace(t *testing.T) {
	// Batch 3 (docs/next/attachments-rewind.md B): the changed-files and rewind-preview
	// queries are workspace-targeted reads that the runner answers; they must reach the
	// runner that owns the workspace, carrying its identity when the UI sends one.
	methods := []string{"conversationFileChangesV4", "conversationFileRewindPreviewV4"}
	for _, method := range methods {
		t.Run(method, func(t *testing.T) {
			route := router.Resolve("nex-agent", method,
				json.RawMessage(`[{"workspacePath":"/w","workspaceIdentity":"runner:alpha:/w","sessionId":"s","target":{"rowId":1,"entityId":"pi:turn:u1"},"baseRevision":3,"baseLogEpoch":"e"}]`), localNone)
			if route.Kind != router.TargetWorkspace {
				t.Fatalf("route = %+v, want TargetWorkspace", route)
			}
			if route.WorkspaceIdentity != "runner:alpha:/w" || route.WorkspacePath != "/w" {
				t.Fatalf("route = %+v, want identity runner:alpha:/w and path /w", route)
			}
		})
	}
}

func TestResolveFileChangesWithoutWorkspaceIsNotForwarded(t *testing.T) {
	// No workspace target = no runner can own the request; the browser gets a local
	// "unsupported" answer instead of a broadcast.
	route := router.Resolve("nex-agent", "conversationFileChangesV4",
		json.RawMessage(`[{"sessionId":"s"}]`), localNone)
	if route.Kind != router.TargetLocalUnsupported {
		t.Fatalf("route = %+v, want TargetLocalUnsupported", route)
	}
}

func TestResolveMcpSyncAndSkills(t *testing.T) {
	// Saving/loading MCP is server-owned (the config doc); live statuses belong
	// to the runner that owns the workspace.
	if route := router.Resolve("mcp-sync", "saveMcpToUserDirectory", json.RawMessage(`[{"action":"upsert","name":"x"}]`), localNone); route.Kind != router.TargetLocal {
		t.Fatalf("save route = %+v", route)
	}
	if route := router.Resolve("mcp-sync", "loadMcpFromUserDirectory", json.RawMessage(`[{"workspacePath":"/w"}]`), localNone); route.Kind != router.TargetLocal {
		t.Fatalf("load route = %+v", route)
	}
	statuses := router.Resolve("mcp-sync", "listWorkspaceMcpServerStatuses",
		json.RawMessage(`[{"workspacePath":"/w","workspaceIdentity":"runner:beta:/w"}]`), localNone)
	if statuses.Kind != router.TargetWorkspace || statuses.WorkspaceIdentity != "runner:beta:/w" {
		t.Fatalf("statuses route = %+v", statuses)
	}
	// Without a workspace there is one candidate: the first connected runner.
	if route := router.Resolve("mcp-sync", "listWorkspaceMcpServerStatuses", rpcwire.Undefined{}, localNone); route.Kind != router.TargetFirstRunner {
		t.Fatalf("workspace-less statuses route = %+v", route)
	}
	listen := router.ResolveEvent("mcp-sync", "onDidChange", rpcwire.Undefined{}, localNone)
	if listen.Kind != router.TargetLocal {
		t.Fatalf("mcp-sync event = %+v", listen)
	}
	for _, method := range []string{"list", "setEnabled", "deleteSkill"} {
		if route := router.Resolve("skills", method, json.RawMessage(`[{"workspacePath":"/w"}]`), localNone); route.Kind != router.TargetLocal {
			t.Fatalf("skills.%s route = %+v", method, route)
		}
	}
	if route := router.Resolve("commands", "list", rpcwire.Undefined{}, func(name string) bool { return name == "commands" }); route.Kind != router.TargetLocal {
		t.Fatalf("commands route = %+v", route)
	}
}
