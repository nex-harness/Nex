// writes.go: the command file mutation surface — write/update (with the
// rename-aware disable-override migration), delete, set-enabled, and the
// primary-directory materialization.
package commands

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/tylinux/nex/go/internal/plugins"
)

// WriteCommandFile ports ICommandsService.writeCommandFile.
func (s *Service) WriteCommandFile(ctx context.Context, agentSource string, storageLevel, workspacePath string, config commandConfig) (*UserCommand, error) {
	descriptor := nexCommandDescriptor
	commandsRoot, scope := s.getCommandsRootForStorage(descriptor, storageLevel, workspacePath)
	if commandsRoot == "" {
		return nil, fmt.Errorf("Missing workspace path for project command")
	}
	if err := os.MkdirAll(commandsRoot, 0o755); err != nil {
		return nil, err
	}
	fileName := getCommandFileName(config)
	filePath := filepath.Join(commandsRoot, fileName)
	if _, err := os.Stat(filePath); err == nil {
		return nil, fmt.Errorf("Command file already exists: %s", fileName)
	}
	content := generateCommandFileContent(config, "")
	if err := os.WriteFile(filePath, []byte(content), 0o644); err != nil {
		return nil, err
	}
	// An explicit enable:true entry is written on create (the TS writes the
	// override with true; later set-enabled normalizes it away).
	if err := s.setCommandEnabledOverride(filePath, true); err != nil {
		return nil, err
	}
	parsed := parseCommandFile(content, filePath)
	if parsed == nil {
		return nil, fmt.Errorf("Failed to parse written command file")
	}
	name := getCommandName(commandsRoot, filePath, descriptor)
	commandLocation := buildCommandLocation(descriptor, commandsRoot, scope, workspacePath)
	return &UserCommand{
		Name:         name,
		Prompt:       parsed.Prompt,
		Content:      parsed.Content,
		FilePath:     filePath,
		Description:  parsed.Description,
		ArgumentHint: parsed.ArgumentHint,
		AgentSource:  descriptor.AgentSource,
		Location:     commandLocation,
		ID:           buildCommandID(descriptor.AgentSource, scope, name, commandLocation),
		Source:       "user",
		Enabled:      true,
		Scope:        scope,
		ProjectPath:  workspacePath,
	}, nil
}

// UpdateCommandFile ports ICommandsService.updateCommandFile: rename-aware
// update that migrates the disable override to the new path.
func (s *Service) UpdateCommandFile(ctx context.Context, agentSource string, storageLevel, workspacePath, oldFilePath string, config commandConfig) (*UserCommand, error) {
	descriptor := nexCommandDescriptor
	commandsRoot, scope := s.getCommandsRootForStorage(descriptor, storageLevel, workspacePath)
	if commandsRoot == "" {
		return nil, fmt.Errorf("Missing workspace path for project command")
	}
	newFileName := getCommandFileName(config)
	newFilePath := filepath.Join(commandsRoot, newFileName)
	enabledOverrides := s.readCommandEnabledOverrides()
	existingContent := ""
	if oldFilePath != "" {
		if data, err := os.ReadFile(oldFilePath); err == nil {
			existingContent = string(data)
		}
	}
	if oldFilePath != "" && oldFilePath != newFilePath {
		if err := os.Remove(oldFilePath); err != nil && !os.IsNotExist(err) {
			// The old file may already be gone; ENOENT is normal.
			return nil, err
		}
	}
	if newFilePath != oldFilePath {
		if _, err := os.Stat(newFilePath); err == nil {
			return nil, fmt.Errorf("Command file already exists: %s", newFileName)
		}
	}
	content := generateCommandFileContent(config, existingContent)
	if err := os.MkdirAll(filepath.Dir(newFilePath), 0o755); err != nil {
		return nil, err
	}
	if err := os.WriteFile(newFilePath, []byte(content), 0o644); err != nil {
		return nil, err
	}
	if oldFilePath != "" && oldFilePath != newFilePath {
		if override, ok := enabledOverrides[oldFilePath]; ok {
			// Disable state is keyed by file path; a rename would silently
			// re-enable a command the user just disabled.
			if err := s.setCommandEnabledOverride(oldFilePath, true); err != nil {
				return nil, err
			}
			if err := s.setCommandEnabledOverride(newFilePath, override); err != nil {
				return nil, err
			}
		}
	}
	parsed := parseCommandFile(content, newFilePath)
	if parsed == nil {
		return nil, fmt.Errorf("Failed to parse written command file")
	}
	name := getCommandName(commandsRoot, newFilePath, descriptor)
	commandLocation := buildCommandLocation(descriptor, commandsRoot, scope, workspacePath)
	return &UserCommand{
		Name:         name,
		Prompt:       parsed.Prompt,
		Content:      parsed.Content,
		FilePath:     newFilePath,
		Description:  parsed.Description,
		ArgumentHint: parsed.ArgumentHint,
		AgentSource:  descriptor.AgentSource,
		Location:     commandLocation,
		ID:           buildCommandID(descriptor.AgentSource, scope, name, commandLocation),
		Source:       "user",
		Enabled:      getCommandEnabled(newFilePath, s.readCommandEnabledOverrides()),
		Scope:        scope,
		ProjectPath:  workspacePath,
	}, nil
}

// DeleteCommandFile ports ICommandsService.deleteCommandFile.
func (s *Service) DeleteCommandFile(ctx context.Context, filePath string) error {
	if err := os.Remove(filePath); err != nil && !os.IsNotExist(err) {
		return err
	}
	return s.setCommandEnabledOverride(filePath, true)
}

// SetCommandEnabled ports ICommandsService.setCommandEnabled.
func (s *Service) SetCommandEnabled(ctx context.Context, filePath string, enabled bool) error {
	return s.setCommandEnabledOverride(filePath, enabled)
}

// GetPrimaryUserCommandsDirectory ports getPrimaryUserCommandsDirectory:
// the directory is materialized so Windows file managers can open it.
func (s *Service) GetPrimaryUserCommandsDirectory(ctx context.Context, agentSource string) (string, error) {
	descriptor := nexCommandDescriptor
	path := s.userCommandsRoot(descriptor)
	if err := os.MkdirAll(path, 0o755); err != nil {
		return "", err
	}
	return path, nil
}

// readCommandEnabledOverrides reads config.command.<path>.enable.
func (s *Service) readCommandEnabledOverrides() map[string]bool {
	config := s.readUserCliConfig()
	commandConfig, _ := config["command"].(map[string]any)
	out := map[string]bool{}
	for path, entry := range commandConfig {
		if record, ok := entry.(map[string]any); ok {
			if enable, ok := record["enable"].(bool); ok {
				out[path] = enable
			}
		}
	}
	return out
}

// setCommandEnabledOverride ports setCommandEnabledOverride: enable deletes
// the override (the default), disable writes {enable:false}; an empty
// command block is removed entirely.
func (s *Service) setCommandEnabledOverride(filePath string, enabled bool) error {
	config := s.readUserCliConfig()
	commandConfig, _ := config["command"].(map[string]any)
	if commandConfig == nil {
		commandConfig = map[string]any{}
	}
	if enabled {
		delete(commandConfig, filePath)
	} else {
		commandConfig[filePath] = map[string]any{"enable": false}
	}
	if len(commandConfig) > 0 {
		config["command"] = commandConfig
	} else {
		delete(config, "command")
	}
	return s.writeUserCliConfig(config)
}

func (s *Service) readUserCliConfig() map[string]any {
	raw, err := os.ReadFile(s.userCliConfigPath())
	if err != nil {
		return map[string]any{}
	}
	var parsed map[string]any
	if err := json.Unmarshal(raw, &parsed); err != nil || parsed == nil {
		return map[string]any{}
	}
	return parsed
}

func (s *Service) writeUserCliConfig(config map[string]any) error {
	if err := os.MkdirAll(filepath.Dir(s.userCliConfigPath()), 0o755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(config, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(s.userCliConfigPath(), append(data, '\n'), 0o644)
}

// discoverPluginCommands ports discoverPluginCommands over the shared
// plugins library. A parse failure in one file must not block the rest.
func (s *Service) discoverPluginCommands(enabledOverrides map[string]bool) ([]PluginCommand, error) {
	rootDescriptors := s.resolvePluginCommandRootDescriptors()
	var commands []PluginCommand
	seenFilePaths := map[string]bool{}
	for _, descriptor := range rootDescriptors {
		s.discoverPluginCommandsRecursive(descriptor.rootPath, descriptor.rootPath, descriptor, enabledOverrides, seenFilePaths, &commands)
	}
	sort.Slice(commands, func(i, j int) bool { return commands[i].Name < commands[j].Name })
	return commands, nil
}

type pluginCommandRootDescriptor struct {
	pluginEnabled     bool
	pluginMarketplace string
	pluginName        string
	rootPath          string
}

func (s *Service) resolvePluginCommandRootDescriptors() []pluginCommandRootDescriptor {
	config := s.readCliPluginConfig()
	if !config.enabled {
		return nil
	}
	pluginStorageRoot := s.resolvePluginStorageRoot(config.storageDir)

	type candidate struct {
		defaultEnabled bool
		marketplace    string
		rootPath       string
	}
	var candidates []candidate
	for _, dir := range config.dirs {
		candidates = append(candidates, candidate{true, plugins.InlineMarketplace, s.resolveConfigPath(dir)})
	}
	for _, rootPath := range s.scanOfficialPluginCacheRoots(pluginStorageRoot) {
		candidates = append(candidates, candidate{false, plugins.OfficialMarketplaceID, rootPath})
	}
	for _, record := range plugins.ReadInstalledPluginRoots(pluginStorageRoot) {
		candidates = append(candidates, candidate{false, record.Marketplace, record.InstallPath})
	}

	var descriptors []pluginCommandRootDescriptor
	seenPluginIDs := map[string]bool{}
	for _, item := range candidates {
		manifest, err := plugins.ReadManifest(item.rootPath)
		if err != nil || manifest == nil {
			continue
		}
		pluginID := manifest.Name + "@" + item.marketplace
		if item.marketplace == plugins.OfficialMarketplaceID && containsStringList(config.suppressedBuiltins, pluginID) {
			continue
		}
		if seenPluginIDs[pluginID] {
			continue
		}
		seenPluginIDs[pluginID] = true
		defaultEnabled := item.defaultEnabled || plugins.DefaultEnabledOfficialPluginIDs[pluginID]
		enabled := defaultEnabled
		if override, ok := config.enabledPlugins[pluginID]; ok {
			enabled = override
		}
		if !enabled {
			continue
		}
		// The command management page previously showed only local commands
		// and only the builtin cache; marketplace installed plugins are also
		// read-only command sources.
		for _, rootPath := range plugins.ResolveComponentRoots(item.rootPath, manifest.Commands, "commands") {
			descriptors = append(descriptors, pluginCommandRootDescriptor{
				pluginEnabled:     enabled,
				pluginMarketplace: item.marketplace,
				pluginName:        manifest.Name,
				rootPath:          rootPath,
			})
		}
	}
	return descriptors
}

func (s *Service) discoverPluginCommandsRecursive(rootDir, currentDir string, descriptor pluginCommandRootDescriptor,
	enabledOverrides map[string]bool, seenFilePaths map[string]bool, commands *[]PluginCommand) {
	entries, err := os.ReadDir(currentDir)
	if err != nil {
		return
	}
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), ".") {
			continue
		}
		fullPath := filepath.Join(currentDir, entry.Name())
		info, err := os.Lstat(fullPath)
		if err != nil {
			continue
		}
		if info.IsDir() {
			s.discoverPluginCommandsRecursive(rootDir, fullPath, descriptor, enabledOverrides, seenFilePaths, commands)
			continue
		}
		if !strings.HasSuffix(strings.ToLower(entry.Name()), ".md") {
			continue
		}
		content, err := os.ReadFile(fullPath)
		if err != nil {
			continue
		}
		parsed := parseCommandFile(string(content), fullPath)
		if parsed == nil {
			continue
		}
		name := getCommandName(rootDir, fullPath, nexCommandDescriptor)
		commandKey := strings.ToLower(strings.ReplaceAll(fullPath, "\\", "/"))
		if seenFilePaths[commandKey] {
			continue
		}
		seenFilePaths[commandKey] = true
		*commands = append(*commands, PluginCommand{
			Name:              name,
			Prompt:            parsed.Prompt,
			Content:           parsed.Content,
			FilePath:          fullPath,
			Description:       parsed.Description,
			ArgumentHint:      parsed.ArgumentHint,
			Enabled:           descriptor.pluginEnabled && getCommandEnabled(fullPath, enabledOverrides),
			ID:                fmt.Sprintf("plugin:%s:%s:%s:%s", descriptor.pluginMarketplace, descriptor.pluginName, name, fullPath),
			PluginEnabled:     descriptor.pluginEnabled,
			PluginMarketplace: descriptor.pluginMarketplace,
			PluginName:        descriptor.pluginName,
			Scope:             "global",
			Source:            "plugin",
		})
	}
}

func containsStringList(list []string, value string) bool {
	for _, item := range list {
		if item == value {
			return true
		}
	}
	return false
}
