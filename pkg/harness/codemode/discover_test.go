package codemode

import (
	"os"
	"path/filepath"
	"testing"
)

func TestDiscoverLocalMCPServersAndMerge(t *testing.T) {
	home := t.TempDir()
	ws := t.TempDir()

	// User-level ~/.nex/mcp.json
	nexDir := filepath.Join(home, ".nex")
	if err := os.MkdirAll(nexDir, 0o755); err != nil {
		t.Fatal(err)
	}
	userJSON := `{
		"mcpServers": {
			"shared": { "command": "npx", "args": ["-y", "user-shared"] },
			"user_only": { "url": "https://example.com/mcp", "description": "User HTTP MCP" }
		}
	}`
	if err := os.WriteFile(filepath.Join(nexDir, "mcp.json"), []byte(userJSON), 0o644); err != nil {
		t.Fatal(err)
	}

	// Workspace-level <ws>/.mcp.json (overrides "shared")
	wsJSON := `{
		"mcpServers": {
			"shared": { "command": "node", "args": ["ws-shared.js"], "description": "Workspace override" },
			"proj_db": { "type": "http", "url": "http://127.0.0.1:9999/mcp" }
		}
	}`
	if err := os.WriteFile(filepath.Join(ws, ".mcp.json"), []byte(wsJSON), 0o644); err != nil {
		t.Fatal(err)
	}

	found := DiscoverLocalMCPServers(ws, home)
	if len(found) != 3 {
		t.Fatalf("expected 3 discovered servers, got %d: %+v", len(found), found)
	}

	byName := map[string]DiscoveredMCPServer{}
	for _, s := range found {
		byName[s.Name] = s
	}
	if byName["shared"].Source != "workspace" || byName["shared"].Command != "node" {
		t.Fatalf("expected workspace .mcp.json to override user shared, got %+v", byName["shared"])
	}
	if byName["proj_db"].Transport != "http" || byName["proj_db"].SourceRoot != ".mcp.json" {
		t.Fatalf("unexpected proj_db entry: %+v", byName["proj_db"])
	}
	if byName["user_only"].Source != "user" || byName["user_only"].Transport != "http" {
		t.Fatalf("unexpected user_only entry: %+v", byName["user_only"])
	}

	// Verify MergeDiscoveredServers populates Pool
	pool := NewPool(nil)
	defer pool.Close()
	pool.MergeDiscoveredServers(found)
	if !pool.IsNamespaceVisibleInWorkspace("proj_db", ws) {
		t.Fatalf("expected proj_db to be visible in workspace %s", ws)
	}
	if !pool.IsNamespaceVisibleInWorkspace("user_only", ws) {
		t.Fatalf("expected user_only to be visible in workspace %s", ws)
	}
}
