// Package promptctx assembles the model-visible system prompt and the meta-user
// context block. It ports apps/nex-cli/packages/core/src/context (builder and
// sections). Prompt text lives in prompts/*.txt, exported by the real TypeScript
// functions (scripts/go-rewrite/export-prompts.mts) so it is never retyped.
package promptctx

import (
	"math"
	"math/big"
	"strconv"
	"strings"
	"unicode/utf16"
)

// jsLen is String.prototype.length: UTF-16 code units, not bytes or runes. The
// section character counts the host displays are in these units.
func jsLen(s string) int {
	n := 0
	for _, r := range s {
		if r >= 0x10000 {
			n += 2
		} else {
			n++
		}
	}
	return n
}

// jsSlice is s.slice(0, end) over UTF-16 code units. A cut that lands inside a
// surrogate pair keeps the lone high surrogate in JavaScript; Go strings cannot
// hold one, so it becomes U+FFFD. The inputs this is used on are previews and
// memory indexes where that case does not occur in practice. KNOWN GAP: a skill
// description of emoji whose 249th UTF-16 unit splits a pair would be sent by the
// Node agent as a lone surrogate (JSON \ud83d); this port sends U+FFFD. The JSON
// round trip in the vector test cannot tell them apart, so it is not asserted.
func jsSlice(s string, start, end int) string {
	u := utf16.Encode([]rune(s))
	if end > len(u) {
		end = len(u)
	}
	if start > end {
		start = end
	}
	return string(utf16.Decode(u[start:end]))
}

// jsLastIndexOf is s.lastIndexOf(sep, from) for a single-character separator,
// in UTF-16 units; -1 if absent.
func jsLastIndexOf(s string, sep rune, from int) int {
	u := utf16.Encode([]rune(s))
	if from >= len(u) {
		from = len(u) - 1
	}
	for i := from; i >= 0; i-- {
		if rune(u[i]) == sep {
			return i
		}
	}
	return -1
}

// estimateTokens mirrors contexts/utils.ts: CJK ideographs U+4E00..U+9FFF count
// double, the sum is divided by ESTIMATED_TOKEN_CHAR_DIVISOR (3) and rounded up.
func estimateTokens(text string) int {
	cjk := 0
	for _, r := range text {
		if r >= 0x4E00 && r <= 0x9FFF {
			cjk++
		}
	}
	other := jsLen(text) - cjk
	return int(math.Ceil(float64(cjk*2+other) / 3))
}

// toFixed1 is Number.prototype.toFixed(1). Where the value sits exactly between
// two candidates JavaScript picks the larger one, whereas Go's formatting rounds
// half to even (25.25 -> "25.3" in JS, "25.2" in Go). The inputs here are
// byte-count divisions with at most ten fractional bits, so their decimal
// expansion is exact and can be rounded by hand.
func toFixed1(x float64) string {
	exact := new(big.Float).SetFloat64(x).Text('f', 40)
	intPart, frac, _ := strings.Cut(exact, ".")
	tenths := int(frac[0] - '0')
	if frac[1] >= '5' {
		tenths++
	}
	whole, _ := strconv.Atoi(intPart)
	if tenths == 10 {
		whole++
		tenths = 0
	}
	return strconv.Itoa(whole) + "." + strconv.Itoa(tenths)
}
