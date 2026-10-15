// settingsmodel.go: the settings-model half of IHooksService — saveHooks
// (user/project config split through the atomic writer) and the snapshot
// builder feeding loadHooks (workspaceHookSettingsModel.ts +
// buildWorkspaceHookBundleSnapshot).
package hooktrust

import (
	"context"
	"path/filepath"
)

// SaveHooks ports saveHooksImpl: user-level hooks (editable, no location or
// nex/user location) go to the user config; project hooks whose source path
// is the current project config go to the project config.
func (s *Service) SaveHooks(ctx context.Context, workspacePath, workspaceIdentity string, hooks []Hook) error {
	currentProjectConfigPath := filepath.Join(workspacePath, ".nex", "config.json")
	if absolute, err := filepath.Abs(currentProjectConfigPath); err == nil {
		currentProjectConfigPath = absolute
	}
	var userHooks, projectHooks []Hook
	for _, hook := range hooks {
		if hook.Editable == false {
			continue
		}
		sourceMatchesProject := hook.Location.Source == "nex" &&
			hook.Location.Scope == "project" &&
			(hook.ConfiguredState == nil || hook.ConfiguredState.SourcePath == currentProjectConfigPath)
		if hook.Location.Source == "" || (hook.Location.Source == "nex" && hook.Location.Scope == "user") {
			userHooks = append(userHooks, hook)
		} else if sourceMatchesProject {
			projectHooks = append(projectHooks, hook)
		}
	}
	if err := s.writeNexHooksConfig("", userHooks); err != nil {
		return err
	}
	return s.writeNexHooksConfig(workspacePath, projectHooks)
}

// writeNexHooksConfig ports writeNexHooksConfig: merge into the existing
// config's hooks.events; root enabled flips true when any hook is enabled
// against its configured state.
func (s *Service) writeNexHooksConfig(workspacePath string, hooks []Hook) error {
	configPath := s.getConfigPath("nex", workspacePath)
	existingConfig := readJSONObjectOrEmpty(configPath)
	existingHooks, _ := existingConfig["hooks"].(map[string]any)
	enabled := resolveNextRootEnabled(optionalBool(existingHooks["enabled"]), hooks)
	events := toNexHooksEvents(hooks)
	nextHooks := map[string]any{}
	for key, value := range existingHooks {
		nextHooks[key] = value
	}
	if enabled != nil {
		nextHooks["enabled"] = *enabled
	}
	nextHooks["events"] = events
	next := map[string]any{}
	for key, value := range existingConfig {
		next[key] = value
	}
	next["hooks"] = nextHooks
	return AtomicWriteConfig(configPath, next)
}

// toNexHooksEvents ports toNexHooksEvents.
func toNexHooksEvents(hooks []Hook) map[string]any {
	events := map[string]any{}
	for _, hook := range hooks {
		eventMatchers, _ := events[hook.Event].([]any)
		var matcher map[string]any
		for _, item := range eventMatchers {
			record, ok := item.(map[string]any)
			if !ok {
				continue
			}
			existingMatcher, _ := record["matcher"].(string)
			if existingMatcher == hook.Matcher {
				matcher = record
				break
			}
		}
		if matcher == nil {
			matcher = map[string]any{}
			if hook.Matcher != "" {
				matcher["matcher"] = hook.Matcher
			}
			matcher["hooks"] = []any{}
			eventMatchers = append(eventMatchers, matcher)
			events[hook.Event] = eventMatchers
		}
		hookList, _ := matcher["hooks"].([]any)
		matcher["hooks"] = append(hookList, getWritableHook(hook))
	}
	return events
}

// getWritableHook ports getWritableHook: the declaration written back,
// with the declaration-level enable resolved from the configured state.
func getWritableHook(hook Hook) map[string]any {
	enabled := hook.Enabled
	if hook.ConfiguredState != nil && hook.Enabled == hook.ConfiguredState.ConfiguredEnabled {
		enabled = hook.ConfiguredState.DeclarationEnabled
	}
	definition := map[string]any{
		"type":    hook.Type,
		"command": hook.Command,
		"enabled": enabled,
	}
	if hook.StatusMessage != "" {
		definition["statusMessage"] = hook.StatusMessage
	}
	if hook.Type == "command" {
		if hook.Async != nil && *hook.Async {
			definition["async"] = true
		}
		if hook.Shell != nil {
			definition["shell"] = hook.Shell
		}
		if hook.Timeout != nil {
			definition["timeout"] = *hook.Timeout
		}
	} else {
		if len(hook.Args) > 0 {
			definition["args"] = hook.Args
		}
		if hook.Timeout != nil {
			definition["timeoutMs"] = *hook.Timeout * 1000
		}
	}
	return definition
}

// resolveNextRootEnabled ports resolveNextRootEnabled.
func resolveNextRootEnabled(existing *bool, hooks []Hook) *bool {
	for _, hook := range hooks {
		if hook.Enabled && (hook.ConfiguredState == nil || hook.Enabled != hook.ConfiguredState.ConfiguredEnabled) {
			value := true
			return &value
		}
	}
	return existing
}

func optionalBool(value any) *bool {
	if b, ok := value.(bool); ok {
		return &b
	}
	return nil
}

// readUserNexSource ports readUserNexSource.
func (s *Service) readUserNexSource() *SourceInput {
	path := s.userNexConfigPath()
	config := readJSONObjectOrEmpty(path)
	hooks, err := ParseHooksConfigValue(config["hooks"])
	if err != nil || hooks == nil {
		return nil
	}
	source := CreateSourceInput(path, s.homeDir, hooks, 0, true)
	source.Editable = true
	return &source
}

// fromProjectSnapshot ports fromProjectSnapshot.
func (s *Service) fromProjectSnapshot(sources []SourceInput, snapshot *BundleSnapshotData, workspaceIdentity, workspacePath string, persistentDigests map[string]bool) []Hook {
	if snapshot == nil {
		return nil
	}
	var hooks []Hook
	for _, entry := range snapshot.Hooks {
		if entry.SourceFileIndex >= len(sources) {
			continue
		}
		source := sources[entry.SourceFileIndex]
		raw := rawHookForEntry(source.Hooks, entry)
		hook := Hook{
			ID:            entry.ReviewItemID,
			Event:         entry.Event,
			Matcher:       matcherValue(entry.Matcher),
			Type:          entry.Type,
			Command:       entry.Command,
			StatusMessage: entry.StatusMessage,
			Enabled:       entry.ConfiguredEnabled,
			Editable:      entry.Editable,
			Location: HookLocation{
				Source:        "nex",
				Scope:         "project",
				DirectoryPath: filepath.Dir(source.CanonicalPath),
				ProjectPath:   workspacePath,
			},
		}
		if entry.Type == "process" {
			hook.Args = entry.Args
		}
		if entry.Type == "command" {
			hook.Async = entry.Async
			hook.Shell = entry.Shell
		}
		if raw != nil {
			hook.Timeout = raw.Timeout
		}
		hook.ConfiguredState = &ConfiguredState{
			SourceRootEnabled:   entry.SourceRootEnabled,
			DeclarationEnabled:  entry.DeclarationEnabled,
			RuntimeHooksEnabled: entry.RuntimeHooksEnabled,
			ConfiguredEnabled:   entry.ConfiguredEnabled,
			SourcePath:          source.CanonicalPath,
		}
		trustState := "pending_trust"
		if persistentDigests[entry.HookDeclarationDigest] {
			trustState = "trusted_persistent"
		}
		hook.WorkspaceHook = &WorkspaceHookState{
			ConfiguredState:       *hook.ConfiguredState,
			ReviewItemID:          entry.ReviewItemID,
			SourceFileIndex:       entry.SourceFileIndex,
			WorkspaceIdentity:     workspaceIdentity,
			BundleDigest:          snapshot.BundleDigest,
			HookDeclarationDigest: entry.HookDeclarationDigest,
			TrustState:            trustState,
		}
		hooks = append(hooks, hook)
	}
	return hooks
}

func rawHookForEntry(hooks *HooksConfig, entry EntryData) *Definition {
	if hooks == nil {
		return nil
	}
	matchers := hooks.Events[entry.Event]
	if entry.MatcherIndex >= len(matchers) {
		return nil
	}
	if entry.HookIndex >= len(matchers[entry.MatcherIndex].Hooks) {
		return nil
	}
	hook := matchers[entry.MatcherIndex].Hooks[entry.HookIndex]
	return &hook
}

// fromUserNexSource ports fromUserNexSource.
func (s *Service) fromUserNexSource(source *SourceInput, runtimeRoot RuntimeRoot, workspacePath string) []Hook {
	if source == nil {
		return nil
	}
	var hooks []Hook
	index := 0
	for _, event := range EventNames {
		for matcherIndex, matcher := range source.Hooks.Events[event] {
			for hookIndex, definition := range matcher.Hooks {
				gates := ResolveConfiguredGates(source.Hooks.Enabled, definition.Enabled, runtimeRoot.Enabled)
				hook := Hook{
					ID:            "hook-nex-user-" + itoa(index),
					Event:         event,
					Matcher:       matcher.Matcher,
					Type:          definition.Type,
					Command:       definition.Command,
					StatusMessage: definition.StatusMessage,
					Enabled:       gates.ConfiguredEnabled,
					Editable:      true,
					Location: HookLocation{
						Source:        "nex",
						Scope:         "user",
						DirectoryPath: filepath.Join(s.homeDir, ".nex", "cli"),
					},
				}
				if definition.Type == "process" {
					hook.Args = definition.Args
				} else {
					hook.Async = definition.Async
					hook.Shell = definition.Shell
				}
				if definition.Timeout != nil {
					hook.Timeout = definition.Timeout
				} else if definition.TimeoutMs != nil {
					value := *definition.TimeoutMs / 1000
					hook.Timeout = &value
				}
				hook.ConfiguredState = &ConfiguredState{
					SourceRootEnabled:   gates.SourceRootEnabled,
					DeclarationEnabled:  gates.DeclarationEnabled,
					RuntimeHooksEnabled: gates.RuntimeHooksEnabled,
					ConfiguredEnabled:   gates.ConfiguredEnabled,
					SourcePath:          source.CanonicalPath,
				}
				hooks = append(hooks, hook)
				index++
				_ = matcherIndex
				_ = hookIndex
			}
		}
	}
	return hooks
}
