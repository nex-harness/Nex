// Package pluginsync is the IPluginSyncService port (step 3.7): local
// inline-plugin candidates (config plugins.dirs), remote existence statuses,
// the tar.gz plugin archive export/import through the shared plugins
// archive codec, and the marketplace-source mirror export/import.
//
// Source: packages/services/src/plugin-sync/pluginSyncService.ts. The import
// extracts to a system temp dir, validates each plugin manifest (and that
// the manifest id matches the metadata), copies via a staging dir, and
// registers the dir in the user config with rollback on config-write
// failure (a half-synced dir would be misread as "already present" on
// retry).
package pluginsync

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/tylinux/nex/go/internal/plugins"
)

// ChannelName is the rpc channel name (ServiceChannels.PluginSync).
const ChannelName = "plugin-sync"

// DefaultMaxArchiveBytes ports DEFAULT_MAX_ARCHIVE_BYTES (50 MiB).
const DefaultMaxArchiveBytes = 50 * 1024 * 1024

const (
	inlinePluginMarketplace        = plugins.InlineMarketplace
	marketplaceSourceRootDirectory = "marketplace-sources"
	mirroredPluginRoot             = "plugins"
)

// Candidate ports PluginSyncCandidate.
type Candidate struct {
	ID              string   `json:"id"`
	Name            string   `json:"name"`
	PluginID        string   `json:"pluginId"`
	DirectoryName   string   `json:"directoryName"`
	Description     string   `json:"description,omitempty"`
	Version         string   `json:"version,omitempty"`
	Path            string   `json:"path"`
	SizeBytes       int64    `json:"sizeBytes"`
	Enabled         bool     `json:"enabled"`
	EnabledOverride *bool    `json:"enabledOverride,omitempty"`
	ComponentTypes  []string `json:"componentTypes"`
}

// CandidateListResult ports PluginSyncCandidateListResult.
type CandidateListResult struct {
	Candidates      []Candidate `json:"candidates"`
	MaxArchiveBytes int64       `json:"maxArchiveBytes"`
}

// RemoteStatus ports PluginSyncRemoteStatus.
type RemoteStatus struct {
	PluginID      string `json:"pluginId"`
	DirectoryName string `json:"directoryName"`
	Exists        bool   `json:"exists"`
	Path          string `json:"path,omitempty"`
	Reason        string `json:"reason,omitempty"` // samePluginId | targetExists
}

// RemoteStatusResult ports PluginSyncRemoteStatusResult.
type RemoteStatusResult struct {
	Statuses []RemoteStatus `json:"statuses"`
}

// ArchiveExportResult ports PluginSyncArchiveExportResult.
type ArchiveExportResult struct {
	Archive      []byte `json:"archive"`
	ArchiveBytes int    `json:"archiveBytes"`
	Plugins      []struct {
		ID            string `json:"id"`
		Name          string `json:"name"`
		PluginID      string `json:"pluginId"`
		DirectoryName string `json:"directoryName"`
		Enabled       *bool  `json:"enabled,omitempty"`
	} `json:"plugins"`
}

// MarketplaceSourceExportResult ports the export result shape.
type MarketplaceSourceExportResult struct {
	Archive       []byte   `json:"archive"`
	ArchiveBytes  int      `json:"archiveBytes"`
	MarketplaceID string   `json:"marketplaceId"`
	PluginNames   []string `json:"pluginNames"`
}

// MarketplaceSourceImportResult ports PluginSyncMarketplaceSourceImportResult.
type MarketplaceSourceImportResult struct {
	MarketplaceID string `json:"marketplaceId"`
	Path          string `json:"path"`
	Status        string `json:"status"` // synced | skipped
}

// ImportResultItem ports PluginSyncImportResultItem.
type ImportResultItem struct {
	Name          string `json:"name"`
	PluginID      string `json:"pluginId"`
	DirectoryName string `json:"directoryName"`
	Status        string `json:"status"` // synced | skipped | failed
	Path          string `json:"path,omitempty"`
	Error         string `json:"error,omitempty"`
}

// ImportResult ports PluginSyncImportResult.
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
	HomeDir         string
	MaxArchiveBytes int64
	// TempDir overrides the system temp root (tests).
	TempDir string
}

// Service is the IPluginSyncService implementation.
type Service struct {
	options         Options
	homeDir         string
	maxArchiveBytes int64
}

// New builds the service.
func New(options Options) *Service {
	if options.MaxArchiveBytes <= 0 {
		options.MaxArchiveBytes = DefaultMaxArchiveBytes
	}
	return &Service{options: options, homeDir: options.HomeDir, maxArchiveBytes: options.MaxArchiveBytes}
}

func (s *Service) userNexConfigPath() string {
	return filepath.Join(s.homeDir, ".nex", "cli", "config.json")
}

func (s *Service) userNexPluginRoot() string {
	return filepath.Join(s.homeDir, ".nex", "plugins")
}

func (s *Service) tempRoot(prefix string) string {
	base := s.options.TempDir
	if base == "" {
		base = os.TempDir()
	}
	return filepath.Join(base, prefix+"-"+randomHexID())
}

// ListLocalUserPluginCandidates ports listLocalUserPluginCandidates: the
// config's plugins.dirs entries with valid manifests, deduped by realpath.
func (s *Service) ListLocalUserPluginCandidates(ctx context.Context) (*CandidateListResult, error) {
	candidates, err := s.collectLocalUserPluginCandidates()
	if err != nil {
		return nil, err
	}
	return &CandidateListResult{Candidates: candidates, MaxArchiveBytes: s.maxArchiveBytes}, nil
}

type userPluginConfigState struct {
	dirs             []string
	enabledOverrides map[string]bool
}

func (s *Service) readUserPluginConfigState() (userPluginConfigState, error) {
	raw, err := os.ReadFile(s.userNexConfigPath())
	if err != nil {
		if os.IsNotExist(err) {
			return userPluginConfigState{enabledOverrides: map[string]bool{}}, nil
		}
		return userPluginConfigState{}, err
	}
	var parsed map[string]any
	if err := json.Unmarshal(raw, &parsed); err != nil || parsed == nil {
		return userPluginConfigState{}, fmt.Errorf("invalid json object: %s", s.userNexConfigPath())
	}
	state := userPluginConfigState{enabledOverrides: map[string]bool{}}
	if pluginsConfig, ok := parsed["plugins"].(map[string]any); ok {
		if list, ok := pluginsConfig["dirs"].([]any); ok {
			for _, item := range list {
				if str, ok := item.(string); ok && strings.TrimSpace(str) != "" {
					state.dirs = append(state.dirs, str)
				}
			}
		}
		if record, ok := pluginsConfig["enabledPlugins"].(map[string]any); ok {
			for key, value := range record {
				if b, ok := value.(bool); ok {
					state.enabledOverrides[normalizePluginIDKey(key)] = b
				}
			}
		}
	}
	return state, nil
}
