// skills.go: the skills category — the bounded SKILL.md walk
// (skillDiscoveryWalk.ts + skill-scan-policy.ts from shared), frontmatter
// name/version reading (yaml with the loose regex fallback), and the
// skip-reason logic.

package settingssync

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// SkillFileName and the scan policy constants (shared skill-scan-policy.ts).
const (
	skillFileName     = "SKILL.md"
	maxSkillScanDepth = 8
)

// skillScanExcludedDirectoryNames ports SKILL_SCAN_EXCLUDED_DIRECTORY_NAMES.
var skillScanExcludedDirectoryNames = map[string]bool{
	"node_modules": true, "dist": true, "build": true, "out": true,
	"target": true, "vendor": true, "coverage": true, ".cache": true,
	".next": true, ".turbo": true, ".venv": true, "__pycache__": true,
}

// skillDiscoveryDotDirAllowlist ports the .system allowlist.
var skillDiscoveryDotDirAllowlist = map[string]bool{".system": true}

// shouldWalkSkillDirectoryEntry ports shouldWalkSkillDirectoryEntry.
func shouldWalkSkillDirectoryEntry(entryName string) bool {
	if skillScanExcludedDirectoryNames[entryName] {
		return false
	}
	if !strings.HasPrefix(entryName, ".") {
		return true
	}
	return skillDiscoveryDotDirAllowlist[entryName]
}

// walkSkillMarkdownPaths ports walkSkillMarkdownPaths: depth-first from the
// root, only .system dot-dirs and non-excluded names, symlink dirs deduped
// by realpath, SKILL.md yielded per directory.
func walkSkillMarkdownPaths(rootPath string) []string {
	type frame struct {
		dir   string
		depth int
	}
	stack := []frame{{rootPath, 0}}
	visitedSymlinkTargets := map[string]bool{}
	var found []string
	for len(stack) > 0 {
		current := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		entries, err := os.ReadDir(current.dir)
		if err != nil {
			continue
		}
		hasSkillFile := false
		var childDirs, childSymlinks []string
		for _, entry := range entries {
			if entry.Name() == skillFileName && !entry.IsDir() {
				hasSkillFile = true
				continue
			}
			if !shouldWalkSkillDirectoryEntry(entry.Name()) {
				continue
			}
			entryPath := filepath.Join(current.dir, entry.Name())
			if entry.IsDir() {
				childDirs = append(childDirs, entryPath)
			} else if entry.Type()&os.ModeSymlink != 0 {
				childSymlinks = append(childSymlinks, entryPath)
			}
		}
		if hasSkillFile {
			found = append(found, filepath.Join(current.dir, skillFileName))
		}
		if current.depth >= maxSkillScanDepth {
			continue
		}
		for _, dir := range childDirs {
			stack = append(stack, frame{dir, current.depth + 1})
		}
		for _, symlink := range childSymlinks {
			info, err := os.Stat(symlink)
			if err != nil || !info.IsDir() {
				continue
			}
			canonical, err := filepath.EvalSymlinks(symlink)
			if err != nil {
				canonical = symlink
			}
			if visitedSymlinkTargets[canonical] {
				continue
			}
			visitedSymlinkTargets[canonical] = true
			stack = append(stack, frame{symlink, current.depth + 1})
		}
	}
	return found
}

// collectSkillMarkdownPaths ports collectSkillMarkdownPaths: dedupe + sort
// with localeCompare (byte-wise for the ASCII paths in play; see notes).
func collectSkillMarkdownPaths(rootPath string) []string {
	discovered := map[string]bool{}
	for _, path := range walkSkillMarkdownPaths(rootPath) {
		discovered[path] = true
	}
	out := make([]string, 0, len(discovered))
	for path := range discovered {
		out = append(out, path)
	}
	sortLocale(out)
	return out
}

// sortLocale sorts strings the way String.localeCompare does on this
// machine's en_US locale for the path/name shapes involved. Node's
// localeCompare is ICU-based; for ASCII paths without case mixes the byte
// order agrees. Non-ASCII ordering differences are covered by the
// differential harness.
func sortLocale(items []string) { sort.Strings(items) }

// nameVersion is the readSkillMetadata result.
type nameVersion struct {
	Name    string
	Version string // "" when absent
}

var (
	looseNameLine    = regexp.MustCompile(`(?m)^name\s*:\s*(.+)$`)
	looseVersionLine = regexp.MustCompile(`(?m)^version\s*:\s*(.+)$`)
	frontmatterRe    = regexp.MustCompile(`(?s)\A---\n(.*?)\n---(?:\n|$)`)
)

// readSkillMetadata ports readSkillMetadata: frontmatter YAML name/version
// with the loose regex fallback when parsing fails or fields are empty.
func readSkillMetadata(skillPath string) nameVersion {
	fallbackName := filepath.Base(filepath.Dir(skillPath))
	data, err := os.ReadFile(skillPath)
	if err != nil {
		return nameVersion{Name: fallbackName}
	}
	return readSkillMetadataFromMarkdown(string(data), fallbackName)
}

func readSkillMetadataFromMarkdown(content, fallbackName string) nameVersion {
	normalized := strings.ReplaceAll(strings.ReplaceAll(content, "\r\n", "\n"), "\r", "\n")
	match := frontmatterRe.FindStringSubmatch(normalized)
	if match == nil {
		return nameVersion{Name: fallbackName}
	}
	frontmatter := match[1]
	if parsed, ok := parseSimpleYAMLNameVersion(frontmatter); ok {
		name := fallbackName
		if parsed.name != "" {
			name = parsed.name
		} else {
			name = looseName(frontmatter, fallbackName)
		}
		version := parsed.version
		if version == "" {
			version = looseVersion(frontmatter)
		}
		return nameVersion{Name: name, Version: version}
	}
	// YAML parse failure → loose fallback for both fields.
	return nameVersion{
		Name:    looseName(frontmatter, fallbackName),
		Version: looseVersion(frontmatter),
	}
}

type yamlNameVersion struct{ name, version string }

// parseSimpleYAMLNameVersion parses the name/version scalars the skill
// frontmatter uses (string or number version), returning ok=false when the
// frontmatter is not a YAML mapping (the TS falls back to loose regexes).
func parseSimpleYAMLNameVersion(frontmatter string) (yamlNameVersion, bool) {
	out := yamlNameVersion{}
	sawAny := false
	for _, line := range strings.Split(frontmatter, "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		if strings.HasPrefix(line, " ") || strings.HasPrefix(line, "\t") {
			// Nested structure: the yaml lib parses it into a record; the
			// top-level name/version are then absent → loose fallback.
			continue
		}
		key, value, found := strings.Cut(trimmed, ":")
		if !found {
			// Not a simple mapping line (list item, etc.): treat as a parse
			// failure so the loose fallback runs (mirrors the yaml throw).
			return yamlNameVersion{}, false
		}
		key = strings.TrimSpace(key)
		value = strings.TrimSpace(value)
		switch key {
		case "name":
			out.name = stripYamlScalarQuotes(value)
			sawAny = true
		case "version":
			out.version = stripYamlScalarQuotes(value)
			sawAny = true
		}
	}
	return out, sawAny
}

func stripYamlScalarQuotes(value string) string {
	trimmed := strings.TrimSpace(value)
	if len(trimmed) >= 2 &&
		((trimmed[0] == '"' && trimmed[len(trimmed)-1] == '"') ||
			(trimmed[0] == '\'' && trimmed[len(trimmed)-1] == '\'')) {
		return strings.TrimSpace(trimmed[1 : len(trimmed)-1])
	}
	return trimmed
}

func looseName(frontmatter, fallback string) string {
	if match := looseNameLine.FindStringSubmatch(frontmatter); match != nil {
		if name := stripYamlScalarQuotes(match[1]); name != "" {
			return name
		}
	}
	return fallback
}

func looseVersion(frontmatter string) string {
	if match := looseVersionLine.FindStringSubmatch(frontmatter); match != nil {
		if version := stripYamlScalarQuotes(match[1]); version != "" {
			return version
		}
	}
	return ""
}
