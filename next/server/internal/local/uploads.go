package local

// Skill and prompt uploads (docs/next/config-mcp-skills.md P2).
//
// The unchanged Skills settings page has list/setEnabled/delete only, so the
// content-addressed bundles enter the doc through HTTP helpers wired by
// httpapi.ManagerAdmin (httpapi.SkillUploader):
//
//	POST /api/config/skills?name=<name>[&scope=global|runner&runnerId=<id>]
//	  body: SKILL.md text, {"content": "..."} or {"files":[{path, content|contentBase64}]}
//	POST /api/config/prompts?name=<name>[&scope=...&runnerId=...]
//	  body: markdown text or {"content": "..."}
//	POST /api/config/skills/delete?name=<name>   (same as Settings → Delete)
//
// Both upsert by name (a re-upload is the update path). Nothing executes the
// uploaded text: the runner materializes the files and pi reads them.

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/nex-harness/nex/server/internal/configstore"
)

// UploadBundleFiles stores one skill/prompt bundle and references it in the doc
// (the file-list form used by the JSON and multipart upload routes). It returns
// the doc version and the content digest.
func (m *Manager) UploadBundleFiles(scope, runnerID, kind, name, description string, files []configstore.BundleFile) (UploadResult, error) {
	trimmedName := strings.TrimSpace(name)
	if trimmedName == "" {
		return UploadResult{}, fmt.Errorf("name is required")
	}
	doc, digest, err := m.store.UpsertBundle(scope, runnerID, configstore.BundleRef{
		Name:        trimmedName,
		Kind:        kind,
		Enabled:     true,
		Description: strings.TrimSpace(description),
	}, files)
	if err != nil {
		return UploadResult{}, err
	}
	return UploadResult{Name: trimmedName, Kind: kind, Digest: digest, Version: doc.Version, Files: len(files)}, nil
}

// DeleteBundle removes a skill/prompt from the doc.
func (m *Manager) DeleteBundle(scope, runnerID, kind, name string) (UploadResult, error) {
	doc, err := m.store.RemoveBundle(scope, runnerID, kind, name)
	if err != nil {
		return UploadResult{}, err
	}
	return UploadResult{Name: name, Kind: kind, Version: doc.Version}, nil
}

// UploadResult is the HTTP answer of an upload (never a secret).
type UploadResult struct {
	Name    string `json:"name"`
	Kind    string `json:"kind"`
	Digest  string `json:"digest,omitempty"`
	Version uint64 `json:"version"`
	Files   int    `json:"files,omitempty"`
}

// SkillUploadRequest is the decoded JSON body of one upload.
type SkillUploadRequest struct {
	Content     string                   `json:"content"`
	Description string                   `json:"description"`
	Files       []configstore.BundleFile `json:"files"`
}

// UploadSkillOrPrompt implements httpapi.SkillUploader for one kind.
func (m *Manager) UploadSkillOrPrompt(kind, scope, runnerID, name string, body []byte) (UploadResult, error) {
	files, description, err := parseUploadBody(kind, name, body)
	if err != nil {
		return UploadResult{}, err
	}
	return m.UploadBundleFiles(scope, runnerID, kind, name, description, files)
}

// DeleteSkillOrPrompt implements httpapi.SkillUploader's delete.
func (m *Manager) DeleteSkillOrPrompt(kind, scope, runnerID, name string) (UploadResult, error) {
	return m.DeleteBundle(scope, runnerID, kind, name)
}

func parseUploadBody(kind, name string, body []byte) ([]configstore.BundleFile, string, error) {
	trimmed := strings.TrimSpace(string(body))
	description := ""
	var request SkillUploadRequest
	if strings.HasPrefix(trimmed, "{") {
		if err := json.Unmarshal(body, &request); err != nil {
			return nil, "", fmt.Errorf("decoding upload: %w", err)
		}
		description = request.Description
		if len(request.Files) == 0 && request.Content == "" {
			return nil, "", fmt.Errorf(`body must carry "content" or "files"`)
		}
		if len(request.Files) > 0 {
			return request.Files, description, nil
		}
	}
	content := request.Content
	if content == "" {
		content = string(body)
	}
	if strings.TrimSpace(content) == "" {
		return nil, "", fmt.Errorf("content is empty")
	}
	if description == "" {
		description = frontmatterDescription(content)
	}
	if kind == "prompt" {
		return []configstore.BundleFile{{Path: name + ".md", Content: content}}, description, nil
	}
	return []configstore.BundleFile{{Path: "SKILL.md", Content: content}}, description, nil
}

// frontmatterDescription reads `description:` from a YAML frontmatter block.
func frontmatterDescription(content string) string {
	normalized := strings.ReplaceAll(strings.TrimPrefix(content, "\ufeff"), "\r\n", "\n")
	if !strings.HasPrefix(normalized, "---\n") {
		return ""
	}
	rest := normalized[4:]
	end := strings.Index(rest, "\n---")
	if end < 0 {
		return ""
	}
	for _, line := range strings.Split(rest[:end], "\n") {
		key, value, found := strings.Cut(line, ":")
		if !found || strings.TrimSpace(key) != "description" {
			continue
		}
		return strings.Trim(strings.TrimSpace(value), `"'`)
	}
	return ""
}
