// service.go: the IHooksService port — loadHooks (project snapshot + legacy
// agents/claude configs + user nex source, with the trust-digest overlay and
// the trustStoreCorrupt flag) and saveHooks (split user/project writes
// through the atomic config writer).
//
// Source: packages/services/src/hooks/hooksService.ts. grantWorkspaceHookTrust
// forwards to the Agent authority — Phase 4 (the agent bridge) owns the real
// implementation; this port exposes the load/save surface only.
package hooktrust

import (
	"context"
	"path/filepath"
	"strings"
	"time"
)

func timeNowUTC() time.Time { return time.Now().UTC() }

// ChannelName is the rpc channel name (ServiceChannels.Hooks).
const ChannelName = "hooks"

// Hook is the settings-model Hook (packages/shared/src/settings-directory.ts
// shape consumed by the UI).
type Hook struct {
	ID              string              `json:"id"`
	Event           string              `json:"event"`
	Matcher         string              `json:"matcher,omitempty"`
	Type            string              `json:"type"` // command | process
	Command         string              `json:"command"`
	Args            []string            `json:"args,omitempty"`
	Async           *bool               `json:"async,omitempty"`
	Shell           any                 `json:"shell,omitempty"`
	StatusMessage   string              `json:"statusMessage,omitempty"`
	Timeout         *float64            `json:"timeout,omitempty"`
	Enabled         bool                `json:"enabled"`
	Editable        bool                `json:"editable"`
	ConfiguredState *ConfiguredState    `json:"configuredState,omitempty"`
	WorkspaceHook   *WorkspaceHookState `json:"workspaceHook,omitempty"`
	Location        HookLocation        `json:"location"`
}

// ConfiguredState ports HookConfiguredState.
type ConfiguredState struct {
	SourceRootEnabled   bool   `json:"sourceRootEnabled"`
	DeclarationEnabled  bool   `json:"declarationEnabled"`
	RuntimeHooksEnabled bool   `json:"runtimeHooksEnabled"`
	ConfiguredEnabled   bool   `json:"configuredEnabled"`
	SourcePath          string `json:"sourcePath"`
}

// WorkspaceHookState is the workspace-hook portion of a Hook.
type WorkspaceHookState struct {
	ConfiguredState
	ReviewItemID          string `json:"reviewItemId"`
	SourceFileIndex       int    `json:"sourceFileIndex"`
	WorkspaceIdentity     string `json:"workspaceIdentity"`
	BundleDigest          string `json:"bundleDigest"`
	HookDeclarationDigest string `json:"hookDeclarationDigest"`
	// TrustState: trusted_persistent | pending_trust.
	TrustState string `json:"trustState"`
}

// HookLocation ports SettingsDirectoryLocation.
type HookLocation struct {
	Source        string `json:"source"` // nex | agents | claude
	Scope         string `json:"scope"`  // user | project
	DirectoryPath string `json:"directoryPath"`
	ProjectPath   string `json:"projectPath,omitempty"`
}

// LoadResult is the loadHooks result.
type LoadResult struct {
	Hooks                 []Hook              `json:"hooks"`
	HooksEnabled          bool                `json:"hooksEnabled"`
	WorkspaceHookSnapshot *BundleSnapshotData `json:"workspaceHookSnapshot,omitempty"`
	// TrustStoreCorrupt: fail-closed marker — every hook is treated as
	// un-persisted-trust until the store is readable again.
	TrustStoreCorrupt bool `json:"trustStoreCorrupt,omitempty"`
}

// BundleSnapshotData ports WorkspaceHookBundleSnapshotData.
type BundleSnapshotData struct {
	SchemaVersion     int              `json:"schemaVersion"`
	WorkspaceIdentity string           `json:"workspaceIdentity"`
	DiscoveredAt      string           `json:"discoveredAt"`
	SourceFiles       []SourceFileData `json:"sourceFiles"`
	Hooks             []EntryData      `json:"hooks"`
	DigestAlgorithm   string           `json:"digestAlgorithm"`
	BundleDigest      string           `json:"bundleDigest"`
}

// SourceFileData is one snapshot sourceFiles entry.
type SourceFileData struct {
	CanonicalPath         string    `json:"canonicalPath"`
	BaseDir               string    `json:"baseDir"`
	DiscoveryOrder        int       `json:"discoveryOrder"`
	ConfigFileKind        string    `json:"configFileKind"`
	ExplicitProjectConfig bool      `json:"explicitProjectConfig"`
	Editable              bool      `json:"editable"`
	HooksRoot             HooksRoot `json:"hooksRoot"`
}

// HooksRoot is the sourceFile.hooksRoot shape.
type HooksRoot struct {
	Enabled        *bool    `json:"enabled,omitempty"`
	TimeoutMs      *float64 `json:"timeoutMs,omitempty"`
	MaxOutputBytes *float64 `json:"maxOutputBytes,omitempty"`
}

// EntryData ports CanonicalWorkspaceHookEntryData.
type EntryData struct {
	ReviewItemID               string   `json:"reviewItemId"`
	Event                      string   `json:"event"`
	MatcherIndex               int      `json:"matcherIndex"`
	HookIndex                  int      `json:"hookIndex"`
	SourceFileIndex            int      `json:"sourceFileIndex"`
	SourceRelativePath         string   `json:"sourceRelativePath"`
	Matcher                    *string  `json:"matcher"`
	Type                       string   `json:"type"`
	Command                    string   `json:"command"`
	Args                       []string `json:"args,omitempty"`
	Async                      *bool    `json:"async,omitempty"`
	Shell                      any      `json:"shell,omitempty"`
	ResolvedTimeoutMs          int64    `json:"resolvedTimeoutMs"`
	ResolvedMaxOutputBytes     int64    `json:"resolvedMaxOutputBytes"`
	StatusMessage              string   `json:"statusMessage,omitempty"`
	SourceRootEnabled          bool     `json:"sourceRootEnabled"`
	DeclarationEnabled         bool     `json:"declarationEnabled"`
	RuntimeHooksEnabled        bool     `json:"runtimeHooksEnabled"`
	ConfiguredEnabled          bool     `json:"configuredEnabled"`
	Editable                   bool     `json:"editable"`
	DeclarationDigestAlgorithm string   `json:"declarationDigestAlgorithm"`
	HookDeclarationDigest      string   `json:"hookDeclarationDigest"`
}

// Options carry the seams.
type Options struct {
	HomeDir string
	// NowISO supplies discoveredAt timestamps.
	NowISO func() string
}

// Service is the IHooksService implementation.
type Service struct {
	options Options
	homeDir string
}

// New builds the service.
func New(options Options) *Service {
	if options.NowISO == nil {
		options.NowISO = defaultNowISO
	}
	return &Service{options: options, homeDir: options.HomeDir}
}

func (s *Service) userNexConfigPath() string {
	return filepath.Join(s.homeDir, ".nex", "cli", "config.json")
}

// getConfigPath ports getConfigPath: nex → <root>/config.json (user root is
// ~/.nex/cli); agents/claude → settings.json.
func (s *Service) getConfigPath(source string, workspacePath string) string {
	baseDir := s.homeDir
	if workspacePath != "" {
		baseDir = workspacePath
	}
	switch source {
	case "nex":
		if workspacePath == "" {
			return filepath.Join(baseDir, ".nex", "cli", "config.json")
		}
		return filepath.Join(baseDir, ".nex", "config.json")
	case "agents":
		return filepath.Join(baseDir, ".agents", "settings.json")
	default:
		return filepath.Join(baseDir, ".claude", "settings.json")
	}
}

// LoadHooks ports loadHooksImpl.
func (s *Service) LoadHooks(ctx context.Context, workspacePath, workspaceIdentity string) (*LoadResult, error) {
	absoluteWorkspace, err := filepath.Abs(workspacePath)
	if err != nil {
		absoluteWorkspace = workspacePath
	}
	identity := strings.TrimSpace(workspaceIdentity)
	if identity == "" {
		identity = absoluteWorkspace
	}
	projectSources, _ := ReadProjectSources(absoluteWorkspace, "")
	userSource := s.readUserNexSource()
	roots := []*HooksConfig{}
	if userSource != nil {
		roots = append(roots, userSource.Hooks)
	}
	for _, source := range projectSources {
		roots = append(roots, source.Hooks)
	}
	runtimeRoot := ResolveRuntimeRoot(roots)
	snapshot := s.buildBundleSnapshot(identity, absoluteWorkspace, projectSources, runtimeRoot)
	persistentDigests, corrupt := s.readPersistentTrustDigests(identity)
	hooks := s.fromProjectSnapshot(projectSources, snapshot, identity, absoluteWorkspace, persistentDigests)
	hooks = append(hooks, s.loadLegacyHooksFromLocation("agents", absoluteWorkspace)...)
	hooks = append(hooks, s.loadLegacyHooksFromLocation("claude", absoluteWorkspace)...)
	hooks = append(hooks, s.fromUserNexSource(userSource, runtimeRoot, absoluteWorkspace)...)
	hooks = append(hooks, s.loadLegacyHooksFromLocation("agents", "")...)
	hooks = append(hooks, s.loadLegacyHooksFromLocation("claude", "")...)
	result := &LoadResult{Hooks: hooks, HooksEnabled: false}
	for _, hook := range hooks {
		if hook.Enabled {
			result.HooksEnabled = true
			break
		}
	}
	result.WorkspaceHookSnapshot = snapshot
	result.TrustStoreCorrupt = corrupt
	return result, nil
}
