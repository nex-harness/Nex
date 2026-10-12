package promptctx

import (
	"regexp"
	"strings"
)

// jsTrim is String.prototype.trim: it strips the ECMAScript WhiteSpace and
// LineTerminator sets, which differ from Go's unicode.IsSpace (U+FEFF is trimmed
// by JavaScript and not by Go; U+0085 is the reverse).
func jsTrim(s string) string {
	return strings.TrimFunc(s, func(r rune) bool {
		switch r {
		case '\t', '\n', '\v', '\f', '\r', ' ', 0xA0, 0x1680, 0x2028, 0x2029, 0x202F, 0x205F, 0x3000, 0xFEFF:
			return true
		}
		return r >= 0x2000 && r <= 0x200A
	})
}

var leadingFrontmatter = regexp.MustCompile(`^---\s*\n[\s\S]*?---\s*\n?`)

// stripFrontmatter removes a leading YAML block. HTML comment stripping in
// formatProjectMemoryIndexContent needs the marked Markdown lexer and is only
// applied when the content contains a comment; that case is rejected loudly by
// MemoryNeedsMarkdownLexer rather than approximated.
func stripFrontmatter(s string) string { return leadingFrontmatter.ReplaceAllString(s, "") }

// MemoryNeedsMarkdownLexer reports content the Go port cannot format faithfully.
func MemoryNeedsMarkdownLexer(s string) bool { return strings.Contains(s, "<!--") }
