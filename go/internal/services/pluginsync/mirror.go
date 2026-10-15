// mirror.go: the marketplace-source mirror internals — dependency closure,
// local-source resolution with the manifest-required guard, and the mirrored
// manifest construction.
package pluginsync

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/tylinux/nex/go/internal/plugins"
	"github.com/tylinux/nex/go/internal/skills"
)

// resolveMarketplacePluginClosure ports resolveMarketplacePluginClosure.
func resolveMarketplacePluginClosure(manifest *plugins.MarketplaceManifest, marketplaceID string, pluginNames []string) ([]plugins.MarketplacePluginEntry, error) {
	byName := map[string]plugins.MarketplacePluginEntry{}
	for _, entry := range manifest.Plugins {
		byName[entry.Name] = entry
	}
	visited := map[string]bool{}
	var result []plugins.MarketplacePluginEntry
	var visit func(pluginName, requiredBy string) error
	visit = func(pluginName, requiredBy string) error {
		trimmed := strings.TrimSpace(pluginName)
		if trimmed == "" {
			return fmt.Errorf("missing marketplace plugin name")
		}
		if visited[trimmed] {
			return nil
		}
		entry, ok := byName[trimmed]
		if !ok {
			return fmt.Errorf("marketplace plugin not found: %s", trimmed)
		}
		visited[trimmed] = true
		result = append(result, entry)
		for _, dependency := range entry.Dependencies {
			normalized, err := normalizeMarketplaceDependency(dependency, marketplaceID, requiredBy)
			if err != nil {
				return err
			}
			if err := visit(normalized, entry.Name); err != nil {
				return err
			}
		}
		return nil
	}
	for _, pluginName := range pluginNames {
		if err := visit(pluginName, pluginName); err != nil {
			return nil, err
		}
	}
	return result, nil
}

func normalizeMarketplaceDependency(dependency, marketplaceID, requiredBy string) (string, error) {
	trimmed := strings.TrimSpace(dependency)
	separatorIndex := strings.LastIndex(trimmed, "@")
	if separatorIndex < 0 {
		return trimmed, nil
	}
	name := trimmed[:separatorIndex]
	marketplace := trimmed[separatorIndex+1:]
	if marketplace == marketplaceID {
		return name, nil
	}
	return "", fmt.Errorf("cross-marketplace dependency is not supported for SSH source mirror: %s required by %s", dependency, requiredBy)
}

func isRemoteMarketplacePluginSource(source any) bool {
	record, ok := source.(map[string]any)
	if !ok {
		return false
	}
	sourceKind, _ := record["source"].(string)
	switch sourceKind {
	case "github", "git", "url", "git-subdir", "npm", "pip":
		return true
	}
	return false
}

func (s *Service) resolveLocalMarketplacePluginSource(loaded *marketplaceSourceLoadResult, entry plugins.MarketplacePluginEntry) (string, error) {
	switch source := entry.Source.(type) {
	case string:
		return s.resolveLocalMarketplacePluginSourcePath(loaded, source, entry.Name+" source", false)
	case map[string]any:
		if kind, _ := source["source"].(string); kind == "directory" {
			if path, ok := source["path"].(string); ok {
				return s.resolveLocalMarketplacePluginSourcePath(loaded, path, entry.Name+" directory source", loaded.sourceRoot == "")
			}
		}
		return s.resolveLocalMarketplacePluginFallback(loaded, entry)
	default:
		return s.resolveLocalMarketplacePluginFallback(loaded, entry)
	}
}

func (s *Service) resolveLocalMarketplacePluginFallback(loaded *marketplaceSourceLoadResult, entry plugins.MarketplacePluginEntry) (string, error) {
	if loaded.sourceRoot == "" {
		return "", fmt.Errorf("cannot mirror local marketplace plugin source without source root: %s", entry.Name)
	}
	pluginBaseDir, err := resolveMarketplacePluginBaseDir(loaded)
	if err != nil {
		return "", err
	}
	fallbackPath, err := skills.ResolveSyncPathWithin(pluginBaseDir, entry.Name, "unsafe marketplace plugin source path")
	if err != nil {
		return "", err
	}
	if directoryExists(fallbackPath) {
		return assertLocalMarketplacePluginSourceRoot(fallbackPath)
	}
	return "", fmt.Errorf("local marketplace plugin source does not exist: %s", fallbackPath)
}

func (s *Service) resolveLocalMarketplacePluginSourcePath(loaded *marketplaceSourceLoadResult, sourcePath, label string, allowSettingsAbsolute bool) (string, error) {
	trimmed := strings.TrimSpace(sourcePath)
	if trimmed == "" {
		return "", fmt.Errorf("missing %s", label)
	}
	relativeSourcePath := strings.TrimPrefix(trimmed, "./")
	if loaded.sourceRoot == "" {
		if !allowSettingsAbsolute {
			return "", fmt.Errorf("cannot mirror local marketplace plugin source without source root: %s", sourcePath)
		}
		if !filepath.IsAbs(trimmed) {
			return "", fmt.Errorf("cannot mirror local marketplace plugin source without source root: %s", sourcePath)
		}
		absolutePath, err := filepath.Abs(trimmed)
		if err != nil {
			return "", err
		}
		if directoryExists(absolutePath) {
			return assertLocalMarketplacePluginSourceRoot(absolutePath)
		}
		return "", fmt.Errorf("local marketplace plugin source does not exist: %s", sourcePath)
	}
	pluginBaseDir, err := resolveMarketplacePluginBaseDir(loaded)
	if err != nil {
		return "", err
	}
	relativePath, err := skills.ResolveSyncPathWithin(pluginBaseDir, relativeSourcePath, "unsafe marketplace plugin source path")
	if err != nil {
		return "", err
	}
	if directoryExists(relativePath) {
		return assertLocalMarketplacePluginSourceRoot(relativePath)
	}
	return "", fmt.Errorf("local marketplace plugin source does not exist: %s", sourcePath)
}

// assertLocalMarketplacePluginSourceRoot refuses to mirror a directory
// without a plugin manifest: a tampered marketplace entry must not be able
// to ship local sensitive files to the remote as "plugin source".
func assertLocalMarketplacePluginSourceRoot(pluginRoot string) (string, error) {
	if readPluginManifestInfo(pluginRoot) == nil {
		return "", fmt.Errorf("local marketplace plugin source is missing plugin manifest: %s", pluginRoot)
	}
	return pluginRoot, nil
}

func resolveMarketplacePluginBaseDir(loaded *marketplaceSourceLoadResult) (string, error) {
	if loaded.sourceRoot == "" {
		return "", fmt.Errorf("cannot resolve marketplace plugin source root")
	}
	if loaded.manifest.PluginRoot == "" {
		return loaded.sourceRoot, nil
	}
	// metadata.pluginRoot conventionally carries a leading ./ — strip it
	// before the containment resolve (the TS resolve joins it the same way).
	pluginRootValue := strings.TrimPrefix(loaded.manifest.PluginRoot, "./")
	pluginRoot, err := skills.ResolveSyncPathWithin(loaded.sourceRoot, pluginRootValue, "unsafe marketplace pluginRoot")
	if err != nil {
		return "", err
	}
	if pathExists(pluginRoot) {
		return pluginRoot, nil
	}
	return loaded.sourceRoot, nil
}

func buildMirroredMarketplaceManifest(manifest *plugins.MarketplaceManifest, entries []mirrorEntry) map[string]any {
	hasMirroredLocalSource := false
	for _, entry := range entries {
		if entry.relative != "" {
			hasMirroredLocalSource = true
			break
		}
	}
	rawMetadata, _ := manifest.Raw["metadata"].(map[string]any)
	rawManifest := map[string]any{}
	for key, value := range manifest.Raw {
		rawManifest[key] = value
	}
	delete(rawManifest, "pluginRoot")
	rawManifest["name"] = manifest.Name
	if manifest.Description != "" {
		rawManifest["description"] = manifest.Description
	}
	if hasMirroredLocalSource {
		// The agent marketplace resolver reads metadata.pluginRoot only; a
		// top-level pluginRoot on the mirror would resolve ./hello-world
		// against the marketplace root instead of ./plugins/.
		nextMetadata := map[string]any{}
		for key, value := range rawMetadata {
			nextMetadata[key] = value
		}
		nextMetadata["pluginRoot"] = mirroredPluginRoot
		rawManifest["metadata"] = nextMetadata
	}
	var pluginList []any
	for _, entry := range entries {
		raw := map[string]any{}
		for key, value := range entry.entry.Raw {
			raw[key] = value
		}
		raw["name"] = entry.entry.Name
		if entry.relative != "" {
			segments := strings.Split(entry.relative, "/")
			raw["source"] = "./" + strings.Join(segments[1:], "/")
		}
		pluginList = append(pluginList, raw)
	}
	rawManifest["plugins"] = pluginList
	return rawManifest
}

func createMarketplaceSourceDirectoryName(marketplaceID, manifestContent string, sourceFingerprints []*string) string {
	fingerprintValues := make([]any, 0, len(sourceFingerprints))
	for _, fingerprint := range sourceFingerprints {
		if fingerprint == nil {
			fingerprintValues = append(fingerprintValues, nil)
		} else {
			fingerprintValues = append(fingerprintValues, *fingerprint)
		}
	}
	hashInput := canonicalJSONOf(map[string]any{
		"manifestContent":    manifestContent,
		"sourceFingerprints": fingerprintValues,
	})
	return toSafeDirectoryName(marketplaceID) + "-" + skills.ShortHash(hashInput)[:12]
}

func canonicalJSONOf(value any) string {
	data, _ := json.Marshal(value)
	return string(data)
}

func marshalIndentedNL(value any) string {
	data, _ := json.MarshalIndent(value, "", "  ")
	return string(data) + "\n"
}

func directoryExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.IsDir()
}

// mirrorEntry is one mirrored plugin: its manifest entry, the local source
// directory (empty for remote-source plugins), and the in-archive path.
type mirrorEntry struct {
	entry      plugins.MarketplacePluginEntry
	sourcePath string
	relative   string
}
