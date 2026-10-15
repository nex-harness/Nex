// frontmatter.go: the SKILL.md frontmatter reader, a byte-faithful port of
// readFrontmatter in packages/services/src/skills/skillsService.ts (lines
// 296–486). It normalizes newlines, splits leaked body out of the frontmatter
// (historical skills wrote their H1 before the closing ---), and falls back
// from strict YAML to a loose line reader for the unquoted-Chinese-description
// shapes strict parsing rejects.
package skills

import (
	"regexp"
	"strings"

	"go.yaml.in/yaml/v4"
)

// ParsedFrontmatter is the readFrontmatter result.
type ParsedFrontmatter struct {
	HasFrontmatter bool
	Name           string
	Description    string
	Body           string
	// Keys are the top-level frontmatter keys (kept for capability checks,
	// no longer surfaced as unknown-key warnings).
	Keys []string
	// ParseOK reports whether strict YAML parsing succeeded.
	ParseOK bool
}

var frontmatterRe = regexp.MustCompile(`(?s)\A---\n(.*?)\n---(?:\n|$)`)

// markdownHeadingRe matches the leaked-body heading split: a line at index>0
// that is an ATX heading (#{1..6} followed by non-space content).
var markdownHeadingRe = regexp.MustCompile(`^#{1,6}\s+\S`)

// ReadFrontmatter parses one SKILL.md's content.
func ReadFrontmatter(content string) ParsedFrontmatter {
	// SKILL.md may come from Windows toolchains; normalize newlines first.
	normalized := strings.ReplaceAll(strings.ReplaceAll(content, "\r\n", "\n"), "\r", "\n")
	match := frontmatterRe.FindStringSubmatch(normalized)
	if match == nil {
		return ParsedFrontmatter{
			HasFrontmatter: false,
			Body:           strings.TrimSpace(normalized),
		}
	}

	frontmatterText := match[1]
	metadataText, leakedBody := splitFrontmatterAndLeakedBody(frontmatterText)
	bodyAfterFrontmatter := strings.TrimSpace(normalized[len(match[0]):])
	var bodyParts []string
	for _, part := range []string{leakedBody, bodyAfterFrontmatter} {
		if len(strings.TrimSpace(part)) > 0 {
			bodyParts = append(bodyParts, part)
		}
	}
	body := strings.TrimSpace(strings.Join(bodyParts, "\n\n"))
	looseFields := readLooseFrontmatterFields(metadataText)
	looseKeys := extractLooseFrontmatterKeys(metadataText)

	// Historical Chinese descriptions were written unquoted (`触发场景: ...`);
	// `: ` inside a YAML plain scalar is parsed as a mapping separator, so
	// strict parsing only errors. Adopt the loose read directly instead of
	// repeatedly entering the failing parse path in concurrent tests.
	if hasYAMLUnsafeLooseInlineField(metadataText) {
		return ParsedFrontmatter{
			HasFrontmatter: true,
			Name:           looseFields.name,
			Description:    looseFields.description,
			Body:           body,
			Keys:           looseKeys,
			ParseOK:        false,
		}
	}

	// yaml.Unmarshal returns an error for a non-mapping document (e.g. a bare
	// scalar), whereas the TS isObjectRecord check covers that; both land in
	// the same loose fallback below.
	var parsed map[string]any
	if err := yaml.Unmarshal([]byte(metadataText), &parsed); err != nil || parsed == nil {
		// Historical skills mixed body text in frontmatter; fall back to the
		// loose field reader so existing metadata (Chinese description etc.)
		// is not lost.
		return ParsedFrontmatter{
			HasFrontmatter: true,
			Name:           looseFields.name,
			Description:    looseFields.description,
			Body:           body,
			Keys:           looseKeys,
			ParseOK:        false,
		}
	}

	name := readFrontmatterString(parsed["name"])
	if name == "" {
		name = looseFields.name
	}
	description := readFrontmatterString(parsed["description"])
	if description == "" {
		description = looseFields.description
	}
	keys := make([]string, 0, len(parsed))
	for key := range parsed {
		keys = append(keys, key)
	}
	sortStrings(keys)
	return ParsedFrontmatter{
		HasFrontmatter: true,
		Name:           name,
		Description:    description,
		Body:           body,
		Keys:           keys,
		ParseOK:        true,
	}
}

// splitFrontmatterAndLeakedBody moves a leaked Markdown heading (and what
// follows) from the frontmatter back into the body.
func splitFrontmatterAndLeakedBody(frontmatterText string) (metadataText, leakedBody string) {
	lines := strings.Split(frontmatterText, "\n")
	leakedBodyStartIndex := -1
	for index, line := range lines {
		if index > 0 && markdownHeadingRe.MatchString(line) {
			leakedBodyStartIndex = index
			break
		}
	}
	if leakedBodyStartIndex < 0 {
		return frontmatterText, ""
	}
	return strings.TrimRight(strings.Join(lines[:leakedBodyStartIndex], "\n"), " \t"),
		strings.TrimSpace(strings.Join(lines[leakedBodyStartIndex:], "\n"))
}

type looseFields struct{ name, description string }

// readLooseFrontmatterFields ports readLooseFrontmatterFields: name/description
// via line scanning, with `|`/`>` block scalars joined per YAML block style.
func readLooseFrontmatterFields(frontmatterText string) looseFields {
	var out looseFields
	lines := strings.Split(frontmatterText, "\n")
	for index := 0; index < len(lines); index++ {
		line := lines[index]
		colonIndex := strings.Index(line, ":")
		if colonIndex < 0 {
			continue
		}
		key := strings.TrimSpace(line[:colonIndex])
		rawValue := strings.TrimSpace(line[colonIndex+1:])
		switch key {
		case "name":
			out.name = readLooseInlineString(rawValue)
		case "description":
			if rawValue != "" && (rawValue[0] == '|' || rawValue[0] == '>') {
				blockStyle := rawValue[0]
				var blockLines []string
				for nextIndex := index + 1; nextIndex < len(lines); nextIndex++ {
					nextLine := lines[nextIndex]
					if !strings.HasPrefix(nextLine, " ") && !strings.HasPrefix(nextLine, "\t") {
						break
					}
					blockLines = append(blockLines, trimLeadingBlockIndent(nextLine))
					index = nextIndex
				}
				if blockStyle == '>' {
					out.description = strings.Join(blockLines, " ")
				} else {
					out.description = strings.Join(blockLines, "\n")
				}
			} else {
				out.description = readLooseInlineString(rawValue)
			}
		}
	}
	return out
}

// trimLeadingBlockIndent strips up to two leading spaces (the TS
// `replace(/^\s{1,2}/, "")`).
func trimLeadingBlockIndent(line string) string {
	if strings.HasPrefix(line, "  ") {
		return line[2:]
	}
	if strings.HasPrefix(line, " ") {
		return line[1:]
	}
	if strings.HasPrefix(line, "\t") {
		return line[1:]
	}
	return line
}

// extractLooseFrontmatterKeys collects top-level frontmatter keys only
// (indented lines are child fields; comments are skipped).
func extractLooseFrontmatterKeys(frontmatterText string) []string {
	var keys []string
	for _, line := range strings.Split(frontmatterText, "\n") {
		if len(line) == 0 {
			continue
		}
		// The TS tests /^\s/ (leading space OR tab).
		if line[0] == ' ' || line[0] == '\t' {
			continue
		}
		if strings.HasPrefix(strings.TrimSpace(line), "#") {
			continue
		}
		colonIndex := strings.Index(line, ":")
		if colonIndex <= 0 {
			continue
		}
		key := strings.TrimSpace(line[:colonIndex])
		if len(key) == 0 {
			continue
		}
		if !containsString(keys, key) {
			keys = append(keys, key)
		}
	}
	return keys
}

// hasYAMLUnsafeLooseInlineField ports hasYamlUnsafeLooseInlineField: an
// unquoted name/description value containing `: ` breaks strict YAML.
func hasYAMLUnsafeLooseInlineField(frontmatterText string) bool {
	for _, line := range strings.Split(frontmatterText, "\n") {
		colonIndex := strings.Index(line, ":")
		if colonIndex < 0 {
			continue
		}
		key := strings.TrimSpace(line[:colonIndex])
		if key != "name" && key != "description" {
			continue
		}
		rawValue := strings.TrimSpace(line[colonIndex+1:])
		if len(rawValue) == 0 ||
			strings.HasPrefix(rawValue, "\"") ||
			strings.HasPrefix(rawValue, "'") ||
			strings.HasPrefix(rawValue, "|") ||
			strings.HasPrefix(rawValue, ">") {
			continue
		}
		if strings.Contains(rawValue, ": ") {
			return true
		}
	}
	return false
}

func readLooseInlineString(value string) string {
	trimmed := strings.TrimSpace(value)
	if len(trimmed) >= 2 {
		first, last := trimmed[0], trimmed[len(trimmed)-1]
		if (first == '"' && last == '"') || (first == '\'' && last == '\'') {
			// The TS strips exactly one leading and one trailing quote
			// (regex `^["']|["']$`), not a full run of them.
			return trimmed[1 : len(trimmed)-1]
		}
	}
	return trimmed
}

func readFrontmatterString(value any) string {
	switch v := value.(type) {
	case string:
		return v
	case nil:
		return ""
	default:
		// String(value): numbers become their canonical form.
		return scalarToString(v)
	}
}
