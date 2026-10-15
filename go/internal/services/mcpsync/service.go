// Package mcpsync is the IMcpSyncService port (step 3.7): user-directory
// MCP server load/save (nex config.json `mcp.servers` primary, agents
// mcp.json `mcpServers` fallback), the sync candidates/export/import with
// filesystem-path rewriting, and the legacy `enable` → `enabled` migration.
//
// Source: packages/services/src/mcp-sync/mcpSyncService.ts. listWorkspace-
// McpServerStatuses must run in the agent process (workspace PATH/cwd); the
// Go port keeps that boundary — constructing without the dependency and
// calling it fails with the exact TS error. Plugin RUNTIME effects are
// Phase 4.
package mcpsync

import (
	"context"
	"fmt"
	"path/filepath"
	"sort"
)

// ChannelName is the rpc channel name (ServiceChannels.McpSync).
const ChannelName = "mcp-sync"

// StatusesUnavailableError is the exact TS error for the agent-side listing.
const StatusesUnavailableError = "MCP server status listing is unavailable in this runtime"

// directoryMcpDescriptor ports DirectoryMcpDescriptor.
type directoryMcpDescriptor struct {
	source                     string // nex | agents
	directorySource            string
	userConfigDirSegments      []string
	workspaceConfigDirSegments []string
	fileName                   string
	configKeyName              string // "mcp.servers" | "mcpServers"
}

var nexMcpDescriptor = directoryMcpDescriptor{
	source:                     "nex",
	directorySource:            "nex",
	userConfigDirSegments:      []string{".nex", "cli"},
	workspaceConfigDirSegments: []string{".nex"},
	fileName:                   "config.json",
	configKeyName:              "mcp.servers",
}

var agentsMcpDescriptor = directoryMcpDescriptor{
	source:                     "agents",
	directorySource:            "agents",
	userConfigDirSegments:      []string{".agents"},
	workspaceConfigDirSegments: []string{".agents"},
	fileName:                   "mcp.json",
	configKeyName:              "mcpServers",
}

const (
	enabledKey      = "enabled"
	legacyEnableKey = "enable"
	secretFileMode  = 0o600
)

// ServerRecord is one directory server entry (NativeMcpServerRecord read side).
type ServerRecord struct {
	projectPathValue string
	Name             string         `json:"name"`
	Config           map[string]any `json:"config"`
	Enabled          bool           `json:"enabled"`
	Source           string         `json:"source"` // nex | agents
	Path             string         `json:"path"`
	// Scope: "user" | "workspace".
	Scope string `json:"scope"`
}

// Candidate ports McpSyncCandidate.
type Candidate struct {
	ID      string         `json:"id"`
	Name    string         `json:"name"`
	Config  map[string]any `json:"config"`
	Enabled bool           `json:"enabled"`
	Source  string         `json:"source"`
	Path    string         `json:"path"`
}

// CandidateListResult ports McpSyncCandidateListResult.
type CandidateListResult struct {
	Candidates   []Candidate `json:"candidates"`
	LocalHomeDir string      `json:"localHomeDir"`
}

// RemoteStatus ports McpSyncRemoteStatus.
type RemoteStatus struct {
	Name   string `json:"name"`
	Exists bool   `json:"exists"`
	Path   string `json:"path,omitempty"`
}

// RemoteStatusResult ports McpSyncRemoteStatusResult.
type RemoteStatusResult struct {
	Statuses      []RemoteStatus `json:"statuses"`
	RemoteHomeDir string         `json:"remoteHomeDir"`
}

// ExportedServer ports McpSyncExportedServer.
type ExportedServer struct {
	ID      string         `json:"id"`
	Name    string         `json:"name"`
	Config  map[string]any `json:"config"`
	Enabled bool           `json:"enabled"`
	Source  string         `json:"source"`
	Path    string         `json:"path"`
}

// ExportResult ports McpSyncExportResult.
type ExportResult struct {
	Servers      []ExportedServer `json:"servers"`
	LocalHomeDir string           `json:"localHomeDir"`
}

// ImportResultItem ports McpSyncImportResultItem.
type ImportResultItem struct {
	Name   string `json:"name"`
	Status string `json:"status"`
	Path   string `json:"path,omitempty"`
	Error  string `json:"error,omitempty"`
}

// ImportResult ports McpSyncImportResult.
type ImportResult struct {
	Results []ImportResultItem `json:"results"`
}

// WriteAccessResult ports RemoteSyncWriteAccessResult.
type WriteAccessResult struct {
	OK    bool   `json:"ok"`
	Path  string `json:"path"`
	Error string `json:"error,omitempty"`
}

// Options carry the seams.
type Options struct {
	HomeDir string
	// ListMcpServerStatuses is the agent-side execution surface (optional —
	// a construction without it is directory-sync-only, and calling
	// listWorkspaceMcpServerStatuses then fails with the exact TS error).
	ListMcpServerStatuses func(ctx context.Context, params map[string]any) (any, error)
}

// Service is the IMcpSyncService implementation.
type Service struct {
	options Options
	homeDir string
}

// New builds the service.
func New(options Options) *Service { return &Service{options: options, homeDir: options.HomeDir} }

func (s *Service) buildDirectoryConfigPath(descriptor directoryMcpDescriptor, scope, workspacePath string) string {
	baseDir := s.homeDir
	if scope == "workspace" {
		baseDir = workspacePath
	}
	segments := descriptor.userConfigDirSegments
	if scope == "workspace" {
		segments = descriptor.workspaceConfigDirSegments
	}
	return filepath.Join(append([]string{baseDir}, append(segments, descriptor.fileName)...)...)
}

func (s *Service) userNexMcpConfigPath() string {
	return s.buildDirectoryConfigPath(nexMcpDescriptor, "user", "")
}

// ListWorkspaceMcpServerStatuses ports the agent-forwarded method.
func (s *Service) ListWorkspaceMcpServerStatuses(ctx context.Context, params map[string]any) (any, error) {
	if s.options.ListMcpServerStatuses == nil {
		return nil, fmt.Errorf("%s", StatusesUnavailableError)
	}
	return s.options.ListMcpServerStatuses(ctx, params)
}

// LoadMcpFromUserDirectory ports loadMcpFromUserDirectory: workspace
// servers (when a workspacePath is given) then user servers, each from the
// preferred source (.nex first, .agents fallback).
func (s *Service) LoadMcpFromUserDirectory(ctx context.Context, workspacePath string) (map[string]any, error) {
	servers := []map[string]any{}
	if workspacePath != "" {
		records, err := s.readDirectoryServersFromPreferredSources("workspace", workspacePath)
		if err != nil {
			return nil, err
		}
		for _, record := range records {
			servers = append(servers, serverRecordToMap(record))
		}
	}
	records, err := s.readDirectoryServersFromPreferredSources("user", workspacePath)
	if err != nil {
		return nil, err
	}
	for _, record := range records {
		servers = append(servers, serverRecordToMap(record))
	}
	return map[string]any{"servers": servers}, nil
}

func serverRecordToMap(record ServerRecord) map[string]any {
	out := map[string]any{
		"source":  "nexagentmcp",
		"scope":   record.Scope,
		"name":    record.Name,
		"config":  record.Config,
		"enabled": record.Enabled,
	}
	if record.Scope == "workspace" {
		out["projectPath"] = record.projectPathValue
	}
	return out
}

// SaveMcpToUserDirectory ports saveMcpToUserDirectory: upsert/remove in the
// nex config, or set-enabled in the server's own config object.
func (s *Service) SaveMcpToUserDirectory(ctx context.Context, payload map[string]any) error {
	action, _ := payload["action"].(string)
	name, _ := payload["name"].(string)
	projectPath, _ := payload["projectPath"].(string)
	scope := "user"
	if projectPath != "" {
		scope = "workspace"
	}
	if action == "set-enabled" {
		enabled, ok := payload["enabled"].(bool)
		if !ok {
			return fmt.Errorf("Missing enabled value for MCP set-enabled action")
		}
		descriptor := nexMcpDescriptor
		if location, ok := payload["location"].(map[string]any); ok {
			if source, ok := location["source"].(string); ok {
				if source == "agents" {
					descriptor = agentsMcpDescriptor
				}
			}
		}
		if err := s.writeServerEnabledToFile(descriptor, scope, projectPath, name, enabled); err != nil {
			return err
		}
		return nil
	}
	existingServers, err := s.readDirectoryServersFromFile(nexMcpDescriptor, scope, projectPath)
	if err != nil {
		return err
	}
	nextServers := map[string]any{}
	for _, server := range existingServers {
		nextServers[server.Name] = server.Config
	}
	switch action {
	case "upsert":
		config, ok := payload["config"].(map[string]any)
		if !ok {
			return fmt.Errorf("Missing MCP config for upsert action")
		}
		nextServers[name] = config
	case "remove":
		delete(nextServers, name)
	default:
		return fmt.Errorf("Unknown MCP save action: %s", action)
	}
	return s.writeNexServersToFile(scope, nextServers, projectPath)
}

func (s *Service) readDirectoryServersFromPreferredSources(scope, workspacePath string) ([]ServerRecord, error) {
	nexServers, err := s.readDirectoryServersFromFile(nexMcpDescriptor, scope, workspacePath)
	if err != nil {
		return nil, err
	}
	if len(nexServers) > 0 {
		return nexServers, nil
	}
	return s.readDirectoryServersFromFile(agentsMcpDescriptor, scope, workspacePath)
}

func (s *Service) readDirectoryServersFromFile(descriptor directoryMcpDescriptor, scope, workspacePath string) ([]ServerRecord, error) {
	filePath := s.buildDirectoryConfigPath(descriptor, scope, workspacePath)
	parsed, err := readJSONObject(filePath)
	if err != nil || parsed == nil {
		return nil, err
	}
	serverMap, changed := readServerMapWithLegacyMigration(parsed, descriptor.configKeyName)
	if changed {
		next := writeServerMapToJson(parsed, descriptor.configKeyName, serverMap)
		if err := s.writeTextAtomic(filePath, marshalIndentedNL(next)); err == nil {
			_ = err
		}
	}
	// Sorted by name for deterministic listing (Object.entries order in the
	// TS is insertion order; JSON objects from disk keep file order — Go
	// maps do not, so sort).
	names := make([]string, 0, len(serverMap))
	for name := range serverMap {
		names = append(names, name)
	}
	sort.Strings(names)
	var out []ServerRecord
	for _, name := range names {
		config, _ := serverMap[name].(map[string]any)
		if config == nil {
			config = map[string]any{}
		}
		out = append(out, ServerRecord{
			Name:    name,
			Config:  config,
			Enabled: readServerEnabled(config),
			Source:  descriptor.source,
			Path:    filePath,
			Scope:   scope,
		})
	}
	return out, nil
}

func (s *Service) writeNexServersToFile(scope string, servers map[string]any, workspacePath string) error {
	filePath := s.buildDirectoryConfigPath(nexMcpDescriptor, scope, workspacePath)
	current, err := readJSONObject(filePath)
	if err != nil {
		return err
	}
	if current == nil {
		current = map[string]any{}
	}
	next := writeServerMapToJson(current, nexMcpDescriptor.configKeyName, servers)
	return s.writeTextAtomic(filePath, marshalIndentedNL(next))
}

// writeServerEnabledToFile ports writeServerEnabledToFile.
func (s *Service) writeServerEnabledToFile(descriptor directoryMcpDescriptor, scope, projectPath, name string, enabled bool) error {
	filePath := s.buildDirectoryConfigPath(descriptor, scope, projectPath)
	current, err := readJSONObject(filePath)
	if err != nil {
		return err
	}
	if current == nil {
		current = map[string]any{}
	}
	serverMap := readServerMapFromJson(current, descriptor.configKeyName)
	currentServer, ok := serverMap[name].(map[string]any)
	if !ok {
		return nil
	}
	serverMap[name] = setServerEnabled(currentServer, enabled)
	next := writeServerMapToJson(current, descriptor.configKeyName, serverMap)
	return s.writeTextAtomic(filePath, marshalIndentedNL(next))
}
