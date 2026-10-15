// discover.go: the discovery pipeline over the shared skills library —
// workspace roots (both .nex and .agents at every worktree level), the
// user-level roots (desktop runtime only), and the plugin skill roots from
// the CLI config (inline dirs, official cache, installed records), with the
// user .agents dedupe rules and per-skill frontmatter validation.
package skills

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/tylinux/nex/go/internal/plugins"
	"github.com/tylinux/nex/go/internal/skills"
)

// skillRootDescriptor ports SkillRootDescriptor.
type skillRootDescriptor struct {
	scope      string // workspace | user | plugin
	rootPath   string
	pluginName string
	pluginID   string
}

// discover ports discoverSkills. workspaceIdentity is accepted for the
// AGENTS.md identity rules but the skill scan itself is path-based (matching
// the TS, which passes it only to the audit log).
func (s *Service) discover(workspacePath string, includeUserSkills bool, provider string) ([]Summary, []Diagnostic) {
	var diagnostics []Diagnostic
	workspaceRoots := skills.DedupeRoots(skills.ResolveAncestorWorkspaceRoots(workspacePath))
	roots := make([]skillRootDescriptor, 0, len(workspaceRoots)+2)
	for _, rootPath := range workspaceRoots {
		roots = append(roots, skillRootDescriptor{scope: "workspace", rootPath: rootPath})
	}
	if includeUserSkills {
		// User-level skills are global resources: .agents/skills is scanned
		// unconditionally, not as a .nex fallback (imported external skills
		// must not vanish from the settings page).
		roots = append(roots,
			skillRootDescriptor{scope: "user", rootPath: skills.UserNexSkillRoot(s.homeDir)},
			skillRootDescriptor{scope: "user", rootPath: skills.UserAgentsSkillRoot(s.homeDir)})
	}
	pluginRoots := s.resolvePluginSkillRootDescriptors()
	roots = append(roots, pluginRoots...)

	var out []Summary
	seenSkillPaths := map[string]bool{}
	userNexSkillNameKeys := map[string]bool{}
	if includeUserSkills {
		userNexSkillNameKeys = s.collectSkillNameKeysInRoot(skills.UserNexSkillRoot(s.homeDir))
	}

	rootByPath := map[string]skillRootDescriptor{}
	var rootPaths []string
	for _, root := range roots {
		if _, ok := rootByPath[root.rootPath]; !ok {
			rootByPath[root.rootPath] = root
			rootPaths = append(rootPaths, root.rootPath)
		}
	}
	scanRootPaths := skills.DedupeScanRootsByRealpath(rootPaths)

	for _, rootPath := range scanRootPaths {
		root := rootByPath[rootPath]
		if _, err := os.Stat(root.rootPath); err != nil {
			continue
		}
		for _, skillPath := range skills.WalkSkillMarkdownPaths(root.rootPath, func(path string, err error) {
			diagnostics = append(diagnostics, Diagnostic{
				Code:     "skill_scan_failed",
				Severity: "warning",
				Message:  err.Error(),
				Path:     path,
			})
		}) {
			if s.isUserAgentsSkillCoveredByNex(skillPath, root.rootPath, userNexSkillNameKeys) {
				continue
			}
			canonicalSkillPath := skillPath
			if resolved, err := filepath.EvalSymlinks(skillPath); err == nil {
				canonicalSkillPath = resolved
			}
			if seenSkillPaths[canonicalSkillPath] {
				continue
			}
			seenSkillPaths[canonicalSkillPath] = true
			markdown, err := os.ReadFile(skillPath)
			if err != nil {
				diagnostics = append(diagnostics, Diagnostic{
					Code:     "skill_read_failed",
					Severity: "warning",
					Message:  err.Error(),
					Path:     skillPath,
				})
				continue
			}
			parsed := skills.ReadFrontmatter(string(markdown))
			skillFolderName := filepath.Base(filepath.Dir(skillPath))

			// Name validation only requires non-empty; case/underscore shapes
			// are legal display names in other skill ecosystems.
			rawName := skillFolderName
			if parsed.HasFrontmatter {
				rawName = strings.TrimSpace(parsed.Name)
			}
			resolvedName := rawName
			if resolvedName == "" {
				resolvedName = skillFolderName
			}
			if resolvedName == "" {
				diagnostics = append(diagnostics, Diagnostic{
					Code:     "skill_missing_name",
					Severity: "error",
					Message:  fmt.Sprintf("Skill frontmatter must include a name: %s", skillPath),
					Path:     skillPath,
				})
				continue
			}
			description := ""
			if parsed.HasFrontmatter {
				description = strings.TrimSpace(parsed.Description)
			}
			if len(description) > skills.MaxDescriptionLength {
				diagnostics = append(diagnostics, Diagnostic{
					Code:      "skill_description_too_long",
					Severity:  "error",
					Message:   fmt.Sprintf("Skill description is too long (>%d): %s", skills.MaxDescriptionLength, resolvedName),
					Path:      skillPath,
					SkillName: resolvedName,
				})
				continue
			}
			out = append(out, Summary{
				ID:          skills.BuildSkillID(provider, root.scope, resolvedName, canonicalSkillPath),
				Name:        resolvedName,
				Description: description,
				Body:        strings.TrimSpace(parsed.Body),
				Path:        canonicalSkillPath,
				SourcePath:  skillPath,
				Scope:       root.scope,
				Enabled:     true,
				PluginName:  root.pluginName,
				PluginID:    root.pluginID,
				Metadata:    readSkillMetadata(skillPath),
			})
		}
	}

	// name, then scope, then path (localeCompare ≙ byte order here).
	sort.Slice(out, func(i, j int) bool {
		if out[i].Name != out[j].Name {
			return out[i].Name < out[j].Name
		}
		if out[i].Scope != out[j].Scope {
			return out[i].Scope < out[j].Scope
		}
		return out[i].Path < out[j].Path
	})
	return out, diagnostics
}

// readSkillMetadata ports readSkillMetadata: the _meta.json installer
// addendum; a corrupt file must not affect the SKILL.md listing.
func readSkillMetadata(skillPath string) *skillsMetadata {
	raw, err := os.ReadFile(filepath.Join(filepath.Dir(skillPath), "_meta.json"))
	if err != nil {
		return nil
	}
	var parsed map[string]any
	if err := unmarshalJSONObject(raw, &parsed); err != nil {
		return nil
	}
	metadata := &skillsMetadata{}
	set := false
	if slug, ok := parsed["slug"].(string); ok && strings.TrimSpace(slug) != "" {
		metadata.Slug = strings.TrimSpace(slug)
		set = true
	}
	if version, ok := parsed["version"].(string); ok && strings.TrimSpace(version) != "" {
		metadata.Version = strings.TrimSpace(version)
		set = true
	}
	if ownerID, ok := parsed["ownerId"].(string); ok && strings.TrimSpace(ownerID) != "" {
		metadata.OwnerID = strings.TrimSpace(ownerID)
		set = true
	}
	if publishedAt, ok := parsed["publishedAt"].(float64); ok {
		metadata.PublishedAt = publishedAt
		set = true
	}
	if !set {
		return nil
	}
	return metadata
}

func (s *Service) collectSkillNameKeysInRoot(rootPath string) map[string]bool {
	nameKeys := map[string]bool{}
	if _, err := os.Stat(rootPath); err != nil {
		return nameKeys
	}
	for _, skillPath := range skills.WalkSkillMarkdownPaths(rootPath, nil) {
		nameKeys[s.readSkillNameKey(skillPath)] = true
	}
	return nameKeys
}

func (s *Service) readSkillNameKey(skillPath string) string {
	fallbackName := filepath.Base(filepath.Dir(skillPath))
	data, err := os.ReadFile(skillPath)
	if err != nil {
		return skills.NormalizeSkillNameKey(fallbackName)
	}
	parsed := skills.ReadFrontmatter(string(data))
	rawName := fallbackName
	if parsed.HasFrontmatter {
		rawName = strings.TrimSpace(parsed.Name)
	}
	if rawName == "" {
		rawName = fallbackName
	}
	return skills.NormalizeSkillNameKey(rawName)
}

// isUserAgentsSkillCoveredByNex ports isUserAgentsSkillCoveredByNex: a
// ~/.agents/skills skill is skipped when ~/.nex/skills has the same folder
// or the same frontmatter name.
func (s *Service) isUserAgentsSkillCoveredByNex(skillPath, rootPath string, userNexSkillNameKeys map[string]bool) bool {
	if rootPath != skills.UserAgentsSkillRoot(s.homeDir) {
		return false
	}
	if _, err := os.Stat(filepath.Join(skills.UserNexSkillRoot(s.homeDir), filepath.Base(filepath.Dir(skillPath)), skills.FileName)); err == nil {
		return true
	}
	return userNexSkillNameKeys[s.readSkillNameKey(skillPath)]
}

// resolvePluginSkillRootDescriptors ports resolvePluginSkillRootDescriptors:
// inline config dirs + official cache + marketplace installed records, with
// the suppressed-builtin filter and the enabled gate.
func (s *Service) resolvePluginSkillRootDescriptors() []skillRootDescriptor {
	config := s.readPluginConfig()
	if !config.Enabled {
		return nil
	}
	pluginStorageRoot := s.resolvePluginStorageRoot(config.StorageDir)
	officialCacheRoots := s.scanOfficialPluginCacheRoots(pluginStorageRoot)

	type candidate struct {
		defaultEnabled bool
		marketplace    string
		rootPath       string
	}
	var candidates []candidate
	for _, dir := range config.Dirs {
		candidates = append(candidates, candidate{true, plugins.InlineMarketplace, s.resolveConfigPath(dir)})
	}
	for _, rootPath := range officialCacheRoots {
		candidates = append(candidates, candidate{false, plugins.OfficialMarketplaceID, rootPath})
	}
	for _, record := range plugins.ReadInstalledPluginRoots(pluginStorageRoot) {
		candidates = append(candidates, candidate{false, record.Marketplace, record.InstallPath})
	}

	var descriptors []skillRootDescriptor
	seenPluginIDs := map[string]bool{}
	for _, item := range candidates {
		manifest, err := plugins.ReadManifest(item.rootPath)
		if err != nil || manifest == nil {
			continue
		}
		pluginID := manifest.Name + "@" + item.marketplace
		// An "uninstalled" builtin only writes suppressedBuiltins; the
		// official cache scan must apply the same skip or the uninstalled
		// builtin keeps contributing skills.
		if item.marketplace == plugins.OfficialMarketplaceID && containsStringList(config.SuppressedBuiltins, pluginID) {
			continue
		}
		if seenPluginIDs[pluginID] {
			continue
		}
		seenPluginIDs[pluginID] = true
		defaultEnabled := item.defaultEnabled || plugins.DefaultEnabledOfficialPluginIDs[pluginID]
		enabled := defaultEnabled
		if override, ok := config.EnabledPlugins[pluginID]; ok {
			enabled = override
		}
		if !enabled {
			continue
		}
		// Marketplace-installed plugins contribute read-only skill roots so
		// the plugin detail page lists skill names, not just counts.
		for _, rootPath := range plugins.ResolveComponentRoots(item.rootPath, manifest.Skills, "skills") {
			descriptors = append(descriptors, skillRootDescriptor{
				scope:      "plugin",
				rootPath:   rootPath,
				pluginName: manifest.Name,
				pluginID:   pluginID,
			})
		}
	}
	return descriptors
}

func containsStringList(list []string, value string) bool {
	for _, item := range list {
		if item == value {
			return true
		}
	}
	return false
}
