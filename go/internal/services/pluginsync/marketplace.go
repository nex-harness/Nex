// marketplace.go: the marketplace-source mirror export/import. The mirror
// bundles the marketplace manifest (rewritten to local ./plugins/... source
// paths) plus the selected plugins' directories (dependency closure
// resolved, cross-marketplace dependencies refused), into
// <root>/marketplace-sources/<id>-<hash>/ on the remote side.
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

// ExportMarketplaceSourceArchive ports exportMarketplaceSourceArchive.
func (s *Service) ExportMarketplaceSourceArchive(ctx context.Context, marketplaceID string, pluginNames []string, source map[string]any) (*MarketplaceSourceExportResult, error) {
	normalizedID, err := normalizePluginID(marketplaceID)
	if err != nil {
		return nil, err
	}
	uniqueNames := map[string]bool{}
	var selectedNames []string
	for _, name := range pluginNames {
		trimmed := strings.TrimSpace(name)
		if trimmed == "" {
			return nil, fmt.Errorf("missing plugin name")
		}
		if !uniqueNames[trimmed] {
			uniqueNames[trimmed] = true
			selectedNames = append(selectedNames, trimmed)
		}
	}
	if len(selectedNames) == 0 {
		return nil, fmt.Errorf("missing marketplace plugin names")
	}
	loaded, err := loadMarketplaceSource(source)
	if err != nil {
		return nil, err
	}
	if loaded.manifest.Name != normalizedID {
		return nil, fmt.Errorf("marketplace id mismatch: %s !== %s", loaded.manifest.Name, normalizedID)
	}
	closure, err := resolveMarketplacePluginClosure(loaded.manifest, normalizedID, selectedNames)
	if err != nil {
		return nil, err
	}

	var mirrorEntries []mirrorEntry
	for _, entry := range closure {
		if isRemoteMarketplacePluginSource(entry.Source) {
			mirrorEntries = append(mirrorEntries, mirrorEntry{entry: entry})
			continue
		}
		sourcePath, err := s.resolveLocalMarketplacePluginSource(loaded, entry)
		if err != nil {
			return nil, err
		}
		sourceDirectoryName := toSafeDirectoryName(entry.Name)
		mirrorEntries = append(mirrorEntries, mirrorEntry{
			entry:      entry,
			sourcePath: sourcePath,
			relative:   mirroredPluginRoot + "/" + sourceDirectoryName,
		})
	}
	manifest := buildMirroredMarketplaceManifest(loaded.manifest, mirrorEntries)
	manifestContent := marshalIndentedNL(manifest)
	var selectedBytes int64 = int64(len(manifestContent))
	for _, entry := range mirrorEntries {
		if entry.sourcePath != "" {
			size, err := plugins.RecursiveSize(entry.sourcePath)
			if err != nil {
				return nil, err
			}
			selectedBytes += size
		}
	}
	if selectedBytes > s.maxArchiveBytes {
		return nil, &plugins.ArchiveExceedsLimitError{ActualBytes: selectedBytes, MaxBytes: s.maxArchiveBytes}
	}
	var sourceFingerprints []*string
	for _, entry := range mirrorEntries {
		if entry.sourcePath == "" {
			sourceFingerprints = append(sourceFingerprints, nil)
			continue
		}
		fingerprint, err := plugins.ContentFingerprint(entry.sourcePath)
		if err != nil {
			return nil, err
		}
		sourceFingerprints = append(sourceFingerprints, &fingerprint)
	}
	directoryName := createMarketplaceSourceDirectoryName(normalizedID, manifestContent, sourceFingerprints)
	entries := []plugins.ArchiveEntry{{
		ArchivePath: directoryName + "/marketplace.json",
		Content:     []byte(manifestContent),
	}}
	for _, entry := range mirrorEntries {
		if entry.sourcePath != "" {
			entries = append(entries, plugins.ArchiveEntry{
				SourcePath:  entry.sourcePath,
				ArchivePath: directoryName + "/" + entry.relative,
			})
		}
	}
	archive, err := plugins.CreateSyncArchive(entries, plugins.ArchiveMetadata{
		MarketplaceSources: []plugins.ArchiveMarketplaceSource{{
			MarketplaceID: normalizedID,
			DirectoryName: directoryName,
		}},
	})
	if err != nil {
		return nil, err
	}
	if int64(len(archive)) > s.maxArchiveBytes {
		return nil, &plugins.ArchiveExceedsLimitError{ActualBytes: int64(len(archive)), MaxBytes: s.maxArchiveBytes}
	}
	result := &MarketplaceSourceExportResult{
		Archive:       archive,
		ArchiveBytes:  len(archive),
		MarketplaceID: normalizedID,
	}
	for _, entry := range closure {
		result.PluginNames = append(result.PluginNames, entry.Name)
	}
	return result, nil
}

// ImportMarketplaceSourceArchive ports importMarketplaceSourceArchive.
func (s *Service) ImportMarketplaceSourceArchive(ctx context.Context, archive []byte, overwrite bool) (*MarketplaceSourceImportResult, error) {
	if overwrite {
		return nil, fmt.Errorf("plugin marketplace source overwrite is not supported")
	}
	if int64(len(archive)) > s.maxArchiveBytes {
		return nil, &plugins.ArchiveExceedsLimitError{ActualBytes: int64(len(archive)), MaxBytes: s.maxArchiveBytes}
	}
	tempRoot := s.tempRoot("nex-plugin-marketplace-source")
	defer os.RemoveAll(tempRoot)
	if err := plugins.ExtractSyncArchive(archive, tempRoot, plugins.ExtractSyncArchiveOptions{
		MaxExtractedBytes: s.maxArchiveBytes,
	}); err != nil {
		return nil, err
	}
	metadata, err := readMarketplaceSourceArchiveMetadata(tempRoot)
	if err != nil {
		return nil, err
	}
	directoryName, err := skills.NormalizeSyncRelativePath(metadata.directoryName, "")
	if err != nil {
		return nil, err
	}
	extractedPath, err := skills.ResolveSyncPathWithin(tempRoot, directoryName, "")
	if err != nil {
		return nil, err
	}
	manifest, err := plugins.ReadMarketplaceManifestFile(filepath.Join(extractedPath, "marketplace.json"))
	if err != nil {
		return nil, err
	}
	if manifest.Name != metadata.marketplaceID {
		return nil, fmt.Errorf("marketplace source archive id mismatch: %s !== %s", manifest.Name, metadata.marketplaceID)
	}
	targetRoot := filepath.Join(s.userNexPluginRoot(), marketplaceSourceRootDirectory)
	targetPath, err := skills.ResolveSyncPathWithin(targetRoot, directoryName, "")
	if err != nil {
		return nil, err
	}
	if pathExists(targetPath) {
		return &MarketplaceSourceImportResult{MarketplaceID: metadata.marketplaceID, Path: targetPath, Status: "skipped"}, nil
	}
	if err := os.MkdirAll(filepath.Dir(targetPath), 0o755); err != nil {
		return nil, err
	}
	if err := copyTreeNoOverwrite(extractedPath, targetPath); err != nil {
		return nil, err
	}
	return &MarketplaceSourceImportResult{MarketplaceID: metadata.marketplaceID, Path: targetPath, Status: "synced"}, nil
}

func readMarketplaceSourceArchiveMetadata(tempRoot string) (*struct {
	directoryName string
	marketplaceID string
}, error) {
	raw, err := os.ReadFile(filepath.Join(tempRoot, plugins.MetadataArchivePath))
	if err != nil {
		return nil, fmt.Errorf("invalid plugin marketplace source archive metadata")
	}
	var parsed struct {
		MarketplaceSources []struct {
			MarketplaceID string `json:"marketplaceId"`
			DirectoryName string `json:"directoryName"`
		} `json:"marketplaceSources"`
	}
	if err := json.Unmarshal(raw, &parsed); err != nil || len(parsed.MarketplaceSources) == 0 {
		return nil, fmt.Errorf("invalid plugin marketplace source archive metadata")
	}
	if len(parsed.MarketplaceSources) != 1 {
		return nil, fmt.Errorf("plugin marketplace source archive must contain one marketplace")
	}
	item := parsed.MarketplaceSources[0]
	marketplaceID, err := normalizePluginID(item.MarketplaceID)
	if err != nil {
		return nil, err
	}
	directoryName, err := skills.NormalizeSyncRelativePath(item.DirectoryName, "")
	if err != nil {
		return nil, err
	}
	return &struct {
		directoryName string
		marketplaceID string
	}{directoryName: directoryName, marketplaceID: marketplaceID}, nil
}

type marketplaceSourceLoadResult struct {
	manifest   *plugins.MarketplaceManifest
	sourceRoot string
}

// loadMarketplaceSource ports loadMarketplaceSource: file | directory |
// settings kinds.
func loadMarketplaceSource(source map[string]any) (*marketplaceSourceLoadResult, error) {
	sourceKind, _ := source["source"].(string)
	switch sourceKind {
	case "file":
		filePath, _ := source["path"].(string)
		if strings.TrimSpace(filePath) == "" {
			return nil, fmt.Errorf("missing marketplace source file path")
		}
		filePath = strings.TrimSpace(filePath)
		manifest, err := plugins.ReadMarketplaceManifestFile(filePath)
		if err != nil {
			return nil, err
		}
		return &marketplaceSourceLoadResult{manifest: manifest, sourceRoot: filepath.Dir(filePath)}, nil
	case "directory":
		sourceRoot, _ := source["path"].(string)
		if strings.TrimSpace(sourceRoot) == "" {
			return nil, fmt.Errorf("missing marketplace source directory path")
		}
		sourceRoot = strings.TrimSpace(sourceRoot)
		manifestPath := plugins.FindMarketplaceManifestPath(sourceRoot)
		if manifestPath == "" {
			return nil, fmt.Errorf("marketplace manifest not found in directory: %s", sourceRoot)
		}
		manifest, err := plugins.ReadMarketplaceManifestFile(manifestPath)
		if err != nil {
			return nil, err
		}
		return &marketplaceSourceLoadResult{manifest: manifest, sourceRoot: sourceRoot}, nil
	case "settings":
		marketplace, ok := source["marketplace"].(map[string]any)
		if !ok {
			return nil, fmt.Errorf("settings marketplace source is missing marketplace manifest")
		}
		manifest, err := plugins.ParseMarketplaceManifest(marketplace)
		if err != nil {
			return nil, err
		}
		return &marketplaceSourceLoadResult{manifest: manifest}, nil
	default:
		kind := sourceKind
		if kind == "" {
			kind = "unknown"
		}
		return nil, fmt.Errorf("unsupported marketplace source for SSH sync: %s", kind)
	}
}
