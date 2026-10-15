// service.go: the ICommandsService implementation — directory sources
// (.nex/.agents, project-then-user with the .nex-priority rule), the CLI
// config enable map, write/update/delete with the rename-override
// migration, and plugin command discovery.
package commands

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// ChannelName is the rpc channel name (ServiceChannels.Commands).
const ChannelName = "commands"

// location ports SettingsDirectoryLocation.
type location struct {
	Source        string `json:"source"` // nex | agents | claude
	Scope         string `json:"scope"`  // user | project
	DirectoryPath string `json:"directoryPath"`
	ProjectPath   string `json:"projectPath,omitempty"`
}

// UserCommand ports UserCommand.
type UserCommand struct {
	Name         string   `json:"name"`
	Prompt       string   `json:"prompt"`
	Content      string   `json:"content"`
	FilePath     string   `json:"filePath"`
	Description  string   `json:"description,omitempty"`
	ArgumentHint string   `json:"argumentHint,omitempty"`
	AgentSource  string   `json:"agentSource"`
	Location     location `json:"location"`
	ID           string   `json:"id"`
	Source       string   `json:"source"` // user
	Enabled      bool     `json:"enabled"`
	Scope        string   `json:"scope"` // global | project
	ProjectPath  string   `json:"projectPath,omitempty"`
}

// PluginCommand ports PluginCommand.
type PluginCommand struct {
	Name              string `json:"name"`
	Prompt            string `json:"prompt"`
	Content           string `json:"content"`
	FilePath          string `json:"filePath"`
	Description       string `json:"description,omitempty"`
	ArgumentHint      string `json:"argumentHint,omitempty"`
	Enabled           bool   `json:"enabled"`
	ID                string `json:"id"`
	PluginEnabled     bool   `json:"pluginEnabled"`
	PluginMarketplace string `json:"pluginMarketplace"`
	PluginName        string `json:"pluginName"`
	Scope             string `json:"scope"`
	Source            string `json:"source"` // plugin
}

// ListResult ports CommandsListResult.
type ListResult struct {
	Commands       []any           `json:"commands"`
	UserCommands   []UserCommand   `json:"userCommands"`
	PluginCommands []PluginCommand `json:"pluginCommands"`
	Capability     struct {
		UserScopeAvailable bool `json:"userScopeAvailable"`
	} `json:"capability"`
}

// directorySourceDescriptor ports CommandAgentSourceDescriptor (nexAgent only).
type directorySourceDescriptor struct {
	AgentSource                string
	DirectorySource            string
	UserDirectorySegments      []string
	WorkspaceDirectorySegments []string
	FileExtension              string
	NamespaceSeparator         string
	SupportsArgumentHint       bool
}

var nexCommandDescriptor = directorySourceDescriptor{
	AgentSource:                "nexAgent",
	DirectorySource:            "nex",
	UserDirectorySegments:      []string{".nex", "commands"},
	WorkspaceDirectorySegments: []string{".nex", "commands"},
	FileExtension:              ".md",
	NamespaceSeparator:         "/",
	SupportsArgumentHint:       true,
}

var agentsCommandDescriptor = directorySourceDescriptor{
	AgentSource:                "nexAgent",
	DirectorySource:            "agents",
	UserDirectorySegments:      []string{".agents", "commands"},
	WorkspaceDirectorySegments: []string{".agents", "commands"},
	FileExtension:              ".md",
	NamespaceSeparator:         "/",
	SupportsArgumentHint:       true,
}

// commandAgentSourceOrder ports NEX_COMMAND_AGENT_SOURCES (["nexAgent"]).
var commandAgentSourceOrder = []string{"nexAgent"}

// Options carry the seams.
type Options struct {
	HomeDir string
}

// Service is the ICommandsService implementation.
type Service struct {
	homeDir string
}

// New builds the service.
func New(options Options) *Service {
	return &Service{homeDir: options.HomeDir}
}

func (s *Service) userCliConfigPath() string {
	return filepath.Join(s.homeDir, ".nex", "cli", "config.json")
}

func (s *Service) userCommandsRoot(descriptor directorySourceDescriptor) string {
	return filepath.Join(append([]string{s.homeDir}, descriptor.UserDirectorySegments...)...)
}

// List ports ICommandsService.list.
func (s *Service) List(ctx context.Context, agentSource, workspacePath, workspaceIdentity string) (*ListResult, error) {
	var userCommands []UserCommand
	agentSources := commandAgentSourceOrder
	if agentSource != "" {
		agentSources = []string{agentSource}
	}
	enabledOverrides := s.readCommandEnabledOverrides()

	// The Nex Agent merges ALL workspace dirs first, then ALL user dirs;
	// interleaving per directory would let user .nex precede workspace .agents.
	for _, source := range agentSources {
		if source != "nexAgent" {
			continue
		}
		descriptors := []directorySourceDescriptor{nexCommandDescriptor, agentsCommandDescriptor}
		if workspacePath != "" {
			s.discoverCommandsFromDirectorySources(descriptors, func(descriptor directorySourceDescriptor) string {
				return filepath.Join(append([]string{workspacePath}, descriptor.WorkspaceDirectorySegments...)...)
			}, "project", workspacePath, enabledOverrides, &userCommands)
		}
		s.discoverCommandsFromDirectorySources(descriptors, func(descriptor directorySourceDescriptor) string {
			return s.userCommandsRoot(descriptor)
		}, "global", "", enabledOverrides, &userCommands)
	}

	deduped := dedupeCommandsByName(userCommands)
	var pluginCommands []PluginCommand
	if agentSource == "" || agentSource == "nexAgent" {
		var err error
		pluginCommands, err = s.discoverPluginCommands(enabledOverrides)
		if err != nil {
			return nil, err
		}
	}
	result := &ListResult{
		UserCommands:   deduped,
		PluginCommands: pluginCommands,
		Commands:       make([]any, 0, len(deduped)+len(pluginCommands)),
	}
	for _, command := range deduped {
		result.Commands = append(result.Commands, command)
	}
	for _, command := range pluginCommands {
		result.Commands = append(result.Commands, command)
	}
	result.Capability.UserScopeAvailable = true
	return result, nil
}

func dedupeCommandsByName(commands []UserCommand) []UserCommand {
	selected := map[string]int{}
	out := make([]UserCommand, 0, len(commands))
	for _, command := range commands {
		if _, ok := selected[command.Name]; !ok {
			selected[command.Name] = len(out)
			out = append(out, command)
		}
	}
	return out
}

func (s *Service) discoverCommandsFromDirectorySources(
	descriptors []directorySourceDescriptor,
	commandsRootForDescriptor func(directorySourceDescriptor) string,
	scope, projectPath string,
	enabledOverrides map[string]bool,
	commands *[]UserCommand,
) {
	for _, descriptor := range descriptors {
		before := len(*commands)
		root := commandsRootForDescriptor(descriptor)
		if _, err := os.Stat(root); err != nil {
			continue
		}
		s.discoverUserCommandsRecursive(root, root, descriptor, scope, projectPath, enabledOverrides, commands)
		// `.nex` is the strong-priority source: once it yields commands, the
		// same-scope `.agents` does not participate.
		if descriptor.DirectorySource == "nex" && len(*commands) > before {
			break
		}
	}
}

var fileNameSeparatorRe = regexp.MustCompile(`[\\/]+`)

// getCommandName ports getCommandName: /-prefixed relative path with the
// extension stripped and separators joined by the namespace separator.
func getCommandName(rootDir, filePath string, descriptor directorySourceDescriptor) string {
	relative, err := filepath.Rel(rootDir, filePath)
	if err != nil {
		return ""
	}
	extensionRe := regexp.MustCompile(`(?i)\.md$`)
	withoutExtension := extensionRe.ReplaceAllString(relative, "")
	parts := fileNameSeparatorRe.Split(withoutExtension, -1)
	filtered := parts[:0]
	for _, part := range parts {
		if part != "" {
			filtered = append(filtered, part)
		}
	}
	return "/" + strings.Join(filtered, descriptor.NamespaceSeparator)
}

// getCommandFileName ports getCommandFileName.
func getCommandFileName(config commandConfig) string {
	rawName := strings.TrimPrefix(config.Name, "/")
	return rawName + ".md"
}

func (s *Service) discoverUserCommandsRecursive(rootDir, currentDir string, descriptor directorySourceDescriptor,
	scope, projectPath string, enabledOverrides map[string]bool, commands *[]UserCommand) {
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
			s.discoverUserCommandsRecursive(rootDir, fullPath, descriptor, scope, projectPath, enabledOverrides, commands)
			continue
		}
		if !strings.HasSuffix(strings.ToLower(entry.Name()), descriptor.FileExtension) {
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
		commandLocation := buildCommandLocation(descriptor, rootDir, scope, projectPath)
		*commands = append(*commands, UserCommand{
			Name:         getCommandName(rootDir, fullPath, descriptor),
			Prompt:       parsed.Prompt,
			Content:      parsed.Content,
			FilePath:     fullPath,
			Description:  parsed.Description,
			ArgumentHint: parsed.ArgumentHint,
			AgentSource:  descriptor.AgentSource,
			Location:     commandLocation,
			ID:           buildCommandID(descriptor.AgentSource, scope, getCommandName(rootDir, fullPath, descriptor), commandLocation),
			Source:       "user",
			Enabled:      getCommandEnabled(fullPath, enabledOverrides),
			Scope:        scope,
			ProjectPath:  projectPath,
		})
	}
}

func buildCommandLocation(descriptor directorySourceDescriptor, commandsRoot, scope, projectPath string) location {
	scopeValue := "user"
	if scope == "project" {
		scopeValue = "project"
	}
	return location{
		Source:        descriptor.DirectorySource,
		Scope:         scopeValue,
		DirectoryPath: commandsRoot,
		ProjectPath:   projectPath,
	}
}

func buildCommandID(agentSource, scope, name string, commandLocation location) string {
	return fmt.Sprintf("%s:%s:%s:%s", agentSource, commandLocation.Source, scope, name)
}

func getCommandEnabled(filePath string, overrides map[string]bool) bool {
	if enabled, ok := overrides[filePath]; ok {
		return enabled
	}
	return true
}

// getCommandsRootForStorage ports getCommandsRootForStorage.
func (s *Service) getCommandsRootForStorage(descriptor directorySourceDescriptor, storageLevel, workspacePath string) (string, string) {
	if storageLevel == "project" {
		if workspacePath == "" {
			return "", ""
		}
		return filepath.Join(append([]string{workspacePath}, descriptor.WorkspaceDirectorySegments...)...), "project"
	}
	return s.userCommandsRoot(descriptor), "global"
}
