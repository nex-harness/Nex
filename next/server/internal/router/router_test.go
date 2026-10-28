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
	// searchTasks 不在任何 allowlist 里：nex-task 按参数里的 workspace target
	// 路由（docs/next/automations-views-search.md §C）。浏览器不直接调用它
	// （只有 window-controller 通过 registry 扇出），这里只固化“带 target 就能
	// 路由”的事实，避免以后误加一个仅限 controller 的本地条目。
	route = router.Resolve("nex-task", "searchTasks",
		json.RawMessage(`[{"workspacePath":"/w","query":"needle","limit":200}]`), localNone)
	if route.Kind != router.TargetWorkspace || route.WorkspacePath != "/w" {
		t.Fatalf("searchTasks route = %+v", route)
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

// TestResolveWorkspaceToolChannels covers the batch-1 routing (docs/next/
// workspace-tools.md decision 1): file/git/watcher/media/terminal/system
// forward by the injected workspace target and fall back to local stubs (or
// first runner for system) without one; id-only calls route by the tool id
// kinds the browserws dispatcher resolves through its binding tables.
func TestResolveWorkspaceToolChannels(t *testing.T) {
	target := json.RawMessage(`[{"workspacePath":"/w","workspaceIdentity":"runner:alpha:/w"}]`)

	// Workspace-targeted calls forward to the runner.
	for _, channel := range []string{"file", "git", "git-checkpoint", "media-preview"} {
		for _, method := range []string{"readdir", "refresh", "prepare"} {
			route := router.Resolve(channel, method, target, localNone)
			if route.Kind != router.TargetWorkspace || route.WorkspaceIdentity != "runner:alpha:/w" {
				t.Fatalf("%s.%s route = %+v, want TargetWorkspace alpha", channel, method, route)
			}
		}
	}

	// Without a target the call stays local (the stub answers the folder picker
	// and conversation-workspace helpers).
	for _, channel := range []string{"file", "git", "git-checkpoint", "media-preview"} {
		route := router.Resolve(channel, "readdir", json.RawMessage(`[{"path":"/any"}]`), localNone)
		if route.Kind != router.TargetLocal {
			t.Fatalf("%s target-less route = %+v, want TargetLocal", channel, route)
		}
	}

	// file-watcher: watch carries the target; unwatch routes by id binding.
	if route := router.Resolve("file-watcher", "watch", target, localNone); route.Kind != router.TargetWorkspace {
		t.Fatalf("file-watcher.watch route = %+v", route)
	}
	if route := router.Resolve("file-watcher", "unwatch", json.RawMessage(`[{"id":"w1"}]`), localNone); route.Kind != router.TargetWatchID {
		t.Fatalf("file-watcher.unwatch route = %+v, want TargetWatchID", route)
	}
	if route := router.ResolveEvent("file-watcher", "onDynamicChange", json.RawMessage(`["w1"]`), localNone); route.Kind != router.TargetWatchID {
		t.Fatalf("file-watcher.onDynamicChange route = %+v, want TargetWatchID", route)
	}

	// terminal: create routes by workspace (or first runner without a target);
	// write/resize/dispose and the listens route by the terminal id binding.
	if route := router.Resolve("terminal", "create", target, localNone); route.Kind != router.TargetWorkspace {
		t.Fatalf("terminal.create route = %+v", route)
	}
	if route := router.Resolve("terminal", "create", rpcwire.Undefined{}, localNone); route.Kind != router.TargetFirstRunner {
		t.Fatalf("terminal.create target-less route = %+v, want TargetFirstRunner", route)
	}
	for _, method := range []string{"write", "resize", "dispose"} {
		route := router.Resolve("terminal", method, json.RawMessage(`[{"id":"t1","data":"x"}]`), localNone)
		if route.Kind != router.TargetTerminalID {
			t.Fatalf("terminal.%s route = %+v, want TargetTerminalID", method, route)
		}
	}
	for _, event := range []string{"onDynamicData", "onDynamicExit"} {
		route := router.ResolveEvent("terminal", event, json.RawMessage(`["t1"]`), localNone)
		if route.Kind != router.TargetTerminalID {
			t.Fatalf("terminal.%s route = %+v, want TargetTerminalID", event, route)
		}
	}

	// system: host facts of the workspace machine; without a target the first
	// runner answers (DirectoryBrowser's picker).
	if route := router.Resolve("system", "info", target, localNone); route.Kind != router.TargetWorkspace {
		t.Fatalf("system.info route = %+v", route)
	}
	if route := router.Resolve("system", "info", rpcwire.Undefined{}, localNone); route.Kind != router.TargetFirstRunner {
		t.Fatalf("system.info target-less route = %+v, want TargetFirstRunner", route)
	}
}

// TestResolveRunnerWorkspacesAndMachineScope covers docs/next/runner-folders.md
// decisions 2/3: `runner-workspaces.*` and the browse-only file/system calls
// made with the machine identity `runner:<id>:~` must reach that runner, never a
// local stub.
func TestResolveRunnerWorkspacesAndMachineScope(t *testing.T) {
	machine := json.RawMessage(`[{"workspacePath":"~","workspaceIdentity":"runner:abc:~","path":"/home/me/x"}]`)

	for _, method := range []string{"list", "add", "remove"} {
		route := router.Resolve("runner-workspaces", method, machine, localNone)
		if route.Kind != router.TargetWorkspace {
			t.Fatalf("runner-workspaces.%s route = %+v, want TargetWorkspace", method, route)
		}
		if route.WorkspaceIdentity != "runner:abc:~" || route.WorkspacePath != "~" {
			t.Fatalf("runner-workspaces.%s target = %+v", method, route)
		}
		// The registry target carries the identity that routes to the machine.
		if route.Target() != (runners.Target{WorkspacePath: "~", WorkspaceIdentity: "runner:abc:~"}) {
			t.Fatalf("runner-workspaces.%s Target() = %+v", method, route.Target())
		}
	}

	// Without a target there is no machine to address; it must not fall back to
	// the first runner (that would edit the wrong machine's folder list).
	if route := router.Resolve("runner-workspaces", "add", json.RawMessage(`[{"path":"/x"}]`), localNone); route.Kind != router.TargetLocalUnsupported {
		t.Fatalf("target-less runner-workspaces route = %+v, want local unsupported", route)
	}

	// Browse-only machine scope: file.readdir / file.stat and system.info route
	// to the machine; every other file method is the runner's own guard.
	for _, method := range []string{"readdir", "stat"} {
		route := router.Resolve("file", method, machine, localNone)
		if route.Kind != router.TargetWorkspace || route.WorkspaceIdentity != "runner:abc:~" {
			t.Fatalf("file.%s route = %+v", method, route)
		}
	}
	if route := router.Resolve("system", "info", machine, localNone); route.Kind != router.TargetWorkspace || route.WorkspaceIdentity != "runner:abc:~" {
		t.Fatalf("system.info route = %+v", route)
	}

	// The machine identity is not a workspace: sessions still route there only
	// because the runner rejects them, but the server must not rewrite it to a
	// path route.
	identityOnly := json.RawMessage(`[{"workspaceIdentity":"runner:abc:~"}]`)
	if route := router.Resolve("nex-task", "listTasks", identityOnly, localNone); route.Kind != router.TargetWorkspace || route.WorkspaceIdentity != "runner:abc:~" {
		t.Fatalf("nex-task identity-only route = %+v", route)
	}
}

// TestAutomationsStayLocal covers the server-owned automations channel
// (docs/next/automations-views-search.md §A): all ten methods answer from the
// NexAgentLocal automations delegate, never a runner forward. They carry
// workspacePath in their arguments, so a workspaceTargetMethods entry would
// silently break them with runner UnknownMethod (found by the E2E: the form
// submit failed while listAllAutomations — no such entry — worked).
func TestAutomationsStayLocal(t *testing.T) {
	for _, method := range []string{
		"listAllAutomations", "listAutomations", "createAutomation", "updateAutomation",
		"deleteAutomation", "setAutomationEnabled", "restartAutomation", "runAutomationNow",
		"listAutomationRuns", "deleteAutomationRun",
	} {
		for _, arg := range []string{
			`[{"workspacePath":"/w","title":"x"}]`,
			`[{"automationId":"a:1"}]`,
		} {
			route := router.Resolve("nex-agent", method, json.RawMessage(arg), localNone)
			if route.Kind != router.TargetLocalUnsupported {
				t.Fatalf("%s(%s) = %v, want TargetLocalUnsupported (server-owned)", method, arg, route.Kind)
			}
		}
	}
}
