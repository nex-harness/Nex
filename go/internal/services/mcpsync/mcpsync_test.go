// mcpsync_test.go: the IMcpSyncService behavior suite — the mcp.servers /
// mcpServers dual-source read with the legacy enable migration, the
// enabled-in-config write semantics, candidates/export/import with
// filesystem path rewriting, and the agent-side listing refusal.
package mcpsync

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func newService(t *testing.T) (*Service, string) {
	t.Helper()
	home := t.TempDir()
	return New(Options{HomeDir: home}), home
}

func writeNexConfig(t *testing.T, home string, configJSON string) {
	t.Helper()
	path := filepath.Join(home, ".nex", "cli", "config.json")
	os.MkdirAll(filepath.Dir(path), 0o755)
	os.WriteFile(path, []byte(configJSON), 0o644)
}

// TestCandidatesNexPrimary: .nex mcp.servers wins; .agents is the fallback.
func TestCandidatesNexPrimary(t *testing.T) {
	service, home := newService(t)
	writeNexConfig(t, home, `{"mcp":{"servers":{"primary":{"command":"run-primary"}}}}`)
	agentsPath := filepath.Join(home, ".agents", "mcp.json")
	os.MkdirAll(filepath.Dir(agentsPath), 0o755)
	os.WriteFile(agentsPath, []byte(`{"mcpServers":{"fallback":{"command":"run-fallback"}}}`), 0o644)

	result, err := service.ListLocalUserMcpCandidates(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Candidates) != 1 || result.Candidates[0].Name != "primary" {
		t.Fatalf("candidates = %+v", result.Candidates)
	}
	if result.Candidates[0].Source != "nex" || result.Candidates[0].Path != filepath.Join(home, ".nex", "cli", "config.json") {
		t.Fatalf("candidate = %+v", result.Candidates[0])
	}
	if result.LocalHomeDir != home {
		t.Fatalf("localHomeDir = %s", result.LocalHomeDir)
	}

	// Without the nex config, the agents file answers.
	os.Remove(filepath.Join(home, ".nex", "cli", "config.json"))
	result, err = service.ListLocalUserMcpCandidates(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Candidates) != 1 || result.Candidates[0].Name != "fallback" || result.Candidates[0].Source != "agents" {
		t.Fatalf("fallback candidates = %+v", result.Candidates)
	}
}

// TestLegacyEnableMigration: a config carrying `enable` migrates to
// `enabled` in memory and on disk; conflicts resolve to disabled.
func TestLegacyEnableMigration(t *testing.T) {
	service, home := newService(t)
	writeNexConfig(t, home, `{"mcp":{"servers":{
	  "plain":{"command":"a"},
	  "off":{"command":"b","enable":false},
	  "conflict":{"command":"c","enable":false,"enabled":true}
	}}}`)
	result, err := service.ListLocalUserMcpCandidates(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	byName := map[string]Candidate{}
	for _, candidate := range result.Candidates {
		byName[candidate.Name] = candidate
	}
	if !byName["plain"].Enabled {
		t.Fatal("plain server must be enabled by default")
	}
	if byName["off"].Enabled {
		t.Fatal("legacy enable:false must disable")
	}
	if byName["conflict"].Enabled {
		t.Fatal("enable:false + enabled:true conflict must resolve to disabled")
	}
	// The migration persisted the normalized shape.
	raw, _ := os.ReadFile(filepath.Join(home, ".nex", "cli", "config.json"))
	var persisted struct {
		Mcp struct {
			Servers map[string]map[string]any `json:"servers"`
		} `json:"mcp"`
	}
	json.Unmarshal(raw, &persisted)
	if _, hasLegacy := persisted.Mcp.Servers["off"]["enable"]; hasLegacy {
		t.Fatal("legacy enable must be gone after migration")
	}
	if persisted.Mcp.Servers["off"]["enabled"] != false {
		t.Fatal("disabled state must be persisted as enabled:false")
	}
	// Idempotent: a second read does not rewrite.
	firstStat, _ := os.Stat(filepath.Join(home, ".nex", "cli", "config.json"))
	service.ListLocalUserMcpCandidates(context.Background())
	secondStat, _ := os.Stat(filepath.Join(home, ".nex", "cli", "config.json"))
	if !firstStat.ModTime().Equal(secondStat.ModTime()) {
		t.Fatal("an already-migrated config must not be rewritten")
	}
}

// TestSaveMcpToUserDirectory: upsert/remove/set-enabled.
func TestSaveMcpToUserDirectory(t *testing.T) {
	service, home := newService(t)
	ctx := context.Background()

	// Upsert adds a server.
	if err := service.SaveMcpToUserDirectory(ctx, map[string]any{
		"action": "upsert", "name": "added", "config": map[string]any{"command": "run"},
	}); err != nil {
		t.Fatal(err)
	}
	result, _ := service.ListLocalUserMcpCandidates(ctx)
	found := false
	for _, candidate := range result.Candidates {
		if candidate.Name == "added" {
			found = true
		}
	}
	if !found {
		t.Fatalf("upserted server missing: %+v", result.Candidates)
	}

	// Set-enabled false writes enabled:false into the server object.
	if err := service.SaveMcpToUserDirectory(ctx, map[string]any{
		"action": "set-enabled", "name": "added", "enabled": false,
	}); err != nil {
		t.Fatal(err)
	}
	result, _ = service.ListLocalUserMcpCandidates(ctx)
	for _, candidate := range result.Candidates {
		if candidate.Name == "added" && candidate.Enabled {
			t.Fatal("set-enabled false must disable")
		}
	}
	// Re-enable drops the field (the default state is not persisted).
	if err := service.SaveMcpToUserDirectory(ctx, map[string]any{
		"action": "set-enabled", "name": "added", "enabled": true,
	}); err != nil {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile(filepath.Join(home, ".nex", "cli", "config.json"))
	if string(raw) == "" {
		t.Fatal("config unreadable")
	}
	var config map[string]any
	json.Unmarshal(raw, &config)
	mcp := config["mcp"].(map[string]any)
	servers := mcp["servers"].(map[string]any)
	added := servers["added"].(map[string]any)
	if _, has := added["enabled"]; has {
		t.Fatal("enabled:true must not be persisted")
	}

	// Remove deletes the server.
	if err := service.SaveMcpToUserDirectory(ctx, map[string]any{
		"action": "remove", "name": "added",
	}); err != nil {
		t.Fatal(err)
	}
	result, _ = service.ListLocalUserMcpCandidates(ctx)
	for _, candidate := range result.Candidates {
		if candidate.Name == "added" {
			t.Fatal("removed server still listed")
		}
	}
}

// TestListWorkspaceMcpServerStatusesRequiresAgent: the exact error when the
// agent-side dependency is absent.
func TestListWorkspaceMcpServerStatusesRequiresAgent(t *testing.T) {
	service, _ := newService(t)
	_, err := service.ListWorkspaceMcpServerStatuses(context.Background(), map[string]any{})
	if err == nil || err.Error() != StatusesUnavailableError {
		t.Fatalf("err = %v", err)
	}
	// With the dependency, it forwards.
	forwarded := false
	serviceWithAgent, _ := newService(t)
	serviceWithAgent.options.ListMcpServerStatuses = func(ctx context.Context, params map[string]any) (any, error) {
		forwarded = true
		return map[string]any{"ok": true}, nil
	}
	if _, err := serviceWithAgent.ListWorkspaceMcpServerStatuses(context.Background(), map[string]any{}); err != nil || !forwarded {
		t.Fatalf("forward = %v, %v", forwarded, err)
	}
}

// TestImportMcpServersRewritesFilesystemPaths: filesystem server args
// remap from the local home to the remote home; non-filesystem servers are
// untouched; posix join for posix remotes.
func TestImportMcpServersRewritesFilesystemPaths(t *testing.T) {
	service, home := newService(t)
	localHome := "/local/home"
	servers := []ExportedServer{
		{
			Name: "filesystem", Enabled: true, Source: "nex",
			Config: map[string]any{
				"command": "npx",
				"args":    []any{"-y", "@modelcontextprotocol/server-filesystem", localHome + "/project"},
			},
		},
		{
			Name: "plain", Enabled: true, Source: "nex",
			Config: map[string]any{"command": "run", "args": []any{"/other/path"}},
		},
		{
			Name: "http", Enabled: true, Source: "nex",
			Config: map[string]any{"type": "http", "url": "https://example.com"},
		},
	}
	result, err := service.ImportMcpServers(context.Background(), servers, localHome, "", "", false)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Results) != 3 {
		t.Fatalf("results = %+v", result.Results)
	}
	for _, item := range result.Results {
		if item.Status != "synced" {
			t.Fatalf("item = %+v", item)
		}
	}
	raw, _ := os.ReadFile(filepath.Join(home, ".nex", "cli", "config.json"))
	var config map[string]any
	json.Unmarshal(raw, &config)
	serversMap := config["mcp"].(map[string]any)["servers"].(map[string]any)
	filesystem := serversMap["filesystem"].(map[string]any)
	args := filesystem["args"].([]any)
	expected := home + "/project"
	if args[len(args)-1] != expected {
		t.Fatalf("rewritten arg = %v want %v", args[len(args)-1], expected)
	}
	if _, has := filesystem["enabled"]; has {
		t.Fatal("enabled:true must not be persisted")
	}
	if _, has := serversMap["plain"]; !has {
		t.Fatal("plain server missing")
	}
	if _, has := serversMap["http"]; !has {
		t.Fatal("http server missing")
	}
}

// TestImportMcpServersSkips: existing by name in target or by name-key in
// the fallback source.
func TestImportMcpServersSkips(t *testing.T) {
	service, home := newService(t)
	writeNexConfig(t, home, `{"mcp":{"servers":{"dupe":{"command":"existing"}}}}`)
	result, err := service.ImportMcpServers(context.Background(), []ExportedServer{
		{Name: "dupe", Config: map[string]any{"command": "x"}, Enabled: true, Source: "nex"},
	}, "/local", "", "", false)
	if err != nil {
		t.Fatal(err)
	}
	if result.Results[0].Status != "skipped" {
		t.Fatalf("results = %+v", result.Results)
	}
	if _, err := service.ImportMcpServers(context.Background(), nil, "/local", "", "", true); err == nil || err.Error() != "mcp sync overwrite is not supported" {
		t.Fatalf("err = %v", err)
	}
}

// TestRemoteStatusesAndExportAndWriteAccess.
func TestRemoteStatusesAndExportAndWriteAccess(t *testing.T) {
	service, home := newService(t)
	writeNexConfig(t, home, `{"mcp":{"servers":{"Alpha":{"command":"a"},"beta":{"command":"b"}}}}`)

	statuses, err := service.ListRemoteUserMcpStatuses(context.Background(), []string{"ALPHA", "beta", "missing"})
	if err != nil {
		t.Fatal(err)
	}
	if len(statuses.Statuses) != 3 {
		t.Fatalf("statuses = %+v", statuses.Statuses)
	}
	if !statuses.Statuses[0].Exists || statuses.Statuses[0].Name != "ALPHA" {
		t.Fatalf("name-key match = %+v", statuses.Statuses[0])
	}
	if !statuses.Statuses[1].Exists {
		t.Fatal("exact name must exist")
	}
	if statuses.Statuses[2].Exists {
		t.Fatal("missing name must not exist")
	}

	candidates, _ := service.ListLocalUserMcpCandidates(context.Background())
	if len(candidates.Candidates) != 2 || candidates.Candidates[0].Name != "Alpha" {
		t.Fatalf("candidates = %+v", candidates.Candidates)
	}
	var exported *ExportResult
	exported, err = service.ExportMcpServers(context.Background(), []string{candidates.Candidates[0].ID})
	if err != nil {
		t.Fatal(err)
	}
	if len(exported.Servers) != 1 || exported.Servers[0].Name != "Alpha" {
		t.Fatalf("export = %+v", exported)
	}
	if _, err := service.ExportMcpServers(context.Background(), []string{"missing"}); err == nil || err.Error() != "mcp sync candidate not found: missing" {
		t.Fatalf("err = %v", err)
	}

	access, err := service.CheckRemoteUserMcpWriteAccess(context.Background())
	if err != nil || !access.OK {
		t.Fatalf("access = %+v, %v", access, err)
	}
}

// TestRewritePathArgForRemote: posix and windows remote join styles.
func TestRewritePathArgForRemote(t *testing.T) {
	// Posix remote.
	if got := rewritePathArgForRemote("/local/home/sub/file", "/local/home", "", "/remote/home", ""); got != "/remote/home/sub/file" {
		t.Fatalf("posix = %q", got)
	}
	// Workspace-relative with a remote workspace.
	if got := rewritePathArgForRemote("/local/ws/docs", "/local/home", "/local/ws", "/remote/home", "/remote/ws"); got != "/remote/ws/docs" {
		t.Fatalf("workspace = %q", got)
	}
	// Windows remote base keeps backslash joining.
	if got := rewritePathArgForRemote(`C:\home\docs`, `C:\home`, "", `C:\remote`, ""); got != `C:\remote\docs` {
		t.Fatalf("windows = %q", got)
	}
	// Outside both → untouched.
	if got := rewritePathArgForRemote("/elsewhere", "/local/home", "", "/remote/home", ""); got != "/elsewhere" {
		t.Fatalf("outside = %q", got)
	}
}

// TestServerRecordMapShape pins the loadMcpFromUserDirectory record shape.
func TestServerRecordMapShape(t *testing.T) {
	service, home := newService(t)
	writeNexConfig(t, home, `{"mcp":{"servers":{"a":{"command":"cmd"}}}}`)
	result, err := service.LoadMcpFromUserDirectory(context.Background(), "")
	if err != nil {
		t.Fatal(err)
	}
	servers := result["servers"].([]map[string]any)
	if len(servers) != 1 {
		t.Fatalf("servers = %+v", servers)
	}
	record := servers[0]
	if record["source"] != "nexagentmcp" || record["scope"] != "user" || record["name"] != "a" {
		t.Fatalf("record = %+v", record)
	}
	if _, ok := record["config"].(map[string]any); !ok {
		t.Fatal("config must be a map")
	}
	if !reflect.DeepEqual(record["config"], map[string]any{"command": "cmd"}) {
		t.Fatalf("config = %v", record["config"])
	}
}
