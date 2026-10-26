package local

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"strings"
	"testing"

	"github.com/tylinux/nex-next/server/internal/configstore"
	"github.com/tylinux/nex-next/server/internal/rpcwire"
	"github.com/tylinux/nex-next/server/internal/runners"
)

func newTestMcpSync(t *testing.T) (*McpSync, *Manager, *runners.Registry) {
	t.Helper()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	registry := runners.NewRegistry(logger)
	store := configstore.NewStore(t.TempDir(), logger)
	manager := NewManager(store, registry, logger, func() {})
	return newMcpSync(manager), manager, registry
}

func callChannel(t *testing.T, channel Channel, method, arg string) (json.RawMessage, error) {
	t.Helper()
	value, err := channel.Call(context.Background(), &Env{}, method, rpcwire.Value(json.RawMessage(arg)))
	if err != nil {
		return nil, err
	}
	encoded, marshalErr := json.Marshal(value)
	return encoded, marshalErr
}

// The settings page's upsert → load → set-enabled → delete loop, in the exact
// argument shapes the browser sends.
func TestMcpSyncUpsertLoadToggleDelete(t *testing.T) {
	channel, manager, _ := newTestMcpSync(t)

	if _, err := callChannel(t, channel, "saveMcpToUserDirectory", `[{"action":"upsert","source":"nexagentmcp",`+
		`"name":"docs","config":{"type":"http","url":"https://docs.example.test/mcp",`+
		`"headers":{"Authorization":"Bearer secret-token"}}}]`); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	loaded, err := callChannel(t, channel, "loadMcpFromUserDirectory", `[{"workspacePath":"/w"}]`)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	var list struct {
		Servers []map[string]any `json:"servers"`
	}
	if err := json.Unmarshal(loaded, &list); err != nil {
		t.Fatal(err)
	}
	if len(list.Servers) != 1 || list.Servers[0]["name"] != "docs" {
		t.Fatalf("servers = %s", loaded)
	}
	// The settings list shows a mask, never the header value.
	if strings.Contains(string(loaded), "secret-token") {
		t.Fatalf("load leaked the header: %s", loaded)
	}
	config := list.Servers[0]["config"].(map[string]any)
	headers := config["headers"].(map[string]any)
	if headers["Authorization"] != mcpSecretMask {
		t.Fatalf("headers = %#v", headers)
	}
	// The plaintext became a stored secret (the runner link carries it).
	doc, err := manager.store.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	ref := doc.Scopes.Global.MCP["docs"].Transport.Headers["Authorization"]
	if !strings.HasPrefix(ref, configstore.SecretRefPrefix) {
		t.Fatalf("header ref = %q", ref)
	}
	if value, ok, _ := manager.store.Secrets().Get(ref); !ok || value != "Bearer secret-token" {
		t.Fatalf("stored secret = %q %v", value, ok)
	}

	if _, err := callChannel(t, channel, "saveMcpToUserDirectory", `[{"action":"set-enabled","source":"nexagentmcp","name":"docs","enabled":false}]`); err != nil {
		t.Fatalf("set-enabled: %v", err)
	}
	doc, _ = manager.store.Snapshot()
	if doc.Scopes.Global.MCP["docs"].Enabled {
		t.Fatal("still enabled")
	}
	if _, err := callChannel(t, channel, "saveMcpToUserDirectory", `[{"action":"delete","source":"nexagentmcp","name":"docs"}]`); err != nil {
		t.Fatalf("delete: %v", err)
	}
	doc, _ = manager.store.Snapshot()
	if _, ok := doc.Scopes.Global.MCP["docs"]; ok {
		t.Fatal("not deleted")
	}
}

func TestMcpSyncStdioRoundTripAndVariants(t *testing.T) {
	channel, manager, _ := newTestMcpSync(t)
	if _, err := callChannel(t, channel, "saveMcpToUserDirectory", `[{"action":"upsert","source":"nexagentmcp",`+
		`"name":"echo","config":{"type":"stdio","command":"node","args":["server.mjs"],`+
		`"env":{"TOKEN":"abc"},"variants":{"darwin":{"command":"/opt/node","args":["x"]}}}}]`); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	doc, _ := manager.store.Snapshot()
	server := doc.Scopes.Global.MCP["echo"]
	if server.Transport.Command != "node" || len(server.Transport.Variants) != 1 {
		t.Fatalf("server = %#v", server)
	}
	if server.Transport.Variants["darwin"].Command != "/opt/node" {
		t.Fatalf("variant = %#v", server.Transport.Variants["darwin"])
	}
}

func TestMcpSyncRejectsUnknownTransport(t *testing.T) {
	channel, _, _ := newTestMcpSync(t)
	// An empty config falls back to stdio (the UI omits `type` for commands);
	// without a command the doc write is refused by validation.
	if _, err := callChannel(t, channel, "saveMcpToUserDirectory", `[{"action":"upsert","source":"nexagentmcp",`+
		`"name":"broken","config":{}}]`); err == nil || !strings.Contains(err.Error(), "command") {
		t.Fatalf("expected a command error, got %v", err)
	}
}

// A doc without MCP servers answers empty statuses without needing a runner.
func TestMcpSyncStatusesEmptyWithoutDocServers(t *testing.T) {
	channel, _, _ := newTestMcpSync(t)
	statuses, err := callChannel(t, channel, "listWorkspaceMcpServerStatuses",
		`[{"workspacePath":"/w","mode":"connect","mcpServers":[]}]`)
	if err != nil {
		t.Fatalf("statuses: %v", err)
	}
	if string(statuses) != `{"statuses":{}}` {
		t.Fatalf("statuses = %s", statuses)
	}
}

// With servers in the doc the method forwards to the owning runner.
func TestMcpSyncStatusesForwardToRunner(t *testing.T) {
	channel, manager, registry := newTestMcpSync(t)
	if _, err := callChannel(t, channel, "saveMcpToUserDirectory", `[{"action":"upsert","source":"nexagentmcp",`+
		`"name":"echo","config":{"type":"stdio","command":"node"}}]`); err != nil {
		t.Fatal(err)
	}
	link := &statusLink{statuses: `{"statuses":{"echo":{"status":"connected","transport":"stdio","toolCount":1,"updatedAt":"2026-01-01T00:00:00Z"}}}`}
	registry.Register(runners.Info{RunnerID: "alpha", Name: "alpha", Version: "test",
		Workspaces: []runners.Workspace{{Path: "/w", Label: "w"}}}, link)
	statuses, err := callChannel(t, channel, "listWorkspaceMcpServerStatuses", `[{"workspacePath":"/w"}]`)
	if err != nil {
		t.Fatalf("statuses: %v", err)
	}
	if !strings.Contains(string(statuses), `"echo"`) || link.lastChannel != "mcp-sync" {
		t.Fatalf("statuses = %s channel = %s", statuses, link.lastChannel)
	}
	_ = manager
}

// statusLink answers the forwarded status call.
type statusLink struct {
	statuses    string
	lastChannel string
}

func (l *statusLink) Call(_ context.Context, channel, _ string, _ json.RawMessage) (json.RawMessage, error) {
	l.lastChannel = channel
	return json.RawMessage(l.statuses), nil
}
func (l *statusLink) Listen(string, string, string, json.RawMessage, func(json.RawMessage)) error {
	return nil
}
func (l *statusLink) Unlisten(string)       {}
func (l *statusLink) Close()                {}
func (l *statusLink) Done() <-chan struct{} { return nil }
