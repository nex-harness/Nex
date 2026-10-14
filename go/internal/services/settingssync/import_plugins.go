// import_plugins.go: buildPluginsDiscovery + importPluginsForAgent and the
// cli-config plugin dir registration (addPluginDirToConfig).
package settingssync

import (
	"os"
	"path/filepath"
)

// buildPluginsDiscovery ports buildPluginsDiscovery.
func (s *Service) buildPluginsDiscovery(l *layout) *DiscoveryResult {
	candidates := s.collectPluginImportCandidates(l)
	var agents []AgentSummary
	for _, agent := range pluginAgentSources {
		agentCandidates := filterPluginsByAgent(candidates, agent.Agent)
		discoveredCount := len(agentCandidates)
		importableCount := 0
		existingPluginIDs := map[string]map[string]bool{}
		for _, candidate := range agentCandidates {
			if _, skip := pluginSkipReason(candidate, existingPluginIDs, l, candidate.sourceRootScope); !skip {
				importableCount++
			}
		}
		agents = append(agents, AgentSummary{
			Agent:      agent.Agent,
			Discovered: discoveredCount > 0,
			Categories: []CategorySummary{{
				Category:          CategoryPlugins,
				DiscoveredCount:   discoveredCount,
				ImportableCount:   importableCount,
				SkippedCount:      intPtr(discoveredCount - importableCount),
				SourcePaths:       uniqueSortedRootPathsPlugins(agentCandidates),
				SourceRoots:       buildPluginSourceRootSummaries(agentCandidates, l),
				SelectedByDefault: false,
			}},
		})
	}
	return &DiscoveryResult{Agents: filterDiscovered(agents)}
}

func buildPluginSourceRootSummaries(candidates []pluginCandidate, l *layout) []*SourceRootSummary {
	existingPluginIDs := map[string]map[string]bool{}
	grouped := groupPluginsByRoot(candidates)
	var summaries []*SourceRootSummary
	for _, group := range grouped {
		first := group[0]
		importableCount := 0
		plugins := make([]PluginSummary, 0, len(group))
		for _, candidate := range group {
			skipReason, skipped := pluginSkipReason(candidate, existingPluginIDs, l, candidate.sourceRootScope)
			importable := !skipped
			if importable {
				importableCount++
			}
			plugins = append(plugins, PluginSummary{
				Name:       candidate.name,
				Path:       candidate.sourcePath,
				Importable: importable,
				SkipReason: skipReason,
				Version:    candidate.version,
			})
		}
		sortPluginSummaries(plugins)
		skipped := len(group) - importableCount
		summaries = append(summaries, &SourceRootSummary{
			Scope:           first.sourceRootScope,
			Path:            first.sourceRootPath,
			DiscoveredCount: len(group),
			ImportableCount: importableCount,
			SkippedCount:    &skipped,
			Plugins:         plugins,
		})
	}
	sortSummaries(summaries)
	return summaries
}

func groupPluginsByRoot(candidates []pluginCandidate) [][]pluginCandidate {
	order := []string{}
	groups := map[string][]pluginCandidate{}
	for _, candidate := range candidates {
		key := sourceRootKey(candidate.sourceRootScope, candidate.sourceRootPath)
		if _, exists := groups[key]; !exists {
			order = append(order, key)
		}
		groups[key] = append(groups[key], candidate)
	}
	out := make([][]pluginCandidate, 0, len(order))
	for _, key := range order {
		out = append(out, groups[key])
	}
	return out
}

func filterPluginsByAgent(candidates []pluginCandidate, agent Agent) []pluginCandidate {
	var out []pluginCandidate
	for _, candidate := range candidates {
		if candidate.agent == agent {
			out = append(out, candidate)
		}
	}
	return out
}

func uniqueSortedRootPathsPlugins(candidates []pluginCandidate) []string {
	seen := map[string]bool{}
	for _, candidate := range candidates {
		seen[candidate.sourceRootPath] = true
	}
	out := make([]string, 0, len(seen))
	for path := range seen {
		out = append(out, path)
	}
	sortLocale(out)
	return out
}

func sortPluginSummaries(plugins []PluginSummary) {
	sortSlice(plugins, func(a, b PluginSummary) bool { return a.Name < b.Name })
}

// collectConfiguredPluginIDs ports collectConfiguredPluginIds: read the
// config's plugins.dirs and map each dir to its plugin id (lowercased).
func collectConfiguredPluginIDs(configPath string) map[string]bool {
	parsed := readJSONFileOrEmpty(configPath)
	ids := map[string]bool{}
	plugins, _ := parsed["plugins"].(map[string]any)
	dirs := readStringArray(plugins["dirs"])
	for _, pluginPath := range dirs {
		if metadata, ok := readPluginMetadata(absPath(pluginPath)); ok {
			ids[normalizeNameKey(metadata.ID)] = true
		}
	}
	return ids
}

func readStringArray(value any) []string {
	arr, ok := value.([]any)
	if !ok {
		return nil
	}
	var out []string
	for _, item := range arr {
		if s, ok := item.(string); ok {
			out = append(out, s)
		}
	}
	return out
}

func absPath(p string) string {
	absolute, err := filepath.Abs(p)
	if err != nil {
		return p
	}
	return absolute
}

// addPluginDirToConfig ports addPluginDirToConfig: append the resolved plugin
// dir to plugins.dirs when not already present (resolved comparison).
func addPluginDirToConfig(filePath, pluginPath string) error {
	parsed := readJSONFileOrEmpty(filePath)
	plugins, _ := parsed["plugins"].(map[string]any)
	if plugins == nil {
		plugins = map[string]any{}
	}
	dirs := readStringArray(plugins["dirs"])
	resolvedPluginPath := absPath(pluginPath)
	for _, item := range dirs {
		if absPath(item) == resolvedPluginPath {
			return nil
		}
	}
	nextDirs := append(append([]string{}, dirs...), resolvedPluginPath)
	plugins["dirs"] = nextDirs
	parsed["plugins"] = plugins
	return writeJSONFile(filePath, parsed)
}

// writeJSONFile ports writeJsonFile: mkdir + 2-space JSON with trailing
// newline (NOT atomic in the TS; kept non-atomic for byte parity).
func writeJSONFile(filePath string, value map[string]any) error {
	if err := os.MkdirAll(parentDir(filePath), 0o777); err != nil {
		return err
	}
	content := renderJSONTwoSpace(value) + "\n"
	return os.WriteFile(filePath, []byte(content), 0o644)
}
