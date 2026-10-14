// discovery.go: candidate collection per category + the discovery builders
// (buildSkillsDiscovery &c.): group by source root, count importable, sort.
package settingssync

import (
	"sort"
)

// skillCandidate is SkillImportCandidate.
type skillCandidate struct {
	agent           Agent
	name            string
	nameKey         string
	version         string
	sourceRootScope string
	sourceRootPath  string
	sourcePath      string // source dir (parent of SKILL.md)
	targetRoot      string
	targetPath      string
	scope           string // "user" | "workspace"
}

// commandCandidate is CommandImportCandidate.
type commandCandidate struct {
	agent              Agent
	argumentHint       string
	description        string
	name               string
	nameKey            string
	sourcePath         string
	sourceRelativePath string
	sourceRootPath     string
	sourceRootScope    string
	targetPath         string
	targetRoot         string
	scope              string
}

// pluginCandidate is PluginImportCandidate.
type pluginCandidate struct {
	agent           Agent
	id              string
	name            string
	sourcePath      string
	sourceRootPath  string
	sourceRootScope string
	targetPath      string
	targetRoot      string
	version         string
}

// mcpCandidate is McpImportCandidate.
type mcpCandidate struct {
	agent           Agent
	name            string
	nameKey         string
	config          map[string]any
	sourceRootScope string
	sourceRootPath  string
	sourcePath      string // "<file>#<name>"
}

// collectSkillImportCandidates ports collectSkillImportCandidates.
func (s *Service) collectSkillImportCandidates(l *layout) []skillCandidate {
	var candidates []skillCandidate
	seenSourcePaths := map[string]bool{}
	for _, source := range skillAgentSources {
		for _, root := range externalSkillRoots(l, source) {
			if !dirExists(root.rootPath) {
				continue
			}
			for _, skillPath := range collectSkillMarkdownPaths(root.rootPath) {
				if seenSourcePaths[skillPath] {
					continue
				}
				seenSourcePaths[skillPath] = true
				sourceDir := parentDir(skillPath)
				var targetRoot string
				if root.scope == ScopeWorkspace {
					if l.workspacePath == "" {
						continue
					}
					targetRoot = l.workspaceNexSkillRoot()
				} else {
					targetRoot = l.userNexSkillRoot()
				}
				metadata := readSkillMetadata(skillPath)
				candidates = append(candidates, skillCandidate{
					agent:           root.agent,
					name:            metadata.Name,
					nameKey:         normalizeNameKey(metadata.Name),
					version:         metadata.Version,
					sourceRootScope: sourceScopeOf(root.scope),
					sourceRootPath:  root.rootPath,
					sourcePath:      sourceDir,
					targetRoot:      targetRoot,
					targetPath:      joinName(targetRoot, baseName(sourceDir)),
					scope:           root.scope,
				})
			}
		}
	}
	return candidates
}

// skillSourceRoot pairs a source root with its scope.
type sourceRoot struct {
	agent    Agent
	rootPath string
	scope    string // "user" | "workspace"
}

func externalSkillRoots(l *layout, source pathSource) []sourceRoot {
	roots := []sourceRoot{{source.Agent, joinParts(l.home, source.GlobalPath), ScopeUser}}
	if l.workspacePath != "" {
		// 全局技能导入不应依赖当前窗口必须打开 workspace。
		roots = append(roots, sourceRoot{source.Agent, joinParts(l.workspacePath, source.ProjectPath), ScopeWorkspace})
	}
	return roots
}

func externalCommandRoots(l *layout, source pathSource) []sourceRoot {
	return externalSkillRoots(l, source)
}

func externalPluginRoots(l *layout, source pathSource) []sourceRoot {
	return externalSkillRoots(l, source)
}

func externalMcpRoots(l *layout, source mcpPathSource) []struct {
	sourceRoot
	format mcpConfigFormat
} {
	var out []struct {
		sourceRoot
		format mcpConfigFormat
	}
	for _, globalFile := range source.GlobalFiles {
		out = append(out, struct {
			sourceRoot
			format mcpConfigFormat
		}{sourceRoot{source.Agent, joinParts(l.home, globalFile), ScopeUser}, source.Format})
	}
	if l.workspacePath != "" {
		for _, projectFile := range source.ProjectFiles {
			out = append(out, struct {
				sourceRoot
				format mcpConfigFormat
			}{sourceRoot{source.Agent, joinParts(l.workspacePath, projectFile), ScopeWorkspace}, source.Format})
		}
	}
	return out
}

// collectCommandImportCandidates ports collectCommandImportCandidates.
func (s *Service) collectCommandImportCandidates(l *layout) []commandCandidate {
	var candidates []commandCandidate
	seenSourcePaths := map[string]bool{}
	for _, source := range commandAgentSources {
		for _, root := range externalCommandRoots(l, source) {
			if !dirExists(root.rootPath) {
				continue
			}
			for _, commandPath := range collectCommandMarkdownPaths(root.rootPath) {
				if seenSourcePaths[commandPath] {
					continue
				}
				seenSourcePaths[commandPath] = true
				var targetRoot string
				if root.scope == ScopeWorkspace {
					if l.workspacePath == "" {
						continue
					}
					targetRoot = l.workspaceNexCommandRoot()
				} else {
					targetRoot = l.userNexCommandRoot()
				}
				metadata := readCommandMetadata(commandPath, root.rootPath)
				sourceRelativePath := relativePath(root.rootPath, commandPath)
				candidates = append(candidates, commandCandidate{
					agent:              root.agent,
					argumentHint:       metadata.ArgumentHint,
					description:        metadata.Description,
					name:               getCommandNameFromPath(root.rootPath, commandPath),
					sourcePath:         commandPath,
					sourceRelativePath: sourceRelativePath,
					sourceRootPath:     root.rootPath,
					sourceRootScope:    sourceScopeOf(root.scope),
					targetRoot:         targetRoot,
					targetPath:         joinName(targetRoot, sourceRelativePath),
					scope:              root.scope,
				})
			}
		}
	}
	return candidates
}

// collectPluginImportCandidates ports collectPluginImportCandidates.
func (s *Service) collectPluginImportCandidates(l *layout) []pluginCandidate {
	var candidates []pluginCandidate
	seenSourcePaths := map[string]bool{}
	for _, source := range pluginAgentSources {
		for _, root := range externalPluginRoots(l, source) {
			if !dirExists(root.rootPath) {
				continue
			}
			for _, pluginPath := range collectPluginPaths(root.rootPath) {
				if seenSourcePaths[pluginPath] {
					continue
				}
				seenSourcePaths[pluginPath] = true
				var targetRoot string
				if root.scope == ScopeWorkspace {
					if l.workspacePath == "" {
						continue
					}
					targetRoot = l.workspaceNexPluginRoot()
				} else {
					targetRoot = l.userNexPluginRoot()
				}
				metadata, ok := readPluginMetadata(pluginPath)
				if !ok {
					continue
				}
				candidates = append(candidates, pluginCandidate{
					agent:           root.agent,
					id:              metadata.ID,
					name:            metadata.Name,
					version:         metadata.Version,
					sourceRootScope: sourceScopeOf(root.scope),
					sourceRootPath:  root.rootPath,
					sourcePath:      pluginPath,
					targetRoot:      targetRoot,
					targetPath:      joinName(targetRoot, baseName(pluginPath)),
				})
			}
		}
	}
	return candidates
}

// collectMcpImportCandidates ports collectMcpImportCandidates.
func (s *Service) collectMcpImportCandidates(l *layout) []mcpCandidate {
	var candidates []mcpCandidate
	seenSourceNames := map[string]bool{}
	for _, source := range mcpAgentSources {
		for _, root := range externalMcpRoots(l, source) {
			if !pathExists(root.rootPath) {
				continue
			}
			serverMap := readMcpServersFromSourceFile(root.rootPath, root.format)
			for _, name := range sortedKeys(serverMap) {
				config := serverMap[name]
				sourcePath := root.rootPath + "#" + name
				seenKey := root.agent + ":" + sourcePath
				if seenSourceNames[seenKey] {
					continue
				}
				seenSourceNames[seenKey] = true
				candidates = append(candidates, mcpCandidate{
					agent:           root.agent,
					name:            name,
					nameKey:         normalizeNameKey(name),
					config:          stripExternalMcpTimeoutFields(config),
					sourceRootScope: sourceScopeOf(root.scope),
					sourceRootPath:  root.rootPath,
					sourcePath:      sourcePath,
				})
			}
		}
	}
	return candidates
}

// stripExternalMcpTimeoutFields ports stripExternalMcpTimeoutFields.
func stripExternalMcpTimeoutFields(config map[string]any) map[string]any {
	if _, hasTimeout := config["timeout"]; !hasTimeout {
		if _, hasStartup := config["startup_timeout_sec"]; !hasStartup {
			return config
		}
	}
	next := copyMapShallow(config)
	delete(next, "timeout")
	delete(next, "startup_timeout_sec")
	return next
}

// sortedKeys returns map keys sorted (deterministic iteration).
func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// sourceRootKey groups candidates: "<scope>:<path>".
func sourceRootKey(scope, path string) string { return scope + ":" + path }

func sortSummaries(summaries []*SourceRootSummary) {
	sort.Slice(summaries, func(i, j int) bool {
		if summaries[i].Scope != summaries[j].Scope {
			return summaries[i].Scope < summaries[j].Scope
		}
		return summaries[i].Path < summaries[j].Path
	})
}
