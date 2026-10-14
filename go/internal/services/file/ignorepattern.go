package file

// ignorepattern.go holds the string-replacement helpers of the .gitignore
// matcher port (see ignore.go for the algorithm's provenance and the corpus
// that gates it). Each helper transcribes one entry of the TS `ignore`
// package's REPLACERS table; the ones Go's regexp cannot express (lookahead,
// match indices) are manual scans.

import "strings"

// replaceQuestion ports the TS `/(?!\\)\?/g` replacer (see step 5).
func replaceQuestion(pattern string) string {
	if !strings.ContainsRune(pattern, '?') {
		return pattern
	}
	return strings.ReplaceAll(pattern, "?", "[^/]")
}

// replaceGlobstars ports the TS two-globstar replacer with its index check.
// By this point slashes are escaped (`/` → `\/`) and stars escaped
// (`*` → `\*`), so the two-globstar token is the 6-byte run `\/\*\*`. The TS
// lookahead `(?=\\\/|$)` checks for an ESCAPED slash (the 2 bytes `\` `/`)
// after the token: that is the /**/ case → `(?:\/[^\/]+)*` (zero or more
// directories; the following `\/` stays). At the very end it is a trailing
// /** → `\/.+` (everything inside but not the folder itself, per the TS #21
// comment).
func replaceGlobstars(pattern string) string {
	const token = `\/\*\*`
	var b strings.Builder
	i := 0
	for i < len(pattern) {
		if strings.HasPrefix(pattern[i:], token) {
			rest := pattern[i:]
			switch {
			case len(rest) > len(token)+1 && rest[len(token)] == '\\' && rest[len(token)+1] == '/':
				// case /**/ — the trailing `\/` is left as the literal tail
				b.WriteString(`(?:\/[^\/]+)*`)
				i += len(token)
				continue
			case len(rest) == len(token):
				// case /** at the very end
				b.WriteString(`\/.+`)
				i += len(token)
				continue
			}
		}
		b.WriteByte(pattern[i])
		i++
	}
	return b.String()
}

// replaceIntermediateWildcards ports the TS `(^|[^\\]+)(\\\*)+(?=.+)`
// replacer. Two subtleties transcribed from the JS regex:
//
//  1. The (\\\*)+ group captures only its LAST iteration and the replacer
//     rewrites that capture alone — so a RUN of N consecutive stars collapses
//     into ONE `[^\/]*` class (not one class per star).
//  2. The (?=.+) lookahead is evaluated after the greedy run: a run ending
//     at the end of the pattern backtracks one iteration, leaving the last
//     star pair for the trailing-wildcard step (step 15).
//
// The p1 precondition: the run must sit at the start or after a non-backslash
// byte; a run preceded by backslashes belongs to an escaped backslash and is
// left for the unescape step.
func replaceIntermediateWildcards(pattern string) string {
	var b strings.Builder
	i := 0
	for i < len(pattern) {
		if pattern[i] == '\\' && i+1 < len(pattern) && pattern[i+1] == '*' {
			if i > 0 && pattern[i-1] == '\\' {
				b.WriteByte(pattern[i])
				i++
				continue
			}
			j := i
			for j+1 < len(pattern) && pattern[j] == '\\' && pattern[j+1] == '*' {
				j += 2
			}
			switch {
			case j < len(pattern):
				// content follows the run: whole run -> one class
				b.WriteString(`[^\/]*`)
				i = j
			case j-i > 2:
				// run at the very end: greedy backtrack one iteration; the
				// last star pair is left for step 15
				b.WriteString(`[^\/]*`)
				b.WriteString(pattern[j-2:])
				i = len(pattern)
			default:
				// single trailing star pair — step 15 owns it
				b.WriteString(pattern[i:])
				i = len(pattern)
			}
			continue
		}
		b.WriteByte(pattern[i])
		i++
	}
	return b.String()
}

// replaceEscapedEscapeBeforeMeta ports the TS `\\\\\\(?=[$.|*+(){^])` step:
// a triple backslash before a metacharacter collapses to one backslash (an
// escaped backslash that was itself escaping a metacharacter).
func replaceEscapedEscapeBeforeMeta(pattern string) string {
	var b strings.Builder
	i := 0
	for i < len(pattern) {
		if strings.HasPrefix(pattern[i:], `\\\`) && i+3 < len(pattern) {
			if strings.IndexByte(`$.|*+(){^`, pattern[i+3]) >= 0 {
				b.WriteByte('\\')
				i += 3
				continue
			}
		}
		b.WriteByte(pattern[i])
		i++
	}
	return b.String()
}

// neverMatchClass is the Go transcription of the JS empty character class
// `[]` — a class that cannot match anything. It is substituted by
// resolveEmptyClasses for top-level markers only.

// emptyClassMarker stands in for the TS `[]` output. It must not collide with
// user input, so it uses a control byte that checkPattern already excludes.

// testOne walks the rules in order; a matching later rule wins. The skip
// logic is the TS decision matrix transcribed verbatim.
func (m *matcher) testOne(path string, checkUnignored bool) (ignored, unignored bool) {
	for _, r := range m.rules {
		negative := r.negative
		if (unignored == negative && ignored != unignored) ||
			(negative && !ignored && !unignored && !checkUnignored) {
			continue
		}
		if r.re.MatchString(path) {
			ignored = !negative
			unignored = negative
		}
	}
	return ignored, unignored
}

// ignores is the TS `ignores(path)`: recursive parent-directory check with
// the "cannot re-include a file if a parent directory is excluded" rule.
func (m *matcher) ignores(path string) bool {
	converted := convertPath(path)
	if !isPathValid(converted) {
		// The TS throws RangeError for absolute/dot paths; the file service
		// only feeds normalized relative paths, so treat invalid as
		// not-ignored rather than panicking on a hostile rule file.
		return false
	}
	ignored, _ := m.test(converted, false)
	return ignored
}

// test ports _t. The TS works on split('/') slices: a path ending in '/' has
// an empty last slice which pops first, so 'a/b/' recurses to 'a/' (NOT
// 'a/b/'), and the base case is a single segment with its slash.
func (m *matcher) test(path string, checkUnignored bool) (ignored, unignored bool) {
	name := path
	if strings.HasSuffix(path, "/") {
		name = path[:len(path)-1]
	}
	idx := strings.LastIndexByte(name, '/')
	if idx < 0 {
		return m.testOne(path, checkUnignored)
	}
	parentIgnored, _ := m.test(name[:idx+1], checkUnignored)
	if parentIgnored {
		return true, false
	}
	return m.testOne(path, checkUnignored)
}

// convertPath ports checkPath.convert: on Windows a plain backslash path is
// made posix; UNC (`\\?\`) and paths with forbidden chars pass through. On
// non-Windows it is the identity.
func convertPath(path string) string {
	if !isWindowsRuntime {
		return path
	}
	if windowsUNC.MatchString(path) || windowsForbidden.MatchString(path) {
		return path
	}
	return windowsBackslash.ReplaceAllString(path, "/")
}

// isPathValid ports checkPath.isNotRelative, with the Windows drive-letter
// allowance. On Windows `C:/foo` is accepted as a relative probe.
func isPathValid(path string) bool {
	if isWindowsRuntime && windowsDrive.MatchString(path) {
		return true
	}
	return !invalidPath.MatchString(path)
}

// isWindowsRuntime is set per GOOS; a var so tests can flip it.
var isWindowsRuntime = goosWindows

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
// sanitizeRange eliminates out-of-order `a-z` ranges, legal in gitignore but
// fatal in a regex.
// stripIdentityEscapes converts `\<plain byte>` to the plain byte, matching
// JS non-unicode regex identity escapes. Go gives `\a`, `\f`, `\v` control
// meanings and rejects some others, so any escape not in the kept set is
// reduced. Bytes INSIDE a character class are untouched — class escapes like
// `\x{10FFFF}` are literal syntax there, and the TS REPLACERS never emit
// identity escapes inside classes.
// isNumericEscape reports whether pattern[at:] starts a \xNN, \uNNNN, or \0
// escape (2/4 hex digits after the introducer; JS non-unicode requires the
// digits — otherwise the escape degrades to an identity escape).
// isKeptEscape reports whether `\<byte>` must stay escaped: regex
// metacharacters, the escapes the pipeline itself synthesizes, and the JS/Go
// escapes with identical meaning.
