package settingssync

// settingssync_test.go: behavior tests against a synthetic multi-agent
// fixture tree (no real agent dirs): discovery per category with skip
// reasons, import copy/symlink, MCP config merging (all three source
// formats), plugin config registration, the Claude AGENTS.md migration, and
// the first-run prompt state.

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

type fixture struct {
	home      string
	workspace string
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o777); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func newFixture(t *testing.T) fixture {
	t.Helper()
	root := t.TempDir()
	f := fixture{
		home:      filepath.Join(root, "home"),
		workspace: filepath.Join(root, "workspace"),
	}
	writeFile(t, filepath.Join(f.home, ".claude", "skills", "code-review", "SKILL.md"), "---\nname: code-review\nversion: 1.2.0\n---\nReview code.\n")
	writeFile(t, filepath.Join(f.home, ".claude", "skills", "doc-writer", "SKILL.md"), "---\nname: doc-writer\n---\nWrite docs.\n")
	writeFile(t, filepath.Join(f.workspace, ".codex", "skills", "local-skill", "SKILL.md"), "---\nname: local-skill\n---\nLocal.\n")
	// A skill without frontmatter: fallback name = dir name.
	writeFile(t, filepath.Join(f.home, ".qwen", "skills", "no-frontmatter", "SKILL.md"), "Just body.\n")
	// Excluded dir must not be scanned.
	writeFile(t, filepath.Join(f.home, ".qwen", "skills", "node_modules", "dep", "SKILL.md"), "excluded\n")
	return f
}

func newService(f fixture, settings SettingService) *Service {
	return NewService(Options{SettingService: settings, Home: f.home})
}

func fakeSettings(handled bool) SettingService {
	return &stubSettings{handled: handled}
}

type stubSettings struct {
	handled bool
	updates []map[string]any
}

func (s *stubSettings) Get(ctx context.Context) (map[string]any, error) {
	if s.handled {
		return map[string]any{"settingsSyncFirstRunPromptHandled": true}, nil
	}
	return map[string]any{}, nil
}

func (s *stubSettings) Update(ctx context.Context, patch map[string]any) error {
	s.updates = append(s.updates, patch)
	s.handled = patch["settingsSyncFirstRunPromptHandled"] == true
	return nil
}

// ---------------------------------------------------------------------------
// discovery
// ---------------------------------------------------------------------------

func TestDetectSkillsDiscovery(t *testing.T) {
	f := newFixture(t)
	svc := newService(f, fakeSettings(false))
	result, err := svc.Detect(context.Background(), DetectRequest{
		WorkspaceRef: WorkspaceRef{WorkspacePath: f.workspace},
		Categories:   []string{CategorySkills},
		Intent:       "manualImport",
	})
	if err != nil {
		t.Fatal(err)
	}
	var claude *AgentSummary
	for i := range result.Agents {
		if result.Agents[i].Agent == "claudeCode" {
			claude = &result.Agents[i]
		}
	}
	if claude == nil || !claude.Discovered {
		t.Fatal("claudeCode not discovered")
	}
	category := claude.Categories[0]
	if category.Category != CategorySkills || category.DiscoveredCount != 2 || category.ImportableCount != 2 {
		t.Fatalf("category wrong: %+v", category)
	}
	if !category.SelectedByDefault {
		t.Fatal("skills are selectedByDefault when importable")
	}
	// Summaries sorted by name.
	skills := category.SourceRoots[0].Skills
	if skills[0].Name != "code-review" || skills[0].Version != "1.2.0" {
		t.Fatalf("skill summary wrong: %+v", skills[0])
	}
	if skills[1].Name != "doc-writer" || skills[1].Version != "" {
		t.Fatalf("skill summary wrong: %+v", skills[1])
	}

	// qwenCode: frontmatter-less skill gets the dir name; node_modules excluded.
	var qwen *AgentSummary
	for i := range result.Agents {
		if result.Agents[i].Agent == "qwenCode" {
			qwen = &result.Agents[i]
		}
	}
	if qwen == nil || qwen.Categories[0].DiscoveredCount != 1 {
		t.Fatalf("qwen discovery wrong: %+v", qwen)
	}
	if got := qwen.Categories[0].SourceRoots[0].Skills[0].Name; got != "no-frontmatter" {
		t.Fatalf("fallback name: %q", got)
	}
}

func TestDetectIgnoresFirstRunIntent(t *testing.T) {
	// shouldScan: ONLY manualImport + the matching category.
	f := newFixture(t)
	svc := newService(f, fakeSettings(false))
	result, err := svc.Detect(context.Background(), DetectRequest{Intent: "firstRun", Categories: []string{CategorySkills}})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Agents) != 0 {
		t.Fatalf("firstRun intent must not scan: %+v", result.Agents)
	}
}

func TestDetectSameNameSkip(t *testing.T) {
	f := newFixture(t)
	// Existing Nex skill with the same name key.
	writeFile(t, filepath.Join(f.home, ".nex", "skills", "existing", "SKILL.md"), "---\nname: Code-Review\n---\n")
	svc := newService(f, fakeSettings(false))
	result, _ := svc.Detect(context.Background(), DetectRequest{Categories: []string{CategorySkills}, Intent: "manualImport"})
	var claude *AgentSummary
	for i := range result.Agents {
		if result.Agents[i].Agent == "claudeCode" {
			claude = &result.Agents[i]
		}
	}
	if claude == nil {
		t.Fatal("claude missing")
	}
	category := claude.Categories[0]
	if category.ImportableCount != 1 {
		t.Fatalf("same-name skill must be skipped: %+v", category)
	}
	var codeReview *SkillSummary
	for i := range category.SourceRoots[0].Skills {
		if category.SourceRoots[0].Skills[i].Name == "code-review" {
			codeReview = &category.SourceRoots[0].Skills[i]
		}
	}
	if codeReview == nil || codeReview.Importable || codeReview.SkipReason != "sameNameExists" {
		t.Fatalf("skip reason wrong: %+v", codeReview)
	}
}

// ---------------------------------------------------------------------------
// import
// ---------------------------------------------------------------------------

func TestImportSkillsCopy(t *testing.T) {
	f := newFixture(t)
	svc := newService(f, fakeSettings(false))
	result, err := svc.ImportSelected(context.Background(), ImportSelectedRequest{
		WorkspaceRef: WorkspaceRef{WorkspacePath: f.workspace},
		Selections: []Selection{{
			Agent: "claudeCode", Category: CategorySkills, ImportMode: "copy",
			SourceScope: ScopeGlobal,
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.SuccessCount != 2 || result.FailedCount != 0 {
		t.Fatalf("import result wrong: %+v", result)
	}
	// The skill dirs landed under ~/.nex/skills.
	data, err := os.ReadFile(filepath.Join(f.home, ".nex", "skills", "code-review", "SKILL.md"))
	if err != nil || !contains(string(data), "Review code.") {
		t.Fatalf("skill not copied: %v", err)
	}
	// Re-import: everything is now targetExists.
	second, _ := svc.ImportSelected(context.Background(), ImportSelectedRequest{
		WorkspaceRef: WorkspaceRef{WorkspacePath: f.workspace},
		Selections: []Selection{{
			Agent: "claudeCode", Category: CategorySkills, ImportMode: "copy",
			SourceScope: ScopeGlobal,
		}},
	})
	if second.SuccessCount != 0 || second.SkippedCount != 2 {
		t.Fatalf("re-import must skip everything: %+v", second)
	}
}

func TestImportSkillsSymlink(t *testing.T) {
	f := newFixture(t)
	svc := newService(f, fakeSettings(false))
	result, err := svc.ImportSelected(context.Background(), ImportSelectedRequest{
		Selections: []Selection{{Agent: "claudeCode", Category: CategorySkills, ImportMode: "symlink"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.SuccessCount != 2 {
		t.Fatalf("symlink import failed: %+v", result)
	}
	link := filepath.Join(f.home, ".nex", "skills", "code-review")
	info, err := os.Lstat(link)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode()&os.ModeSymlink == 0 {
		t.Fatal("expected a symlink")
	}
	target, err := os.Readlink(link)
	if err != nil {
		t.Fatal(err)
	}
	if !filepath.IsAbs(target) {
		t.Fatalf("symlink target must be absolute: %s", target)
	}
}

func TestImportCommands(t *testing.T) {
	f := newFixture(t)
	writeFile(t, filepath.Join(f.home, ".claude", "commands", "deploy.md"), "---\ndescription: Deploy the app\nargument-hint: [env]\n---\nDeploy to {{env}}.\n")
	writeFile(t, filepath.Join(f.home, ".claude", "commands", "sub", "lint.md"), "Lint.\n")
	svc := newService(f, fakeSettings(false))
	result, err := svc.ImportSelected(context.Background(), ImportSelectedRequest{
		Selections: []Selection{{Agent: "claudeCode", Category: CategoryCommands, ImportMode: "copy"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.SuccessCount != 2 {
		t.Fatalf("commands import wrong: %+v", result)
	}
	data, err := os.ReadFile(filepath.Join(f.home, ".nex", "commands", "deploy.md"))
	if err != nil || !contains(string(data), "Deploy to") {
		t.Fatalf("command not copied: %v", err)
	}
	data, err = os.ReadFile(filepath.Join(f.home, ".nex", "commands", "sub", "lint.md"))
	if err != nil {
		t.Fatalf("nested command not copied: %v", err)
	}
	// Command names are /-prefixed paths.
	for _, task := range result.TaskResults {
		if task.CommandResults[0].Name != "/deploy" {
			t.Fatalf("command name wrong: %+v", task.CommandResults[0])
		}
	}
}

func TestImportMcpServersAllFormats(t *testing.T) {
	f := newFixture(t)
	// mcpServersJson (claudeCode project file .mcp.json)
	writeFile(t, filepath.Join(f.workspace, ".mcp.json"), `{"mcpServers":{"fs":{"command":"uvx","args":["fs-server"]}}}`)
	// codexToml
	writeFile(t, filepath.Join(f.home, ".codex", "config.toml"), "[mcp_servers.search]\ncommand = \"rg\"\nargs = [\"--server\"]\ntimeout = 100\n")
	// mcpJson (openCode: command array + type mapping)
	writeFile(t, filepath.Join(f.home, ".config", "opencode", "opencode.json"), `{"mcp":{"remote":{"type":"remote","command":["curl","--server"]}}}`)

	svc := newService(f, fakeSettings(false))
	result, err := svc.ImportSelected(context.Background(), ImportSelectedRequest{
		WorkspaceRef: WorkspaceRef{WorkspacePath: f.workspace},
		Selections: []Selection{
			{Agent: "claudeCode", Category: CategoryMcpServers},
			{Agent: "codexCli", Category: CategoryMcpServers},
			{Agent: "openCode", Category: CategoryMcpServers},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.SuccessCount != 3 {
		t.Fatalf("mcp import wrong: %+v", result)
	}

	// The workspace config carries the project server; the user config carries
	// the codex + opencode servers.
	workspaceConfig := readJSON(t, filepath.Join(f.workspace, ".nex", "config.json"))
	servers := serversOf(t, workspaceConfig)
	if servers["fs"] == nil {
		t.Fatalf("fs server missing: %v", servers)
	}
	userConfig := readJSON(t, filepath.Join(f.home, ".nex", "cli", "config.json"))
	servers = serversOf(t, userConfig)
	if servers["search"] == nil {
		t.Fatalf("search server missing: %v", servers)
	}
	// codexToml: timeout stripped, args preserved.
	search := servers["search"].(map[string]any)
	if _, has := search["timeout"]; has {
		t.Fatal("timeout must be stripped from external configs")
	}
	if search["command"] != "rg" {
		t.Fatalf("search command wrong: %v", search)
	}
	// openCode: command array → command + args; type remote → http.
	remote := servers["remote"].(map[string]any)
	if remote["command"] != "curl" || remote["type"] != "http" {
		t.Fatalf("openCode normalization wrong: %v", remote)
	}
	args, _ := remote["args"].([]any)
	if len(args) != 1 || args[0] != "--server" {
		t.Fatalf("openCode args wrong: %v", remote["args"])
	}

	// Re-import: sameNameExists.
	second, _ := svc.ImportSelected(context.Background(), ImportSelectedRequest{
		WorkspaceRef: WorkspaceRef{WorkspacePath: f.workspace},
		Selections:   []Selection{{Agent: "codexCli", Category: CategoryMcpServers}},
	})
	if second.SuccessCount != 0 || second.SkippedCount != 1 {
		t.Fatalf("mcp re-import must skip: %+v", second)
	}
}

func TestImportPluginsRegistersDir(t *testing.T) {
	f := newFixture(t)
	writeFile(t, filepath.Join(f.home, ".claude", "plugins", "my-plugin", ".claude-plugin", "plugin.json"), `{"name":"my-plugin","version":"1.0.0"}`)
	// Non-plugin dir (no manifest) must not be discovered.
	writeFile(t, filepath.Join(f.home, ".claude", "plugins", "not-a-plugin", "README.md"), "no manifest\n")
	svc := newService(f, fakeSettings(false))
	result, err := svc.ImportSelected(context.Background(), ImportSelectedRequest{
		Selections: []Selection{{Agent: "claudeCode", Category: CategoryPlugins, ImportMode: "copy"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.SuccessCount != 1 {
		t.Fatalf("plugin import wrong: %+v", result)
	}
	// The dir was copied and registered in the cli config.
	if _, err := os.Stat(filepath.Join(f.home, ".nex", "plugins", "my-plugin", ".claude-plugin", "plugin.json")); err != nil {
		t.Fatal(err)
	}
	config := readJSON(t, filepath.Join(f.home, ".nex", "cli", "config.json"))
	plugins, _ := config["plugins"].(map[string]any)
	dirs, _ := plugins["dirs"].([]any)
	if len(dirs) != 1 {
		t.Fatalf("plugin dir not registered: %v", config)
	}
	if dir, _ := dirs[0].(string); dir != filepath.Join(f.home, ".nex", "plugins", "my-plugin") {
		t.Fatalf("registered dir wrong: %s", dir)
	}
	// Re-import registers nothing new (same id → sameNameExists).
	second, _ := svc.ImportSelected(context.Background(), ImportSelectedRequest{
		Selections: []Selection{{Agent: "claudeCode", Category: CategoryPlugins, ImportMode: "copy"}},
	})
	if second.SuccessCount != 0 {
		t.Fatalf("plugin re-import must skip: %+v", second)
	}
	config = readJSON(t, filepath.Join(f.home, ".nex", "cli", "config.json"))
	plugins, _ = config["plugins"].(map[string]any)
	dirs, _ = plugins["dirs"].([]any)
	if len(dirs) != 1 {
		t.Fatalf("duplicate dir registration: %v", config)
	}
}

// ---------------------------------------------------------------------------
// Claude AGENTS.md migration
// ---------------------------------------------------------------------------

func TestClaudeAgentsFileMigration(t *testing.T) {
	f := newFixture(t)
	writeFile(t, filepath.Join(f.home, ".claude", "CLAUDE.md"), "# project guide\n")
	svc := newService(f, fakeSettings(false))

	status, err := svc.GetClaudeAgentsFileMigrationStatus(context.Background(), WorkspaceRef{})
	if err != nil {
		t.Fatal(err)
	}
	if !status.Supported || !status.SourceExists {
		t.Fatalf("status wrong: %+v", status)
	}
	if status.TargetPath != filepath.Join(f.home, ".nex", "AGENTS.md") {
		t.Fatalf("target path wrong: %s", status.TargetPath)
	}

	// Copy without overwrite.
	result, err := svc.CopyClaudeAgentsFileToNexAgentsFile(context.Background(), CopyClaudeAgentsRequest{})
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != "copied" || result.Overwritten {
		t.Fatalf("copy result wrong: %+v", result)
	}
	data, _ := os.ReadFile(filepath.Join(f.home, ".nex", "AGENTS.md"))
	if !contains(string(data), "project guide") {
		t.Fatal("AGENTS.md not copied")
	}

	// Second copy without overwrite: targetExists.
	result, _ = svc.CopyClaudeAgentsFileToNexAgentsFile(context.Background(), CopyClaudeAgentsRequest{})
	if result.Status != "skipped" || result.SkippedReason != "targetExists" {
		t.Fatalf("second copy wrong: %+v", result)
	}
	// Overwrite works.
	result, _ = svc.CopyClaudeAgentsFileToNexAgentsFile(context.Background(), CopyClaudeAgentsRequest{Overwrite: true})
	if result.Status != "copied" || !result.Overwritten {
		t.Fatalf("overwrite wrong: %+v", result)
	}

	// Missing source on a fresh home.
	fresh := newService(fixture{home: t.TempDir()}, fakeSettings(false))
	status, _ = fresh.GetClaudeAgentsFileMigrationStatus(context.Background(), WorkspaceRef{})
	if status.Supported || status.UnavailableReason != "missingSource" {
		t.Fatalf("missing source status wrong: %+v", status)
	}
}

// ---------------------------------------------------------------------------
// first-run prompt state
// ---------------------------------------------------------------------------

func TestFirstRunPromptState(t *testing.T) {
	settings := fakeSettings(false)
	svc := newService(newFixture(t), settings)
	state, err := svc.GetFirstRunPromptState(context.Background())
	if err != nil || state.Handled {
		t.Fatalf("state wrong: %+v %v", state, err)
	}
	if err := svc.MarkFirstRunPromptHandled(context.Background()); err != nil {
		t.Fatal(err)
	}
	state, err = svc.GetFirstRunPromptState(context.Background())
	if err != nil || !state.Handled {
		t.Fatalf("state after mark wrong: %+v %v", state, err)
	}
	stub := settings.(*stubSettings)
	if len(stub.updates) != 1 || stub.updates[0]["settingsSyncFirstRunPromptHandled"] != true {
		t.Fatalf("update patch wrong: %v", stub.updates)
	}
}

// ---------------------------------------------------------------------------
// helpers
// ---------------------------------------------------------------------------

func readJSON(t *testing.T, path string) map[string]any {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	var parsed map[string]any
	if err := json.Unmarshal(data, &parsed); err != nil {
		t.Fatalf("parse %s: %v", path, err)
	}
	return parsed
}

func serversOf(t *testing.T, config map[string]any) map[string]any {
	t.Helper()
	mcp, _ := config["mcp"].(map[string]any)
	servers, _ := mcp["servers"].(map[string]any)
	if servers == nil {
		servers = map[string]any{}
	}
	return servers
}

func contains(s, substr string) bool {
	return len(s) >= len(substr) && (s == substr || len(s) > 0 && stringContains(s, substr))
}

func stringContains(s, substr string) bool {
	for i := 0; i+len(substr) <= len(s); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}
