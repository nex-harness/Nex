package system

// sshparse.go holds the ssh-config tokenization and block parsing of the
// sshConfigAlias.ts port (sshconfig.go): inline-comment stripping,
// quote/backslash-aware token splitting, ~ expansion, Include resolution with
// glob, and the depth-capped block reader.

import (
	"bufio"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// isConnectableAliasPattern ports isConnectableAliasPattern: no wildcards, no
// negation, not the bare "*".
var sshGlobChars = regexp.MustCompile(`[?*\[\]]`)

func isConnectableAliasPattern(pattern string) bool {
	trimmed := strings.TrimSpace(pattern)
	if trimmed == "" || trimmed == "*" || strings.HasPrefix(trimmed, "!") {
		return false
	}
	return !sshGlobChars.MatchString(trimmed)
}

// stripInlineComment ports stripInlineComment: a '#' outside quotes.
func stripInlineComment(line string) string {
	var quote byte
	escaped := false
	for i := 0; i < len(line); i++ {
		c := line[i]
		if escaped {
			escaped = false
			continue
		}
		if c == '\\' {
			escaped = true
			continue
		}
		if quote != 0 {
			if c == quote {
				quote = 0
			}
			continue
		}
		if c == '\'' || c == '"' {
			quote = c
			continue
		}
		if c == '#' {
			return line[:i]
		}
	}
	return line
}

// splitSSHTokens ports splitSshTokens: quote- and backslash-aware split. A
// backslash only escapes separators/quotes/#; Windows path backslashes stay
// literal (the TS comment: 不能把任意 \ 都当成转义前缀).
func splitSSHTokens(line string) []string {
	var tokens []string
	var current strings.Builder
	var quote byte
	isSpace := func(b byte) bool { return b == ' ' || b == '\t' }

	for i := 0; i < len(line); i++ {
		c := line[i]
		if quote != 0 {
			if c == '\\' {
				if i+1 >= len(line) {
					current.WriteByte('\\')
					continue
				}
				next := line[i+1]
				if next == quote || next == '\\' {
					current.WriteByte(next)
					i++
					continue
				}
				current.WriteByte('\\')
				continue
			}
			if c == quote {
				quote = 0
				continue
			}
			current.WriteByte(c)
			continue
		}
		if c == '\'' || c == '"' {
			quote = c
			continue
		}
		if isSpace(c) {
			if current.Len() > 0 {
				tokens = append(tokens, current.String())
				current.Reset()
			}
			continue
		}
		if c == '\\' {
			if i+1 >= len(line) {
				current.WriteByte('\\')
				continue
			}
			next := line[i+1]
			if isSpace(next) || next == '\\' || next == '\'' || next == '"' || next == '#' {
				current.WriteByte(next)
				i++
				continue
			}
			current.WriteByte('\\')
			continue
		}
		current.WriteByte(c)
	}
	if current.Len() > 0 {
		tokens = append(tokens, current.String())
	}
	return tokens
}

// hasGlobPattern ports hasGlobPattern: only * ? [ ] count (Windows \ is not
// a glob char).
var sshIncludeGlobChars = regexp.MustCompile(`[*?\[\]]`)

func hasGlobPattern(value string) bool {
	return sshIncludeGlobChars.MatchString(value)
}

// expandHomeToken ports expandHomeToken: %d, ~, ~/…, ~\….
func expandHomeToken(rawPath, home string) string {
	trimmed := strings.TrimSpace(rawPath)
	if trimmed == "" {
		return trimmed
	}
	withHome := regexp.MustCompile(`^%d($|[\\/])`).ReplaceAllString(trimmed, home)
	if withHome == "~" {
		return home
	}
	if strings.HasPrefix(withHome, "~/") || strings.HasPrefix(withHome, `~\`) {
		return filepath.Join(home, withHome[2:])
	}
	return withHome
}

// resolveIncludeTargets ports resolveIncludeTargets: relative to the
// including file, ~ expansion, glob via filepath.Glob, dedup.
func resolveIncludeTargets(includeTokens []string, baseDir, home string) []string {
	var results []string
	visited := map[string]bool{}
	for _, token := range includeTokens {
		expanded := expandHomeToken(token, home)
		if expanded == "" {
			continue
		}
		absolutePattern := expanded
		if !filepath.IsAbs(absolutePattern) {
			absolutePattern = filepath.Join(baseDir, expanded)
		}
		absolutePattern = filepath.Clean(absolutePattern)
		if !hasGlobPattern(absolutePattern) {
			if _, err := os.Stat(absolutePattern); err == nil && !visited[absolutePattern] {
				visited[absolutePattern] = true
				results = append(results, absolutePattern)
			}
			continue
		}
		// include 展开失败时按空集合处理，避免单个坏配置阻塞整体加载。
		matches, err := filepath.Glob(absolutePattern)
		if err != nil {
			continue
		}
		for _, match := range matches {
			resolved := filepath.Clean(match)
			if visited[resolved] {
				continue
			}
			visited[resolved] = true
			results = append(results, resolved)
		}
	}
	return results
}

// parseSSHConfigBlocks ports parseConfigBlocks (Include recursion, depth cap,
// visit-once).
func parseSSHConfigBlocks(configPath string, visited map[string]bool, depth int) ([]parsedBlock, error) {
	normalizedPath := filepath.Clean(configPath)
	if visited[normalizedPath] || depth > sshMaxIncludeDepth {
		return nil, nil
	}
	visited[normalizedPath] = true

	file, err := os.Open(normalizedPath)
	if err != nil {
		return nil, nil // the TS returns [] on read failure
	}
	defer file.Close()

	var blocks []parsedBlock
	blocks = append(blocks, parsedBlock{patterns: []string{"*"}, source: normalizedPath})
	currentIndex := 0

	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for scanner.Scan() {
		line := stripInlineComment(scanner.Text())
		stripped := strings.TrimSpace(line)
		if stripped == "" {
			continue
		}
		tokens := splitSSHTokens(stripped)
		if len(tokens) == 0 {
			continue
		}
		key := strings.ToLower(tokens[0])
		switch key {
		case "include":
			home, _ := os.UserHomeDir()
			includePaths := resolveIncludeTargets(tokens[1:], filepath.Dir(normalizedPath), home)
			for _, includePath := range includePaths {
				includeBlocks, err := parseSSHConfigBlocks(includePath, visited, depth+1)
				if err != nil {
					return nil, err
				}
				blocks = append(blocks, includeBlocks...)
			}
			continue
		case "host":
			blocks = append(blocks, parsedBlock{
				patterns: tokens[1:], source: normalizedPath, fromHostDirective: true,
			})
			// NOTE: the block is stored BY VALUE; directives are appended to
			// the slice element (a copy in `current` would silently drop them).
			currentIndex = len(blocks) - 1
			continue
		}
		if len(tokens) < 2 {
			continue
		}
		blocks[currentIndex].directives = append(blocks[currentIndex].directives, parsedDirective{
			key:   key,
			value: strings.Join(tokens[1:], " "),
		})
	}
	if err := scanner.Err(); err != nil {
		return nil, nil
	}
	return blocks, nil
}
