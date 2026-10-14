// summary.go: the wire-shape summaries (SettingsSyncSourceRootSummary,
// SettingsSyncCategorySummary, SettingsSyncAgentSummary,
// SettingsSyncDiscoveryResult) and the skip-reason computation shared by
// discovery and import.
package settingssync

// SkillSummary is SettingsSyncSourceSkillSummary.
type SkillSummary struct {
	Name       string `json:"name"`
	Path       string `json:"path"`
	Importable bool   `json:"importable"`
	SkipReason string `json:"skipReason,omitempty"`
	Version    string `json:"version,omitempty"`
}

// CommandSummary is SettingsSyncSourceCommandSummary.
type CommandSummary struct {
	Name         string `json:"name"`
	Path         string `json:"path"`
	Importable   bool   `json:"importable"`
	SkipReason   string `json:"skipReason,omitempty"`
	Description  string `json:"description,omitempty"`
	ArgumentHint string `json:"argumentHint,omitempty"`
}

// PluginSummary is SettingsSyncSourcePluginSummary.
type PluginSummary struct {
	Name       string `json:"name"`
	Path       string `json:"path"`
	Importable bool   `json:"importable"`
	SkipReason string `json:"skipReason,omitempty"`
	Version    string `json:"version,omitempty"`
}

// McpServerSummary is SettingsSyncSourceMcpServerSummary.
type McpServerSummary struct {
	Name       string `json:"name"`
	Path       string `json:"path"`
	Importable bool   `json:"importable"`
	SkipReason string `json:"skipReason,omitempty"`
}

// SourceRootSummary is SettingsSyncSourceRootSummary.
type SourceRootSummary struct {
	Scope           string               `json:"scope"`
	Path            string               `json:"path"`
	DiscoveredCount int                  `json:"discoveredCount"`
	ImportableCount int                  `json:"importableCount"`
	SkippedCount    *int                 `json:"skippedCount,omitempty"`
	SourcePaths     []string             `json:"sourcePaths,omitempty"`
	SourceRoots     []*SourceRootSummary `json:"sourceRoots,omitempty"`
	Skills          []SkillSummary       `json:"skills,omitempty"`
	Commands        []CommandSummary     `json:"commands,omitempty"`
	Plugins         []PluginSummary      `json:"plugins,omitempty"`
	McpServers      []McpServerSummary   `json:"mcpServers,omitempty"`
}

// CategorySummary is SettingsSyncCategorySummary.
type CategorySummary struct {
	Category          string               `json:"category"`
	DiscoveredCount   int                  `json:"discoveredCount"`
	ImportableCount   int                  `json:"importableCount"`
	SkippedCount      *int                 `json:"skippedCount,omitempty"`
	SourcePaths       []string             `json:"sourcePaths,omitempty"`
	SourceRoots       []*SourceRootSummary `json:"sourceRoots,omitempty"`
	SelectedByDefault bool                 `json:"selectedByDefault"`
}

// AgentSummary is SettingsSyncAgentSummary.
type AgentSummary struct {
	Agent      Agent             `json:"agent"`
	Discovered bool              `json:"discovered"`
	Categories []CategorySummary `json:"categories"`
}

// DiscoveryResult is SettingsSyncDiscoveryResult.
type DiscoveryResult struct {
	Agents []AgentSummary `json:"agents"`
}

// skipReason is the union of SettingsSync*SkipReason.
const (
	skipTargetExists   = "targetExists"
	skipSameNameExists = "sameNameExists"
)

// skillSkipReason ports getSkillImportCandidateSkipReason: target path first,
// then name-key collisions within the target root (cached per root).
func skillSkipReason(c skillCandidate, existingNameKeys map[string]map[string]bool) (string, bool) {
	if dirExists(c.targetPath) {
		return skipTargetExists, true
	}
	keys, ok := existingNameKeys[c.targetRoot]
	if !ok {
		keys = collectExistingSkillNameKeys(c.targetRoot)
		existingNameKeys[c.targetRoot] = keys
	}
	if keys[c.nameKey] {
		return skipSameNameExists, true
	}
	return "", false
}

func collectExistingSkillNameKeys(rootPath string) map[string]bool {
	nameKeys := map[string]bool{}
	if !dirExists(rootPath) {
		return nameKeys
	}
	for _, skillPath := range collectSkillMarkdownPaths(rootPath) {
		nameKeys[normalizeNameKey(readSkillMetadata(skillPath).Name)] = true
	}
	return nameKeys
}

// commandSkipReason ports getCommandImportCandidateSkipReason (pathExists for
// the target path — a command is a FILE).
func commandSkipReason(c commandCandidate, existingNameKeys map[string]map[string]bool) (string, bool) {
	if pathExists(c.targetPath) {
		return skipTargetExists, true
	}
	keys, ok := existingNameKeys[c.targetRoot]
	if !ok {
		keys = collectExistingCommandNameKeys(c.targetRoot)
		existingNameKeys[c.targetRoot] = keys
	}
	if keys[normalizeNameKey(c.name)] {
		return skipSameNameExists, true
	}
	return "", false
}

func collectExistingCommandNameKeys(rootPath string) map[string]bool {
	nameKeys := map[string]bool{}
	if !dirExists(rootPath) {
		return nameKeys
	}
	for _, commandPath := range collectCommandMarkdownPaths(rootPath) {
		nameKeys[normalizeNameKey(getCommandNameFromPath(rootPath, commandPath))] = true
	}
	return nameKeys
}

// pluginSkipReason ports getPluginImportCandidateSkipReason: no workspace for
// the target scope ALSO reports targetExists (the TS's null-root mapping).
func pluginSkipReason(c pluginCandidate, existingPluginIDs map[string]map[string]bool, l *layout, targetScope string) (string, bool) {
	if pathExists(c.targetPath) {
		return skipTargetExists, true
	}
	configPath, ok := l.pluginConfigPathForScope(targetScope)
	if !ok {
		return skipTargetExists, true
	}
	ids, cached := existingPluginIDs[configPath]
	if !cached {
		ids = collectConfiguredPluginIDs(configPath)
		existingPluginIDs[configPath] = ids
	}
	if ids[normalizeNameKey(c.id)] {
		return skipSameNameExists, true
	}
	return "", false
}

// mcpSkipReason ports getMcpImportCandidateSkipReason: only same-name within
// the target scope's config; no config path → sameNameExists (TS mapping).
func mcpSkipReason(c mcpCandidate, existingNameKeys map[string]map[string]bool, l *layout, targetScope string) (string, bool) {
	configPath, ok := l.mcpConfigPathForScope(targetScope)
	if !ok {
		return skipSameNameExists, true
	}
	keys, cached := existingNameKeys[targetScope]
	if !cached {
		keys = collectExistingMcpServerNameKeys(configPath)
		existingNameKeys[targetScope] = keys
	}
	if keys[c.nameKey] {
		return skipSameNameExists, true
	}
	return "", false
}

func collectExistingMcpServerNameKeys(configPath string) map[string]bool {
	nameKeys := map[string]bool{}
	parsed := readJSONFileOrEmpty(configPath)
	if servers, ok := parsed["mcp"].(map[string]any); ok {
		if serverMap, ok := servers["servers"].(map[string]any); ok {
			for name := range serverMap {
				if name != "" {
					nameKeys[normalizeNameKey(name)] = true
				}
			}
		}
	}
	return nameKeys
}

// readJSONFileOrEmpty ports readJsonFileOrEmpty.
func readJSONFileOrEmpty(filePath string) map[string]any {
	data, err := readFileOrEmpty(filePath)
	if err != nil {
		return map[string]any{}
	}
	var parsed any
	if err := unmarshalJSON(data, &parsed); err != nil {
		return map[string]any{}
	}
	if obj, ok := parsed.(map[string]any); ok {
		return obj
	}
	return map[string]any{}
}
