// marketplace.go: marketplace metadata handling — the official marketplace's
// bundled/CDN partitions and the merged marketplace.json
// (official-marketplace.ts), the known-marketplaces list, and the per-
// marketplace metadata path under ~/.nex/cli/plugins/marketplaces/...
// (marketplace.ts + apps/nex-cli/README.md layout).
package plugins

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// Partition file names (official-marketplace.ts).
const (
	BundledPartitionFile = "bundled-marketplace.json"
	CDNPartitionFile     = "cdn-marketplace.json"
	MergedFile           = "marketplace.json"
	// KnownMarketplacesFile is the registry of user-added marketplaces.
	KnownMarketplacesFile = "known_marketplaces.json"
)

// DefaultEnabledOfficialPluginIDs ports DEFAULT_ENABLED_OFFICIAL_PLUGIN_IDS
// (plugin-marketplaces.ts). This set must stay in lockstep with the official
// plugin definitions' defaultEnabled flags.
var DefaultEnabledOfficialPluginIDs = map[string]bool{
	"browser-use@nex-plugins-official":    true,
	"image-search@nex-plugins-official":   true,
	"documents@nex-plugins-official":      true,
	"pdf@nex-plugins-official":            true,
	"presentations@nex-plugins-official":  true,
	"spreadsheets@nex-plugins-official":   true,
	"node-repl-host@nex-plugins-official": true,
	"skill-creator@nex-plugins-official":  true,
	"plugin-creator@nex-plugins-official": true,
	"nex-guide@nex-plugins-official":      true,
}

// RetiredPresetMarketplaceIDs ports RETIRED_PRESET_MARKETPLACE_IDS: legacy
// installs may still carry the official market under its current or pre-rename id.
var RetiredPresetMarketplaceIDs = map[string]bool{
	"nex-plugins-official":   true,
	"zcode-plugins-official": true,
}

// MarketplacesDir is the per-marketplace metadata root
// (<pluginStorageRoot>/marketplaces/<sanitized id>).
func MarketplacesDir(pluginStorageRoot, marketplaceID string) string {
	return filepath.Join(pluginStorageRoot, MarketplacesDirName, SanitizePluginID(marketplaceID))
}

// sanitizePluginID keeps marketplace ids to the safe charset used on disk.
func SanitizePluginID(id string) string {
	sanitized := strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '-', r == '_', r == '.':
			return r
		default:
			return -1
		}
	}, strings.ToLower(strings.TrimSpace(id)))
	if sanitized == "" {
		return "unknown"
	}
	return sanitized
}

// MarketplaceManifest is the parsed marketplace.json / partition manifest.
type MarketplaceManifest struct {
	Name        string
	Description string
	// PluginRoot is manifest.pluginRoot or manifest.metadata.pluginRoot.
	PluginRoot string
	Plugins    []MarketplacePluginEntry
	// Raw keeps the full manifest for merged-metadata reconstruction.
	Raw map[string]any
}

// MarketplacePluginEntry is one plugin entry of the manifest's plugins list.
type MarketplacePluginEntry struct {
	Name         string
	Dependencies []string
	Source       any
	Raw          map[string]any
}

// ParseMarketplaceManifest ports parseMarketplaceManifest: name required,
// plugins accepted as array or as an object keyed by plugin name.
func ParseMarketplaceManifest(value any) (*MarketplaceManifest, error) {
	record, ok := value.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("marketplace manifest is invalid")
	}
	name, ok := record["name"].(string)
	if !ok || strings.TrimSpace(name) == "" {
		return nil, fmt.Errorf("missing marketplace name")
	}
	var pluginValues []any
	switch rawPlugins := record["plugins"].(type) {
	case []any:
		pluginValues = rawPlugins
	case map[string]any:
		for pluginName, plugin := range rawPlugins {
			if pluginRecord, ok := plugin.(map[string]any); ok {
				pluginCopy := map[string]any{}
				for key, value := range pluginRecord {
					pluginCopy[key] = value
				}
				pluginCopy["name"] = pluginName
				pluginValues = append(pluginValues, pluginCopy)
			} else {
				pluginValues = append(pluginValues, map[string]any{"name": pluginName})
			}
		}
	}
	var entries []MarketplacePluginEntry
	for _, item := range pluginValues {
		pluginRecord, ok := item.(map[string]any)
		if !ok {
			continue
		}
		pluginName, ok := pluginRecord["name"].(string)
		if !ok || strings.TrimSpace(pluginName) == "" {
			return nil, fmt.Errorf("missing marketplace plugin name")
		}
		entry := MarketplacePluginEntry{Name: strings.TrimSpace(pluginName), Raw: pluginRecord}
		if source, present := pluginRecord["source"]; present {
			entry.Source = source
		}
		if dependencies, ok := pluginRecord["dependencies"].([]any); ok {
			for _, dependency := range dependencies {
				if s, ok := dependency.(string); ok && strings.TrimSpace(s) != "" {
					entry.Dependencies = append(entry.Dependencies, s)
				}
			}
		}
		entries = append(entries, entry)
	}
	manifest := &MarketplaceManifest{Name: name, Plugins: entries, Raw: record}
	if description, ok := record["description"].(string); ok {
		manifest.Description = description
	}
	manifest.PluginRoot = readMarketplacePluginRoot(record)
	return manifest, nil
}

func readMarketplacePluginRoot(record map[string]any) string {
	if pluginRoot, ok := record["pluginRoot"].(string); ok && strings.TrimSpace(pluginRoot) != "" {
		return strings.TrimSpace(pluginRoot)
	}
	if metadata, ok := record["metadata"].(map[string]any); ok {
		if pluginRoot, ok := metadata["pluginRoot"].(string); ok && strings.TrimSpace(pluginRoot) != "" {
			return strings.TrimSpace(pluginRoot)
		}
	}
	return ""
}

// ReadMarketplaceManifestFile reads and parses a marketplace.json.
func ReadMarketplaceManifestFile(path string) (*MarketplaceManifest, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var value any
	if err := json.Unmarshal(raw, &value); err != nil {
		return nil, err
	}
	return ParseMarketplaceManifest(value)
}

// FindMarketplaceManifestPath ports findMarketplaceManifestPath.
func FindMarketplaceManifestPath(sourceRoot string) string {
	for _, candidate := range []string{
		filepath.Join(sourceRoot, "marketplace.json"),
		filepath.Join(sourceRoot, ".claude-plugin", "marketplace.json"),
	} {
		if _, err := os.Stat(candidate); err == nil {
			return candidate
		}
	}
	return ""
}

// ReadOfficialMergedMarketplace reads the merged official marketplace
// metadata, rebuilding it from the partitions when they are newer than the
// merged file (the merged file is a cache; the partitions are authoritative).
func ReadOfficialMergedMarketplace(pluginStorageRoot string) (map[string]any, error) {
	dir := MarketplacesDir(pluginStorageRoot, OfficialMarketplaceID)
	if merged, err := readJSONObject(filepath.Join(dir, MergedFile)); err == nil && merged != nil {
		return merged, nil
	}
	return RebuildOfficialMarketplace(pluginStorageRoot)
}

// ReadOfficialMergedMarketplaceStrict is ReadOfficialMergedMarketplace
// without the rebuild fallback (for offline-mode reads that must reflect
// only what is on disk).
func ReadOfficialMergedMarketplaceStrict(pluginStorageRoot string) (map[string]any, error) {
	return readJSONObject(filepath.Join(MarketplacesDir(pluginStorageRoot, OfficialMarketplaceID), MergedFile))
}

// RebuildOfficialMarketplace ports rebuildOfficialMarketplaceSync: merge the
// bundled and CDN partitions — same-name entries resolve to the refreshable
// CDN one; the merged file is persisted next to the partitions.
func RebuildOfficialMarketplace(pluginStorageRoot string) (map[string]any, error) {
	dir := MarketplacesDir(pluginStorageRoot, OfficialMarketplaceID)
	bundledManifest := readBundledPartition(dir)
	cdnManifest, _ := readJSONObject(filepath.Join(dir, CDNPartitionFile))

	cdnPlugins := readPluginEntries(cdnManifest)
	cdnPluginNames := map[string]bool{}
	for _, plugin := range cdnPlugins {
		if name, ok := plugin["name"].(string); ok && name != "" {
			cdnPluginNames[name] = true
		}
	}
	var bundledPlugins []map[string]any
	for _, plugin := range readPluginEntries(bundledManifest) {
		if name, ok := plugin["name"].(string); ok && name != "" && !cdnPluginNames[name] {
			bundledPlugins = append(bundledPlugins, plugin)
		}
	}
	merged := map[string]any{}
	for key, value := range bundledManifest {
		merged[key] = value
	}
	for key, value := range cdnManifest {
		merged[key] = value
	}
	merged["name"] = OfficialMarketplaceID
	plugins := make([]any, 0, len(cdnPlugins)+len(bundledPlugins))
	for _, plugin := range cdnPlugins {
		plugins = append(plugins, plugin)
	}
	for _, plugin := range bundledPlugins {
		plugins = append(plugins, plugin)
	}
	merged["plugins"] = plugins
	if err := writeJSONFile(filepath.Join(dir, MergedFile), merged); err != nil {
		return nil, err
	}
	return merged, nil
}

// WriteBundledOfficialPartition ports writeBundledOfficialMarketplacePartitionSync.
func WriteBundledOfficialPartition(pluginStorageRoot string, manifest map[string]any) (map[string]any, error) {
	dir := MarketplacesDir(pluginStorageRoot, OfficialMarketplaceID)
	if err := writeJSONFile(filepath.Join(dir, BundledPartitionFile), map[string]any{
		"manifest": manifest,
		"version":  1,
	}); err != nil {
		return nil, err
	}
	return RebuildOfficialMarketplace(pluginStorageRoot)
}

// WriteCDNOfficialPartition ports writeCdnOfficialMarketplacePartitionSync.
func WriteCDNOfficialPartition(pluginStorageRoot string, manifest map[string]any) (map[string]any, error) {
	dir := MarketplacesDir(pluginStorageRoot, OfficialMarketplaceID)
	if err := writeJSONFile(filepath.Join(dir, CDNPartitionFile), manifest); err != nil {
		return nil, err
	}
	return RebuildOfficialMarketplace(pluginStorageRoot)
}

func readBundledPartition(dir string) map[string]any {
	value, err := readJSONObject(filepath.Join(dir, BundledPartitionFile))
	if err != nil {
		return nil
	}
	if version, _ := value["version"].(float64); version != 1 {
		return nil
	}
	manifest, _ := value["manifest"].(map[string]any)
	return manifest
}

func readPluginEntries(manifest map[string]any) []map[string]any {
	if manifest == nil {
		return nil
	}
	entries, _ := manifest["plugins"].([]any)
	out := make([]map[string]any, 0, len(entries))
	for _, entry := range entries {
		if record, ok := entry.(map[string]any); ok {
			out = append(out, record)
		}
	}
	return out
}

// KnownMarketplace is one known_marketplaces.json record.
type KnownMarketplace struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Source      string `json:"source"`
	Description string `json:"description,omitempty"`
}

// ReadKnownMarketplaces reads known_marketplaces.json, dropping retired
// preset ids the migration removes (marketplace.ts).
func ReadKnownMarketplaces(pluginStorageRoot string) []KnownMarketplace {
	value, err := readJSONObject(filepath.Join(pluginStorageRoot, KnownMarketplacesFile))
	if err != nil {
		return nil
	}
	entries, _ := value["marketplaces"].([]any)
	var out []KnownMarketplace
	for _, entry := range entries {
		record, ok := entry.(map[string]any)
		if !ok {
			continue
		}
		var marketplace KnownMarketplace
		if id, ok := record["id"].(string); ok {
			marketplace.ID = id
		}
		if name, ok := record["name"].(string); ok {
			marketplace.Name = name
		}
		if source, ok := record["source"].(string); ok {
			marketplace.Source = source
		}
		if description, ok := record["description"].(string); ok {
			marketplace.Description = description
		}
		if RetiredPresetMarketplaceIDs[marketplace.ID] {
			continue
		}
		out = append(out, marketplace)
	}
	return out
}

func readJSONObject(path string) (map[string]any, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var value map[string]any
	if err := json.Unmarshal(raw, &value); err != nil {
		return nil, err
	}
	return value, nil
}

func writeJSONFile(path string, value any) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(data, '\n'), 0o644)
}

// SortPluginEntriesByName sorts marketplace plugin entries by name — the
// localeCompare order the TS uses (byte order for the ASCII ids in play).
func SortPluginEntriesByName(entries []map[string]any) {
	sort.Slice(entries, func(i, j int) bool {
		left, _ := entries[i]["name"].(string)
		right, _ := entries[j]["name"].(string)
		return left < right
	})
}
