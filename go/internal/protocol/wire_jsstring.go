package protocol

import "unicode/utf8"

const hexDigits = "0123456789abcdef"

// appendJSString appends s as JSON.stringify renders a string: the short escapes
// for quote, backslash, \b \f \n \r \t, \u00xx (lower-case hex) for the other
// control characters, and everything else verbatim, including U+2028/U+2029 and
// "<>&" (Go's encoding/json escapes those, which would change byte counts).
// Invalid UTF-8 cannot exist in a JS string; it is replaced with U+FFFD as the
// Go runtime does.
func appendJSString(b []byte, s string) []byte {
	b = append(b, '"')
	for i := 0; i < len(s); {
		c := s[i]
		if c < utf8.RuneSelf {
			switch c {
			case '"':
				b = append(b, '\\', '"')
			case '\\':
				b = append(b, '\\', '\\')
			case '\b':
				b = append(b, '\\', 'b')
			case '\f':
				b = append(b, '\\', 'f')
			case '\n':
				b = append(b, '\\', 'n')
			case '\r':
				b = append(b, '\\', 'r')
			case '\t':
				b = append(b, '\\', 't')
			default:
				if c < 0x20 {
					b = append(b, '\\', 'u', '0', '0', hexDigits[c>>4], hexDigits[c&0xf])
				} else {
					b = append(b, c)
				}
			}
			i++
			continue
		}
		r, size := utf8.DecodeRuneInString(s[i:])
		b = utf8.AppendRune(b, r) // RuneError for invalid input
		i += size
	}
	return append(b, '"')
}
