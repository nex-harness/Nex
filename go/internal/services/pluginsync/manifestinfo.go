// manifestinfo.go: the plugin manifest summary (name/id/description/
// version/component types) and the candidate-directory naming helpers.
package pluginsync

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/tylinux/nex/go/internal/plugins"
	"github.com/tylinux/nex/go/internal/skills"
)

// pluginManifestInfo ports PluginManifestInfo.
type pluginManifestInfo struct {
	name           string
	pluginID       string
	description    string
	version        string
	componentTypes []string
}

func readPluginManifestInfo(pluginRoot string) *pluginManifestInfo {
	manifest, err := plugins.ReadManifest(pluginRoot)
	if err != nil || manifest == nil {
		return nil
	}
	info := &pluginManifestInfo{
		name:           manifest.Name,
		pluginID:       manifest.Name + "@" + inlinePluginMarketplace,
		description:    manifest.Description,
		version:        manifest.Version,
		componentTypes: resolvePluginComponentTypes(pluginRoot, manifest),
	}
	return info
}

func resolvePluginComponentTypes(pluginRoot string, manifest *plugins.Manifest) []string {
	var componentTypes []string
	var raw map[string]any
	hasKey := func(key string) bool {
		if raw == nil && manifest.Raw != nil {
			json.Unmarshal(manifest.Raw, &raw)
		}
		_, ok := raw[key]
		return ok
	}
	if hasKey("skills") || pathExists(filepath.Join(pluginRoot, "skills")) {
		componentTypes = append(componentTypes, "skills")
	}
	if hasKey("commands") || pathExists(filepath.Join(pluginRoot, "commands")) {
		componentTypes = append(componentTypes, "commands")
	}
	if hasKey("hooks") || pathExists(filepath.Join(pluginRoot, "hooks", "hooks.json")) {
		componentTypes = append(componentTypes, "hooks")
	}
	if hasKey("mcpServers") || pathExists(filepath.Join(pluginRoot, ".mcp.json")) {
		componentTypes = append(componentTypes, "mcp")
	}
	return componentTypes
}

func (s *Service) collectLocalUserPluginCandidates() ([]Candidate, error) {
	config, err := s.readUserPluginConfigState()
	if err != nil {
		return nil, err
	}
	var candidates []Candidate
	seenRealpaths := map[string]bool{}
	usedDirectoryNames := map[string]bool{}
	for _, rawDir := range config.dirs {
		pluginRoot, err := filepath.Abs(rawDir)
		if err != nil {
			pluginRoot = rawDir
		}
		canonicalRoot := pluginRoot
		if resolved, err := filepath.EvalSymlinks(pluginRoot); err == nil {
			canonicalRoot = resolved
		}
		if seenRealpaths[canonicalRoot] {
			continue
		}
		seenRealpaths[canonicalRoot] = true
		manifest := readPluginManifestInfo(canonicalRoot)
		if manifest == nil {
			continue
		}
		enabledOverride, hasOverride := config.enabledOverrides[normalizePluginIDKey(manifest.pluginID)]
		sizeBytes, err := plugins.RecursiveSize(canonicalRoot)
		if err != nil {
			return nil, err
		}
		candidate := Candidate{
			ID:             skills.ShortHash(manifest.pluginID + ":" + canonicalRoot),
			Name:           manifest.name,
			PluginID:       manifest.pluginID,
			DirectoryName:  createUniqueDirectoryName(filepath.Base(canonicalRoot), manifest.name, canonicalRoot, usedDirectoryNames),
			Description:    manifest.description,
			Version:        manifest.version,
			Path:           canonicalRoot,
			SizeBytes:      sizeBytes,
			Enabled:        true,
			ComponentTypes: manifest.componentTypes,
		}
		if hasOverride {
			candidate.Enabled = enabledOverride
			value := enabledOverride
			candidate.EnabledOverride = &value
		}
		candidates = append(candidates, candidate)
	}
	sort.Slice(candidates, func(i, j int) bool { return candidates[i].Name < candidates[j].Name })
	return candidates, nil
}

func createUniqueDirectoryName(directoryBasename, pluginName, pluginRoot string, used map[string]bool) string {
	baseName := toSafeDirectoryName(directoryBasename)
	if baseName == "" || baseName == "plugin" {
		baseName = toSafeDirectoryName(pluginName)
	}
	if !used[baseName] {
		used[baseName] = true
		return baseName
	}
	dedupedName := toSafeDirectoryName(baseName + "-" + skills.ShortHash(pluginRoot)[:8])
	used[dedupedName] = true
	return dedupedName
}

func toSafeDirectoryName(value string) string {
	sanitized := strings.NewReplacer("\\", "-", "/", "-", ":", "-").Replace(strings.TrimSpace(value))
	if sanitized == "" {
		return "plugin-" + skills.ShortHash(value)[:8]
	}
	if normalized, err := skills.NormalizeSyncRelativePath(sanitized, ""); err == nil {
		return normalized
	}
	return "plugin-" + skills.ShortHash(value)[:8]
}

// ListRemoteUserPluginStatuses ports listRemoteUserPluginStatuses.
func (s *Service) ListRemoteUserPluginStatuses(ctx context.Context, pluginRefs []struct{ PluginID, DirectoryName string }) (*RemoteStatusResult, error) {
	targetRoot := s.userNexPluginRoot()
	existingPluginPathByID, err := s.collectConfiguredInlinePluginPathByID()
	if err != nil {
		return nil, err
	}
	statuses := make([]RemoteStatus, 0, len(pluginRefs))
	for _, ref := range pluginRefs {
		directoryName, err := skills.NormalizeSyncRelativePath(ref.DirectoryName, "")
		if err != nil {
			return nil, err
		}
		pluginID, err := normalizePluginID(ref.PluginID)
		if err != nil {
			return nil, err
		}
		targetPath, err := skills.ResolveSyncPathWithin(targetRoot, directoryName, "")
		if err != nil {
			return nil, err
		}
		if _, statErr := os.Stat(targetPath); statErr == nil {
			statuses = append(statuses, RemoteStatus{PluginID: pluginID, DirectoryName: directoryName, Exists: true, Path: targetPath, Reason: "targetExists"})
			continue
		}
		if existingPath, ok := existingPluginPathByID[normalizePluginIDKey(pluginID)]; ok {
			statuses = append(statuses, RemoteStatus{PluginID: pluginID, DirectoryName: directoryName, Exists: true, Path: existingPath, Reason: "samePluginId"})
			continue
		}
		statuses = append(statuses, RemoteStatus{PluginID: pluginID, DirectoryName: directoryName, Exists: false})
	}
	return &RemoteStatusResult{Statuses: statuses}, nil
}

func (s *Service) collectConfiguredInlinePluginPathByID() (map[string]string, error) {
	config, err := s.readUserPluginConfigState()
	if err != nil {
		return nil, err
	}
	result := map[string]string{}
	for _, rawDir := range config.dirs {
		pluginRoot, err := filepath.Abs(rawDir)
		if err != nil {
			pluginRoot = rawDir
		}
		canonicalRoot := pluginRoot
		if resolved, err := filepath.EvalSymlinks(pluginRoot); err == nil {
			canonicalRoot = resolved
		}
		manifest := readPluginManifestInfo(canonicalRoot)
		if manifest == nil {
			continue
		}
		key := normalizePluginIDKey(manifest.pluginID)
		if _, ok := result[key]; !ok {
			result[key] = canonicalRoot
		}
	}
	return result, nil
}

func normalizePluginID(pluginID string) (string, error) {
	normalized := strings.TrimSpace(pluginID)
	if normalized == "" || strings.Contains(normalized, "/") || strings.Contains(normalized, "\\") {
		return "", fmt.Errorf("invalid plugin id: %s", pluginID)
	}
	return normalized, nil
}

func normalizePluginIDKey(pluginID string) string {
	normalized, err := normalizePluginID(pluginID)
	if err != nil {
		return strings.ToLower(pluginID)
	}
	return strings.ToLower(normalized)
}

func randomHexID() string {
	buffer := make([]byte, 16)
	if _, err := rand.Read(buffer); err != nil {
		return "00000000000000000000000000000000"
	}
	return hex.EncodeToString(buffer)
}

func pathExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}
