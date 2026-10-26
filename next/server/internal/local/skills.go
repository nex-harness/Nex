package local

// `skills` from the config doc (docs/next/config-mcp-skills.md P2; research
// §A3/C5).
//
// The unchanged Settings → Skills page calls `list`, `setEnabled` and
// `deleteSkill`; each is answered from the doc's effective skill bundles for
// the calling runner's scope. Bundles are uploaded through
// `POST /api/config/skills` (there is no upload button in the UI), so a
// re-upload with the same name is the update path.
//
// The ids use the UI's provider prefix (`glm:<slug>`) because the settings
// list filters out skills whose id/path do not come from the agent provider;
// the path points into the content-addressed store, which is the bundle's real
// location on the server.

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/tylinux/nex-next/server/internal/configstore"
	"github.com/tylinux/nex-next/server/internal/rpcwire"
)

// skillIDPrefix matches the UI's agent-provider id convention.
const skillIDPrefix = "glm:"

// Skills serves the skills channel.
type Skills struct {
	manager *Manager
}

func newSkills(manager *Manager) *Skills {
	return &Skills{manager: manager}
}

// NewSkillsChannel exposes the skills channel to the composition root.
func NewSkillsChannel(manager *Manager) Channel { return newSkills(manager) }

// Call implements Channel.
func (s *Skills) Call(_ context.Context, _ *Env, method string, arg rpcwire.Value) (rpcwire.Value, error) {
	switch method {
	case "list":
		return s.list(arg)
	case "setEnabled":
		return s.setEnabled(arg)
	case "deleteSkill":
		return s.delete(arg)
	case "copyToCommon", "removeFromCommon":
		return rpcwire.Undefined{}, nil
	case "buildPromptContext":
		return json.RawMessage(`{"prompt":"","activatedSkillNames":[]}`), nil
	}
	return nil, &MethodNotFoundError{Method: method}
}

// Listen implements Channel.
func (s *Skills) Listen(context.Context, *Env, string, rpcwire.Value, func(rpcwire.Value)) (func(), error) {
	return nil, nil
}

func (s *Skills) list(arg rpcwire.Value) (rpcwire.Value, error) {
	runnerID := s.manager.runnerFor(readViewTarget(arg))
	doc, err := s.manager.store.Snapshot()
	if err != nil {
		return nil, err
	}
	effective := doc.EffectiveForRunner(runnerID)
	skills := []any{}
	diagnostics := []any{}
	for _, ref := range effective.Skills {
		row := map[string]any{
			"id":          skillIDPrefix + ref.Name,
			"name":        ref.Name,
			"description": ref.Description,
			"body":        "",
			"path":        s.manager.store.BundleDir(ref.Digest),
			"scope":       "user",
			"enabled":     ref.Enabled,
		}
		if bundle, err := s.manager.store.LoadBundle(ref.Digest); err == nil {
			if body := skillBody(bundle); body != "" {
				row["body"] = body
			}
		} else {
			diagnostics = append(diagnostics, map[string]any{
				"code":     "skill_read_failed",
				"severity": "warning",
				"message":  err.Error(),
				"path":     s.manager.store.BundleDir(ref.Digest),
			})
		}
		skills = append(skills, row)
	}
	body, err := json.Marshal(map[string]any{
		"skills":      skills,
		"capability":  map[string]any{"userScopeAvailable": false, "userScopeReason": "desktop_only"},
		"diagnostics": diagnostics,
	})
	if err != nil {
		return nil, err
	}
	return json.RawMessage(body), nil
}

// skillBody returns the SKILL.md text minus its frontmatter.
func skillBody(bundle configstore.Bundle) string {
	for _, file := range bundle.Files {
		if strings.EqualFold(file.Path, "SKILL.md") {
			return stripFrontmatter(file.Content)
		}
	}
	return ""
}

func stripFrontmatter(content string) string {
	trimmed := strings.TrimPrefix(strings.TrimPrefix(content, "\ufeff"), "---\n")
	if trimmed == content {
		if strings.HasPrefix(content, "---\r\n") {
			if end := strings.Index(content[5:], "\r\n---"); end >= 0 {
				return strings.TrimSpace(content[5+end+6:])
			}
		}
		return strings.TrimSpace(content)
	}
	if end := strings.Index(trimmed, "\n---"); end >= 0 {
		return strings.TrimSpace(trimmed[end+4:])
	}
	return strings.TrimSpace(content)
}

// bundleNameFromSkillID strips the provider prefix the UI adds.
func bundleNameFromSkillID(id string) string {
	return strings.TrimPrefix(strings.TrimSpace(id), skillIDPrefix)
}

func (s *Skills) setEnabled(arg rpcwire.Value) (rpcwire.Value, error) {
	fields, err := decodeObject(arg)
	if err != nil {
		return nil, err
	}
	name := bundleNameFromSkillID(jsonString(fields["skillId"]))
	if name == "" {
		return nil, fmt.Errorf("skillId is required")
	}
	enabled := boolField(fields, "enabled")
	if _, err := s.manager.store.SetBundleEnabled("", "", "skill", name, enabled); err != nil {
		return nil, err
	}
	return s.list(arg)
}

func (s *Skills) delete(arg rpcwire.Value) (rpcwire.Value, error) {
	fields, err := decodeObject(arg)
	if err != nil {
		return nil, err
	}
	name := bundleNameFromSkillID(jsonString(fields["skillId"]))
	if name == "" {
		return nil, fmt.Errorf("skillId is required")
	}
	if _, err := s.manager.store.RemoveBundle("", "", "skill", name); err != nil {
		return nil, err
	}
	return s.list(arg)
}
