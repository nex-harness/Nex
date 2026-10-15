// archive.go: the export/import pipelines (plugin archives and marketplace
// source mirrors) with their size caps, staging-dir installs, and config
// write-backs.
package pluginsync

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/tylinux/nex/go/internal/plugins"
	"github.com/tylinux/nex/go/internal/skills"
)

// ExportPluginsArchive ports exportPluginsArchive.
func (s *Service) ExportPluginsArchive(ctx context.Context, pluginIDs []string) (*ArchiveExportResult, error) {
	candidates, err := s.collectLocalUserPluginCandidates()
	if err != nil {
		return nil, err
	}
	candidateByID := map[string]Candidate{}
	for _, candidate := range candidates {
		candidateByID[candidate.ID] = candidate
	}
	var selected []Candidate
	var selectedBytes int64
	for _, id := range pluginIDs {
		candidate, ok := candidateByID[id]
		if !ok {
			return nil, fmt.Errorf("plugin sync candidate not found: %s", id)
		}
		selected = append(selected, candidate)
		selectedBytes += candidate.SizeBytes
	}
	if selectedBytes > s.maxArchiveBytes {
		return nil, &plugins.ArchiveExceedsLimitError{ActualBytes: selectedBytes, MaxBytes: s.maxArchiveBytes}
	}
	entries := make([]plugins.ArchiveEntry, 0, len(selected))
	metadata := plugins.ArchiveMetadata{}
	for _, candidate := range selected {
		sourcePath, err := filepath.EvalSymlinks(candidate.Path)
		if err != nil {
			sourcePath = candidate.Path
		}
		entries = append(entries, plugins.ArchiveEntry{SourcePath: sourcePath, ArchivePath: candidate.DirectoryName})
		metadata.Plugins = append(metadata.Plugins, plugins.ArchivePluginMetadata{
			Name:          candidate.Name,
			PluginID:      candidate.PluginID,
			DirectoryName: candidate.DirectoryName,
			Enabled:       candidate.EnabledOverride,
		})
	}
	archive, err := plugins.CreateSyncArchive(entries, metadata)
	if err != nil {
		return nil, err
	}
	if int64(len(archive)) > s.maxArchiveBytes {
		return nil, &plugins.ArchiveExceedsLimitError{ActualBytes: int64(len(archive)), MaxBytes: s.maxArchiveBytes}
	}
	result := &ArchiveExportResult{Archive: archive, ArchiveBytes: len(archive)}
	for _, candidate := range selected {
		result.Plugins = append(result.Plugins, struct {
			ID            string `json:"id"`
			Name          string `json:"name"`
			PluginID      string `json:"pluginId"`
			DirectoryName string `json:"directoryName"`
			Enabled       *bool  `json:"enabled,omitempty"`
		}{ID: candidate.ID, Name: candidate.Name, PluginID: candidate.PluginID, DirectoryName: candidate.DirectoryName, Enabled: candidate.EnabledOverride})
	}
	return result, nil
}

// ImportPluginsArchive ports importPluginsArchive (overwrite refused).
func (s *Service) ImportPluginsArchive(ctx context.Context, archive []byte, overwrite bool) (*ImportResult, error) {
	if overwrite {
		return nil, fmt.Errorf("plugin sync overwrite is not supported")
	}
	if int64(len(archive)) > s.maxArchiveBytes {
		return nil, &plugins.ArchiveExceedsLimitError{ActualBytes: int64(len(archive)), MaxBytes: s.maxArchiveBytes}
	}
	tempRoot := s.tempRoot("nex-plugin-sync")
	defer os.RemoveAll(tempRoot)
	if err := plugins.ExtractSyncArchive(archive, tempRoot, plugins.ExtractSyncArchiveOptions{
		MaxExtractedBytes: s.maxArchiveBytes,
	}); err != nil {
		return nil, err
	}
	metadata, err := readArchiveMetadata(tempRoot)
	if err != nil {
		return nil, err
	}
	targetRoot := s.userNexPluginRoot()
	existingPluginPathByID, err := s.collectConfiguredInlinePluginPathByID()
	if err != nil {
		return nil, err
	}
	result := &ImportResult{}
	for _, plugin := range metadata.Plugins {
		directoryName, err := skills.NormalizeSyncRelativePath(plugin.DirectoryName, "")
		if err != nil {
			return nil, err
		}
		pluginID, err := normalizePluginID(plugin.PluginID)
		if err != nil {
			return nil, err
		}
		extractedPath, err := skills.ResolveSyncPathWithin(tempRoot, directoryName, "")
		if err != nil {
			return nil, err
		}
		targetPath, err := skills.ResolveSyncPathWithin(targetRoot, directoryName, "")
		if err != nil {
			return nil, err
		}
		stagingPath := ""
		manifest := readPluginManifestInfo(extractedPath)
		if manifest == nil {
			result.Results = append(result.Results, ImportResultItem{
				Name: plugin.Name, PluginID: pluginID, DirectoryName: directoryName,
				Status: "failed", Error: fmt.Sprintf("plugin manifest not found: %s", directoryName),
			})
			continue
		}
		if normalizePluginIDKey(manifest.pluginID) != normalizePluginIDKey(pluginID) {
			result.Results = append(result.Results, ImportResultItem{
				Name: plugin.Name, PluginID: pluginID, DirectoryName: directoryName,
				Status: "failed", Error: fmt.Sprintf("plugin manifest id mismatch: %s !== %s", manifest.pluginID, pluginID),
			})
			continue
		}
		if pathExists(targetPath) {
			result.Results = append(result.Results, ImportResultItem{
				Name: manifest.name, PluginID: pluginID, DirectoryName: directoryName,
				Status: "skipped", Path: targetPath,
			})
			continue
		}
		if existingPath, ok := existingPluginPathByID[normalizePluginIDKey(pluginID)]; ok {
			result.Results = append(result.Results, ImportResultItem{
				Name: manifest.name, PluginID: pluginID, DirectoryName: directoryName,
				Status: "skipped", Path: existingPath,
			})
			continue
		}
		if err := os.MkdirAll(filepath.Dir(targetPath), 0o755); err != nil {
			result.Results = append(result.Results, ImportResultItem{
				Name: manifest.name, PluginID: pluginID, DirectoryName: directoryName,
				Status: "failed", Error: err.Error(),
			})
			continue
		}
		stagingPath, err = skills.ResolveSyncPathWithin(targetRoot,
			".importing-"+toSafeDirectoryName(directoryName)+"-"+randomHexID(), "")
		if err != nil {
			result.Results = append(result.Results, ImportResultItem{
				Name: manifest.name, PluginID: pluginID, DirectoryName: directoryName,
				Status: "failed", Error: err.Error(),
			})
			continue
		}
		if err := copyTreeNoOverwrite(extractedPath, stagingPath); err != nil {
			os.RemoveAll(stagingPath)
			result.Results = append(result.Results, ImportResultItem{
				Name: manifest.name, PluginID: pluginID, DirectoryName: directoryName,
				Status: "failed", Error: err.Error(),
			})
			continue
		}
		if pathExists(targetPath) {
			os.RemoveAll(stagingPath)
			result.Results = append(result.Results, ImportResultItem{
				Name: manifest.name, PluginID: pluginID, DirectoryName: directoryName,
				Status: "failed", Error: fmt.Sprintf("plugin target already exists: %s", targetPath),
			})
			continue
		}
		if err := os.Rename(stagingPath, targetPath); err != nil {
			os.RemoveAll(stagingPath)
			result.Results = append(result.Results, ImportResultItem{
				Name: manifest.name, PluginID: pluginID, DirectoryName: directoryName,
				Status: "failed", Error: err.Error(),
			})
			continue
		}
		// The agent runtime does not discover a bare directory; register the
		// remote absolute path in this remote service's user config.
		if err := s.addPluginDirToUserConfig(targetPath, pluginID, plugin.Enabled); err != nil {
			// A dir on disk with a failed config write is half-synced state;
			// a retry would misread it as already present — roll back.
			os.RemoveAll(targetPath)
			result.Results = append(result.Results, ImportResultItem{
				Name: manifest.name, PluginID: pluginID, DirectoryName: directoryName,
				Status: "failed", Error: err.Error(),
			})
			continue
		}
		existingPluginPathByID[normalizePluginIDKey(pluginID)] = targetPath
		result.Results = append(result.Results, ImportResultItem{
			Name: manifest.name, PluginID: pluginID, DirectoryName: directoryName,
			Status: "synced", Path: targetPath,
		})
	}
	return result, nil
}

func readArchiveMetadata(tempRoot string) (*plugins.ArchiveMetadata, error) {
	raw, err := os.ReadFile(filepath.Join(tempRoot, plugins.MetadataArchivePath))
	if err != nil {
		return nil, fmt.Errorf("invalid plugin sync archive metadata")
	}
	var parsed struct {
		Plugins []json.RawMessage `json:"plugins"`
	}
	if err := json.Unmarshal(raw, &parsed); err != nil {
		return nil, fmt.Errorf("invalid plugin sync archive metadata")
	}
	metadata := &plugins.ArchiveMetadata{}
	for _, item := range parsed.Plugins {
		var record struct {
			Name          string `json:"name"`
			PluginID      string `json:"pluginId"`
			DirectoryName string `json:"directoryName"`
			Enabled       *bool  `json:"enabled"`
		}
		if err := json.Unmarshal(item, &record); err != nil {
			return nil, fmt.Errorf("invalid plugin sync archive plugin metadata")
		}
		if strings.TrimSpace(record.Name) == "" {
			return nil, fmt.Errorf("missing plugin metadata name")
		}
		if _, err := normalizePluginID(record.PluginID); err != nil {
			return nil, err
		}
		directoryName, err := skills.NormalizeSyncRelativePath(record.DirectoryName, "")
		if err != nil {
			return nil, err
		}
		metadata.Plugins = append(metadata.Plugins, plugins.ArchivePluginMetadata{
			Name:          strings.TrimSpace(record.Name),
			PluginID:      strings.TrimSpace(record.PluginID),
			DirectoryName: directoryName,
			Enabled:       record.Enabled,
		})
	}
	return metadata, nil
}

// addPluginDirToUserConfig ports addPluginDirToUserConfig.
func (s *Service) addPluginDirToUserConfig(pluginPath, pluginID string, enabledOverride *bool) error {
	filePath := s.userNexConfigPath()
	parsed := map[string]any{}
	if raw, err := os.ReadFile(filePath); err == nil {
		if err := json.Unmarshal(raw, &parsed); err != nil || parsed == nil {
			return fmt.Errorf("invalid json object: %s", filePath)
		}
	}
	pluginsConfig, _ := parsed["plugins"].(map[string]any)
	if pluginsConfig == nil {
		pluginsConfig = map[string]any{}
	}
	resolvedPluginPath, err := filepath.Abs(pluginPath)
	if err != nil {
		resolvedPluginPath = pluginPath
	}
	var dirs []string
	if list, ok := pluginsConfig["dirs"].([]any); ok {
		for _, item := range list {
			if str, ok := item.(string); ok {
				dirs = append(dirs, str)
			}
		}
	}
	nextDirs := dirs
	alreadyListed := false
	for _, item := range dirs {
		if resolved, err := filepath.Abs(item); err == nil && resolved == resolvedPluginPath {
			alreadyListed = true
			break
		}
	}
	if !alreadyListed {
		nextDirs = append(dirs, resolvedPluginPath)
	}
	enabledPlugins, _ := pluginsConfig["enabledPlugins"].(map[string]any)
	next := map[string]any{}
	for key, value := range parsed {
		next[key] = value
	}
	nextPlugins := map[string]any{}
	for key, value := range pluginsConfig {
		nextPlugins[key] = value
	}
	nextPlugins["dirs"] = nextDirs
	if enabledOverride != nil {
		if enabledPlugins == nil {
			enabledPlugins = map[string]any{}
		}
		enabledPlugins[pluginID] = *enabledOverride
		nextPlugins["enabledPlugins"] = enabledPlugins
	} else if len(enabledPlugins) > 0 {
		nextPlugins["enabledPlugins"] = enabledPlugins
	}
	next["plugins"] = nextPlugins
	return s.writeJSONFileAtomic(filePath, next)
}

func (s *Service) writeJSONFileAtomic(filePath string, value map[string]any) error {
	if err := os.MkdirAll(filepath.Dir(filePath), 0o755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	tempPath := filepath.Join(filepath.Dir(filePath), ".tmp-"+filepath.Base(filePath)+"-"+randomHexID())
	if err := os.WriteFile(tempPath, data, 0o600); err != nil {
		return err
	}
	return os.Rename(tempPath, filePath)
}

// CheckRemoteUserPluginWriteAccess ports checkRemoteUserPluginWriteAccess:
// both the plugin root and the config directory must be writable.
func (s *Service) CheckRemoteUserPluginWriteAccess(ctx context.Context) (*WriteAccessResult, error) {
	for _, dir := range []string{s.userNexPluginRoot(), filepath.Dir(s.userNexConfigPath())} {
		if result := checkDirectoryWriteAccess(dir); !result.OK {
			return result, nil
		}
	}
	return &WriteAccessResult{OK: true, Path: s.userNexPluginRoot() + "," + filepath.Dir(s.userNexConfigPath())}, nil
}

func checkDirectoryWriteAccess(directoryPath string) *WriteAccessResult {
	if err := os.MkdirAll(directoryPath, 0o755); err != nil {
		return &WriteAccessResult{OK: false, Path: directoryPath, Error: err.Error()}
	}
	markerPath := filepath.Join(directoryPath,
		fmt.Sprintf(".nex-sync-preflight-%d-%s", os.Getpid(), randomHexID()))
	if err := os.WriteFile(markerPath, []byte("ok"), 0o644); err != nil {
		os.Remove(markerPath)
		return &WriteAccessResult{OK: false, Path: directoryPath, Error: err.Error()}
	}
	os.Remove(markerPath)
	return &WriteAccessResult{OK: true, Path: directoryPath}
}

func copyTreeNoOverwrite(source, target string) error {
	if _, err := os.Lstat(target); err == nil {
		return fmt.Errorf("target already exists: %s", target)
	}
	info, err := os.Lstat(source)
	if err != nil {
		return err
	}
	if info.Mode().IsRegular() {
		data, err := os.ReadFile(source)
		if err != nil {
			return err
		}
		return os.WriteFile(target, data, info.Mode().Perm())
	}
	if !info.IsDir() {
		return nil
	}
	if err := os.MkdirAll(target, 0o755); err != nil {
		return err
	}
	entries, err := os.ReadDir(source)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if err := copyTreeNoOverwrite(filepath.Join(source, entry.Name()), filepath.Join(target, entry.Name())); err != nil {
			return err
		}
	}
	return nil
}
