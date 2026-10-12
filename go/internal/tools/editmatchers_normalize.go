package tools

import (
	"regexp"
	"strings"
	"unicode"
)

// stripReadLineNumberPrefixes mirrors stripReadLineNumberPrefixes().
func stripReadLineNumberPrefixes(search string) (string, bool) {
	lines := strings.Split(search, "\n")
	stripped := make([]string, len(lines))
	for i, line := range lines {
		if m := colonLinePrefix.FindStringSubmatch(line); m != nil {
			stripped[i] = m[1]
			continue
		}
		if m := tabLinePrefix.FindStringSubmatch(line); m != nil {
			stripped[i] = m[1]
			continue
		}
		return "", false
	}
	return strings.Join(stripped, "\n"), true
}

var visibleEscape = regexp.MustCompile(`\\([ntr"'` + "`" + `\\$])`)

// unescapeVisibleCharacters mirrors unescapeVisibleCharacters().
func unescapeVisibleCharacters(search string) string {
	return visibleEscape.ReplaceAllStringFunc(search, func(match string) string {
		token := match[1:2]
		switch token {
		case "n":
			return "\n"
		case "t":
			return "\t"
		case "r":
			return "\r"
		default:
			return token
		}
	})
}

var unicodeEscape = regexp.MustCompile(`(\\\\)|\\u([0-9a-fA-F]{4})`)

// unescapeUnicodeCharacters mirrors unescapeUnicodeCharacters().
func unescapeUnicodeCharacters(search string) string {
	return unicodeEscape.ReplaceAllStringFunc(search, func(match string) string {
		if strings.HasPrefix(match, `\\`) {
			return match
		}
		hex := match[2:]
		code := 0
		for _, ch := range hex {
			code = code*16 + hexDigit(ch)
		}
		return string(rune(code))
	})
}

func hexDigit(ch rune) int {
	switch {
	case ch >= '0' && ch <= '9':
		return int(ch - '0')
	case ch >= 'a' && ch <= 'f':
		return int(ch-'a') + 10
	case ch >= 'A' && ch <= 'F':
		return int(ch-'A') + 10
	}
	return 0
}

// NormalizeReplacementForMatch mirrors normalizeReplacementForMatch().
func NormalizeReplacementForMatch(strategy EditMatchStrategy, newString string) string {
	if strategy == StrategyEscapeNormalized {
		return unescapeVisibleCharacters(newString)
	}
	return newString
}

// PreserveQuoteStyle mirrors preserveQuoteStyle().
func PreserveQuoteStyle(oldString, actualOldString, newString string) string {
	if oldString == actualOldString {
		return newString
	}
	result := newString
	if strings.Contains(actualOldString, leftDoubleCurly) || strings.Contains(actualOldString, rightDoubleCurly) {
		result = applyCurlyDoubleQuotes(result)
	}
	if strings.Contains(actualOldString, leftSingleCurly) || strings.Contains(actualOldString, rightSingleCurly) {
		result = applyCurlySingleQuotes(result)
	}
	return result
}

func applyCurlyDoubleQuotes(value string) string {
	chars := []rune(value)
	out := make([]rune, len(chars))
	for i, ch := range chars {
		if ch != '"' {
			out[i] = ch
			continue
		}
		if isOpeningQuoteContext(chars, i) {
			out[i] = []rune(leftDoubleCurly)[0]
		} else {
			out[i] = []rune(rightDoubleCurly)[0]
		}
	}
	return string(out)
}

func applyCurlySingleQuotes(value string) string {
	chars := []rune(value)
	out := make([]rune, len(chars))
	for i, ch := range chars {
		if ch != '\'' {
			out[i] = ch
			continue
		}
		var previous, next rune
		if i > 0 {
			previous = chars[i-1]
		}
		if i+1 < len(chars) {
			next = chars[i+1]
		}
		if i > 0 && i+1 < len(chars) && isLetter(previous) && isLetter(next) {
			out[i] = []rune(rightSingleCurly)[0]
			continue
		}
		if isOpeningQuoteContext(chars, i) {
			out[i] = []rune(leftSingleCurly)[0]
		} else {
			out[i] = []rune(rightSingleCurly)[0]
		}
	}
	return string(out)
}

func isOpeningQuoteContext(chars []rune, index int) bool {
	if index == 0 {
		return true
	}
	switch chars[index-1] {
	case ' ', '\t', '\n', '\r', '(', '[', '{', '\u2014', '\u2013':
		return true
	}
	return false
}

func isLetter(ch rune) bool { return unicode.IsLetter(ch) }
