// import_exec.go: importSelected + the four category executors (copy/symlink
// import with skip accounting) + the Claude AGENTS.md migration pair.
package settingssync

import (
	"context"
)

// Selection is SettingsSyncSelection.
type Selection struct {
	Agent          Agent    `json:"agent"`
	Category       string   `json:"category"`
	SourceScope    string   `json:"sourceScope,omitempty"`
	TargetScope    string   `json:"targetScope,omitempty"`
	ImportMode     string   `json:"importMode,omitempty"` // "copy" | "symlink"
	SkillPaths     []string `json:"skillPaths,omitempty"`
	CommandPaths   []string `json:"commandPaths,omitempty"`
	PluginPaths    []string `json:"pluginPaths,omitempty"`
	McpServerPaths []string `json:"mcpServerPaths,omitempty"`
}

// ImportStatus values for per-item results.
const (
	statusImported = "imported"
	statusSkipped  = "skipped"
	statusFailed   = "failed"
)

// SkillImportResult is SettingsSyncSkillImportResult.
type SkillImportResult struct {
	Name        string `json:"name"`
	Path        string `json:"path"`
	SourceScope string `json:"sourceScope"`
	Status      string `json:"status"`
	SkipReason  string `json:"skipReason,omitempty"`
	Version     string `json:"version,omitempty"`
}

// CommandImportResult is SettingsSyncCommandImportResult.
type CommandImportResult struct {
	Name        string `json:"name"`
	Path        string `json:"path"`
	SourceScope string `json:"sourceScope"`
	Status      string `json:"status"`
	SkipReason  string `json:"skipReason,omitempty"`
}

// PluginImportResult is SettingsSyncPluginImportResult.
type PluginImportResult struct {
	Name        string `json:"name"`
	Path        string `json:"path"`
	SourceScope string `json:"sourceScope"`
	Status      string `json:"status"`
	SkipReason  string `json:"skipReason,omitempty"`
	Version     string `json:"version,omitempty"`
}

// McpImportResult is SettingsSyncMcpServerImportResult.
type McpImportResult struct {
	Name        string `json:"name"`
	Path        string `json:"path"`
	SourceScope string `json:"sourceScope"`
	Status      string `json:"status"`
	SkipReason  string `json:"skipReason,omitempty"`
}

// TaskImportResult is SettingsSyncTaskImportResult.
type TaskImportResult struct {
	Agent          string                `json:"agent"`
	Category       string                `json:"category"`
	Status         string                `json:"status"` // success | skipped | failed
	ImportedCount  int                   `json:"importedCount"`
	SkippedCount   int                   `json:"skippedCount"`
	FailedCount    int                   `json:"failedCount"`
	SkillResults   []SkillImportResult   `json:"skillResults,omitempty"`
	CommandResults []CommandImportResult `json:"commandResults,omitempty"`
	PluginResults  []PluginImportResult  `json:"pluginResults,omitempty"`
	McpResults     []McpImportResult     `json:"mcpServerResults,omitempty"`
}

// ImportResult is SettingsSyncImportResult.
type ImportResult struct {
	SuccessCount int                `json:"successCount"`
	SkippedCount int                `json:"skippedCount"`
	FailedCount  int                `json:"failedCount"`
	TaskResults  []TaskImportResult `json:"taskResults"`
}

// ImportSelectedRequest is the importSelected request.
type ImportSelectedRequest struct {
	WorkspaceRef
	Selections []Selection
}

// ImportSelected ports importSelected.
func (s *Service) ImportSelected(ctx context.Context, request ImportSelectedRequest) (*ImportResult, error) {
	l := s.layoutFor(request.WorkspacePath)
	var taskResults []TaskImportResult
	for _, selection := range request.Selections {
		switch selection.Category {
		case CategorySkills:
			taskResults = append(taskResults, s.importSkillsForAgent(l, selection))
		case CategoryCommands:
			taskResults = append(taskResults, s.importCommandsForAgent(l, selection))
		case CategoryPlugins:
			taskResults = append(taskResults, s.importPluginsForAgent(l, selection))
		case CategoryMcpServers:
			taskResults = append(taskResults, s.importMcpServersForAgent(l, selection))
		default:
			taskResults = append(taskResults, TaskImportResult{
				Agent: selection.Agent, Category: selection.Category,
				Status: statusSkipped, SkippedCount: 1,
			})
		}
	}
	result := &ImportResult{TaskResults: taskResults}
	for _, task := range taskResults {
		result.SuccessCount += task.ImportedCount
		result.SkippedCount += task.SkippedCount
		result.FailedCount += task.FailedCount
	}
	return result, nil
}

// taskStatus ports the status derivation.
func taskStatus(failedCount, importedCount int) string {
	switch {
	case failedCount > 0:
		return "failed"
	case importedCount > 0:
		return "success"
	default:
		return "skipped"
	}
}

// importSkillsForAgent ports importSkillsForAgent.
func (s *Service) importSkillsForAgent(l *layout, selection Selection) TaskImportResult {
	task := TaskImportResult{Agent: selection.Agent, Category: CategorySkills}
	selected := toSet(selection.SkillPaths)
	existingNameKeys := map[string]map[string]bool{}
	for _, candidate := range s.collectSkillImportCandidates(l) {
		if candidate.agent != selection.Agent {
			continue
		}
		if selection.SourceScope != "" && candidate.sourceRootScope != selection.SourceScope {
			continue
		}
		if selected != nil && !selected[candidate.sourcePath] {
			continue
		}
		targetRoot, rootOK := l.skillTargetRootForScope(orDefault(selection.TargetScope, targetScopeOf(candidate)))
		if !rootOK {
			task.SkippedCount++
			task.SkillResults = append(task.SkillResults, skippedSkill(candidate, skipTargetExists))
			continue
		}
		targetCandidate := candidate
		targetCandidate.targetRoot = targetRoot
		targetCandidate.targetPath = joinName(targetRoot, baseName(candidate.sourcePath))
		skipReason, skipped := skillSkipReason(targetCandidate, existingNameKeys)
		if skipped {
			task.SkippedCount++
			task.SkillResults = append(task.SkillResults, skippedSkill(targetCandidate, skipReason))
			continue
		}
		if err := mkdirAll(parentDir(targetCandidate.targetPath)); err != nil {
			task.FailedCount++
			task.SkillResults = append(task.SkillResults, failedSkill(targetCandidate))
			continue
		}
		if err := importSkillDirectory(candidate.sourcePath, targetCandidate.targetPath, selection.ImportMode); err != nil {
			task.FailedCount++
			task.SkillResults = append(task.SkillResults, failedSkill(targetCandidate))
			continue
		}
		task.ImportedCount++
		if keys, cached := existingNameKeys[targetCandidate.targetRoot]; cached {
			keys[candidate.nameKey] = true
		}
		task.SkillResults = append(task.SkillResults, importedSkill(targetCandidate))
	}
	task.Status = taskStatus(task.FailedCount, task.ImportedCount)
	return task
}

func skippedSkill(c skillCandidate, reason string) SkillImportResult {
	return SkillImportResult{Name: c.name, Path: c.sourcePath, SourceScope: c.sourceRootScope, Status: statusSkipped, SkipReason: reason, Version: c.version}
}
func failedSkill(c skillCandidate) SkillImportResult {
	return SkillImportResult{Name: c.name, Path: c.sourcePath, SourceScope: c.sourceRootScope, Status: statusFailed, Version: c.version}
}
func importedSkill(c skillCandidate) SkillImportResult {
	return SkillImportResult{Name: c.name, Path: c.sourcePath, SourceScope: c.sourceRootScope, Status: statusImported, Version: c.version}
}

// importCommandsForAgent ports importCommandsForAgent.
func (s *Service) importCommandsForAgent(l *layout, selection Selection) TaskImportResult {
	task := TaskImportResult{Agent: selection.Agent, Category: CategoryCommands}
	selected := toSet(selection.CommandPaths)
	existingNameKeys := map[string]map[string]bool{}
	for _, candidate := range s.collectCommandImportCandidates(l) {
		if candidate.agent != selection.Agent {
			continue
		}
		if selection.SourceScope != "" && candidate.sourceRootScope != selection.SourceScope {
			continue
		}
		if selected != nil && !selected[candidate.sourcePath] {
			continue
		}
		targetRoot, rootOK := l.commandTargetRootForScope(orDefault(selection.TargetScope, targetScopeOfCommand(candidate)))
		if !rootOK {
			task.SkippedCount++
			task.CommandResults = append(task.CommandResults, skippedCommand(candidate, skipTargetExists))
			continue
		}
		targetCandidate := candidate
		targetCandidate.targetRoot = targetRoot
		targetCandidate.targetPath = joinName(targetRoot, candidate.sourceRelativePath)
		skipReason, skipped := commandSkipReason(targetCandidate, existingNameKeys)
		if skipped {
			task.SkippedCount++
			task.CommandResults = append(task.CommandResults, skippedCommand(targetCandidate, skipReason))
			continue
		}
		if err := mkdirAll(parentDir(targetCandidate.targetPath)); err != nil {
			task.FailedCount++
			task.CommandResults = append(task.CommandResults, failedCommand(targetCandidate))
			continue
		}
		if err := importCommandFile(candidate.sourcePath, targetCandidate.targetPath, selection.ImportMode); err != nil {
			task.FailedCount++
			task.CommandResults = append(task.CommandResults, failedCommand(targetCandidate))
			continue
		}
		task.ImportedCount++
		if keys, cached := existingNameKeys[targetCandidate.targetRoot]; cached {
			keys[normalizeNameKey(candidate.name)] = true
		}
		task.CommandResults = append(task.CommandResults, importedCommand(targetCandidate))
	}
	task.Status = taskStatus(task.FailedCount, task.ImportedCount)
	return task
}

func skippedCommand(c commandCandidate, reason string) CommandImportResult {
	return CommandImportResult{Name: c.name, Path: c.sourcePath, SourceScope: c.sourceRootScope, Status: statusSkipped, SkipReason: reason}
}
func failedCommand(c commandCandidate) CommandImportResult {
	return CommandImportResult{Name: c.name, Path: c.sourcePath, SourceScope: c.sourceRootScope, Status: statusFailed}
}
func importedCommand(c commandCandidate) CommandImportResult {
	return CommandImportResult{Name: c.name, Path: c.sourcePath, SourceScope: c.sourceRootScope, Status: statusImported}
}

// importPluginsForAgent ports importPluginsForAgent: copy/symlink the dir AND
// register it in the scoped cli config.
func (s *Service) importPluginsForAgent(l *layout, selection Selection) TaskImportResult {
	task := TaskImportResult{Agent: selection.Agent, Category: CategoryPlugins}
	selected := toSet(selection.PluginPaths)
	existingPluginIDs := map[string]map[string]bool{}
	for _, candidate := range s.collectPluginImportCandidates(l) {
		if candidate.agent != selection.Agent {
			continue
		}
		if selection.SourceScope != "" && candidate.sourceRootScope != selection.SourceScope {
			continue
		}
		if selected != nil && !selected[candidate.sourcePath] {
			continue
		}
		targetScope := orDefault(selection.TargetScope, candidate.sourceRootScope)
		targetRoot, rootOK := l.pluginTargetRootForScope(targetScope)
		configPath, configOK := l.pluginConfigPathForScope(targetScope)
		if !rootOK || !configOK {
			task.SkippedCount++
			task.PluginResults = append(task.PluginResults, skippedPlugin(candidate, skipTargetExists))
			continue
		}
		targetCandidate := candidate
		targetCandidate.targetRoot = targetRoot
		targetCandidate.targetPath = joinName(targetRoot, baseName(candidate.sourcePath))
		skipReason, skipped := pluginSkipReason(targetCandidate, existingPluginIDs, l, targetScope)
		if skipped {
			task.SkippedCount++
			task.PluginResults = append(task.PluginResults, skippedPlugin(targetCandidate, skipReason))
			continue
		}
		if err := mkdirAll(parentDir(targetCandidate.targetPath)); err != nil {
			task.FailedCount++
			task.PluginResults = append(task.PluginResults, failedPlugin(targetCandidate))
			continue
		}
		if err := importPluginDirectory(candidate.sourcePath, targetCandidate.targetPath, selection.ImportMode); err != nil {
			task.FailedCount++
			task.PluginResults = append(task.PluginResults, failedPlugin(targetCandidate))
			continue
		}
		if err := addPluginDirToConfig(configPath, targetCandidate.targetPath); err != nil {
			task.FailedCount++
			task.PluginResults = append(task.PluginResults, failedPlugin(targetCandidate))
			continue
		}
		task.ImportedCount++
		if ids, cached := existingPluginIDs[configPath]; cached {
			ids[normalizeNameKey(candidate.id)] = true
		}
		task.PluginResults = append(task.PluginResults, importedPlugin(targetCandidate))
	}
	task.Status = taskStatus(task.FailedCount, task.ImportedCount)
	return task
}

func skippedPlugin(c pluginCandidate, reason string) PluginImportResult {
	return PluginImportResult{Name: c.name, Path: c.sourcePath, SourceScope: c.sourceRootScope, Status: statusSkipped, SkipReason: reason, Version: c.version}
}
func failedPlugin(c pluginCandidate) PluginImportResult {
	return PluginImportResult{Name: c.name, Path: c.sourcePath, SourceScope: c.sourceRootScope, Status: statusFailed, Version: c.version}
}
func importedPlugin(c pluginCandidate) PluginImportResult {
	return PluginImportResult{Name: c.name, Path: c.sourcePath, SourceScope: c.sourceRootScope, Status: statusImported, Version: c.version}
}

// importMcpServersForAgent ports importMcpServersForAgent.
func (s *Service) importMcpServersForAgent(l *layout, selection Selection) TaskImportResult {
	task := TaskImportResult{Agent: selection.Agent, Category: CategoryMcpServers}
	selected := toSet(selection.McpServerPaths)
	existingNameKeys := map[string]map[string]bool{}
	for _, candidate := range s.collectMcpImportCandidates(l) {
		if candidate.agent != selection.Agent {
			continue
		}
		if selection.SourceScope != "" && candidate.sourceRootScope != selection.SourceScope {
			continue
		}
		if selected != nil && !selected[candidate.sourcePath] {
			continue
		}
		targetScope := orDefault(selection.TargetScope, candidate.sourceRootScope)
		configPath, configOK := l.mcpConfigPathForScope(targetScope)
		if !configOK {
			task.SkippedCount++
			task.McpResults = append(task.McpResults, skippedMcp(candidate, skipSameNameExists))
			continue
		}
		skipReason, skipped := mcpSkipReason(candidate, existingNameKeys, l, targetScope)
		if skipped {
			task.SkippedCount++
			task.McpResults = append(task.McpResults, skippedMcp(candidate, skipReason))
			continue
		}
		if err := addMcpServerToNexConfig(configPath, candidate.name, candidate.config); err != nil {
			task.FailedCount++
			task.McpResults = append(task.McpResults, failedMcp(candidate))
			continue
		}
		task.ImportedCount++
		if keys, cached := existingNameKeys[targetScope]; cached {
			keys[candidate.nameKey] = true
		}
		task.McpResults = append(task.McpResults, importedMcp(candidate))
	}
	task.Status = taskStatus(task.FailedCount, task.ImportedCount)
	return task
}

func skippedMcp(c mcpCandidate, reason string) McpImportResult {
	return McpImportResult{Name: c.name, Path: c.sourcePath, SourceScope: c.sourceRootScope, Status: statusSkipped, SkipReason: reason}
}
func failedMcp(c mcpCandidate) McpImportResult {
	return McpImportResult{Name: c.name, Path: c.sourcePath, SourceScope: c.sourceRootScope, Status: statusFailed}
}
func importedMcp(c mcpCandidate) McpImportResult {
	return McpImportResult{Name: c.name, Path: c.sourcePath, SourceScope: c.sourceRootScope, Status: statusImported}
}
