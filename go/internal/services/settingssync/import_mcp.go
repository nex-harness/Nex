// import_mcp.go: buildMcpDiscovery + importMcpServersForAgent + the nex
// cli-config MCP server registration (addMcpServerToNexConfig).
package settingssync

// buildMcpDiscovery ports buildMcpDiscovery.
func (s *Service) buildMcpDiscovery(l *layout) *DiscoveryResult {
	candidates := s.collectMcpImportCandidates(l)
	var agents []AgentSummary
	for _, agent := range mcpAgentSources {
		agentCandidates := filterMcpByAgent(candidates, agent.Agent)
		discoveredCount := len(agentCandidates)
		importableCount := 0
		existingNameKeys := map[string]map[string]bool{}
		for _, candidate := range agentCandidates {
			if _, skip := mcpSkipReason(candidate, existingNameKeys, l, candidate.sourceRootScope); !skip {
				importableCount++
			}
		}
		agents = append(agents, AgentSummary{
			Agent:      agent.Agent,
			Discovered: discoveredCount > 0,
			Categories: []CategorySummary{{
				Category:          CategoryMcpServers,
				DiscoveredCount:   discoveredCount,
				ImportableCount:   importableCount,
				SkippedCount:      intPtr(discoveredCount - importableCount),
				SourcePaths:       uniqueSortedRootPathsMcp(agentCandidates),
				SourceRoots:       buildMcpSourceRootSummaries(agentCandidates, l),
				SelectedByDefault: false,
			}},
		})
	}
	return &DiscoveryResult{Agents: filterDiscovered(agents)}
}

func buildMcpSourceRootSummaries(candidates []mcpCandidate, l *layout) []*SourceRootSummary {
	existingNameKeys := map[string]map[string]bool{}
	grouped := groupMcpByRoot(candidates)
	var summaries []*SourceRootSummary
	for _, group := range grouped {
		first := group[0]
		importableCount := 0
		servers := make([]McpServerSummary, 0, len(group))
		for _, candidate := range group {
			skipReason, skipped := mcpSkipReason(candidate, existingNameKeys, l, candidate.sourceRootScope)
			importable := !skipped
			if importable {
				importableCount++
			}
			servers = append(servers, McpServerSummary{
				Name:       candidate.name,
				Path:       candidate.sourcePath,
				Importable: importable,
				SkipReason: skipReason,
			})
		}
		sortMcpSummaries(servers)
		skipped := len(group) - importableCount
		summaries = append(summaries, &SourceRootSummary{
			Scope:           first.sourceRootScope,
			Path:            first.sourceRootPath,
			DiscoveredCount: len(group),
			ImportableCount: importableCount,
			SkippedCount:    &skipped,
			McpServers:      servers,
		})
	}
	sortSummaries(summaries)
	return summaries
}

func groupMcpByRoot(candidates []mcpCandidate) [][]mcpCandidate {
	order := []string{}
	groups := map[string][]mcpCandidate{}
	for _, candidate := range candidates {
		key := sourceRootKey(candidate.sourceRootScope, candidate.sourceRootPath)
		if _, exists := groups[key]; !exists {
			order = append(order, key)
		}
		groups[key] = append(groups[key], candidate)
	}
	out := make([][]mcpCandidate, 0, len(order))
	for _, key := range order {
		out = append(out, groups[key])
	}
	return out
}

func filterMcpByAgent(candidates []mcpCandidate, agent Agent) []mcpCandidate {
	var out []mcpCandidate
	for _, candidate := range candidates {
		if candidate.agent == agent {
			out = append(out, candidate)
		}
	}
	return out
}

func uniqueSortedRootPathsMcp(candidates []mcpCandidate) []string {
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

func sortMcpSummaries(servers []McpServerSummary) {
	sortSlice(servers, func(a, b McpServerSummary) bool { return a.Name < b.Name })
}

// readNexMcpServers ports readNexMcpServers: parsed.mcp.servers.
func readNexMcpServers(parsed map[string]any) mcpServersMap {
	mcp, ok := parsed["mcp"].(map[string]any)
	if !ok {
		return mcpServersMap{}
	}
	return normalizeMcpServerMap(mcp["servers"])
}

// addMcpServerToNexConfig ports addMcpServerToNexConfig: merge the server
// into mcp.servers, preserving the other mcp fields.
func addMcpServerToNexConfig(filePath, name string, config map[string]any) error {
	parsed := readJSONFileOrEmpty(filePath)
	currentMcp, _ := parsed["mcp"].(map[string]any)
	if currentMcp == nil {
		currentMcp = map[string]any{}
	}
	servers := readNexMcpServers(parsed)
	nextServers := map[string]any{}
	for _, serverName := range sortedKeys(servers) {
		nextServers[serverName] = servers[serverName]
	}
	nextServers[name] = config
	currentMcp["servers"] = nextServers
	parsed["mcp"] = currentMcp
	return writeJSONFile(filePath, parsed)
}
