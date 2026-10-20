package router_test

import (
	"encoding/json"
	"testing"

	"github.com/tylinux/nex-next/server/internal/router"
	"github.com/tylinux/nex-next/server/internal/rpcwire"
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
	}
}

func TestResolveUnknownChannel(t *testing.T) {
	route := router.Resolve("nope", "anything", rpcwire.Undefined{}, localNone)
	if route.Kind != router.TargetUnknownChannel {
		t.Fatalf("route = %+v, want unknown", route)
	}
}
