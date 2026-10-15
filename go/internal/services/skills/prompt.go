// prompt.go: the buildPromptContext helpers — skill-mention extraction,
// the activated-skills prompt block, and the skills-audit-log writer.
package skills

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

var skillMentionRe = regexp.MustCompile(`\$([a-z0-9]+(?:-[a-z0-9]+)*)`)

func collectMentionedSkillNames(prompt string) map[string]bool {
	names := map[string]bool{}
	for _, match := range skillMentionRe.FindAllStringSubmatch(prompt, -1) {
		if match[1] != "" {
			names[match[1]] = true
		}
	}
	return names
}

func escapeXmlAttribute(value string) string {
	replacer := strings.NewReplacer("&", "&amp;", `"`, "&quot;", "<", "&lt;", ">", "&gt;")
	return replacer.Replace(value)
}

// buildActivatedSkillsPromptBlock ports buildActivatedSkillsPromptBlock.
func buildActivatedSkillsPromptBlock(skills []Summary) string {
	var builder strings.Builder
	builder.WriteString("<available_skills>")
	for _, skill := range skills {
		builder.WriteString("\n")
		builder.WriteString(fmt.Sprintf(`<activated_skill name="%s" path="%s">`, escapeXmlAttribute(skill.Name), escapeXmlAttribute(skill.Path)))
		builder.WriteString("\n")
		builder.WriteString(skill.Body)
		builder.WriteString("\n</activated_skill>")
	}
	if len(skills) > 0 {
		builder.WriteString("\n")
	}
	builder.WriteString("</available_skills>")
	return builder.String()
}

type skillsAuditLogEntry struct {
	CreatedAt           int64    `json:"createdAt"`
	WorkspacePath       string   `json:"workspacePath"`
	WorkspaceIdentity   *string  `json:"workspaceIdentity"`
	ActivatedSkillNames []string `json:"activatedSkillNames"`
}

func (s *Service) appendSkillsAuditLog(workspacePath, workspaceIdentity string, names []string) error {
	if err := os.MkdirAll(s.skillSettingsDir(), 0o755); err != nil {
		return err
	}
	identity := (*string)(nil)
	if workspaceIdentity != "" {
		value := workspaceIdentity
		identity = &value
	}
	entry := skillsAuditLogEntry{
		CreatedAt:           s.options.Now(),
		WorkspacePath:       workspacePath,
		WorkspaceIdentity:   identity,
		ActivatedSkillNames: names,
	}
	data, err := json.Marshal(entry)
	if err != nil {
		return err
	}
	file, err := os.OpenFile(filepath.Join(s.skillSettingsDir(), "skills-audit.log"),
		os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	defer file.Close()
	_, err = file.Write(append(data, '\n'))
	return err
}
