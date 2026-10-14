package file

// ignoreclass.go holds the character-class half of the .gitignore matcher
// port: the TS range-notation replacer, its lazy-body semantics, and the
// empty-class resolution that reconciles JS `[]` (empty class, matches
// nothing) with Go's `[]` (a literal `]` member). See ignore.go for the
// provenance and the corpus gate.

import "strings"

// replaceCharClasses ports the TS `(\\)?\[([^\]/]*?)(\\*)($|\])` replacer
// with its LAZY body semantics. At each `[` the body grows only over bytes
// that are neither `]` nor `/`; then:
//
//   - a `]` closes the class — the trailing backslash run before it is the
//     endEscape group; an ODD count makes the class invalid and it degrades
//     to the empty class `[]`; an even count is re-emitted inside;
//   - end-of-string instead of `]` also produces the empty class;
//   - a `/` inside the would-be body means no match at this `[` — left
//     verbatim (this protects the synthetic `[^\/]` from earlier steps and
//     lets a user `[a/b]` keep its own `]` as the class terminator);
//   - a `\` before `[` (leadEscape) makes the TS re-emit the same text.
//
// The empty class `[]` is emitted as the MARKER string: whether it is a
// never-matching class or an ordinary pair of class members depends on whether
// an enclosing `[` is open in the FINAL regex — exactly the JS parse the TS
// gets for free. resolveEmptyClasses (the last pass) makes that decision.
func replaceCharClasses(pattern string) string {
	var b strings.Builder
	i := 0
	for i < len(pattern) {
		if pattern[i] != '[' {
			b.WriteByte(pattern[i])
			i++
			continue
		}
		// leadEscape: the TS re-emits verbatim.
		if i > 0 && pattern[i-1] == '\\' {
			b.WriteByte('[')
			i++
			continue
		}
		// lazy body: bytes that are neither ']' nor '/'.
		k := i + 1
		for k < len(pattern) && pattern[k] != ']' && pattern[k] != '/' {
			k++
		}
		if k >= len(pattern) {
			// ran to end without ']' or '/': close='' -> empty class, and the
			// match consumed everything to the end.
			b.WriteString(emptyClassMarker)
			i = len(pattern)
			continue
		}
		if pattern[k] == '/' {
			// body would contain '/': no match at this '['.
			b.WriteByte('[')
			i++
			continue
		}
		// ']' found: the trailing backslash run is the (\\*) group.
		body := pattern[i+1 : k]
		endEscape := 0
		for endEscape < len(body) && body[len(body)-1-endEscape] == '\\' {
			endEscape++
		}
		if endEscape%2 == 1 {
			// invalid range notation: '[bar\\]' -> '[]'
			b.WriteString(emptyClassMarker)
			i = k + 1
			continue
		}
		kept := body[:len(body)-endEscape]
		sanitized := sanitizeRange(kept)
		if kept == "" || sanitized == "" {
			// empty body, or a range that sanitized away entirely: '[]'
			b.WriteString(emptyClassMarker)
		} else {
			b.WriteString("[" + sanitized + body[len(body)-endEscape:] + "]")
		}
		i = k + 1
	}
	return b.String()
}

// sanitizeRange eliminates out-of-order `a-z` ranges, legal in gitignore but
// fatal in a regex.
func sanitizeRange(rangeBody string) string {
	return rangeNotation.ReplaceAllStringFunc(rangeBody, func(match string) string {
		loc := rangeNotation.FindStringSubmatchIndex(match)
		from, to := match[loc[2]:loc[3]], match[loc[4]:loc[5]]
		if from[0] <= to[0] {
			return match
		}
		return ""
	})
}

// stripIdentityEscapes converts `\<plain byte>` to the plain byte, matching
// JS non-unicode regex identity escapes. Go gives `\a`, `\f`, `\v` control
// meanings and rejects some others, so any escape not in the kept set is
// reduced. Bytes INSIDE a character class are untouched — class escapes like
// `\x{10FFFF}` are literal syntax there, and the TS REPLACERS never emit
// identity escapes inside classes.
func stripIdentityEscapes(pattern string) string {
	var b strings.Builder
	inClass := false
	// NOTE: manual advancement (no post statement) — several branches consume
	// two bytes and a `continue` with i++ would eat a third.
	i := 0
	for i < len(pattern) {
		c := pattern[i]
		if inClass {
			if c == ']' && pattern[i-1] != '\\' {
				inClass = false
			}
			b.WriteByte(c)
			i++
			continue
		}
		if c == '[' {
			inClass = true
			b.WriteByte(c)
			i++
			continue
		}
		if c == '\\' && i+1 < len(pattern) {
			next := pattern[i+1]
			if next == '\\' {
				// an escaped backslash consumes BOTH bytes; the char after
				// the pair is not "preceded by a backslash" for identity
				// purposes
				b.WriteString(`\\`)
				i += 2
				continue
			}
			if isNumericEscape(pattern, i) {
				// \xNN / \uNNNN / \0N: JS parses these as value escapes —
				// and so does Go — keep them verbatim.
				b.WriteByte(c)
				b.WriteByte(next)
				i += 2
				continue
			}
			if !isKeptEscape(next) {
				b.WriteByte(next)
				i += 2
				continue
			}
		}
		b.WriteByte(c)
		i++
	}
	return b.String()
}

// isKeptEscape reports whether `\<byte>` must stay escaped: regex
// metacharacters, the escapes the pipeline itself synthesizes, and the JS/Go
// escapes with identical meaning.
func isKeptEscape(c byte) bool {
	switch c {
	case '\\', '$', '.', '|', '*', '+', '(', ')', '{', '}', '^', '[', ']', '/', 'n', 'r', 't', 'b', 'B', 's', 'S', 'w', 'W', 'd', 'D':
		return true
	}
	return false
}

func isHexByte(c byte) bool {
	return (c >= '0' && c <= '9') || (c >= 'a' && c <= 'f') || (c >= 'A' && c <= 'F')
}

// isNumericEscape reports whether pattern[at:] starts a \xNN, \uNNNN, or \0
// escape (2/4 hex digits after the introducer; JS non-unicode requires the
// digits — otherwise the escape degrades to an identity escape).
func isNumericEscape(pattern string, at int) bool {
	if at+1 >= len(pattern) {
		return false
	}
	switch pattern[at+1] {
	case 'x':
		return at+3 < len(pattern) && isHexByte(pattern[at+2]) && isHexByte(pattern[at+3])
	case 'u':
		for k := 2; k <= 5; k++ {
			if at+k >= len(pattern) || !isHexByte(pattern[at+k]) {
				return false
			}
		}
		return true
	case '0':
		return at+2 < len(pattern) && pattern[at+2] >= '0' && pattern[at+2] <= '7'
	}
	return false
}

// resolveEmptyClasses walks the finished regex with JS class-parsing rules.
// Two things need fixing before Go can parse the string the way JS does:
//
//   - each marker: at TOP LEVEL it becomes neverMatchClass (JS `[]` = matches
//     nothing); inside an OPEN class the `[` and `]` are ordinary members and
//     the `]` closes the class, so the marker becomes the literal `[]`;
//   - a literal `[]` (from a leadEscape `[` the TS replacer re-emits
//     verbatim) at TOP LEVEL: JS reads an empty class, Go reads `]` as a
//     literal member and the class runs to some later `]` — replace it with
//     neverMatchClass too.
func resolveEmptyClasses(pattern string) string {
	if !strings.Contains(pattern, emptyClassMarker) && !strings.Contains(pattern, "[]") {
		return pattern
	}
	var b strings.Builder
	inClass := false
	i := 0
	for i < len(pattern) {
		if strings.HasPrefix(pattern[i:], emptyClassMarker) {
			if inClass {
				b.WriteString("[]")
			} else {
				b.WriteString(neverMatchClass)
			}
			i += len(emptyClassMarker)
			continue
		}
		c := pattern[i]
		// Escape pairs are consumed atomically everywhere (top level and
		// inside classes): the byte after an escaped byte is never itself an
		// escape, and a class opener must not be misread as escaped because
		// the preceding byte happened to be the second byte of a pair.
		if c == '\\' && i+1 < len(pattern) {
			b.WriteString(pattern[i : i+2])
			i += 2
			continue
		}
		if inClass {
			if c == ']' {
				inClass = false
			}
			b.WriteByte(c)
			i++
			continue
		}
		if c == '[' {
			if i+1 < len(pattern) && pattern[i+1] == ']' {
				// top-level literal '[]': JS empty class
				b.WriteString(neverMatchClass)
				i += 2
				continue
			}
			inClass = true
			b.WriteByte(c)
			i++
			continue
		}
		b.WriteByte(c)
		i++
	}
	return b.String()
}

const neverMatchClass = `[^\x00-\x{10FFFF}]`

const emptyClassMarker = "\x00EMPTYCLASS\x00"
