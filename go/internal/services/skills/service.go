// Package skills is the ISkillsService port (step 3.7): skill discovery over
// the shared internal/skills library, the CLI-config-backed enable map, the
// prompt-context builder with its audit log, and the common-directory copy /
// remove / delete guards.
//
// Source: packages/services/src/skills/skillsService.ts. All destructive
// operations (copyToCommon / removeFromCommon / deleteSkill) re-list and locate
// the skill by id first, so a stale UI id cannot act on arbitrary paths.
package skills

import (
	"context"
	"fmt"
	"path/filepath"
	"sync"
	"time"
)

// ChannelName is the rpc channel name (ServiceChannels.Skills).
const ChannelName = "skills"

// Capability ports SkillsCapability: the user scope is desktop-only
// (resolveCapabilities — NEX_PROCESS_LABEL marks the desktop runtime; the
// Go server sets the flag through Options).
type Capability struct {
	UserScopeAvailable bool   `json:"userScopeAvailable"`
	UserScopeReason    string `json:"userScopeReason,omitempty"`
}

// Summary ports SkillSummary. Path is the canonical SKILL.md file path (the
// UI generates skill mentions from it); SourcePath is the pre-realpath scan
// path used to delete symlinked skills without touching their target.
type Summary struct {
	ID          string          `json:"id"`
	Name        string          `json:"name"`
	Description string          `json:"description"`
	Body        string          `json:"body"`
	Path        string          `json:"path"`
	SourcePath  string          `json:"sourcePath,omitempty"`
	Scope       string          `json:"scope"` // workspace | user | plugin
	Enabled     bool            `json:"enabled"`
	PluginName  string          `json:"pluginName,omitempty"`
	PluginID    string          `json:"pluginId,omitempty"`
	Metadata    *skillsMetadata `json:"metadata,omitempty"`
}

// skillsMetadata ports SkillMetadata (_meta.json next to SKILL.md).
type skillsMetadata struct {
	Slug        string  `json:"slug,omitempty"`
	Version     string  `json:"version,omitempty"`
	OwnerID     string  `json:"ownerId,omitempty"`
	PublishedAt float64 `json:"publishedAt,omitempty"`
}

// Diagnostic ports SkillDiagnostic.
type Diagnostic struct {
	Code      string `json:"code"`
	Severity  string `json:"severity"` // warning | error
	Message   string `json:"message"`
	Path      string `json:"path,omitempty"`
	SkillName string `json:"skillName,omitempty"`
}

// ListResult ports SkillsListResult.
type ListResult struct {
	Skills      []Summary    `json:"skills"`
	Capability  Capability   `json:"capability"`
	Diagnostics []Diagnostic `json:"diagnostics"`
}

// PromptContext ports SkillsPromptContext.
type PromptContext struct {
	Prompt              string   `json:"prompt"`
	ActivatedSkillNames []string `json:"activatedSkillNames"`
}

// Options carry the runtime seams.
type Options struct {
	// HomeDir resolves the user-level roots and the CLI config path.
	HomeDir string
	// IsDesktopRuntime gates the user scope (the TS checks NEX_PROCESS_LABEL).
	IsDesktopRuntime bool
	// Now is the clock seam for the audit log timestamps.
	Now func() int64
}

// Service is the ISkillsService implementation.
type Service struct {
	options    Options
	homeDir    string
	writeMutex sync.Mutex // the TS serializes setEnabled through a write queue
}

// New builds the service.
func New(options Options) *Service {
	if options.Now == nil {
		options.Now = func() int64 { return time.Now().UnixMilli() }
	}
	return &Service{options: options, homeDir: options.HomeDir}
}

// cliConfigPath is ~/.nex/cli/config.json — the enable-map state owner.
func (s *Service) cliConfigPath() string {
	return filepath.Join(s.homeDir, ".nex", "cli", "config.json")
}

// skillSettingsDir is ~/.nex/v2 — the audit log root (SKILL_SETTINGS_DIR).
func (s *Service) skillSettingsDir() string {
	return filepath.Join(s.homeDir, ".nex", "v2")
}

func (s *Service) capability() Capability {
	if s.options.IsDesktopRuntime {
		return Capability{UserScopeAvailable: true}
	}
	return Capability{UserScopeAvailable: false, UserScopeReason: "desktop_only"}
}

// List ports ISkillsService.list.
func (s *Service) List(ctx context.Context, workspacePath, workspaceIdentity string) (*ListResult, error) {
	capability := s.capability()
	provider := "glm"
	discovered, diagnostics := s.discover(workspacePath, capability.UserScopeAvailable, provider)
	enabledByPath := s.readSkillEnabledMap()
	for index := range discovered {
		// attachEnabledState: absent override → enabled (true is the default
		// and never persisted).
		discovered[index].Enabled = true
		if enable, ok := enabledByPath[normalizeSkillConfigPath(discovered[index].Path)]; ok {
			discovered[index].Enabled = enable
		}
	}
	return &ListResult{Skills: discovered, Capability: capability, Diagnostics: diagnostics}, nil
}

// SetEnabled ports ISkillsService.setEnabled (serialized writes).
func (s *Service) SetEnabled(ctx context.Context, workspacePath, workspaceIdentity, skillID string, enabled bool) error {
	s.writeMutex.Lock()
	defer s.writeMutex.Unlock()
	capability := s.capability()
	discovered, _ := s.discover(workspacePath, capability.UserScopeAvailable, "glm")
	var found *Summary
	for index := range discovered {
		if discovered[index].ID == skillID {
			found = &discovered[index]
			break
		}
	}
	if found == nil {
		return fmt.Errorf("Skill not found: %s", skillID)
	}
	return s.writeSkillEnabledMap(map[string]bool{
		normalizeSkillConfigPath(found.Path): enabled,
	})
}

// BuildPromptContext ports ISkillsService.buildPromptContext: only skills
// explicitly mentioned ($name) AND enabled enter the context; the activation
// is audited.
func (s *Service) BuildPromptContext(ctx context.Context, workspacePath, workspaceIdentity, prompt string) (*PromptContext, error) {
	mentioned := collectMentionedSkillNames(prompt)
	if len(mentioned) == 0 {
		return &PromptContext{Prompt: prompt, ActivatedSkillNames: []string{}}, nil
	}
	result, err := s.List(ctx, workspacePath, workspaceIdentity)
	if err != nil {
		return nil, err
	}
	var activated []Summary
	for _, skill := range result.Skills {
		if skill.Enabled && mentioned[skill.Name] {
			activated = append(activated, skill)
		}
	}
	if len(activated) == 0 {
		return &PromptContext{Prompt: prompt, ActivatedSkillNames: []string{}}, nil
	}
	names := make([]string, 0, len(activated))
	for _, skill := range activated {
		names = append(names, skill.Name)
	}
	if err := s.appendSkillsAuditLog(workspacePath, workspaceIdentity, names); err != nil {
		return nil, err
	}
	return &PromptContext{
		Prompt:              prompt + "\n\n" + buildActivatedSkillsPromptBlock(activated),
		ActivatedSkillNames: names,
	}, nil
}
