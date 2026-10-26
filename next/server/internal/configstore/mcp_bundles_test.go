package configstore

import (
	"encoding/json"
	"strings"
	"testing"
)

// P1: validation mirrors pi (name, transport, SSE normalization, variants).
func TestMcpValidation(t *testing.T) {
	cases := []struct {
		name   string
		server McpServer
		code   string
	}{
		{
			name:   "empty transport",
			server: McpServer{Name: "docs", Enabled: true},
			code:   "mcp-transport-missing",
		},
		{
			name:   "stdio without command",
			server: McpServer{Name: "docs", Transport: MCPTransport{Kind: MCPKindStdio}},
			code:   "mcp-command-missing",
		},
		{
			name:   "http without url",
			server: McpServer{Name: "docs", Transport: MCPTransport{Kind: MCPKindHTTP}},
			code:   "mcp-url-invalid",
		},
		{
			name:   "bad name",
			server: McpServer{Name: "docs tools", Transport: MCPTransport{Kind: MCPKindStdio, Command: "x"}},
			code:   "mcp-name-invalid",
		},
		{
			name:   "bad exposure",
			server: McpServer{Name: "docs", Exposure: "loud", Transport: MCPTransport{Kind: MCPKindStdio, Command: "x"}},
			code:   "mcp-exposure-unsupported",
		},
		{
			name: "unknown variant os",
			server: McpServer{Name: "docs", Transport: MCPTransport{Kind: MCPKindStdio, Command: "x",
				Variants: map[string]*MCPCommand{"plan9": {Command: "y"}}}},
			code: "mcp-variant-os-unsupported",
		},
		{
			name:   "malformed secret ref",
			server: McpServer{Name: "docs", Transport: MCPTransport{Kind: MCPKindStdio, Command: "x", Env: map[string]string{"TOKEN": "secret-looking"}}},
			code:   "mcp-secret-ref-invalid",
		},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			normalized := CloneMcp(&testCase.server)
			NormalizeMcp(normalized)
			issues := ValidateMcpServer("scopes.global.mcp."+testCase.server.Name, normalized)
			found := false
			for _, issue := range issues {
				if issue.Code == testCase.code {
					found = true
				}
			}
			if !found {
				t.Fatalf("missing %s in %#v", testCase.code, issues)
			}
		})
	}
}

func TestMcpNormalizeSSEToHTTP(t *testing.T) {
	server := &McpServer{Name: "legacy", Enabled: true, Transport: MCPTransport{Kind: "sse", URL: "https://mcp.example.test/sse"}}
	warnings := NormalizeMcp(server)
	if server.Transport.Kind != MCPKindHTTP {
		t.Fatalf("kind = %q", server.Transport.Kind)
	}
	if len(warnings) != 1 || !strings.Contains(warnings[0], "SSE") {
		t.Fatalf("warnings = %v", warnings)
	}
	// The normalized server validates (pi rejects sse itself).
	if issues := ValidateMcpServer("scopes.global.mcp.legacy", server); len(issues) != 0 {
		t.Fatalf("issues = %v", issues)
	}
}

func TestMcpMergeToggleAndDelete(t *testing.T) {
	global := Layer{MCP: map[string]*McpServer{
		"docs": {Name: "docs", Enabled: true, Transport: MCPTransport{Kind: MCPKindHTTP, URL: "https://docs.test/mcp"}},
	}}
	runner := Layer{MCP: map[string]*McpServer{
		"docs": {Name: "docs", Enabled: false},
	}}
	merged := MergeLayers(global, runner)
	docs := merged.MCP["docs"]
	if docs == nil || docs.Enabled || docs.Transport.URL != "https://docs.test/mcp" {
		t.Fatalf("toggle-only merge lost the entry: %#v", docs)
	}
	deleted := MergeLayers(global, Layer{MCP: map[string]*McpServer{"docs": nil}})
	if _, ok := deleted.MCP["docs"]; ok {
		t.Fatal("null should delete the server")
	}
}

// The runner receives resolved values and never a secret reference.
func TestMcpPushResolvesSecretsAndSkipsDisabled(t *testing.T) {
	store := NewStore(t.TempDir(), testLogger())
	if err := store.Secrets().Put("sec_mcp_docs_env_TOKEN", "plain-token"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.UpsertMcpServer("", "", McpServer{
		Name: "docs", Enabled: true,
		Transport: MCPTransport{Kind: MCPKindHTTP, URL: "https://docs.test/mcp",
			Headers: map[string]string{"Authorization": "sec_mcp_docs_env_TOKEN"}},
	}, nil); err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.UpsertMcpServer("", "", McpServer{
		Name: "off", Enabled: false,
		Transport: MCPTransport{Kind: MCPKindStdio, Command: "x", Env: map[string]string{"K": "v"}},
	}, nil); err != nil {
		t.Fatal(err)
	}
	payload, issues, err := store.ProjectPush("alpha")
	if err != nil {
		t.Fatal(err)
	}
	if len(issues) != 0 || !payload.Doc.MCPOwned {
		t.Fatalf("issues=%v owned=%v", issues, payload.Doc.MCPOwned)
	}
	if len(payload.Doc.MCP) != 2 {
		t.Fatalf("servers = %#v", payload.Doc.MCP)
	}
	// The doc never carries a reference; the reference id travels as a key of
	// `secrets` only (the runner looks values up by id).
	docOnly, _ := json.Marshal(payload.Doc)
	if strings.Contains(string(docOnly), "sec_mcp_docs_env_TOKEN") {
		t.Fatalf("push doc leaks the reference: %s", docOnly)
	}
	headers := payload.Doc.MCP[0].Transport.Headers
	if headers["Authorization"] != "plain-token" {
		t.Fatalf("headers = %#v", headers)
	}
	if payload.Secrets["sec_mcp_docs_env_TOKEN"] != "plain-token" {
		t.Fatalf("secret not in the payload: %#v", payload.Secrets)
	}
	// The disabled server carries no env value in the push.
	for _, server := range payload.Doc.MCP {
		if server.Name == "off" && len(server.Transport.Env) != 0 {
			t.Fatalf("disabled server env leaked: %#v", server.Transport.Env)
		}
	}
}

func TestMcpNamespaceClashRejected(t *testing.T) {
	store := NewStore(t.TempDir(), testLogger())
	if _, _, err := store.UpsertMcpServer("", "", McpServer{Name: "my-server", Transport: MCPTransport{Kind: MCPKindStdio, Command: "x"}}, nil); err != nil {
		t.Fatal(err)
	}
	_, _, err := store.UpsertMcpServer("", "", McpServer{Name: "my_server", Transport: MCPTransport{Kind: MCPKindStdio, Command: "y"}}, nil)
	if err == nil || !strings.Contains(err.Error(), "conflicts") {
		t.Fatalf("expected a namespace clash, got %v", err)
	}
}

func TestBundleRoundTripAndDigest(t *testing.T) {
	store := NewStore(t.TempDir(), testLogger())
	files := []BundleFile{
		{Path: "SKILL.md", Content: "# Skill\n"},
		{Path: "references/notes.md", Content: "notes"},
	}
	doc, digest, err := store.UpsertBundle("", "", BundleRef{Name: "demo", Kind: "skill", Enabled: true}, files)
	if err != nil {
		t.Fatal(err)
	}
	if doc.Version != 1 || len(doc.Scopes.Global.Skills) != 1 || doc.Scopes.Global.Skills[0].Digest != digest {
		t.Fatalf("doc = %#v", doc.Scopes.Global.Skills)
	}
	loaded, err := store.LoadBundle(digest)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Name != "demo" || len(loaded.Files) != 2 {
		t.Fatalf("loaded = %#v", loaded)
	}
	// Path traversal is refused before anything is written.
	if _, _, err := store.UpsertBundle("", "", BundleRef{Name: "bad", Kind: "skill"}, []BundleFile{{Path: "../escape.md", Content: "x"}}); err == nil {
		t.Fatal("expected a path error")
	}
	// A corrupted file fails the reload check.
	tampered := Bundle{Name: "demo", Kind: "skill", Digest: digest, Files: []BundleFile{{Path: "SKILL.md", Content: "changed"}}}
	if DigestFiles(tampered.Files) == digest {
		t.Fatal("digest should differ")
	}
}

func TestBundleToggleAndDelete(t *testing.T) {
	store := NewStore(t.TempDir(), testLogger())
	if _, _, err := store.UpsertBundle("", "", BundleRef{Name: "demo", Kind: "skill", Enabled: true}, []BundleFile{{Path: "SKILL.md", Content: "x"}}); err != nil {
		t.Fatal(err)
	}
	doc, err := store.SetBundleEnabled("", "", "skill", "demo", false)
	if err != nil || doc.Scopes.Global.Skills[0].Enabled {
		t.Fatalf("toggle = %v, %v", doc.Scopes.Global.Skills, err)
	}
	payload, _, err := store.ProjectPush("alpha")
	if err != nil {
		t.Fatal(err)
	}
	if len(payload.Doc.Skills) != 0 || len(payload.Bundles) != 0 {
		t.Fatalf("disabled bundle pushed: %#v", payload.Doc.Skills)
	}
	if _, err := store.RemoveBundle("", "", "skill", "demo"); err != nil {
		t.Fatal(err)
	}
	doc, _ = store.Snapshot()
	if len(doc.Scopes.Global.Skills) != 0 {
		t.Fatalf("skills = %#v", doc.Scopes.Global.Skills)
	}
}

func TestBundlePushCarriesVerifiedFiles(t *testing.T) {
	store := NewStore(t.TempDir(), testLogger())
	_, digest, err := store.UpsertBundle("", "", BundleRef{Name: "demo", Kind: "skill", Enabled: true}, []BundleFile{{Path: "SKILL.md", Content: "x"}})
	if err != nil {
		t.Fatal(err)
	}
	payload, issues, err := store.ProjectPush("alpha")
	if err != nil || len(issues) != 0 {
		t.Fatalf("push: %v %v", issues, err)
	}
	if len(payload.Doc.Skills) != 1 || payload.Doc.Skills[0].Digest != digest {
		t.Fatalf("skills = %#v", payload.Doc.Skills)
	}
	var bundle Bundle
	if err := json.Unmarshal(payload.Bundles[0], &bundle); err != nil {
		t.Fatal(err)
	}
	if bundle.Digest != digest || len(bundle.Files) != 1 {
		t.Fatalf("bundle = %#v", bundle)
	}
}

func TestPromptBundlesAreSeparateLists(t *testing.T) {
	store := NewStore(t.TempDir(), testLogger())
	if _, _, err := store.UpsertBundle("", "", BundleRef{Name: "review", Kind: "prompt", Enabled: true}, []BundleFile{{Path: "review.md", Content: "Review $1"}}); err != nil {
		t.Fatal(err)
	}
	payload, _, err := store.ProjectPush("alpha")
	if err != nil {
		t.Fatal(err)
	}
	if len(payload.Doc.Prompts) != 1 || len(payload.Doc.Skills) != 0 {
		t.Fatalf("prompts = %#v skills = %#v", payload.Doc.Prompts, payload.Doc.Skills)
	}
}
