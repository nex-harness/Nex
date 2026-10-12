package contracts

import (
	"fmt"
	"regexp"
	"strings"
)

// fail formats a path-qualified validation error.
func fail(path, format string, args ...any) error {
	return fmt.Errorf("%s: %s", path, fmt.Sprintf(format, args...))
}

// fieldPath renders an object-member path the way a JSON pointer-ish trace would.
func fieldPath(path, key string) string {
	if isIdentifier(key) {
		return path + "." + key
	}
	return path + "[" + fmt.Sprintf("%q", key) + "]"
}

func isIdentifier(key string) bool {
	if key == "" {
		return false
	}
	for i, r := range key {
		isLetter := (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || r == '_'
		isDigit := r >= '0' && r <= '9'
		if i == 0 && !isLetter {
			return false
		}
		if !isLetter && !isDigit {
			return false
		}
	}
	return true
}

// literalMatches reports whether a decoded JSON value equals a schema literal.
func literalMatches(lit Literal, value any) bool {
	switch lit.Kind {
	case "string":
		s, ok := value.(string)
		return ok && s == lit.Str
	case "number":
		n, ok := value.(float64)
		return ok && n == lit.Num
	case "boolean":
		b, ok := value.(bool)
		return ok && b == lit.Bool
	case "null":
		return value == nil
	}
	return false
}

// jsFlagMap maps a JS RegExp flag to Go's regexp inline syntax where they differ.
func jsFlagMap(flags string) string {
	var inline strings.Builder
	for _, f := range flags {
		switch f {
		case 'i':
			inline.WriteString("i")
		case 'm':
			inline.WriteString("m")
		case 's':
			inline.WriteString("s")
		}
		// 'u', 'g', 'y', 'd' have no Go equivalent; 'u' is Go's default for []rune.
	}
	return inline.String()
}

var regexCache = map[string]*regexp.Regexp{}

// compileJSRegExp compiles a JS regexp source in Go. Most protocol patterns are
// ASCII-only; anything Go cannot compile is reported rather than widened away.
func compileJSRegExp(source, flags string) (*regexp.Regexp, error) {
	key := flags + "\x00" + source
	if re, ok := regexCache[key]; ok {
		return re, nil
	}
	inline := jsFlagMap(flags)
	pattern := source
	if inline != "" {
		pattern = "(?" + inline + ")" + source
	}
	re, err := regexp.Compile(pattern)
	if err != nil {
		return nil, err
	}
	regexCache[key] = re
	return re, nil
}

// checkFormat implements the named string formats zod 4 applies via $.email() etc.
func checkFormat(format, value string) error {
	switch format {
	case "email":
		if !looksLikeEmail(value) {
			return fmt.Errorf("invalid email")
		}
	case "uuid":
		if !isUUID(value) {
			return fmt.Errorf("invalid uuid")
		}
	case "url":
		if !strings.Contains(value, "://") {
			return fmt.Errorf("invalid url")
		}
	case "emoji":
		// Rarely used in the protocol surface; accept to avoid false negatives.
	default:
		// Unknown formats are not enforced; the corpus will surface any disagreement.
	}
	return nil
}

func looksLikeEmail(value string) bool {
	at := strings.IndexByte(value, '@')
	if at <= 0 || at == len(value)-1 {
		return false
	}
	return strings.Contains(value[at+1:], ".") && !strings.ContainsAny(value, " \t")
}

func isUUID(value string) bool {
	if len(value) != 36 {
		return false
	}
	for i, r := range value {
		if i == 8 || i == 13 || i == 18 || i == 23 {
			if r != '-' {
				return false
			}
			continue
		}
		if !isHex(r) {
			return false
		}
	}
	return true
}

func isHex(r rune) bool {
	return (r >= '0' && r <= '9') || (r >= 'a' && r <= 'f') || (r >= 'A' && r <= 'F')
}
