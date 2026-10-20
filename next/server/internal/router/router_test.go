package router_test

import (
	"encoding/json"
	"testing"

	"github.com/tylinux/nex-next/server/internal/router"
	"github.com/tylinux/nex-next/server/internal/rpcwire"
	"github.com/tylinux/nex-next/server/internal/runners"
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
	for _, channel := range []string{"model-selection", "provider-settings"} {
		route := router.Resolve(channel, "getView", rpcwire.Undefined{}, localNone)
		if route.Kind != router.TargetFirstRunner {
			t.Fatalf("%s route = %+v, want first runner", channel, route)
		}
		eventRoute := router.ResolveEvent(channel, "onDidChange", rpcwire.Undefined{}, localNone)
		if eventRoute.Kind != router.TargetFirstRunner {
			t.Fatalf("%s event route = %+v, want first runner", channel, eventRoute)
		}
		// Other operations have no workspace target and stay first-runner.
		if refresh := router.Resolve(channel, "refresh", json.RawMessage(`[{"reason":"manual"}]`), localNone); refresh.Kind != router.TargetFirstRunner {
			t.Fatalf("%s.refresh route = %+v", channel, refresh)
		}
	}
}

func TestResolveModelCatalogGetViewWithWorkspaceTarget(t *testing.T) {
	route := router.Resolve("model-selection", "getView",
		json.RawMessage(`[{"selection":null,"workspacePath":"/w","workspaceIdentity":"runner:beta:/w"}]`), localNone)
	if route.Kind != router.TargetWorkspace || route.WorkspaceIdentity != "runner:beta:/w" || route.WorkspacePath != "/w" {
		t.Fatalf("model-selection.getView route = %+v", route)
	}

	// A path without identity is not a machine key for the model catalog: it
	// stays on the first connected runner (matching the spec's "identity, else
	// first runner").
	pathOnly := router.Resolve("provider-settings", "getView",
		json.RawMessage(`[{"selection":null,"workspacePath":"/w"}]`), localNone)
	if pathOnly.Kind != router.TargetFirstRunner {
		t.Fatalf("provider-settings.getView path-only route = %+v, want first runner", pathOnly)
	}

	// onDidChange is a plain event with no arg: it always follows the first
	// runner.
	eventRoute := router.ResolveEvent("model-selection", "onDidChange",
		json.RawMessage(`[{"workspacePath":"/w","workspaceIdentity":"runner:beta:/w"}]`), localNone)
	if eventRoute.Kind != router.TargetFirstRunner {
		t.Fatalf("onDidChange route = %+v, want first runner", eventRoute)
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
