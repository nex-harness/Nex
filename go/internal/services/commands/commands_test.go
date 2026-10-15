// commands_test.go: the ICommandsService behavior suite — discovery order
// (.nex priority), frontmatter generation/preservation, the enabled
// override migration on rename, and the plugin command discovery.
package commands

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func newService(t *testing.T) (*Service, string, string) {
	t.Helper()
	home := t.TempDir()
	workspace := t.TempDir()
	return New(Options{HomeDir: home}), home, workspace
}

func writeCommand(t *testing.T, root, name, content string) {
	t.Helper()
	os.MkdirAll(root, 0o755)
	os.WriteFile(filepath.Join(root, name), []byte(content), 0o644)
}

// TestListPriorityAndDedupe: .nex wins over .agents in the same scope;
// duplicate names keep the first (workspace before user).
func TestListPriorityAndDedupe(t *testing.T) {
	service, home, workspace := newService(t)
	writeCommand(t, filepath.Join(workspace, ".agents", "commands"), "dup.md", "agents version")
	writeCommand(t, filepath.Join(workspace, ".nex", "commands"), "dup.md", "nex version")
	writeCommand(t, filepath.Join(home, ".nex", "commands"), "global.md", "global version")

	result, err := service.List(context.Background(), "", workspace, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(result.UserCommands) != 2 {
		t.Fatalf("commands = %+v", result.UserCommands)
	}
	byName := map[string]UserCommand{}
	for _, command := range result.UserCommands {
		byName[command.Name] = command
	}
	if byName["/dup"].Prompt != "nex version" {
		t.Fatal(".nex must take priority over .agents")
	}
	if byName["/global"].Scope != "global" {
		t.Fatalf("scope = %+v", byName["/global"])
	}
	if len(result.Commands) != len(result.UserCommands)+len(result.PluginCommands) {
		t.Fatal("commands must be user + plugin combined")
	}
}

// TestListAgentsFallback: .agents participates when .nex has no commands.
func TestListAgentsFallback(t *testing.T) {
	service, _, workspace := newService(t)
	writeCommand(t, filepath.Join(workspace, ".agents", "commands"), "only.md", "agents version")
	result, err := service.List(context.Background(), "", workspace, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(result.UserCommands) != 1 || result.UserCommands[0].Prompt != "agents version" {
		t.Fatalf("commands = %+v", result.UserCommands)
	}
}

// TestWriteUpdateDelete: create, duplicate refusal, update with rename
// (override migration), delete idempotence.
func TestWriteUpdateDelete(t *testing.T) {
	service, home, workspace := newService(t)

	created, err := service.WriteCommandFile(context.Background(), "nexAgent", "project", workspace, commandConfig{
		Name: "my-cmd", Prompt: "do the thing", Description: "desc", ArgumentHint: "[x]",
	})
	if err != nil {
		t.Fatal(err)
	}
	if created.Name != "/my-cmd" {
		t.Fatalf("name = %s", created.Name)
	}
	raw, _ := os.ReadFile(filepath.Join(workspace, ".nex", "commands", "my-cmd.md"))
	expected := "---\ndescription: desc\nargument-hint: [x]\n---\n\ndo the thing"
	if string(raw) != expected {
		t.Fatalf("content = %q want %q", raw, expected)
	}

	// Duplicate name refused.
	if _, err := service.WriteCommandFile(context.Background(), "nexAgent", "project", workspace, commandConfig{Name: "my-cmd", Prompt: "x"}); err == nil || err.Error() != "Command file already exists: my-cmd.md" {
		t.Fatalf("err = %v", err)
	}

	// Disable, then rename via update — the override must migrate.
	if err := service.SetCommandEnabled(context.Background(), created.FilePath, false); err != nil {
		t.Fatal(err)
	}
	updated, err := service.UpdateCommandFile(context.Background(), "nexAgent", "project", workspace, created.FilePath, commandConfig{
		Name: "renamed", Prompt: "new prompt",
	})
	if err != nil {
		t.Fatal(err)
	}
	if updated.Name != "/renamed" {
		t.Fatalf("updated name = %s", updated.Name)
	}
	if updated.Enabled {
		t.Fatal("the disable override must survive the rename")
	}
	if _, err := os.Stat(created.FilePath); err == nil {
		t.Fatal("old file must be removed after rename")
	}
	// Frontmatter: description/argument-hint dropped when cleared.
	raw, _ = os.ReadFile(filepath.Join(workspace, ".nex", "commands", "renamed.md"))
	if string(raw) != "new prompt" {
		t.Fatalf("renamed content = %q", raw)
	}

	// Delete is idempotent.
	if err := service.DeleteCommandFile(context.Background(), filepath.Join(workspace, ".nex", "commands", "renamed.md")); err != nil {
		t.Fatal(err)
	}
	if err := service.DeleteCommandFile(context.Background(), filepath.Join(workspace, ".nex", "commands", "renamed.md")); err != nil {
		t.Fatal(err)
	}
	_ = home
}

// TestParseCommandFile: frontmatter description / argument-hint multiline.
func TestParseCommandFile(t *testing.T) {
	parsed := parseCommandFile("---\ndescription: runs a thing\n  with continuation\nargument-hint: [file]\n---\nbody prompt", "/root/sub/cmd.md")
	if parsed == nil {
		t.Fatal("parse failed")
	}
	// parseCommandFile derives the name from the file's basename only; the
	// relative-path shape comes from getCommandName at discovery.
	if parsed.Name != "/cmd" {
		t.Fatalf("name = %s", parsed.Name)
	}
	if parsed.Description != "runs a thing with continuation" {
		t.Fatalf("description = %q", parsed.Description)
	}
	if parsed.ArgumentHint != "[file]" {
		t.Fatalf("argumentHint = %q", parsed.ArgumentHint)
	}
	if parsed.Prompt != "body prompt" {
		t.Fatalf("prompt = %q", parsed.Prompt)
	}
}

// TestPluginCommandsDiscovery: an installed plugin contributes read-only
// commands with the plugin identity fields.
func TestPluginCommandsDiscovery(t *testing.T) {
	service, home, workspace := newService(t)
	pluginRoot := filepath.Join(home, "plugin-a")
	os.MkdirAll(filepath.Join(pluginRoot, ".nex-plugin"), 0o755)
	os.WriteFile(filepath.Join(pluginRoot, ".nex-plugin", "plugin.json"), []byte(`{"name":"plug"}`), 0o644)
	os.MkdirAll(filepath.Join(pluginRoot, "commands", "sub"), 0o755)
	os.WriteFile(filepath.Join(pluginRoot, "commands", "hi.md"), []byte("hi prompt"), 0o644)
	os.WriteFile(filepath.Join(pluginRoot, "commands", "sub", "deep.md"), []byte("deep prompt"), 0o644)

	// Register through the config dirs + enable.
	configPath := filepath.Join(home, ".nex", "cli", "config.json")
	os.MkdirAll(filepath.Dir(configPath), 0o755)
	os.WriteFile(configPath, []byte(`{"plugins":{"dirs":["`+pluginRoot+`"]}}`), 0o644)

	result, err := service.List(context.Background(), "", workspace, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(result.PluginCommands) != 2 {
		t.Fatalf("plugin commands = %+v", result.PluginCommands)
	}
	names := map[string]bool{}
	for _, command := range result.PluginCommands {
		if command.PluginName != "plug" || command.Source != "plugin" || command.ID == "" {
			t.Fatalf("command = %+v", command)
		}
		if !command.Enabled {
			t.Fatal("inline dir plugins default to enabled")
		}
		names[command.Name] = true
	}
	if !names["/hi"] || !names["/sub/deep"] {
		t.Fatalf("names = %v", names)
	}
}

// TestGetPrimaryUserCommandsDirectory creates the dir.
func TestGetPrimaryUserCommandsDirectory(t *testing.T) {
	service, home, _ := newService(t)
	path, err := service.GetPrimaryUserCommandsDirectory(context.Background(), "")
	if err != nil {
		t.Fatal(err)
	}
	if path != filepath.Join(home, ".nex", "commands") {
		t.Fatalf("path = %s", path)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatal("directory must be materialized")
	}
}
