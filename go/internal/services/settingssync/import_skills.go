// import_skills.go: buildSkillsDiscovery + importSkillsForAgent.
package settingssync

// buildSkillsDiscovery ports buildSkillsDiscovery.
func (s *Service) buildSkillsDiscovery(l *layout) *DiscoveryResult {
	candidates := s.collectSkillImportCandidates(l)
	var agents []AgentSummary
	for _, agent := range skillAgentSources {
		agentCandidates := filterSkillsByAgent(candidates, agent.Agent)
		discoveredCount := len(agentCandidates)
		sourcePaths := uniqueSortedRootPathsSkills(agentCandidates)
		sourceRoots := buildSkillSourceRootSummaries(agentCandidates)
		importableCount := 0
		existingNameKeys := map[string]map[string]bool{}
		for _, candidate := range agentCandidates {
			if _, skip := skillSkipReason(candidate, existingNameKeys); !skip {
				importableCount++
			}
		}
		agents = append(agents, AgentSummary{
			Agent:      agent.Agent,
			Discovered: discoveredCount > 0,
			Categories: []CategorySummary{{
				Category:          CategorySkills,
				DiscoveredCount:   discoveredCount,
				ImportableCount:   importableCount,
				SkippedCount:      intPtr(discoveredCount - importableCount),
				SourcePaths:       sourcePaths,
				SourceRoots:       sourceRoots,
				SelectedByDefault: importableCount > 0,
			}},
		})
	}
	return &DiscoveryResult{Agents: filterDiscovered(agents)}
}

// buildSkillSourceRootSummaries ports buildSourceRootSummaries.
func buildSkillSourceRootSummaries(candidates []skillCandidate) []*SourceRootSummary {
	existingNameKeys := map[string]map[string]bool{}
	grouped := groupSkillsByRoot(candidates)
	var summaries []*SourceRootSummary
	for _, group := range grouped {
		first := group[0]
		importableCount := 0
		skills := make([]SkillSummary, 0, len(group))
		for _, candidate := range group {
			skipReason, skipped := skillSkipReason(candidate, existingNameKeys)
			importable := !skipped
			if importable {
				importableCount++
			}
			skills = append(skills, SkillSummary{
				Name:       candidate.name,
				Path:       candidate.sourcePath,
				Importable: importable,
				SkipReason: skipReason,
				Version:    candidate.version,
			})
		}
		sortSkillSummaries(skills)
		skipped := len(group) - importableCount
		summaries = append(summaries, &SourceRootSummary{
			Scope:           first.sourceRootScope,
			Path:            first.sourceRootPath,
			DiscoveredCount: len(group),
			ImportableCount: importableCount,
			SkippedCount:    &skipped,
			Skills:          skills,
		})
	}
	sortSummaries(summaries)
	return summaries
}

func groupSkillsByRoot(candidates []skillCandidate) [][]skillCandidate {
	order := []string{}
	groups := map[string][]skillCandidate{}
	for _, candidate := range candidates {
		key := sourceRootKey(candidate.sourceRootScope, candidate.sourceRootPath)
		if _, exists := groups[key]; !exists {
			order = append(order, key)
		}
		groups[key] = append(groups[key], candidate)
	}
	// Map iteration order is random; the TS preserves candidate order. Use the
	// candidate order of first appearance.
	out := make([][]skillCandidate, 0, len(order))
	for _, key := range order {
		out = append(out, groups[key])
	}
	return out
}

func filterSkillsByAgent(candidates []skillCandidate, agent Agent) []skillCandidate {
	var out []skillCandidate
	for _, candidate := range candidates {
		if candidate.agent == agent {
			out = append(out, candidate)
		}
	}
	return out
}

func uniqueSortedRootPathsSkills(candidates []skillCandidate) []string {
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

func sortSkillSummaries(skills []SkillSummary) {
	sortSlice(skills, func(a, b SkillSummary) bool { return a.Name < b.Name })
}

func filterDiscovered(agents []AgentSummary) []AgentSummary {
	out := agents[:0]
	for _, agent := range agents {
		if agent.Discovered {
			out = append(out, agent)
		}
	}
	return out
}
