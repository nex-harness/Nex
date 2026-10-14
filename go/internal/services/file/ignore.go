// Package file ports the Tier-1 IFileService surface
// (packages/services/src/file/*) of the Go rewrite, step 3.2.
//
// This file holds the .gitignore matcher — a port of the npm `ignore` package
// (v5.3.2, the gitignore spec 2.22 reference implementation ESLint uses) that
// workspaceFileIgnore.ts delegates to.
//
// WHY A HAND-PORTED MATCHER: the phase-3 plan allows a Go gitignore library
// only after a corpus generated from the TS package passes. The corpus
// (go/testdata/services/file/gitignore-corpus.json, 58 rule sets / 3944 path
// checks, scripts/go-rewrite/gen-gitignore-corpus.mjs) was run against every
// candidate:
//
//   - sabhiram/go-gitignore: 179 mismatches (`?`, `**` suffix, negation of
//     ignored-dir children, case sensitivity);
//   - denormal/go-gitignore: panics on the `/` rule (index out of range) and
//     11 semantic mismatches even with an ancestor-propagation wrapper
//     (`a/**/b` zero-dir case, `[!a]` char class, case);
//   - monochromegane/go-gitignore: panics on blank lines.
//
// All three fail the gate, so the matcher is ported. The TS algorithm turns
// each gitignore line into a JS RegExp through a chain of string
// replacements (REPLACERS in ignore/index.js); this file transcribes the
// chain 1:1. Go's regexp lacks lookahead, so the lookaheads the pipeline
// EMITS are replaced by consuming equivalents that are equivalent at the
// positions they occur (always at pattern end; see the ending step). The
// corpus test in ignore_corpus_test.go plus the adversarial
// gitignore-fuzz-corpus.json are the proof of equivalence.
package file

import (
	"regexp"
	"strings"
)

// rule is one compiled gitignore line.
type rule struct {
	pattern  string
	negative bool
	re       *regexp.Regexp
}

// matcher is the port of the TS Ignore instance. The zero value is ready to
// use and Add appends rules; later rules override earlier ones (the TS
// "后声明覆盖" — the evaluation order inside testOne).
type matcher struct {
	rules []rule
	// ignoreCase reproduces the TS default `ignorecase: true` — matching is
	// case-INsensitive, unlike stock git. The corpus pins this.
	ignoreCase bool
}

func newMatcher() *matcher { return &matcher{ignoreCase: true} }

// Add appends rules from one gitignore document. Lines split on \r?\n; blank
// lines and comments are dropped by compileRule returning nil.
func (m *matcher) Add(content string) *matcher {
	for _, line := range splitCRLF.Split(content, -1) {
		if r := compileRule(line, m.ignoreCase); r != nil {
			m.rules = append(m.rules, *r)
		}
	}
	return m
}

var (
	// checkPattern helpers, ported from the TS:
	splitCRLF                 = regexp.MustCompile(`\r?\n`)
	blankLine                 = regexp.MustCompile(`^\s+$`)
	invalidTrailingBackslash  = regexp.MustCompile(`(?:[^\\]|^)\\$`)
	leadingEscapedExclamation = regexp.MustCompile(`^\\!`)
	leadingEscapedHash        = regexp.MustCompile(`^\\#`)
	trailingSpaceQuoted       = regexp.MustCompile(`((?:\\\\)*?)(\\?\s+)$`)
	escapedSpace              = regexp.MustCompile(`(\\+?)\s`)
	regexMeta                 = regexp.MustCompile(`[\\$.|*+(){^]`)
	leadingSlash              = regexp.MustCompile(`^/`)
	anySlash                  = regexp.MustCompile(`/`)
	leadingDoubleStarSlash    = regexp.MustCompile(`^\^*\\\*\\\*\\/`)
	doubleEscape              = regexp.MustCompile(`\\\\`)
	rangeNotation             = regexp.MustCompile(`([0-z])-([0-z])`)
	doesNotEndWithStar        = regexp.MustCompile(`(?:[^*])$`)
	endsWithSlash             = regexp.MustCompile(`/$`)
	trailingWildcard          = regexp.MustCompile(`(\^|\\\/)?\\\*$`)
	// path validity (checkPath): `/foo`, `./foo`, `../foo`, `.`, `..`
	invalidPath      = regexp.MustCompile(`^\.*\/|^\.+$`)
	windowsDrive     = regexp.MustCompile(`^[a-z]:/`)
	windowsUNC       = regexp.MustCompile(`^\\\\\?\\`)
	windowsForbidden = regexp.MustCompile(`["<>|\x00-\x1F]+`)
	windowsBackslash = regexp.MustCompile(`\\`)
)

// compileRule is the TS checkPattern + createRule + makeRegex chain. A nil
// return means the line is a blank/comment/invalid line that matches nothing.
func compileRule(origin string, ignoreCase bool) *rule {
	if origin == "" || blankLine.MatchString(origin) ||
		invalidTrailingBackslash.MatchString(origin) ||
		strings.HasPrefix(origin, "#") {
		return nil
	}
	r := rule{pattern: origin}
	pattern := origin
	if strings.HasPrefix(pattern, "!") {
		r.negative = true
		pattern = pattern[1:]
	}
	pattern = leadingEscapedExclamation.ReplaceAllString(pattern, "!")
	pattern = leadingEscapedHash.ReplaceAllString(pattern, "#")
	source := replacePattern(pattern)
	var err error
	if ignoreCase {
		r.re, err = regexp.Compile(`(?i)` + source)
	} else {
		r.re, err = regexp.Compile(source)
	}
	if err != nil {
		// The TS throws for malformed ranges; the corpus contains none, and a
		// broken rule must not take the scan down (fail-open policy of
		// loadWorkspaceFileSearchIgnoreRules).
		return nil
	}
	return &r
}

// replacePattern applies the REPLACERS pipeline in the TS's order.
func replacePattern(pattern string) string {
	// The TS starting replacer checks for a mid-slash against `this`, which
	// replacer.bind(pattern) binds to the ORIGINAL pattern — keep it here.
	originalPattern := pattern

	// 1. BOM.
	pattern = strings.TrimPrefix(pattern, "\ufeff")
	// 2. trailing spaces unless backslash-escaped: (a\ ) -> (a ), (a  ) -> (a).
	pattern = trailingSpaceQuoted.ReplaceAllStringFunc(pattern, func(match string) string {
		loc := trailingSpaceQuoted.FindStringSubmatch(match)
		m1, m2 := loc[1], loc[2]
		if strings.HasPrefix(m2, `\`) {
			return m1 + " "
		}
		return m1
	})
	// 3. (\ ) -> ' ' (an odd number of backslashes escapes the space).
	pattern = escapedSpace.ReplaceAllStringFunc(pattern, func(match string) string {
		loc := escapedSpace.FindStringSubmatch(match)
		m1 := loc[1]
		return m1[:len(m1)-len(m1)%2] + " "
	})
	// 4. escape regex metacharacters the user wrote literally. Note the TS
	// class [\\$.|*+(){^] contains neither '[' nor '?'.
	pattern = regexMeta.ReplaceAllStringFunc(pattern, func(match string) string {
		return `\` + match
	})
	// 5. '?' -> '[^/]' — the TS /(?!\\)\?/ has its lookahead AT the match
	// position (which holds '?'), so it can never fail: every '?' converts.
	pattern = replaceQuestion(pattern)
	// 6. leading slash -> '^'.
	if leadingSlash.MatchString(pattern) {
		pattern = "^" + pattern[1:]
	}
	// 7. remaining slashes escaped.
	pattern = anySlash.ReplaceAllString(pattern, `\/`)
	// 8. leading **/ -> ^(?:.*\/)?.
	pattern = leadingDoubleStarSlash.ReplaceAllString(pattern, `^(?:.*\/)?`)
	// 9. starting: no mid-slash -> match at any level; mid-slash -> anchored
	// to the ignore file's own level. The TS /^(?=[^^])/ needs at least one
	// byte, so the empty pattern (from a bare `!`) stays empty — an empty
	// regex matches every path.
	if pattern != "" && !strings.HasPrefix(pattern, "^") {
		if !hasSlashNotAtEnd(originalPattern) {
			pattern = `(?:^|\/)` + pattern
		} else {
			pattern = "^" + pattern
		}
	}
	// 10. /**/ and trailing /** globstars.
	pattern = replaceGlobstars(pattern)
	// 11. intermediate wildcard runs.
	pattern = replaceIntermediateWildcards(pattern)
	// 12. unescape \ before metacharacters, then \\ -> \.
	pattern = replaceEscapedEscapeBeforeMeta(pattern)
	pattern = doubleEscape.ReplaceAllString(pattern, `\`)
	// 13. character classes.
	pattern = replaceCharClasses(pattern)
	// 14. ending: foo/ -> foo$ ; foo -> foo(?=$|\/$). The lookahead is
	// emitted as the consuming equivalent "(?:$|/$)": both forms require
	// either end-of-string or a '/' that is the LAST byte, which is what the
	// lookahead checked.
	if pattern != "" && doesNotEndWithStar.MatchString(pattern) {
		if endsWithSlash.MatchString(pattern) {
			pattern += "$"
		} else {
			pattern += "(?:$|/$)"
		}
	}
	// 15. trailing wildcard: (\^|\\\/)?\*$ — the leftover star from step 11's
	// backtrack, with a stricter prefix when it follows an anchor.
	pattern = trailingWildcard.ReplaceAllStringFunc(pattern, func(match string) string {
		loc := trailingWildcard.FindStringSubmatch(match)
		p1 := loc[1]
		prefix := `[^/]*`
		if p1 != "" {
			prefix = p1 + `[^/]+`
		}
		return prefix + "(?:$|/$)"
	})
	// 16. JS identity escapes: '\<plain byte>' is the plain byte in a JS
	// (non-unicode) regex but carries control-char meaning in Go.
	pattern = stripIdentityEscapes(pattern)
	// 17. Resolve the empty-class markers against the final class structure.
	pattern = resolveEmptyClasses(pattern)
	return pattern
}

// hasSlashNotAtEnd ports the TS `/(?!$)`: a '/' that is not the final byte.
func hasSlashNotAtEnd(pattern string) bool {
	if len(pattern) == 0 {
		return false
	}
	return strings.Contains(pattern[:len(pattern)-1], "/")
}
