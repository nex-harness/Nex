// Package plugins is the single shared implementation of plugin metadata
// handling (step 3.7's "single implementation rule"): plugin manifest
// reading, marketplace metadata (CDN partitions + merged marketplace.json),
// semver-shaped version comparison, the atomic directory install, and the
// plugin-sync tar.gz archive codec. The server services
// (internal/services/{plugins,pluginmgmt,pluginsync}) consume it now; the
// Phase 4 agent loading (step 4.7) consumes the same package.
//
// Sources: apps/nex-cli/packages/adapters/src/plugins/{index,marketplace,
// official-marketplace,version-compare,atomic-directory}.ts and
// packages/services/src/plugin-sync/pluginSync{Archive,Service}.ts.
package plugins

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
)

// Marketplace ids and storage layout (CONTEXT.md vocabulary: the official
// marketplace is `nex-plugins-official`; `zcode-plugins-official` is the
// retired pre-rename id).
const (
	OfficialMarketplaceID = "nex-plugins-official"
	RetiredPresetID1      = "nex-plugins-official"
	RetiredPresetID2      = "zcode-plugins-official"
	InlineMarketplace     = "inline"
	// MarketplacesDirName is the per-marketplace metadata root under the
	// plugin storage root (~/.nex/cli/plugins/marketplaces/<id>/...).
	MarketplacesDirName = "marketplaces"
)

var pluginNameRe = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,127}$`)

// ManifestRelativePaths are the three accepted manifest locations, in order
// (PLUGIN_MANIFEST_RELATIVE_PATHS).
var ManifestRelativePaths = []string{
	filepath.Join(".nex-plugin", "plugin.json"),
	filepath.Join(".claude-plugin", "plugin.json"),
	filepath.Join(".codex-plugin", "plugin.json"),
}

// Manifest is the functional plugin.json summary (name is the identity;
// skills/commands are the component roots; the rest is read by the
// management services).
type Manifest struct {
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	Version     string          `json:"version,omitempty"`
	Raw         json.RawMessage `json:"-"`
	// Skills and Commands keep the raw manifest values: a string or a list of
	// strings (parsePathList).
	Skills   any `json:"skills,omitempty"`
	Commands any `json:"commands,omitempty"`
}

// ReadManifest reads the plugin manifest from a plugin root (the first of
// the three locations that exists and parses). It returns nil when no valid
// manifest exists — the TS readPluginManifest's null.
func ReadManifest(pluginRoot string) (*Manifest, error) {
	for _, relative := range ManifestRelativePaths {
		raw, err := os.ReadFile(filepath.Join(pluginRoot, relative))
		if err != nil {
			continue
		}
		var parsed map[string]any
		if err := json.Unmarshal(raw, &parsed); err != nil {
			return nil, nil
		}
		name, _ := parsed["name"].(string)
		name = trimSpace(name)
		if !pluginNameRe.MatchString(name) {
			return nil, nil
		}
		manifest := &Manifest{
			Name:     name,
			Skills:   parsed["skills"],
			Commands: parsed["commands"],
		}
		if description, ok := parsed["description"].(string); ok && trimSpace(description) != "" {
			manifest.Description = trimSpace(description)
		}
		if version, ok := parsed["version"].(string); ok && trimSpace(version) != "" {
			manifest.Version = trimSpace(version)
		}
		if rawJSON, err := json.Marshal(parsed); err == nil {
			manifest.Raw = rawJSON
		}
		return manifest, nil
	}
	return nil, nil
}

// FindManifestPath returns the first existing manifest path, or "".
func FindManifestPath(pluginRoot string) string {
	for _, relative := range ManifestRelativePaths {
		path := filepath.Join(pluginRoot, relative)
		if _, err := os.Stat(path); err == nil {
			return path
		}
	}
	return ""
}

// ParsePathList ports parsePathList: a string becomes a single-element list;
// an array keeps its string members; anything else is empty.
func ParsePathList(value any) []string {
	if s, ok := value.(string); ok {
		return []string{s}
	}
	if list, ok := value.([]any); ok {
		out := make([]string, 0, len(list))
		for _, item := range list {
			if s, ok := item.(string); ok {
				out = append(out, s)
			}
		}
		return out
	}
	return nil
}

// ResolveInside ports resolveInside: rawPath joined under rootPath,
// rejected when absolute or escaping. Returns "" on rejection.
func ResolveInside(rootPath, rawPath string) string {
	if filepath.IsAbs(rawPath) {
		return ""
	}
	resolved := filepath.Join(rootPath, rawPath)
	relative, err := filepath.Rel(rootPath, resolved)
	if err != nil {
		return ""
	}
	if relative == "" ||
		relative == ".." ||
		len(relative) >= 3 && relative[:3] == ".."+string(filepath.Separator) {
		return ""
	}
	return resolved
}

// ResolveComponentRoots resolves the manifest's component root directories
// (skills or commands): the listed paths constrained inside the plugin root;
// when the manifest omits the field entirely, the default <root>/<name>
// directory when it exists (resolvePluginSkillRoots / CommandRoots).
func ResolveComponentRoots(pluginRoot string, manifestField any, defaultDirName string) []string {
	var roots []string
	for _, rawPath := range ParsePathList(manifestField) {
		if resolved := ResolveInside(pluginRoot, rawPath); resolved != "" {
			roots = append(roots, resolved)
		}
	}
	if len(roots) == 0 && manifestField == nil {
		defaultRoot := filepath.Join(pluginRoot, defaultDirName)
		if _, err := os.Stat(defaultRoot); err == nil {
			roots = append(roots, defaultRoot)
		}
	}
	return roots
}

// InstalledPluginRecord is one entry of installed_plugins.json.
type InstalledPluginRecord struct {
	ID          string `json:"id"`
	InstallPath string `json:"installPath"`
	Marketplace string `json:"marketplace"`
}

// ReadInstalledPluginRoots ports readInstalledPluginRoots: the
// installed_plugins.json records with absolute install paths.
func ReadInstalledPluginRoots(pluginStorageRoot string) []InstalledPluginRecord {
	raw, err := os.ReadFile(filepath.Join(pluginStorageRoot, "installed_plugins.json"))
	if err != nil {
		return nil
	}
	var parsed struct {
		Plugins []struct {
			ID          string `json:"id"`
			InstallPath string `json:"installPath"`
			Marketplace string `json:"marketplace"`
		} `json:"plugins"`
	}
	if err := json.Unmarshal(raw, &parsed); err != nil {
		return nil
	}
	var out []InstalledPluginRecord
	for _, item := range parsed.Plugins {
		id, marketplace, installPath := trimSpace(item.ID), trimSpace(item.Marketplace), trimSpace(item.InstallPath)
		if id == "" || marketplace == "" || installPath == "" || !filepath.IsAbs(installPath) {
			continue
		}
		out = append(out, InstalledPluginRecord{ID: id, InstallPath: installPath, Marketplace: marketplace})
	}
	return out
}

func trimSpace(value string) string {
	for len(value) > 0 && (value[0] == ' ' || value[0] == '\t' || value[0] == '\n' || value[0] == '\r') {
		value = value[1:]
	}
	for len(value) > 0 && (value[len(value)-1] == ' ' || value[len(value)-1] == '\t' || value[len(value)-1] == '\n' || value[len(value)-1] == '\r') {
		value = value[:len(value)-1]
	}
	return value
}
