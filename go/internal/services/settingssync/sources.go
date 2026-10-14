// Package settingssync ports
// packages/services/src/settings-sync/settingsSyncService.ts (step 3.5):
// ISettingsSyncService — discovery and import of external agents' skills,
// commands, plugins and MCP server configs into the Nex layout
// (~/.nex/skills|commands|plugins and ~/.nex/cli/config.json).
//
// 读取结论（plan 3.5 Special 的核实结果）：settings-sync 不是与远端服务器同步
// 的协议 —— 它是本地文件扫描/导入（各外部 agent 的配置目录 → Nex 目录），
// 唯一的"网络"依赖是 settingService（getFirstRunPromptState /
// markFirstRunPromptHandled）。按本地文件系统服务移植。
package settingssync

import (
	"path/filepath"
	"strings"
)

// ChannelName is ServiceChannels.SettingsSync.
const ChannelName = "settings-sync"

// Agent is SettingsSyncAgent.
type Agent = string

// All agents in SettingsSyncAgent order (used for discovery output order).
var AllAgents = []string{
	"nex", "claudeCode", "codexCli", "openCode", "openClaw", "augment",
	"continue", "goose", "qwenCode", "qode", "qodeCn", "windsurf", "trae",
	"traeCn", "kiroCli", "roo", "codeBuddy", "agents",
}

// category is SettingsSyncCategory.
const (
	CategorySkills     = "skills"
	CategoryCommands   = "commands"
	CategoryPlugins    = "plugins"
	CategoryMcpServers = "mcpServers"
)

// SourceScope is SettingsSyncSourceScope ("global" | "project") and the
// internal import scope ("user" | "workspace").
const (
	ScopeGlobal    = "global"
	ScopeProject   = "project"
	ScopeUser      = "user"
	ScopeWorkspace = "workspace"
)

// pathSource is ExternalAgentPathSource.
type pathSource struct {
	Agent       Agent
	ProjectPath []string
	GlobalPath  []string
}

// skillAgentSources ports SUPPORTED_SKILL_AGENT_SOURCES.
var skillAgentSources = []pathSource{
	{"claudeCode", []string{".claude", "skills"}, []string{".claude", "skills"}},
	{"codexCli", []string{".codex", "skills"}, []string{".codex", "skills"}},
	{"openCode", []string{".opencode", "skills"}, []string{".config", "opencode", "skills"}},
	{"openClaw", []string{"skills"}, []string{".openclaw", "skills"}},
	{"augment", []string{".augment", "skills"}, []string{".augment", "skills"}},
	{"continue", []string{".continue", "skills"}, []string{".continue", "skills"}},
	{"goose", []string{".goose", "skills"}, []string{".config", "goose", "skills"}},
	{"qwenCode", []string{".qwen", "skills"}, []string{".qwen", "skills"}},
	{"qode", []string{".qoder", "skills"}, []string{".qoder", "skills"}},
	{"qodeCn", []string{".qoder", "skills"}, []string{".qoder-cn", "skills"}},
	{"windsurf", []string{".windsurf", "skills"}, []string{".codeium", "windsurf", "skills"}},
	{"trae", []string{".trae", "skills"}, []string{".trae", "skills"}},
	{"traeCn", []string{".trae", "skills"}, []string{".trae-cn", "skills"}},
	{"kiroCli", []string{".kiro", "skills"}, []string{".kiro", "skills"}},
	{"roo", []string{".roo", "skills"}, []string{".roo", "skills"}},
	{"codeBuddy", []string{".codebuddy", "skills"}, []string{".codebuddy", "skills"}},
}

// commandAgentSources ports SUPPORTED_COMMAND_AGENT_SOURCES (note: no traeCn).
var commandAgentSources = []pathSource{
	{"claudeCode", []string{".claude", "commands"}, []string{".claude", "commands"}},
	{"codexCli", []string{".codex", "commands"}, []string{".codex", "commands"}},
	{"openCode", []string{".opencode", "commands"}, []string{".config", "opencode", "commands"}},
	{"openClaw", []string{"commands"}, []string{".openclaw", "commands"}},
	{"augment", []string{".augment", "commands"}, []string{".augment", "commands"}},
	{"continue", []string{".continue", "commands"}, []string{".continue", "commands"}},
	{"goose", []string{".goose", "commands"}, []string{".config", "goose", "commands"}},
	{"qwenCode", []string{".qwen", "commands"}, []string{".qwen", "commands"}},
	{"qode", []string{".qoder", "commands"}, []string{".qoder", "commands"}},
	{"qodeCn", []string{".qoder", "commands"}, []string{".qoder-cn", "commands"}},
	{"windsurf", []string{".windsurf", "commands"}, []string{".codeium", "windsurf", "commands"}},
	{"trae", []string{".trae", "commands"}, []string{".trae", "commands"}},
	{"kiroCli", []string{".kiro", "commands"}, []string{".kiro", "commands"}},
	{"roo", []string{".roo", "commands"}, []string{".roo", "commands"}},
	{"codeBuddy", []string{".codebuddy", "commands"}, []string{".codebuddy", "commands"}},
}

// pluginAgentSources ports SUPPORTED_PLUGIN_AGENT_SOURCES (no traeCn).
var pluginAgentSources = []pathSource{
	{"claudeCode", []string{".claude", "plugins"}, []string{".claude", "plugins"}},
	{"codexCli", []string{".codex", "plugins"}, []string{".codex", "plugins"}},
	{"openCode", []string{".opencode", "plugins"}, []string{".config", "opencode", "plugins"}},
	{"openClaw", []string{"plugins"}, []string{".openclaw", "plugins"}},
	{"augment", []string{".augment", "plugins"}, []string{".augment", "plugins"}},
	{"continue", []string{".continue", "plugins"}, []string{".continue", "plugins"}},
	{"goose", []string{".goose", "plugins"}, []string{".config", "goose", "plugins"}},
	{"qwenCode", []string{".qwen", "plugins"}, []string{".qwen", "plugins"}},
	{"qode", []string{".qoder", "plugins"}, []string{".qoder", "plugins"}},
	{"qodeCn", []string{".qoder", "plugins"}, []string{".qoder-cn", "plugins"}},
	{"windsurf", []string{".windsurf", "plugins"}, []string{".codeium", "windsurf", "plugins"}},
	{"trae", []string{".trae", "plugins"}, []string{".trae", "plugins"}},
	{"kiroCli", []string{".kiro", "plugins"}, []string{".kiro", "plugins"}},
	{"roo", []string{".roo", "plugins"}, []string{".roo", "plugins"}},
	{"codeBuddy", []string{".codebuddy", "plugins"}, []string{".codebuddy", "plugins"}},
}

// mcpConfigFormat is McpConfigFormat.
type mcpConfigFormat string

const (
	formatMcpServersJSON mcpConfigFormat = "mcpServersJson"
	formatMcpJSON        mcpConfigFormat = "mcpJson"
	formatCodexToml      mcpConfigFormat = "codexToml"
)

// mcpPathSource is ExternalAgentMcpPathSource.
type mcpPathSource struct {
	Agent        Agent
	ProjectFiles [][]string
	GlobalFiles  [][]string
	Format       mcpConfigFormat
}

// mcpAgentSources ports SUPPORTED_MCP_AGENT_SOURCES.
var mcpAgentSources = []mcpPathSource{
	{"claudeCode", [][]string{{".claude", "settings.json"}, {".mcp.json"}}, [][]string{{".claude", "settings.json"}}, formatMcpServersJSON},
	{"codexCli", [][]string{{".codex", "config.toml"}}, [][]string{{".codex", "config.toml"}}, formatCodexToml},
	{"openCode", [][]string{{".opencode", "opencode.json"}}, [][]string{{".config", "opencode", "opencode.json"}}, formatMcpJSON},
	{"openClaw", [][]string{{"settings.json"}}, [][]string{{".openclaw", "settings.json"}}, formatMcpServersJSON},
	{"qwenCode", [][]string{{".qwen", "settings.json"}}, [][]string{{".qwen", "settings.json"}}, formatMcpServersJSON},
	{"qode", [][]string{{".qoder", "settings.json"}}, [][]string{{".qoder", "settings.json"}}, formatMcpServersJSON},
	{"qodeCn", [][]string{{".qoder", "settings.json"}}, [][]string{{".qoder-cn", "settings.json"}}, formatMcpServersJSON},
	{"trae", [][]string{{".trae", "settings.json"}}, [][]string{{".trae", "settings.json"}}, formatMcpServersJSON},
	{"kiroCli", [][]string{{".kiro", "settings.json"}}, [][]string{{".kiro", "settings.json"}}, formatMcpServersJSON},
	{"roo", [][]string{{".roo", "settings.json"}}, [][]string{{".roo", "settings.json"}}, formatMcpServersJSON},
	{"codeBuddy", [][]string{{".codebuddy", "settings.json"}}, [][]string{{".codebuddy", "settings.json"}}, formatMcpServersJSON},
	{"agents", [][]string{{".agents", "mcp.json"}}, [][]string{{".agents", "mcp.json"}}, formatMcpServersJSON},
}

// Plugin manifest locations, in probe order.
var (
	nexPluginManifest    = []string{".nex-plugin", "plugin.json"}
	claudePluginManifest = []string{".claude-plugin", "plugin.json"}
	codexPluginManifest  = []string{".codex-plugin", "plugin.json"}
)

// inlinePluginMarketplace is INLINE_PLUGIN_MARKETPLACE.
const inlinePluginMarketplace = "inline"

// layout owns the Nex-side path resolution (the TS module functions). home is
// the user home; workspacePath may be "" (global-only).
type layout struct {
	home          string
	workspacePath string
}

func joinParts(base string, parts []string) string {
	return filepath.Join(append([]string{base}, parts...)...)
}

func (l *layout) workspaceNexSkillRoot() string {
	return filepath.Join(l.workspacePath, ".nex", "skills")
}
func (l *layout) userNexSkillRoot() string {
	return filepath.Join(l.home, ".nex", "skills")
}
func (l *layout) workspaceNexCommandRoot() string {
	return filepath.Join(l.workspacePath, ".nex", "commands")
}
func (l *layout) userNexCommandRoot() string {
	return filepath.Join(l.home, ".nex", "commands")
}
func (l *layout) workspaceNexPluginRoot() string {
	return filepath.Join(l.workspacePath, ".nex", "plugins")
}
func (l *layout) userNexPluginRoot() string {
	return filepath.Join(l.home, ".nex", "plugins")
}
func (l *layout) userNexCliConfigPath() string {
	return filepath.Join(l.home, ".nex", "cli", "config.json")
}
func (l *layout) workspaceNexConfigPath() string {
	return filepath.Join(l.workspacePath, ".nex", "config.json")
}
func (l *layout) claudeUserAgentsFileSourcePath() string {
	return filepath.Join(l.home, ".claude", "CLAUDE.md")
}
func (l *layout) userNexAgentsFilePath() string {
	return filepath.Join(l.home, ".nex", "AGENTS.md")
}

// skillTargetRootForScope / commandTargetRootForScope / pluginTargetRootForScope
// port the resolve*TargetRootForScope family: "global" → user root, else the
// workspace root (nil when no workspace).
func (l *layout) skillTargetRootForScope(scope string) (string, bool) {
	if scope == ScopeGlobal {
		return l.userNexSkillRoot(), true
	}
	if l.workspacePath == "" {
		return "", false
	}
	return l.workspaceNexSkillRoot(), true
}

func (l *layout) commandTargetRootForScope(scope string) (string, bool) {
	if scope == ScopeGlobal {
		return l.userNexCommandRoot(), true
	}
	if l.workspacePath == "" {
		return "", false
	}
	return l.workspaceNexCommandRoot(), true
}

func (l *layout) pluginTargetRootForScope(scope string) (string, bool) {
	if scope == ScopeGlobal {
		return l.userNexPluginRoot(), true
	}
	if l.workspacePath == "" {
		return "", false
	}
	return l.workspaceNexPluginRoot(), true
}

// pluginConfigPathForScope / mcpConfigPathForScope port the config-file
// resolution for the same scopes.
func (l *layout) pluginConfigPathForScope(scope string) (string, bool) {
	if scope == ScopeGlobal {
		return l.userNexCliConfigPath(), true
	}
	if l.workspacePath == "" {
		return "", false
	}
	return l.workspaceNexConfigPath(), true
}

func (l *layout) mcpConfigPathForScope(scope string) (string, bool) {
	return l.pluginConfigPathForScope(scope)
}

// importScope maps the source root scope to the target root scope.
func sourceScopeOf(scope string) string {
	if scope == ScopeWorkspace {
		return ScopeProject
	}
	return ScopeGlobal
}

// nameKey normalization (trim + lowercase) is shared by all categories.
func normalizeNameKey(name string) string {
	return strings.ToLower(strings.TrimSpace(name))
}
