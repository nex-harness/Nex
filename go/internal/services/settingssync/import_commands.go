// import_commands.go: buildCommandsDiscovery + importCommandsForAgent.
package settingssync

// buildCommandsDiscovery ports buildCommandsDiscovery.
func (s *Service) buildCommandsDiscovery(l *layout) *DiscoveryResult {
	candidates := s.collectCommandImportCandidates(l)
	var agents []AgentSummary
	for _, agent := range commandAgentSources {
		agentCandidates := filterCommandsByAgent(candidates, agent.Agent)
		discoveredCount := len(agentCandidates)
		importableCount := 0
		existingNameKeys := map[string]map[string]bool{}
		for _, candidate := range agentCandidates {
			if _, skip := commandSkipReason(candidate, existingNameKeys); !skip {
				importableCount++
			}
		}
		agents = append(agents, AgentSummary{
			Agent:      agent.Agent,
			Discovered: discoveredCount > 0,
			Categories: []CategorySummary{{
				Category:          CategoryCommands,
				DiscoveredCount:   discoveredCount,
				ImportableCount:   importableCount,
				SkippedCount:      intPtr(discoveredCount - importableCount),
				SourcePaths:       uniqueSortedRootPathsCommands(agentCandidates),
				SourceRoots:       buildCommandSourceRootSummaries(agentCandidates),
				SelectedByDefault: false,
			}},
		})
	}
	return &DiscoveryResult{Agents: filterDiscovered(agents)}
}

func buildCommandSourceRootSummaries(candidates []commandCandidate) []*SourceRootSummary {
	existingNameKeys := map[string]map[string]bool{}
	grouped := groupCommandsByRoot(candidates)
	var summaries []*SourceRootSummary
	for _, group := range grouped {
		first := group[0]
		importableCount := 0
		commands := make([]CommandSummary, 0, len(group))
		for _, candidate := range group {
			skipReason, skipped := commandSkipReason(candidate, existingNameKeys)
			importable := !skipped
			if importable {
				importableCount++
			}
			commands = append(commands, CommandSummary{
				Name:         candidate.name,
				Path:         candidate.sourcePath,
				Importable:   importable,
				SkipReason:   skipReason,
				Description:  candidate.description,
				ArgumentHint: candidate.argumentHint,
			})
		}
		sortCommandSummaries(commands)
		skipped := len(group) - importableCount
		summaries = append(summaries, &SourceRootSummary{
			Scope:           first.sourceRootScope,
			Path:            first.sourceRootPath,
			DiscoveredCount: len(group),
			ImportableCount: importableCount,
			SkippedCount:    &skipped,
			Commands:        commands,
		})
	}
	sortSummaries(summaries)
	return summaries
}

func groupCommandsByRoot(candidates []commandCandidate) [][]commandCandidate {
	order := []string{}
	groups := map[string][]commandCandidate{}
	for _, candidate := range candidates {
		key := sourceRootKey(candidate.sourceRootScope, candidate.sourceRootPath)
		if _, exists := groups[key]; !exists {
			order = append(order, key)
		}
		groups[key] = append(groups[key], candidate)
	}
	out := make([][]commandCandidate, 0, len(order))
	for _, key := range order {
		out = append(out, groups[key])
	}
	return out
}

func filterCommandsByAgent(candidates []commandCandidate, agent Agent) []commandCandidate {
	var out []commandCandidate
	for _, candidate := range candidates {
		if candidate.agent == agent {
			out = append(out, candidate)
		}
	}
	return out
}

func uniqueSortedRootPathsCommands(candidates []commandCandidate) []string {
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

func sortCommandSummaries(commands []CommandSummary) {
	sortSlice(commands, func(a, b CommandSummary) bool { return a.Name < b.Name })
}
