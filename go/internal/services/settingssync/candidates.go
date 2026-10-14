// candidates.go: command/plugin/mcp metadata reading and the shared candidate
// collection (collect*ImportCandidates) + skip-reason helpers.
package settingssync

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// ---------------------------------------------------------------------------
// commands
// ---------------------------------------------------------------------------

// collectCommandMarkdownPaths ports collectCommandMarkdownPaths: recursive
// walk skipping dot-entries, .md files only.
func collectCommandMarkdownPaths(rootPath string) []string {
	discovered := map[string]bool{}
	stack := []string{rootPath}
	for len(stack) > 0 {
		currentDir := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		entries, err := os.ReadDir(currentDir)
		if err != nil {
			continue
		}
		for _, entry := range entries {
			if strings.HasPrefix(entry.Name(), ".") {
				continue
			}
			fullPath := filepath.Join(currentDir, entry.Name())
			if entry.IsDir() {
				stack = append(stack, fullPath)
				continue
			}
			if entry.Type().IsRegular() && strings.HasSuffix(strings.ToLower(entry.Name()), ".md") {
				discovered[fullPath] = true
			}
		}
	}
	out := make([]string, 0, len(discovered))
	for path := range discovered {
		out = append(out, path)
	}
	sort.Strings(out)
	return out
}

// getCommandNameFromPath ports getCommandNameFromPath:
// /relativePath (forward slashes, .md stripped).
func getCommandNameFromPath(rootPath, filePath string) string {
	relativePath, err := filepath.Rel(rootPath, filePath)
	if err != nil {
		relativePath = filePath
	}
	relativePath = strings.TrimSuffix(relativePath, ".md")
	// TrimSuffix is case-sensitive; the TS uses /\.md$/i.
	if len(relativePath) > 3 && equalSuffixFold(relativePath, ".md") {
		relativePath = relativePath[:len(relativePath)-3]
	}
	parts := strings.FieldsFunc(relativePath, func(r rune) bool { return r == '/' || r == '\\' })
	var kept []string
	for _, part := range parts {
		if part != "" {
			kept = append(kept, part)
		}
	}
	return "/" + strings.Join(kept, "/")
}

// equalSuffixFold checks a case-insensitive ASCII suffix.
func equalSuffixFold(s, suffix string) bool {
	if len(s) < len(suffix) {
		return false
	}
	return strings.EqualFold(s[len(s)-len(suffix):], suffix)
}

// commandMetadata is the readCommandMetadata result.
type commandMetadata struct {
	ArgumentHint string
	Description  string
}

// readCommandMetadata ports readCommandMetadata via
// CommandFileParser.parseCommandFile: the description / argument-hint
// frontmatter multiline values.
func readCommandMetadata(filePath, rootPath string) commandMetadata {
	name := getCommandNameFromPath(rootPath, filePath)
	_ = name
	data, err := os.ReadFile(filePath)
	if err != nil {
		return commandMetadata{}
	}
	return parseCommandFrontmatter(string(data))
}

// parseCommandFrontmatter ports parseMarkdownCommandFile's frontmatter half:
// split on --- lines, then read the description / argument-hint keys
// including their indented continuation lines.
func parseCommandFrontmatter(content string) commandMetadata {
	lines := strings.Split(content, "\n")
	frontmatterStart := -1
	frontmatterEnd := -1
	for i, line := range lines {
		if strings.TrimSpace(line) == "---" {
			if frontmatterStart == -1 {
				frontmatterStart = i
			} else if i > frontmatterStart {
				frontmatterEnd = i
				break
			}
		}
	}
	if frontmatterStart == -1 || frontmatterEnd == -1 {
		return commandMetadata{}
	}
	frontmatterLines := lines[frontmatterStart+1 : frontmatterEnd]
	return commandMetadata{
		Description:  readFrontmatterMultilineValue(frontmatterLines, "description"),
		ArgumentHint: readFrontmatterMultilineValue(frontmatterLines, "argument-hint"),
	}
}

// readFrontmatterKey ports readFrontmatterKey: top-level `key:` lines only.
func readFrontmatterKey(line string) (string, bool) {
	if strings.HasPrefix(line, " ") || strings.HasPrefix(line, "\t") {
		return "", false
	}
	trimmed := strings.TrimSpace(line)
	// ^([A-Za-z0-9_-]+)\s*:
	idx := strings.Index(trimmed, ":")
	if idx <= 0 {
		return "", false
	}
	key := trimmed[:idx]
	for _, r := range key {
		if !(r >= 'A' && r <= 'Z' || r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '_' || r == '-') {
			return "", false
		}
	}
	return strings.ToLower(key), true
}

// readFrontmatterMultilineValue ports readFrontmatterMultilineValue.
func readFrontmatterMultilineValue(frontmatterLines []string, targetKey string) string {
	var collected []string
	collecting := false
	for _, line := range frontmatterLines {
		key, ok := readFrontmatterKey(line)
		if ok {
			if collecting {
				break
			}
			if key == targetKey {
				collecting = true
				trimmed := strings.TrimSpace(line)
				if idx := strings.Index(trimmed, ":"); idx >= 0 {
					if value := strings.TrimSpace(trimmed[idx+1:]); value != "" {
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

// ---------------------------------------------------------------------------
// plugins
// ---------------------------------------------------------------------------

// findPluginManifestPath ports findPluginManifestPath (.nex-plugin >
// .claude-plugin > .codex-plugin plugin.json).
func findPluginManifestPath(pluginPath string) (string, bool) {
	for _, manifest := range [][]string{nexPluginManifest, claudePluginManifest, codexPluginManifest} {
		manifestPath := joinParts(pluginPath, manifest)
		if pathExists(manifestPath) {
			return manifestPath, true
		}
	}
	return "", false
}

// collectPluginPaths ports collectPluginPaths: direct entries (dirs or
// symlinks), non-dot names, with a plugin manifest.
func collectPluginPaths(rootPath string) []string {
	entries, err := os.ReadDir(rootPath)
	if err != nil {
		return nil
	}
	var discovered []string
	for _, entry := range entries {
		if !entry.IsDir() && entry.Type()&os.ModeSymlink == 0 {
			continue
		}
		if strings.HasPrefix(entry.Name(), ".") {
			continue
		}
		pluginPath := filepath.Join(rootPath, entry.Name())
		if _, ok := findPluginManifestPath(pluginPath); ok {
			discovered = append(discovered, pluginPath)
		}
	}
	sort.Strings(discovered)
	return discovered
}

// pluginMetadata is readPluginMetadata's result.
type pluginMetadata struct {
	ID      string // name@inline
	Name    string
	Version string
}

// readPluginMetadata ports readPluginMetadata.
func readPluginMetadata(pluginPath string) (pluginMetadata, bool) {
	manifestPath, ok := findPluginManifestPath(pluginPath)
	if !ok {
		return pluginMetadata{}, false
	}
	data, err := os.ReadFile(manifestPath)
	if err != nil {
		return pluginMetadata{}, false
	}
	var parsed map[string]any
	if err := json.Unmarshal(data, &parsed); err != nil {
		return pluginMetadata{}, false
	}
	name, _ := parsed["name"].(string)
	name = strings.TrimSpace(name)
	if name == "" {
		return pluginMetadata{}, false
	}
	version := ""
	if v, ok := parsed["version"].(string); ok {
		version = strings.TrimSpace(v)
	}
	return pluginMetadata{
		ID:      name + "@" + inlinePluginMarketplace,
		Name:    name,
		Version: version,
	}, true
}

// ---------------------------------------------------------------------------
// existence helpers (the TS's exists/pathExists semantics)
// ---------------------------------------------------------------------------

// dirExists ports exists(): ReadDir succeeds — real dirs and symlinks TO
// dirs, not plain files or broken symlinks.
func dirExists(path string) bool {
	entries, err := os.ReadDir(path)
	_ = entries
	return err == nil
}

// pathExists ports pathExists(): lstat.
func pathExists(path string) bool {
	_, err := os.Lstat(path)
	return err == nil
}
