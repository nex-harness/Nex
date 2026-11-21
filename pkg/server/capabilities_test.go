package server

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nex-harness/nex/pkg/harness/codemode"
	"github.com/nex-harness/nex/pkg/store"
)

func TestSkillsDiscoverAndVisibilityAPI(t *testing.T) {
	tmp := t.TempDir()
	st, err := store.Open(filepath.Join(tmp, "data"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	srv, err := New(Config{Store: st})
	if err != nil {
		t.Fatal(err)
	}

	ws := filepath.Join(tmp, "workspace-alpha")
	skillDir := filepath.Join(ws, ".agents", "skills", "deploy-check")
	if err := os.MkdirAll(skillDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte("---\nname: deploy-check\ndescription: Pre-deploy verification\n---\n# Deploy Check\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	// 1. Discover skills for workspace-alpha
	req := httptest.NewRequest(http.MethodGet, "/api/v1/skills/discover?workspace_path="+ws, nil)
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("discover expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	var discResp struct {
		Skills []struct {
			Name             string `json:"name"`
			Scope            string `json:"scope"`
			WorkspaceEnabled bool   `json:"workspace_enabled"`
		} `json:"skills"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &discResp); err != nil {
		t.Fatal(err)
	}
	found := false
	for _, sk := range discResp.Skills {
		if sk.Name == "deploy-check" && sk.Scope == "workspace" && sk.WorkspaceEnabled {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected deploy-check workspace skill enabled, got %+v", discResp.Skills)
	}

	// 2. Hide deploy-check specifically in workspace-alpha
	body, _ := json.Marshal(map[string]any{
		"name":           "deploy-check",
		"scope_target":   "workspace",
		"workspace_path": ws,
		"enabled":        false,
	})
	req2 := httptest.NewRequest(http.MethodPut, "/api/v1/skills/visibility", bytes.NewReader(body))
	rec2 := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec2, req2)
	if rec2.Code != http.StatusOK {
		t.Fatalf("visibility update expected 200, got %d: %s", rec2.Code, rec2.Body.String())
	}

	// 3. Re-discover and confirm workspace_enabled == false & workspace_overridden == true
	req3 := httptest.NewRequest(http.MethodGet, "/api/v1/skills/discover?workspace_path="+ws, nil)
	rec3 := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec3, req3)
	if !strings.Contains(rec3.Body.String(), `"workspace_enabled":false`) || !strings.Contains(rec3.Body.String(), `"workspace_overridden":true`) {
		t.Fatalf("expected workspace override false in discover output: %s", rec3.Body.String())
	}
}

func TestMCPImportVisibilityAndExposureAPI(t *testing.T) {
	tmp := t.TempDir()
	st, err := store.Open(filepath.Join(tmp, "data"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	srv, err := New(Config{Store: st})
	if err != nil {
		t.Fatal(err)
	}

	// 1. Bulk JSON import (Pi / Claude Desktop mcpServers format)
	importBody := map[string]any{
		"scope": "global",
		"raw_json": `{
			"mcpServers": {
				"github": {
					"url": "https://api.githubcopilot.com/mcp/",
					"headers": { "Authorization": "Bearer ${GH_TOKEN}" },
					"description": "GitHub repos and PRs",
					"exposure": "deferred"
				},
				"filesystem": {
					"command": "npx",
					"args": ["-y", "@modelcontextprotocol/server-filesystem", "."],
					"exposure": "codemode"
				}
			}
		}`,
	}
	b, _ := json.Marshal(importBody)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/mcp/import", bytes.NewReader(b))
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("mcp import expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	// 2. Reject legacy SSE transport cleanly
	sseBody, _ := json.Marshal(map[string]any{
		"raw_json": `{"legacy": {"type": "sse", "url": "https://example.com/sse"}}`,
	})
	reqSSE := httptest.NewRequest(http.MethodPost, "/api/v1/mcp/import", bytes.NewReader(sseBody))
	recSSE := httptest.NewRecorder()
	srv.Handler().ServeHTTP(recSSE, reqSSE)
	if recSSE.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for legacy sse transport, got %d", recSSE.Code)
	}

	// 3. Per-workspace visibility override + exposure update via /api/v1/mcp/{name}/visibility
	ws := filepath.Join(tmp, "repo-x")
	visBody, _ := json.Marshal(map[string]any{
		"scope_target":   "workspace",
		"workspace_path": ws,
		"enabled":        false,
		"exposure":       "direct",
	})
	reqVis := httptest.NewRequest(http.MethodPut, "/api/v1/mcp/github/visibility", bytes.NewReader(visBody))
	recVis := httptest.NewRecorder()
	srv.Handler().ServeHTTP(recVis, reqVis)
	if recVis.Code != http.StatusOK {
		t.Fatalf("mcp visibility expected 200, got %d: %s", recVis.Code, recVis.Body.String())
	}

	// 4. Verify codemode Pool honors workspace overrides and exposure policy
	list, err := st.ListMCPServers(req.Context())
	if err != nil {
		t.Fatal(err)
	}
	pool := codemode.NewPool(nil)
	defer pool.Close()
	pool.SyncServers(list)

	if pool.IsNamespaceVisibleInWorkspace("github", ws) {
		t.Fatalf("expected github to be disabled in workspace %s", ws)
	}
	if !pool.IsNamespaceVisibleInWorkspace("github", filepath.Join(tmp, "repo-y")) {
		t.Fatalf("expected github to remain enabled in other workspaces")
	}
	summaryWS := pool.NamespaceSummaryPromptForWorkspace(ws)
	if strings.Contains(summaryWS, "- github") {
		t.Fatalf("expected github omitted from prompt summary in %s, got:\n%s", ws, summaryWS)
	}
	if !strings.Contains(summaryWS, "- filesystem") {
		t.Fatalf("expected filesystem present in prompt summary, got:\n%s", summaryWS)
	}
}
