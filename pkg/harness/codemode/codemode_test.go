package codemode

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/nex-harness/nex/pkg/store"
)

type stubRemoteCaller struct {
	online bool
}

func (s *stubRemoteCaller) IsOnline() bool { return s.online }

func (s *stubRemoteCaller) CallRemoteMCP(_ context.Context, ns, tool string, _ json.RawMessage) (json.RawMessage, error) {
	if ns == "github" && tool == "list_prs" {
		return json.RawMessage(`[{"number":101,"branch":"feat/a","draft":false},{"number":102,"branch":"chore/b","draft":true}]`), nil
	}
	return nil, errors.New("unknown remote tool")
}

func TestCodeModeLazySearchPromiseAllTimeoutAndApprovalGuard(t *testing.T) {
	ctx := context.Background()
	remote := &stubRemoteCaller{online: true}
	pool := NewPool(remote)
	defer pool.Close()

	// Register local namespace + server-hosted namespace
	pool.RegisterInProcessNamespace("local_git", []ToolEntry{
		{Name: "branches", Description: "List local git branches", ReadOnly: true},
		{Name: "delete_branch", Description: "Delete a local branch", ReadOnly: false},
	}, func(_ context.Context, toolName string, _ json.RawMessage) (any, error) {
		if toolName == "branches" {
			time.Sleep(15 * time.Millisecond)
			return []string{"feat/a", "main"}, nil
		}
		return map[string]any{"deleted": true}, nil
	})

	ghCatalog, _ := json.Marshal([]ToolEntry{
		{Name: "list_prs", Description: "List open pull requests", ReadOnly: true, InputSchema: json.RawMessage(`{"type":"object","properties":{"state":{"type":"string"}}}`)},
	})
	pool.SyncServers([]store.MCPServerRecord{
		{Name: "github", HostMode: "server_hosted", Enabled: true, CatalogCache: ghCatalog},
	})

	kv := NewSessionKV()
	cmTool := NewTool(pool, kv, func(_ context.Context, fullName string, readOnly bool, _ json.RawMessage) error {
		if !readOnly {
			return errors.New("approval denied for write tool " + fullName)
		}
		return nil
	})

	// 1. Concurrent Promise.all combining local MCP + server-hosted remote MCP + searchTools/describeTool + store()
	script1 := `
		const hits = await searchTools("pull", { namespace: "github" });
		const desc = await describeTool("github.list_prs");
		const [prs, branches] = await Promise.all([
			tools.github.list_prs({ state: "open" }),
			tools.local_git.branches({})
		]);
		const active = prs.filter(p => !p.draft && branches.indexOf(p.branch) >= 0);
		store("saved_pr", active[0].number);
		return { found_tools: hits.length, has_schema: !!desc.input_schema, matched_pr: active[0].number };
	`
	argBytes, _ := json.Marshal(map[string]string{"code": script1})
	res, err := cmTool.Execute(ctx, "", argBytes, nil)
	if err != nil || res.IsError {
		t.Fatalf("codemode script1 failed: %+v err=%v", res, err)
	}
	if !strings.Contains(res.Output, `"matched_pr": 101`) || !strings.Contains(res.Output, `"found_tools": 1`) {
		t.Fatalf("unexpected codemode output:\n%s", res.Output)
	}

	// 2. Verify session store/load persists across calls without LLM context bloat, AND write tool hits approval guard!
	script2 := `
		const prev = load("saved_pr");
		try {
			await tools.local_git.delete_branch({ name: "feat/a" });
			return "SHOULD_NOT_SUCCEED";
		} catch (e) {
			return { prev_pr: prev, blocked: String(e) };
		}
	`
	argBytes2, _ := json.Marshal(map[string]string{"code": script2})
	res2, _ := cmTool.Execute(ctx, "", argBytes2, nil)
	if res2.IsError || !strings.Contains(res2.Output, `"prev_pr": 101`) || !strings.Contains(res2.Output, "approval denied") {
		t.Fatalf("approval guard or session KV failed:\n%s", res2.Output)
	}

	// 3. Offline fallback marks server-hosted MCP as offline_unavailable
	remote.online = false
	offHits := pool.SearchTools("pull", "github", 5)
	if len(offHits) != 1 || offHits[0]["status"] != "offline_unavailable" {
		t.Fatalf("expected offline_unavailable status, got %+v", offHits)
	}

	// 4. Hard CPU infinite loop interruption via vm.Interrupt!
	loopScript := "// @options: {\"timeout_ms\": 80}\nwhile (true) {}\n"
	loopArgs, _ := json.Marshal(map[string]string{"code": loopScript})
	start := time.Now()
	loopRes, _ := cmTool.Execute(ctx, "", loopArgs, nil)
	if !loopRes.IsError || !strings.Contains(loopRes.Output, "timed out") || time.Since(start) > 2*time.Second {
		t.Fatalf("infinite loop was not interrupted cleanly: %+v elapsed=%v", loopRes, time.Since(start))
	}
}
