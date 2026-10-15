// Package commands is the ICommandsService port (step 3.7): markdown command
// files (.nex/commands, .agents/commands at user and project level), the
// enabled-override map in the CLI config, and the read-only plugin command
// discovery over the shared plugins library.
//
// Source: packages/services/src/commands/{commandsService,commandFileParser}.ts.
package commands

import (
	"regexp"
	"strings"
)

// parsedCommandFile is the ParsedCommandFile shape.
type parsedCommandFile struct {
	Name         string
	Prompt       string
	Content      string
	FilePath     string
	Description  string
	ArgumentHint string
}

var frontmatterKeyRe = regexp.MustCompile(`^([A-Za-z0-9_-]+)\s*:`)

// getCrossPlatformBasename ports getCrossPlatformBasename.
func getCrossPlatformBasename(filePath string) string {
	parts := regexp.MustCompile(`[\\/]+`).Split(filePath, -1)
	if len(parts) == 0 {
		return ""
	}
	return parts[len(parts)-1]
}

// splitMarkdownCommandContent ports splitMarkdownCommandContent: the first
// `---` line opens the frontmatter, the NEXT `---` line closes it.
func splitMarkdownCommandContent(content string) (frontmatterLines, contentLines []string) {
	lines := strings.Split(content, "\n")
	frontmatterStart := -1
	frontmatterEnd := -1
	for index, line := range lines {
		if strings.TrimSpace(line) == "---" {
			if frontmatterStart == -1 {
				frontmatterStart = index
				continue
			}
			if index > frontmatterStart {
				frontmatterEnd = index
				break
			}
		}
	}
	if frontmatterStart == -1 || frontmatterEnd == -1 {
		return nil, lines
	}
	return lines[frontmatterStart+1 : frontmatterEnd], lines[frontmatterEnd+1:]
}

// readFrontmatterKey ports readFrontmatterKey: nil for indented lines.
func readFrontmatterKey(line string) (string, bool) {
	if strings.HasPrefix(line, " ") || strings.HasPrefix(line, "\t") {
		return "", false
	}
	match := frontmatterKeyRe.FindStringSubmatch(strings.TrimSpace(line))
	if match == nil {
		return "", false
	}
	return strings.ToLower(match[1]), true
}

// readFrontmatterMultilineValue ports readFrontmatterMultilineValue: the
// key's inline value plus its indented continuation lines, joined by spaces.
func readFrontmatterMultilineValue(lines []string, targetKey string) string {
	var collected []string
	collecting := false
	for _, line := range lines {
		if key, ok := readFrontmatterKey(line); ok {
			if collecting {
				break
			}
			if key == targetKey {
				collecting = true
				// value after the FIRST colon (split(":").slice(1).join(":")).
				parts := strings.SplitN(line, ":", 2)
				if len(parts) == 2 {
					value := strings.TrimSpace(parts[1])
					if value != "" {
						collected = append(collected, value)
					}
				}
			}
			continue
		}
		if collecting && (strings.HasPrefix(line, " ") || strings.HasPrefix(line, "\t")) {
			collected = append(collected, strings.TrimSpace(line))
		}
	}
	value := strings.TrimSpace(strings.Join(collected, " "))
	return value
}

// parseCommandFile ports CommandFileParser.parseCommandFile (markdown).
func parseCommandFile(content, filePath string) *parsedCommandFile {
	frontmatterLines, contentLines := splitMarkdownCommandContent(content)
	fileName := getCrossPlatformBasename(filePath)
	// Strip a case-insensitive .md extension but keep the name's case.
	rawName := fileName
	if strings.HasSuffix(strings.ToLower(rawName), ".md") {
		rawName = rawName[:len(rawName)-3]
	}
	name := "/" + rawName
	prompt := strings.TrimSpace(strings.Join(contentLines, "\n"))
	if name == "/" {
		return nil
	}
	return &parsedCommandFile{
		Name:         name,
		Prompt:       prompt,
		Content:      strings.Join(contentLines, "\n"),
		FilePath:     filePath,
		Description:  readFrontmatterMultilineValue(frontmatterLines, "description"),
		ArgumentHint: readFrontmatterMultilineValue(frontmatterLines, "argument-hint"),
	}
}

// generateCommandFileContent ports generateCommandFileContent: preserve the
// existing frontmatter except the replaced keys, append the new values.
func generateCommandFileContent(config commandConfig, existingContent string) string {
	replacedKeys := map[string]bool{"description": true, "argument-hint": true}
	var frontmatterLines []string
	skipUntilNextKey := false
	existingFrontmatter, _ := splitMarkdownCommandContent(existingContent)
	for _, line := range existingFrontmatter {
		if key, ok := readFrontmatterKey(line); ok {
			skipUntilNextKey = replacedKeys[key]
		}
		if !skipUntilNextKey {
			frontmatterLines = append(frontmatterLines, line)
		}
	}
	if strings.TrimSpace(config.Description) != "" {
		frontmatterLines = append(frontmatterLines, "description: "+strings.TrimSpace(config.Description))
	}
	if strings.TrimSpace(config.ArgumentHint) != "" {
		frontmatterLines = append(frontmatterLines, "argument-hint: "+strings.TrimSpace(config.ArgumentHint))
	}
	if len(frontmatterLines) == 0 {
		return config.Prompt
	}
	return "---\n" + strings.Join(frontmatterLines, "\n") + "\n---\n\n" + config.Prompt
}

// commandConfig ports CommandConfig.
type commandConfig struct {
	Name         string `json:"name"`
	Description  string `json:"description,omitempty"`
	ArgumentHint string `json:"argumentHint,omitempty"`
	Prompt       string `json:"prompt"`
}
