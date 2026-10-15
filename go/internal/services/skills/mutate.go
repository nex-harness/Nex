// mutate.go: the destructive skills-service surface — copyToCommon,
// removeFromCommon, deleteSkill — each re-lists and locates the skill by id
// before touching the filesystem, and deleteSkill confines the removal to
// the known skill roots by comparing realpathed parents against realpathed
// roots (symlink ancestors cannot escape).
package skills

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/tylinux/nex/go/internal/skills"
)

// CopyToCommon ports ISkillsService.copyToCommon: copy the skill directory
// to .nex/skills (workspace scope) or ~/.nex/skills (user scope); an
// existing directory of the same name is refused.
func (s *Service) CopyToCommon(ctx context.Context, workspacePath, workspaceIdentity, skillID string) (newPath string, err error) {
	result, err := s.List(ctx, workspacePath, workspaceIdentity)
	if err != nil {
		return "", err
	}
	skill := findSkillByID(result.Skills, skillID)
	if skill == nil {
		return "", fmt.Errorf("Skill not found: %s", skillID)
	}
	sourceDir := filepath.Dir(skill.Path)
	var commonRoot string
	if skill.Scope == "workspace" {
		commonRoot = filepath.Join(workspacePath, ".nex", "skills")
	} else {
		commonRoot = skills.UserNexSkillRoot(s.homeDir)
	}
	targetDir := filepath.Join(commonRoot, filepath.Base(sourceDir))
	if _, err := os.Stat(targetDir); err == nil {
		return "", fmt.Errorf("通用目录已存在同名技能: %s", filepath.Base(sourceDir))
	}
	if err := os.MkdirAll(commonRoot, 0o755); err != nil {
		return "", err
	}
	if err := copyDirTree(sourceDir, targetDir); err != nil {
		return "", err
	}
	return filepath.Join(targetDir, skills.FileName), nil
}

// RemoveFromCommon ports ISkillsService.removeFromCommon: only skills under
// the workspace or user common roots can be removed.
func (s *Service) RemoveFromCommon(ctx context.Context, workspacePath, workspaceIdentity, skillID string) error {
	result, err := s.List(ctx, workspacePath, workspaceIdentity)
	if err != nil {
		return err
	}
	skill := findSkillByID(result.Skills, skillID)
	if skill == nil {
		return fmt.Errorf("Skill not found: %s", skillID)
	}
	normalizedPath := strings.ToLower(normalizeSkillConfigPath(skill.Path))
	userCommonRoot := strings.ToLower(normalizeSkillConfigPath(skills.UserNexSkillRoot(s.homeDir)))
	workspaceCommonRoot := strings.ToLower(normalizeSkillConfigPath(filepath.Join(workspacePath, ".nex", "skills")))
	inUserCommon := strings.HasPrefix(normalizedPath, userCommonRoot+"/")
	inWorkspaceCommon := strings.HasPrefix(normalizedPath, workspaceCommonRoot+"/")
	if !inUserCommon && !inWorkspaceCommon {
		return fmt.Errorf("该技能不在通用目录中")
	}
	return os.RemoveAll(filepath.Dir(skill.Path))
}

// DeleteSkill ports ISkillsService.deleteSkill: only workspace/user scopes;
// the delete is confined to the known skill roots by comparing realpathed
// parents against realpathed roots (symlink ancestors cannot escape).
func (s *Service) DeleteSkill(ctx context.Context, workspacePath, workspaceIdentity, skillID string) error {
	result, err := s.List(ctx, workspacePath, workspaceIdentity)
	if err != nil {
		return err
	}
	skill := findSkillByID(result.Skills, skillID)
	if skill == nil {
		return fmt.Errorf("Skill not found: %s", skillID)
	}
	if skill.Scope == "plugin" {
		return fmt.Errorf("插件提供的技能不可单独删除，请卸载对应插件")
	}
	sourcePath := skill.SourcePath
	if sourcePath == "" {
		sourcePath = skill.Path
	}
	skillDir := filepath.Dir(sourcePath)
	skillLeafName := filepath.Base(skillDir)
	// Resolve the PARENT only, never the leaf: a symlinked leaf stays a link
	// (delete removes the link, not the target), while realpathing the parent
	// expands ancestor symlinks so the containment check sees real locations.
	canonicalParent, err := filepath.EvalSymlinks(filepath.Dir(skillDir))
	if err != nil {
		return fmt.Errorf("该技能不可删除: %s", skill.Path)
	}
	allowedRoots := skills.ResolveAncestorWorkspaceRoots(workspacePath)
	allowedRoots = append(allowedRoots,
		skills.UserNexSkillRoot(s.homeDir),
		skills.UserAgentsSkillRoot(s.homeDir))
	contained := false
	for _, root := range allowedRoots {
		canonicalRoot, err := filepath.EvalSymlinks(root)
		if err != nil {
			canonicalRoot = root
		}
		relative, err := filepath.Rel(canonicalRoot, canonicalParent)
		if err != nil {
			continue
		}
		if relative == "" ||
			(relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator)) && !filepath.IsAbs(relative)) {
			contained = true
			break
		}
	}
	if !contained {
		return fmt.Errorf("该技能不可删除: %s", skill.Path)
	}
	return os.RemoveAll(filepath.Join(canonicalParent, skillLeafName))
}

func findSkillByID(list []Summary, id string) *Summary {
	for index := range list {
		if list[index].ID == id {
			return &list[index]
		}
	}
	return nil
}

func copyDirTree(source, target string) error {
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
		return fmt.Errorf("unsupported copy source: %s", source)
	}
	if err := os.MkdirAll(target, 0o755); err != nil {
		return err
	}
	entries, err := os.ReadDir(source)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if err := copyDirTree(filepath.Join(source, entry.Name()), filepath.Join(target, entry.Name())); err != nil {
			return err
		}
	}
	return nil
}

func normalizeSkillConfigPath(path string) string {
	return strings.ReplaceAll(path, "\\", "/")
}

// readSkillEnabledMap reads the skills.enable map from the CLI config
// (readSkillEnabledMap: key is the /-normalized SKILL.md path).
func (s *Service) readSkillEnabledMap() map[string]bool {
	config := s.readCliConfig()
	skillsConfig, _ := config["skills"].(map[string]any)
	out := map[string]bool{}
	for path, value := range skillsConfig {
		record, ok := value.(map[string]any)
		if !ok {
			continue
		}
		if enable, ok := record["enable"].(bool); ok {
			out[normalizeSkillConfigPath(path)] = enable
		}
	}
	return out
}

// writeSkillEnabledMap ports writeSkillEnabledMap: enabled (the default) is
// NOT persisted — the override is removed so plugin/default changes flow
// through; only { enable: false } records stay.
func (s *Service) writeSkillEnabledMap(next map[string]bool) error {
	config := s.readCliConfig()
	skillsConfig, _ := config["skills"].(map[string]any)
	if skillsConfig == nil {
		skillsConfig = map[string]any{}
	}
	paths := make([]string, 0, len(next))
	for path := range next {
		paths = append(paths, path)
	}
	sort.Strings(paths)
	for _, path := range paths {
		normalizedPath := normalizeSkillConfigPath(path)
		if next[path] {
			delete(skillsConfig, normalizedPath)
		} else {
			enable := false
			skillsConfig[normalizedPath] = map[string]any{"enable": enable}
		}
	}
	if len(skillsConfig) > 0 {
		config["skills"] = skillsConfig
	} else {
		delete(config, "skills")
	}
	return s.writeCliConfig(config)
}

func (s *Service) readCliConfig() map[string]any {
	raw, err := os.ReadFile(s.cliConfigPath())
	if err != nil {
		return map[string]any{}
	}
	var parsed map[string]any
	if err := json.Unmarshal(raw, &parsed); err != nil {
		return map[string]any{}
	}
	if parsed == nil {
		return map[string]any{}
	}
	return parsed
}

func (s *Service) writeCliConfig(config map[string]any) error {
	if err := os.MkdirAll(filepath.Dir(s.cliConfigPath()), 0o755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(config, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(s.cliConfigPath(), append(data, '\n'), 0o644)
}
