package taskindex

// search.go — buildSearchSnippets. The global session search (TaskSearchDialog)
// wants a few excerpts around each hit, not the whole conversation.

import (
	"strings"
	"unicode"
)

// normalizeSearchSnippetText ports normalizeSearchSnippetText: /\s+/g → " ",
// trimmed, truncated. Done over runes so a multi-byte character is never cut
// in half (JS slices UTF-16 units; rune boundaries are the closest safe match).
func normalizeSearchSnippetText(text string) string {
	var b strings.Builder
	b.Grow(len(text))
	inSpace := false
	started := false
	for _, r := range text {
		if unicode.IsSpace(r) {
			inSpace = true
			continue
		}
		if inSpace && started {
			b.WriteRune(' ')
		}
		inSpace = false
		started = true
		b.WriteRune(r)
	}
	return truncateRunes(b.String(), searchSnippetMaxChars)
}

func truncateRunes(s string, max int) string {
	count := 0
	for i := range s {
		if count == max {
			return s[:i]
		}
		count++
	}
	return s
}

// buildSearchSnippets ports buildSearchSnippets: a window centred on each hit,
// near-duplicates merged, at most four. When the query only hit the title the
// text has no match, so one whole-text excerpt is returned instead of nothing.
func buildSearchSnippets(searchableText, search string) []string {
	if search == "" || strings.TrimSpace(searchableText) == "" {
		return nil
	}
	normalizedSearch := strings.ToLower(search)
	normalizedText := strings.ToLower(searchableText)
	searchRunes := []rune(normalizedText)
	searchNeedle := []rune(normalizedSearch)
	textRunes := []rune(searchableText)
	if len(searchNeedle) == 0 || len(searchRunes) == 0 {
		return nil
	}
	var snippets []string
	type span struct{ start, end int }
	var ranges []span
	searchStart := 0
	for len(snippets) < searchSnippetLimit && searchStart < len(searchRunes) {
		match := runeIndex(searchRunes, searchNeedle, searchStart)
		if match < 0 {
			break
		}
		start := max(0, match-searchSnippetPrefixRadius)
		end := min(len(textRunes), match+len(searchNeedle)+searchSnippetSuffixRadius)
		prefix := ""
		if start > 0 {
			prefix = "..."
		}
		suffix := ""
		if end < len(textRunes) {
			suffix = "..."
		}
		snippet := normalizeSearchSnippetText(prefix + string(textRunes[start:end]) + suffix)
		overlaps := false
		for _, r := range ranges {
			if min(r.end, end)-max(r.start, start) > 0 {
				overlaps = true
				break
			}
		}
		// 同一个关键词在很近的位置多次出现时，摘要窗口会高度重叠；服务端先合并近重复摘要。
		if snippet != "" && !overlaps {
			snippets = append(snippets, snippet)
			ranges = append(ranges, span{start, end})
		}
		searchStart = match + len(searchNeedle)
	}
	if len(snippets) == 0 {
		// title 命中但正文没命中时，仍给一条整段摘要兜底，避免标题下方空白。
		if fallback := normalizeSearchSnippetText(searchableText); fallback != "" {
			return []string{fallback}
		}
		return nil
	}
	return snippets
}

// runeIndex is String.indexOf over runes (ToLower may change byte length, so the
// search has to happen in rune space).
func runeIndex(haystack, needle []rune, from int) int {
	if from >= len(haystack) {
		return -1
	}
	for i := from; i+len(needle) <= len(haystack); i++ {
		match := true
		for j := range needle {
			if haystack[i+j] != needle[j] {
				match = false
				break
			}
		}
		if match {
			return i
		}
	}
	return -1
}
