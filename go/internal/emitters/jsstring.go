package emitters

import "unicode/utf16"

// jsLen returns the JavaScript string .length: the number of UTF-16 code units.
// The telemetry chunkLength is String.length of the delta.
func jsLen(s string) int64 {
	var n int64
	for _, r := range s {
		if r > 0xFFFF {
			n += 2
			continue
		}
		n++
	}
	return n
}

// utf16Len is the same computation via the standard library, kept for tests.
func utf16Len(s string) int { return len(utf16.Encode([]rune(s))) }
