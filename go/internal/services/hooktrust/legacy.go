// legacy.go: the read-only legacy agents/claude settings.json hooks
// loaders and the bundle-snapshot builder feeding loadHooks.
package hooktrust

import (
	"encoding/json"
	"os"
	"path/filepath"
)

// loadLegacyHooksFromLocation ports loadLegacyHooksFromLocation: the
// agents/claude settings.json hooks block, read-only, disabled by default.
func (s *Service) loadLegacyHooksFromLocation(source string, workspacePath string) []Hook {
	configPath := s.getConfigPath(source, workspacePath)
	raw, err := os.ReadFile(configPath)
	if err != nil {
		return nil
	}
	var parsed map[string]any
	if err := json.Unmarshal(raw, &parsed); err != nil {
		return nil
	}
	hooksBlock, _ := parsed["hooks"].(map[string]any)
	if hooksBlock == nil {
		return nil
	}
	scope := "user"
	if workspacePath != "" {
		scope = "project"
	}
	directoryPath := filepath.Dir(configPath)
	var hooks []Hook
	idCounter := 0
	for _, event := range EventNames {
		matchers, _ := hooksBlock[event].([]any)
		for _, matcherItem := range matchers {
			matcher, _ := matcherItem.(map[string]any)
			if matcher == nil {
				continue
			}
			matcherValue, _ := matcher["matcher"].(string)
			definitions, _ := matcher["hooks"].([]any)
			for _, hookItem := range definitions {
				record, _ := hookItem.(map[string]any)
				if record == nil {
					continue
				}
				hookType, _ := record["type"].(string)
				command, _ := record["command"].(string)
				if (hookType != "command" && hookType != "process") || command == "" {
					continue
				}
				hook := Hook{
					ID:       "hook-" + source + "-" + scope + "-" + itoa(idCounter),
					Event:    event,
					Matcher:  matcherValue,
					Type:     hookType,
					Command:  command,
					Enabled:  false,
					Editable: false,
					Location: HookLocation{Source: source, Scope: scope, DirectoryPath: directoryPath},
				}
				if args, ok := record["args"].([]any); ok && hookType == "process" {
					for _, arg := range args {
						if s, ok := arg.(string); ok {
							hook.Args = append(hook.Args, s)
						}
					}
				}
				if async, ok := record["async"].(bool); ok && hookType == "command" {
					hook.Async = &async
				}
				if shell, ok := record["shell"]; ok && hookType == "command" {
					hook.Shell = shell
				}
				if statusMessage, ok := record["statusMessage"].(string); ok {
					hook.StatusMessage = statusMessage
				}
				if timeout, ok := record["timeout"].(float64); ok {
					hook.Timeout = &timeout
				} else if timeoutMs, ok := record["timeoutMs"].(float64); ok {
					value := timeoutMs / 1000
					hook.Timeout = &value
				}
				hooks = append(hooks, hook)
				idCounter++
			}
		}
	}
	return hooks
}

// readPersistentTrustDigests ports readPersistentWorkspaceHookTrustDigests:
// ENOENT → empty and not corrupt; unreadable or schema-invalid → corrupt
// (fail-closed); the display layer must agree with the runtime's verdict.
func (s *Service) readPersistentTrustDigests(workspaceIdentity string) (map[string]bool, bool) {
	userConfig := readJSONObjectOrEmpty(s.userNexConfigPath())
	storage, _ := userConfig["storage"].(map[string]any)
	configured := ""
	if dir, ok := storage["dir"].(string); ok {
		configured = dir
	}
	storageRoot := StorageRootFromUserConfig(s.homeDir, configured)
	digests, corrupt, _ := ReadTrustedDigests(storageRoot, workspaceIdentity)
	return digests, corrupt
}

// buildBundleSnapshot assembles the snapshot data (entries + source files +
// digest) from the sources.
func (s *Service) buildBundleSnapshot(workspaceIdentity, workspacePath string, sources []SourceInput, runtimeRoot RuntimeRoot) *BundleSnapshotData {
	bundleDigest := CreateBundleDigest(BundleDigestInput{
		WorkspaceIdentity: workspaceIdentity,
		WorkspacePath:     workspacePath,
		Sources:           sources,
		RuntimeRoot:       runtimeRoot,
	})
	if bundleDigest == "" {
		return nil
	}
	snapshot := &BundleSnapshotData{
		SchemaVersion:     DigestSchemaVersion,
		WorkspaceIdentity: workspaceIdentity,
		DiscoveredAt:      s.options.NowISO(),
		DigestAlgorithm:   "sha256",
		BundleDigest:      bundleDigest,
	}
	for _, source := range sources {
		hooksRoot := HooksRoot{Enabled: source.Hooks.Enabled, TimeoutMs: source.Hooks.TimeoutMs, MaxOutputBytes: source.Hooks.MaxOutputBytes}
		snapshot.SourceFiles = append(snapshot.SourceFiles, SourceFileData{
			CanonicalPath:         source.CanonicalPath,
			BaseDir:               source.BaseDir,
			DiscoveryOrder:        source.DiscoveryOrder,
			ConfigFileKind:        source.ConfigFileKind,
			ExplicitProjectConfig: source.ExplicitProjectConfig,
			Editable:              source.Editable,
			HooksRoot:             hooksRoot,
		})
	}
	for _, source := range sources {
		relative := normalizeRelativeSourcePath(workspacePath, source.CanonicalPath)
		for _, event := range EventNames {
			for matcherIndex, matcher := range source.Hooks.Events[event] {
				for hookIndex, definition := range matcher.Hooks {
					gates := ResolveConfiguredGates(source.Hooks.Enabled, definition.Enabled, runtimeRoot.Enabled)
					entry := EntryData{
						ReviewItemID:               "workspace-hook-" + itoa(len(snapshot.SourceFiles)) + "-" + event + "-" + itoa(matcherIndex) + "-" + itoa(hookIndex),
						Event:                      event,
						MatcherIndex:               matcherIndex,
						HookIndex:                  hookIndex,
						SourceFileIndex:            source.DiscoveryOrder,
						SourceRelativePath:         relative,
						Matcher:                    matcherPointer(matcher.Matcher),
						Type:                       definition.Type,
						Command:                    definition.Command,
						ResolvedTimeoutMs:          ResolveTimeoutMs(&definition, runtimeRoot.TimeoutMs),
						ResolvedMaxOutputBytes:     runtimeRoot.MaxOutputBytes,
						StatusMessage:              definition.StatusMessage,
						SourceRootEnabled:          gates.SourceRootEnabled,
						DeclarationEnabled:         gates.DeclarationEnabled,
						RuntimeHooksEnabled:        gates.RuntimeHooksEnabled,
						ConfiguredEnabled:          gates.ConfiguredEnabled,
						Editable:                   source.Editable,
						DeclarationDigestAlgorithm: "sha256",
						HookDeclarationDigest: CreateDeclarationDigest(DeclarationDigestInput{
							SourceRelativePath:     relative,
							SourceDiscoveryOrder:   source.DiscoveryOrder,
							Event:                  event,
							Matcher:                matcherPointer(matcher.Matcher),
							MatcherIndex:           matcherIndex,
							HookIndex:              hookIndex,
							Hook:                   definition,
							DefaultTimeoutMs:       runtimeRoot.TimeoutMs,
							ResolvedMaxOutputBytes: runtimeRoot.MaxOutputBytes,
						}),
					}
					if definition.Type == "process" && len(definition.Args) > 0 {
						entry.Args = definition.Args
					}
					if definition.Type == "command" {
						entry.Async = definition.Async
						entry.Shell = definition.Shell
					}
					snapshot.Hooks = append(snapshot.Hooks, entry)
				}
			}
		}
	}
	return snapshot
}

func matcherValue(matcher *string) string {
	if matcher == nil {
		return ""
	}
	return *matcher
}

func readJSONObjectOrEmpty(path string) map[string]any {
	raw, err := os.ReadFile(path)
	if err != nil {
		return map[string]any{}
	}
	var parsed map[string]any
	if err := json.Unmarshal(raw, &parsed); err != nil || parsed == nil {
		return map[string]any{}
	}
	return parsed
}

func itoa(value int) string {
	return strconvItoa(value)
}

// defaultNowISO emits an ISO-8601 timestamp like new Date().toISOString().
func defaultNowISO() string {
	return timeNowUTC().Format("2006-01-02T15:04:05.000Z")
}
