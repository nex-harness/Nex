package codemode

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/nex-harness/nex/pkg/store"
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

func TestLazyHydrateDiscoveredMCPInCodemode(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	ws := t.TempDir()
	wsJSON := `{
		"mcpServers": {
			"mteam": { "command": "/usr/local/bin/mcp-server-mteam" }
		}
	}`
	if err := os.WriteFile(filepath.Join(ws, ".mcp.json"), []byte(wsJSON), 0o644); err != nil {
		t.Fatal(err)
	}
	discovered := DiscoverLocalMCPServers(ws, t.TempDir())
	if len(discovered) != 1 || discovered[0].Name != "mteam" {
		t.Fatalf("expected 1 mteam discovered server, got %+v", discovered)
	}

	type searchArgs struct {
		Keyword string `json:"keyword"`
	}
	newPoolWithMockMTeam := func() *Pool {
		pool := NewPool(nil)
		pool.connectFn = func(cctx context.Context, rec store.MCPServerRecord) (*mcp.ClientSession, *bytes.Buffer, error) {
			srv := mcp.NewServer(&mcp.Implementation{Name: "mock-mteam", Version: "1.0.0"}, nil)
			mcp.AddTool(srv, &mcp.Tool{
				Name:        "search_torrents",
				Description: "Search M-Team torrents by keyword and codec",
				Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true},
			}, func(_ context.Context, _ *mcp.CallToolRequest, in searchArgs) (*mcp.CallToolResult, any, error) {
				return &mcp.CallToolResult{
					Content: []mcp.Content{
						&mcp.TextContent{Text: `{"items":[{"id":"123","title":"` + in.Keyword + ` H265"}]}`},
					},
				}, nil, nil
			})
			srvTr, cliTr := mcp.NewInMemoryTransports()
			if _, err := srv.Connect(ctx, srvTr, nil); err != nil {
				return nil, nil, err
			}
			cli := mcp.NewClient(&mcp.Implementation{Name: "nex-test", Version: "1.0.0"}, nil)
			cs, err := cli.Connect(cctx, cliTr, nil)
			if err != nil {
				return nil, nil, err
			}
			return cs, nil, nil
		}
		pool.MergeDiscoveredServers(discovered)
		return pool
	}

	// 1. Fresh pool with empty catalog: searchTools("mteam"), searchTools({query}), searchTools("none") -> [] + invoke tool
	pool1 := newPoolWithMockMTeam()
	defer pool1.Close()
	cm1 := NewTool(pool1, NewSessionKV(), nil)
	cm1.SetWorkspacePath(ws)

	args1, _ := json.Marshal(map[string]any{
		"code": `
			const a = await searchTools("mteam");
			const b = await searchTools({ query: "torrent" });
			const c = await searchTools("definitely_missing_xyz");
			const res = await tools.mteam.search_torrents({ keyword: "Avengers" });
			return { aCount: a.length, aName: a[0]?.name, bCount: b.length, cIsEmptyArr: Array.isArray(c) && c.length === 0, res };
		`,
	})
	res1, err := cm1.Execute(ctx, "call_1", args1, nil)
	if err != nil || res1.IsError {
		t.Fatalf("codemode run 1 failed: err=%v out=%s", err, res1.Output)
	}
	var out1 struct {
		ACount      int    `json:"aCount"`
		AName       string `json:"aName"`
		BCount      int    `json:"bCount"`
		CIsEmptyArr bool   `json:"cIsEmptyArr"`
		Res         struct {
			Items []struct {
				ID    string `json:"id"`
				Title string `json:"title"`
			} `json:"items"`
		} `json:"res"`
	}
	if err := json.Unmarshal([]byte(res1.Output), &out1); err != nil {
		t.Fatalf("unmarshal run 1 output failed: %v\nout=%s", err, res1.Output)
	}
	if out1.ACount != 1 || out1.AName != "mteam.search_torrents" || out1.BCount != 1 || !out1.CIsEmptyArr ||
		len(out1.Res.Items) != 1 || out1.Res.Items[0].Title != "Avengers H265" {
		t.Fatalf("unexpected codemode run 1 output: %+v raw=%s", out1, res1.Output)
	}

	// 2. Fresh pool: searchTools("torrent") WITHOUT mentioning namespace "mteam" triggers fallback hydration
	pool2 := newPoolWithMockMTeam()
	defer pool2.Close()
	cm2 := NewTool(pool2, NewSessionKV(), nil)
	cm2.SetWorkspacePath(ws)

	args2, _ := json.Marshal(map[string]any{
		"code": `
			const hits = await searchTools("torrent");
			return { count: hits.length, name: hits[0]?.name };
		`,
	})
	res2, err := cm2.Execute(ctx, "call_2", args2, nil)
	if err != nil || res2.IsError || !strings.Contains(res2.Output, "mteam.search_torrents") {
		t.Fatalf("fallback hydration searchTools('torrent') failed: err=%v out=%s", err, res2.Output)
	}

	// 3. Fresh pool: direct describeTool("mteam.search_torrents") lazily hydrates namespace before schema lookup
	pool3 := newPoolWithMockMTeam()
	defer pool3.Close()
	cm3 := NewTool(pool3, NewSessionKV(), nil)
	cm3.SetWorkspacePath(ws)

	args3, _ := json.Marshal(map[string]any{
		"code": `
			const desc = await describeTool("mteam.search_torrents");
			return desc;
		`,
	})
	res3, err := cm3.Execute(ctx, "call_3", args3, nil)
	if err != nil || res3.IsError || !strings.Contains(res3.Output, "mteam.search_torrents") || !strings.Contains(res3.Output, `"keyword"`) {
		t.Fatalf("lazy describeTool failed: err=%v out=%s", err, res3.Output)
	}
}

func TestExtraEnvExpansionMergingAndSetExtraEnvInvalidation(t *testing.T) {
	extra := map[string]string{
		"MTEAM_API_KEY": "vault-secret-123",
	}
	expanded := expandEnvPlaceholdersWithExtra("Bearer ${MTEAM_API_KEY}", extra)
	if expanded != "Bearer vault-secret-123" {
		t.Fatalf("expected extraEnv placeholder expansion, got %q", expanded)
	}

	merged := mergeEnvVars(
		[]string{"PATH=/usr/bin", "MTEAM_API_KEY=stale-os-val"},
		extra,
		map[string]string{"MTEAM_API_BASE": "https://api.m-team.io/api"},
	)
	kv := map[string]string{}
	counts := map[string]int{}
	for _, entry := range merged {
		k, v, ok := strings.Cut(entry, "=")
		if ok {
			kv[k] = v
			counts[k]++
		}
	}
	if kv["MTEAM_API_KEY"] != "vault-secret-123" || kv["MTEAM_API_BASE"] != "https://api.m-team.io/api" {
		t.Fatalf("unexpected merged env: %+v", kv)
	}
	if counts["MTEAM_API_KEY"] != 1 {
		t.Fatalf("expected no duplicate keys in merged env, count=%d", counts["MTEAM_API_KEY"])
	}

	// Verify SetExtraEnv closes live connections only when values change
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	pool := NewPool(nil)
	defer pool.Close()

	var connects int
	pool.connectFn = func(cctx context.Context, _ store.MCPServerRecord) (*mcp.ClientSession, *bytes.Buffer, error) {
		connects++
		srv := mcp.NewServer(&mcp.Implementation{Name: "mock-env", Version: "1.0.0"}, nil)
		srvTr, cliTr := mcp.NewInMemoryTransports()
		if _, err := srv.Connect(ctx, srvTr, nil); err != nil {
			return nil, nil, err
		}
		cli := mcp.NewClient(&mcp.Implementation{Name: "nex-test", Version: "1.0.0"}, nil)
		cs, err := cli.Connect(cctx, cliTr, nil)
		return cs, nil, err
	}
	pool.SyncServers([]store.MCPServerRecord{{Name: "mteam", HostMode: "runner_local", Command: "mock", Enabled: true}})

	if err := pool.EnsureNamespaceHydrated(ctx, "mteam"); err != nil {
		t.Fatal(err)
	}
	pool.SetExtraEnv(map[string]string{"MTEAM_API_KEY": "k1"})
	// Same env again -> should keep existing liveConns untouched (even if hydrated)
	pool.SetExtraEnv(map[string]string{"MTEAM_API_KEY": "k1"})
	if connects != 1 {
		t.Fatalf("expected 1 connect before respawn, got %d", connects)
	}
	if _, err := pool.getOrSpawnLocalSession(ctx, pool.servers["mteam"]); err != nil {
		t.Fatal(err)
	}
	if connects != 2 {
		t.Fatalf("expected SetExtraEnv change to close live conn and trigger respawn, got %d connects", connects)
	}
}
