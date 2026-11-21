package skills

import (
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/nex-harness/nex/pkg/harness/prompts"
	"github.com/nex-harness/nex/pkg/store"
)

// DiscoveredSkill represents a skill found on disk (workspace/user) or in the registry,
// annotated with ZCode-style scope, bundled resources, and per-workspace visibility state.
type DiscoveredSkill struct {
	Name                   string          `json:"name"`
	Description            string          `json:"description"`
	Scope                  string          `json:"scope"` // "workspace" | "user" | "registry"
	SourceKind             string          `json:"source_kind"` // e.g. ".agents/skills", ".claude/skills", ".nex/skills", "registry"
	SourceDir              string          `json:"source_dir"`
	FilePath               string          `json:"file_path"`
	WorkspacePath          string          `json:"workspace_path,omitempty"`
	ContentMD              string          `json:"content_md,omitempty"`
	License                string          `json:"license,omitempty"`
	Compatibility          string          `json:"compatibility,omitempty"`
	DisableModelInvocation bool            `json:"disable_model_invocation,omitempty"`
	HasScripts             bool            `json:"has_scripts,omitempty"`
	HasReferences          bool            `json:"has_references,omitempty"`
	HasAssets              bool            `json:"has_assets,omitempty"`
	ShadowedBy             string          `json:"shadowed_by,omitempty"`
	GlobalEnabled          bool            `json:"global_enabled"`
	WorkspaceEnabled       bool            `json:"workspace_enabled"`
	WorkspaceOverridden    bool            `json:"workspace_overridden"`
	WorkspaceOverrides     map[string]bool `json:"workspace_overrides,omitempty"`
}

// DiscoverOptions configures which roots to scan.
type DiscoverOptions struct {
	WorkspacePath  string
	HomeDir        string
	DataDir        string
	RegistrySkills []store.SkillRecord
	Rules          map[string]store.SkillVisibilityRule
	IncludeContent bool
}

type scanRoot struct {
	dir           string
	scope         string
	sourceKind    string
	workspacePath string
}

// Discover scans workspace, user, and registry skill sources in ZCode/Pi precedence order
// (workspace > user > registry) and merges persisted visibility rules.
func Discover(opts DiscoverOptions) []DiscoveredSkill {
	home := strings.TrimSpace(opts.HomeDir)
	if home == "" {
		home, _ = os.UserHomeDir()
	}
	ws := store.NormalizeWorkspaceKey(opts.WorkspacePath)

	var roots []scanRoot
	if ws != "" {
		roots = append(roots,
			scanRoot{dir: filepath.Join(ws, ".agents", "skills"), scope: "workspace", sourceKind: ".agents/skills", workspacePath: ws},
			scanRoot{dir: filepath.Join(ws, ".claude", "skills"), scope: "workspace", sourceKind: ".claude/skills", workspacePath: ws},
			scanRoot{dir: filepath.Join(ws, ".nex", "skills"), scope: "workspace", sourceKind: ".nex/skills", workspacePath: ws},
		)
	}
	if home != "" {
		roots = append(roots,
			scanRoot{dir: filepath.Join(home, ".agents", "skills"), scope: "user", sourceKind: "~/.agents/skills"},
			scanRoot{dir: filepath.Join(home, ".claude", "skills"), scope: "user", sourceKind: "~/.claude/skills"},
			scanRoot{dir: filepath.Join(home, ".nex", "skills"), scope: "user", sourceKind: "~/.nex/skills"},
		)
	}

	var all []DiscoveredSkill
	seenPrimary := map[string]string{} // skillName -> winning scope label

	for _, root := range roots {
		found := scanSkillsDir(root, opts.IncludeContent)
		for _, sk := range found {
			if winner, exists := seenPrimary[sk.Name]; exists {
				sk.ShadowedBy = winner
			} else {
				seenPrimary[sk.Name] = sk.Scope
			}
			all = append(all, sk)
		}
	}

	for _, reg := range opts.RegistrySkills {
		name := strings.TrimSpace(reg.Name)
		if name == "" {
			continue
		}
		filePath := ""
		if opts.DataDir != "" {
			filePath = filepath.Join(opts.DataDir, "skills", name, "SKILL.md")
		}
		sk := DiscoveredSkill{
			Name:        name,
			Description: strings.TrimSpace(reg.Description),
			Scope:       "registry",
			SourceKind:  "registry",
			SourceDir:   filepath.Dir(filePath),
			FilePath:    filePath,
		}
		if opts.IncludeContent {
			sk.ContentMD = reg.ContentMD
		}
		if winner, exists := seenPrimary[sk.Name]; exists {
			sk.ShadowedBy = winner
		} else {
			seenPrimary[sk.Name] = sk.Scope
		}
		all = append(all, sk)
	}

	// Merge visibility rules
	for i := range all {
		ApplyVisibility(&all[i], opts.Rules, ws)
	}

	// Stable ordering: workspace first, then user, then registry; alphabetical by name inside each group
	scopeRank := map[string]int{"workspace": 0, "user": 1, "registry": 2}
	sort.SliceStable(all, func(i, j int) bool {
		ri, rj := scopeRank[all[i].Scope], scopeRank[all[j].Scope]
		if ri != rj {
			return ri < rj
		}
		if all[i].Name != all[j].Name {
			return all[i].Name < all[j].Name
		}
		return all[i].SourceKind < all[j].SourceKind
	})
	return all
}

// ApplyVisibility fills GlobalEnabled, WorkspaceEnabled, WorkspaceOverridden, and WorkspaceOverrides.
func ApplyVisibility(sk *DiscoveredSkill, rules map[string]store.SkillVisibilityRule, workspacePath string) {
	if sk == nil {
		return
	}
	rule, hasRule := rules[sk.Name]
	globalEnabled := true
	if hasRule && rule.GlobalEnabled != nil {
		globalEnabled = *rule.GlobalEnabled
	}
	sk.GlobalEnabled = globalEnabled
	if hasRule && len(rule.WorkspaceOverrides) > 0 {
		cp := make(map[string]bool, len(rule.WorkspaceOverrides))
		for k, v := range rule.WorkspaceOverrides {
			cp[k] = v
		}
		sk.WorkspaceOverrides = cp
	}
	vis, overridden := store.IsSkillVisibleInWorkspace(rules, sk.Name, workspacePath)
	sk.WorkspaceEnabled = vis
	sk.WorkspaceOverridden = overridden
}

// VisiblePromptSkills returns the deduplicated, visibility-filtered skills to advertise in the LLM system prompt.
func VisiblePromptSkills(opts DiscoverOptions) []prompts.SkillInfo {
	opts.IncludeContent = false
	discovered := Discover(opts)
	out := make([]prompts.SkillInfo, 0, len(discovered))
	for _, sk := range discovered {
		if sk.ShadowedBy != "" || sk.DisableModelInvocation {
			continue
		}
		if !sk.WorkspaceEnabled {
			continue
		}
		out = append(out, prompts.SkillInfo{
			Name:        sk.Name,
			Description: sk.Description,
			Path:        sk.FilePath,
		})
	}
	return out
}

func scanSkillsDir(root scanRoot, includeContent bool) []DiscoveredSkill {
	entries, err := os.ReadDir(root.dir)
	if err != nil {
		return nil
	}
	var out []DiscoveredSkill
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), ".") {
			continue
		}
		fullPath := filepath.Join(root.dir, entry.Name())
		info, err := os.Stat(fullPath)
		if err != nil {
			continue
		}
		if info.IsDir() {
			skillFile := filepath.Join(fullPath, "SKILL.md")
			raw, err := os.ReadFile(skillFile)
			if err != nil {
				continue
			}
			sk := parseSkillMarkdown(entry.Name(), string(raw), root, fullPath, skillFile, includeContent)
			sk.HasScripts = dirExists(filepath.Join(fullPath, "scripts"))
			sk.HasReferences = dirExists(filepath.Join(fullPath, "references"))
			sk.HasAssets = dirExists(filepath.Join(fullPath, "assets"))
			out = append(out, sk)
		} else if strings.HasSuffix(strings.ToLower(entry.Name()), ".md") && strings.ToUpper(entry.Name()) != "README.MD" {
			raw, err := os.ReadFile(fullPath)
			if err != nil {
				continue
			}
			fallbackName := strings.TrimSuffix(entry.Name(), filepath.Ext(entry.Name()))
			sk := parseSkillMarkdown(fallbackName, string(raw), root, root.dir, fullPath, includeContent)
			out = append(out, sk)
		}
	}
	return out
}

func dirExists(p string) bool {
	st, err := os.Stat(p)
	return err == nil && st.IsDir()
}

func parseSkillMarkdown(fallbackName, raw string, root scanRoot, sourceDir, filePath string, includeContent bool) DiscoveredSkill {
	fm, body := splitFrontmatter(raw)
	name := strings.TrimSpace(fm["name"])
	if name == "" {
		name = fallbackName
	}
	desc := strings.TrimSpace(fm["description"])
	if desc == "" {
		desc = firstMeaningfulLine(body)
	}
	disableModel := strings.EqualFold(strings.TrimSpace(fm["disable-model-invocation"]), "true")
	sk := DiscoveredSkill{
		Name:                   name,
		Description:            desc,
		Scope:                  root.scope,
		SourceKind:             root.sourceKind,
		SourceDir:              sourceDir,
		FilePath:               filePath,
		WorkspacePath:          root.workspacePath,
		License:                strings.TrimSpace(fm["license"]),
		Compatibility:          strings.TrimSpace(fm["compatibility"]),
		DisableModelInvocation: disableModel,
	}
	if includeContent {
		sk.ContentMD = raw
	}
	return sk
}

func splitFrontmatter(raw string) (map[string]string, string) {
	meta := map[string]string{}
	trimmed := strings.TrimPrefix(raw, "\xef\xbb\xbf")
	if !strings.HasPrefix(trimmed, "---") {
		return meta, trimmed
	}
	lines := strings.Split(trimmed, "\n")
	if len(lines) < 3 || strings.TrimSpace(lines[0]) != "---" {
		return meta, trimmed
	}
	endIdx := -1
	for i := 1; i < len(lines); i++ {
		if strings.TrimSpace(lines[i]) == "---" {
			endIdx = i
			break
		}
	}
	if endIdx < 0 {
		return meta, trimmed
	}
	for _, line := range lines[1:endIdx] {
		colon := strings.IndexByte(line, ':')
		if colon <= 0 {
			continue
		}
		k := strings.ToLower(strings.TrimSpace(line[:colon]))
		v := strings.TrimSpace(line[colon+1:])
		v = strings.Trim(v, `"'`)
		if k != "" && v != "" {
			meta[k] = v
		}
	}
	body := strings.Join(lines[endIdx+1:], "\n")
	return meta, body
}

func firstMeaningfulLine(body string) string {
	for _, line := range strings.Split(body, "\n") {
		t := strings.TrimSpace(line)
		if t == "" || strings.HasPrefix(t, "#") || strings.HasPrefix(t, "```") {
			continue
		}
		if len(t) > 180 {
			return t[:177] + "..."
		}
		return t
	}
	return ""
}
