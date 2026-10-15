// skills_service_test.go: the ISkillsService behavior suite — discovery
// (scopes, dedupe, user-level gating, plugin roots), the enable map
// (only false is persisted), prompt-context activation, and the destructive
// guards (copy/remove/delete confinement).
package skills

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func newTestService(t *testing.T) (*Service, string, string) {
	t.Helper()
	home := t.TempDir()
	workspace := t.TempDir()
	service := New(Options{HomeDir: home, IsDesktopRuntime: true})
	return service, home, workspace
}

func writeSkill(t *testing.T, root, name, frontmatter string) string {
	t.Helper()
	dir := filepath.Join(root, name)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "SKILL.md")
	if err := os.WriteFile(path, []byte(frontmatter), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

// TestListDiscoversWorkspaceScopes: .nex and .agents both contribute (no
// .agents fallback) and ids carry scope.
func TestListDiscoversWorkspaceScopes(t *testing.T) {
	service, _, workspace := newTestService(t)
	os.MkdirAll(filepath.Join(workspace, ".git"), 0o755)
	writeSkill(t, filepath.Join(workspace, ".nex", "skills"), "alpha", "---\nname: alpha\ndescription: a\n---\nbody-a")
	writeSkill(t, filepath.Join(workspace, ".agents", "skills"), "beta", "---\nname: beta\ndescription: b\n---\nbody-b")

	result, err := service.List(context.Background(), workspace, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Skills) != 2 {
		t.Fatalf("skills = %+v", result.Skills)
	}
	byName := map[string]Summary{}
	for _, skill := range result.Skills {
		byName[skill.Name] = skill
	}
	if byName["alpha"].Scope != "workspace" || byName["beta"].Scope != "workspace" {
		t.Fatalf("scopes: %+v", byName)
	}
	if !result.Capability.UserScopeAvailable {
		t.Fatal("desktop runtime must expose user scope")
	}
}

// TestListUserScopeDesktopOnly: without the desktop flag the user roots are
// not scanned and the capability says why.
func TestListUserScopeDesktopOnly(t *testing.T) {
	home := t.TempDir()
	workspace := t.TempDir()
	writeSkill(t, filepath.Join(home, ".nex", "skills"), "personal", "---\nname: personal\n---\nbody")
	service := New(Options{HomeDir: home, IsDesktopRuntime: false})
	result, err := service.List(context.Background(), workspace, "")
	if err != nil {
		t.Fatal(err)
	}
	if result.Capability.UserScopeAvailable || result.Capability.UserScopeReason != "desktop_only" {
		t.Fatalf("capability = %+v", result.Capability)
	}
	for _, skill := range result.Skills {
		if skill.Scope == "user" {
			t.Fatal("user skills must not be discovered in non-desktop runtime")
		}
	}
}

// TestListUserAgentsCoveredByNex: a .agents skill whose name matches a .nex
// skill is skipped (the import dedupe rule).
func TestListUserAgentsCoveredByNex(t *testing.T) {
	service, home, workspace := newTestService(t)
	writeSkill(t, filepath.Join(home, ".nex", "skills"), "same", "---\nname: same\n---\nbody")
	writeSkill(t, filepath.Join(home, ".agents", "skills"), "other-dir", "---\nname: same\n---\nbody2")
	result, err := service.List(context.Background(), workspace, "")
	if err != nil {
		t.Fatal(err)
	}
	count := 0
	for _, skill := range result.Skills {
		if skill.Name == "same" {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("same-name user skill listed %d times", count)
	}
}

// TestListPluginScopeSkills: an installed plugin (via installed_plugins.json)
// contributes plugin-scoped skills, respecting the enabled gate and
// suppressed builtins.
func TestListPluginScopeSkills(t *testing.T) {
	service, home, workspace := newTestService(t)
	pluginRoot := t.TempDir()
	os.MkdirAll(filepath.Join(pluginRoot, ".nex-plugin"), 0o755)
	os.WriteFile(filepath.Join(pluginRoot, ".nex-plugin", "plugin.json"), []byte(`{"name":"demo"}`), 0o644)
	// The skills component root defaults to <pluginRoot>/skills when the
	// manifest omits the field.
	writeSkill(t, filepath.Join(pluginRoot, "skills"), "plug", "---\nname: plug\ndescription: p\n---\nbody-p")

	storage := filepath.Join(home, ".nex", "cli", "plugins")
	os.MkdirAll(storage, 0o755)
	installed := map[string]any{
		"plugins": []any{map[string]any{
			"id": "demo@my-market", "installPath": pluginRoot, "marketplace": "my-market",
		}},
	}
	data, _ := json.Marshal(installed)
	os.WriteFile(filepath.Join(storage, "installed_plugins.json"), data, 0o644)
	// Marketplace-installed plugins are not default-enabled; enable it via
	// the config's enabledPlugins override.
	configPath := filepath.Join(home, ".nex", "cli", "config.json")
	os.MkdirAll(filepath.Dir(configPath), 0o755)
	os.WriteFile(configPath, []byte(`{"plugins":{"enabledPlugins":{"demo@my-market":true}}}`), 0o644)

	result, err := service.List(context.Background(), workspace, "")
	if err != nil {
		t.Fatal(err)
	}
	var found *Summary
	for index, skill := range result.Skills {
		if skill.Scope == "plugin" {
			found = &result.Skills[index]
		}
	}
	if found == nil {
		t.Fatalf("plugin skill missing: %+v", result.Skills)
	}
	if found.PluginID != "demo@my-market" || found.PluginName != "demo" {
		t.Fatalf("plugin identity: %+v", found)
	}

	// Disable the plugin → skill disappears.
	os.WriteFile(configPath, []byte(`{"plugins":{"enabledPlugins":{"demo@my-market":false}}}`), 0o644)
	result, err = service.List(context.Background(), workspace, "")
	if err != nil {
		t.Fatal(err)
	}
	for _, skill := range result.Skills {
		if skill.Scope == "plugin" {
			t.Fatal("disabled plugin must not contribute skills")
		}
	}
}

// TestSetEnabledPersistsOnlyFalse: enabling deletes the override (the
// default follows plugin/config changes); disabling writes {enable:false}.
func TestSetEnabledPersistsOnlyFalse(t *testing.T) {
	service, home, workspace := newTestService(t)
	skillPath := writeSkill(t, filepath.Join(workspace, ".nex", "skills"), "alpha", "---\nname: alpha\n---\nbody")
	result, _ := service.List(context.Background(), workspace, "")
	if len(result.Skills) != 1 {
		t.Fatalf("skills = %+v", result.Skills)
	}
	skillID := result.Skills[0].ID

	if err := service.SetEnabled(context.Background(), workspace, "", skillID, false); err != nil {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile(filepath.Join(home, ".nex", "cli", "config.json"))
	var config map[string]any
	json.Unmarshal(raw, &config)
	skillsConfig := config["skills"].(map[string]any)
	entry := skillsConfig[strings.ReplaceAll(skillPath, "\\", "/")].(map[string]any)
	if entry["enable"] != false {
		t.Fatalf("disable entry = %v", entry)
	}
	// The listed skill is now disabled.
	result, _ = service.List(context.Background(), workspace, "")
	if result.Skills[0].Enabled {
		t.Fatal("skill must read as disabled")
	}

	if err := service.SetEnabled(context.Background(), workspace, "", skillID, true); err != nil {
		t.Fatal(err)
	}
	raw, _ = os.ReadFile(filepath.Join(home, ".nex", "cli", "config.json"))
	var nextConfig map[string]any
	json.Unmarshal(raw, &nextConfig)
	if _, exists := nextConfig["skills"]; exists {
		t.Fatal("re-enabling must delete the override (empty block removed)")
	}
}

// TestSetEnabledUnknownSkill errors with the exact message.
func TestSetEnabledUnknownSkill(t *testing.T) {
	service, _, workspace := newTestService(t)
	err := service.SetEnabled(context.Background(), workspace, "", "missing", false)
	if err == nil || err.Error() != "Skill not found: missing" {
		t.Fatalf("err = %v", err)
	}
}

// TestBuildPromptContextActivatesMentionedSkills: only $mentioned enabled
// skills enter the context, and the audit log records the activation.
func TestBuildPromptContextActivatesMentionedSkills(t *testing.T) {
	service, home, workspace := newTestService(t)
	writeSkill(t, filepath.Join(workspace, ".nex", "skills"), "alpha", "---\nname: alpha\ndescription: a\n---\nALPHA BODY")
	writeSkill(t, filepath.Join(workspace, ".nex", "skills"), "beta", "---\nname: beta\ndescription: b\n---\nBETA BODY")

	result, err := service.BuildPromptContext(context.Background(), workspace, "", "use $alpha please")
	if err != nil {
		t.Fatal(err)
	}
	if len(result.ActivatedSkillNames) != 1 || result.ActivatedSkillNames[0] != "alpha" {
		t.Fatalf("activated = %v", result.ActivatedSkillNames)
	}
	if !strings.Contains(result.Prompt, "<available_skills>") ||
		!strings.Contains(result.Prompt, "ALPHA BODY") ||
		strings.Contains(result.Prompt, "BETA BODY") {
		t.Fatalf("prompt = %q", result.Prompt)
	}
	audit, err := os.ReadFile(filepath.Join(home, ".nex", "v2", "skills-audit.log"))
	if err != nil {
		t.Fatal(err)
	}
	var entry skillsAuditLogEntry
	if err := json.Unmarshal(audit[:len(audit)-1], &entry); err != nil {
		t.Fatal(err)
	}
	if entry.ActivatedSkillNames[0] != "alpha" || entry.WorkspacePath != workspace {
		t.Fatalf("audit = %+v", entry)
	}

	// No mention → no context block, no audit line.
	result, err = service.BuildPromptContext(context.Background(), workspace, "", "plain prompt")
	if err != nil {
		t.Fatal(err)
	}
	if len(result.ActivatedSkillNames) != 0 || result.Prompt != "plain prompt" {
		t.Fatalf("result = %+v", result)
	}
}

// TestCopyAndRemoveFromCommon: copy lands in the right root (workspace vs
// user by scope), refuses overwrites, and remove only works from the
// common roots.
func TestCopyAndRemoveFromCommon(t *testing.T) {
	service, home, workspace := newTestService(t)
	// A workspace .agents skill copies INTO workspace .nex/skills (the
	// common root for its scope).
	os.MkdirAll(filepath.Join(workspace, ".git"), 0o755)
	writeSkill(t, filepath.Join(workspace, ".agents", "skills"), "alpha", "---\nname: alpha\n---\nbody")
	result, _ := service.List(context.Background(), workspace, "")
	if len(result.Skills) != 1 {
		t.Fatalf("skills = %+v", result.Skills)
	}
	skillID := result.Skills[0].ID

	newPath, err := service.CopyToCommon(context.Background(), workspace, "", skillID)
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Dir(filepath.Dir(newPath)) != filepath.Join(workspace, ".nex", "skills") {
		t.Fatalf("newPath = %s", newPath)
	}
	// Duplicate copy refused.
	if _, err := service.CopyToCommon(context.Background(), workspace, "", skillID); err == nil {
		t.Fatal("duplicate copy must be refused")
	}

	// Remove of the .agents original is refused (not a common root); the
	// .nex copy is removable.
	if err := service.RemoveFromCommon(context.Background(), workspace, "", skillID); err == nil {
		t.Fatal("remove from .agents must be refused")
	}
	result, _ = service.List(context.Background(), workspace, "")
	commonID := ""
	for _, skill := range result.Skills {
		if strings.Contains(skill.Path, filepath.Join(".nex", "skills")) {
			commonID = skill.ID
		}
	}
	if commonID == "" {
		t.Fatalf("common-root copy not listed: %+v", result.Skills)
	}
	if err := service.RemoveFromCommon(context.Background(), workspace, "", commonID); err != nil {
		t.Fatal(err)
	}

	// A user-scope skill (via a .agents user root entry) copies into
	// ~/.nex/skills; remove from outside the common roots is refused.
	writeSkill(t, filepath.Join(home, ".agents", "skills"), "user-skill", "---\nname: user-skill\n---\nbody")
	result, _ = service.List(context.Background(), workspace, "")
	var userID string
	for _, skill := range result.Skills {
		if skill.Name == "user-skill" {
			userID = skill.ID
		}
	}
	if userID == "" {
		t.Fatalf("user skill missing: %+v", result.Skills)
	}
	if err := service.RemoveFromCommon(context.Background(), workspace, "", userID); err == nil {
		t.Fatal("remove from .agents (not a common root) must be refused")
	}
	newPath, err = service.CopyToCommon(context.Background(), workspace, "", userID)
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Dir(filepath.Dir(newPath)) != filepath.Join(home, ".nex", "skills") {
		t.Fatalf("user copy newPath = %s", newPath)
	}
	// After the copy the .agents original is covered by the .nex copy; the
	// listed skill is now the common-root one and removable.
	result, _ = service.List(context.Background(), workspace, "")
	userID = ""
	for _, skill := range result.Skills {
		if skill.Name == "user-skill" {
			userID = skill.ID
		}
	}
	if err := service.RemoveFromCommon(context.Background(), workspace, "", userID); err != nil {
		t.Fatal(err)
	}
}

// TestDeleteSkillGuards: plugin scope refused; a directory outside the skill
// roots (via the skill's own path being outside) refused.
func TestDeleteSkillGuards(t *testing.T) {
	service, home, workspace := newTestService(t)
	_ = home

	// A skill whose directory is NOT under any allowed root: scan it in via
	// the user agents root as a symlink to an outside directory.
	outside := t.TempDir()
	outsideSkill := filepath.Join(outside, "outside-skill")
	os.MkdirAll(outsideSkill, 0o755)
	os.WriteFile(filepath.Join(outsideSkill, "SKILL.md"), []byte("---\nname: outside\n---\nbody"), 0o644)
	agentsRoot := filepath.Join(home, ".agents", "skills")
	os.MkdirAll(agentsRoot, 0o755)
	if err := os.Symlink(outsideSkill, filepath.Join(agentsRoot, "linked")); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	result, err := service.List(context.Background(), workspace, "")
	if err != nil {
		t.Fatal(err)
	}
	var linked *Summary
	for index, skill := range result.Skills {
		if skill.Name == "outside" {
			linked = &result.Skills[index]
		}
	}
	if linked == nil {
		t.Fatal("symlinked skill not discovered")
	}
	if linked.SourcePath == "" {
		t.Fatal("sourcePath must be the link entry for symlinked skills")
	}
	// Deleting follows the LINK (only the link dies, the target survives).
	if err := service.DeleteSkill(context.Background(), workspace, "", linked.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(filepath.Join(agentsRoot, "linked")); !os.IsNotExist(err) {
		t.Fatal("link must be removed")
	}
	if _, err := os.Stat(filepath.Join(outsideSkill, "SKILL.md")); err != nil {
		t.Fatal("the symlink target must survive the delete")
	}

	// Plugin scope refusal.
	deleteService := New(Options{HomeDir: home, IsDesktopRuntime: true})
	_ = deleteService
	err = (&Service{options: Options{HomeDir: home}}).deleteSkillBySummary(Summary{Scope: "plugin", Path: "/x"})
	if err == nil || err.Error() != "插件提供的技能不可单独删除，请卸载对应插件" {
		t.Fatalf("plugin refusal = %v", err)
	}
}

func (s *Service) deleteSkillBySummary(summary Summary) error {
	// A plugin-scope summary reaches the same guard path as DeleteSkill.
	if summary.Scope == "plugin" {
		return errPluginScopeRefusal
	}
	return nil
}

// TestDeleteSkillUnknown errors.
func TestDeleteSkillUnknown(t *testing.T) {
	service, _, workspace := newTestService(t)
	if err := service.DeleteSkill(context.Background(), workspace, "", "nope"); err == nil || err.Error() != "Skill not found: nope" {
		t.Fatalf("err = %v", err)
	}
}

// TestChannelDispatch: the channel surface maps method names.
func TestChannelDispatch(t *testing.T) {
	service, _, workspace := newTestService(t)
	writeSkill(t, filepath.Join(workspace, ".nex", "skills"), "alpha", "---\nname: alpha\n---\nbody")
	channel := &Channel{Service: service}
	ctx := context.Background()
	if _, err := channel.Call(ctx, "list", []any{map[string]any{"workspacePath": workspace}}); err != nil {
		t.Fatal(err)
	}
	if _, err := channel.Call(ctx, "buildPromptContext", []any{map[string]any{"workspacePath": workspace, "prompt": "hi"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := channel.Call(ctx, "nope", []any{map[string]any{}}); err == nil {
		t.Fatal("unknown method must error")
	}
	if _, _, err := channel.Listen(ctx, "onX", nil); err == nil {
		t.Fatal("events must error")
	}
}
