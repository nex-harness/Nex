package emitters

import (
	"encoding/json"
	"strconv"
	"strings"
)

func indexByte(s string, b byte) int { return strings.IndexByte(s, b) }

func containsSubstring(s, sub string) bool { return strings.Contains(s, sub) }

func mustString(m map[string]json.RawMessage, key string) string {
	s, _ := str(m, key)
	return s
}

// mustNonEmpty reports whether a string field is present and truthy (non-empty
// string), matching the TS `payload.x ? {x} : {}` spread on optional strings.
func mustNonEmpty(m map[string]json.RawMessage, key string) bool {
	s, ok := str(m, key)
	return ok && len(s) > 0
}

// safeInteger reports whether s is a JS safe integer (all digits, within
// Number.MAX_SAFE_INTEGER) and returns it. Number(rest) with a leading sign or
// whitespace is rejected: the TS computes Number(rest) and requires
// Number.isSafeInteger && > 0.
func safeInteger(s string) (int64, bool) {
	if s == "" {
		return 0, false
	}
	n, err := strconv.ParseInt(s, 10, 64)
	if err != nil {
		return 0, false
	}
	if n < -9007199254740991 || n > 9007199254740991 {
		return 0, false
	}
	if strconv.FormatInt(n, 10) != s && !(s == "0" && n == 0) {
		return 0, false
	}
	return n, true
}
