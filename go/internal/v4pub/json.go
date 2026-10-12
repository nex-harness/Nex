package v4pub

import (
	"encoding/json"
	"math"
	"strconv"
	"unicode/utf8"
)

// The publisher measures and emits JSON exactly as JSON.stringify does, bit for
// bit, because those bytes decide both the subscriber byte budget and the
// fragment boundaries the wire encoder computes. Go's encoding/json differs
// from JSON.stringify in three ways that matter here:
//
//   - HTML escaping: JSON.stringify does NOT escape <, >, & (Go escapes them by
//     default; SetEscapeHTML(false) fixes that).
//   - Numbers: JSON.stringify uses the ECMAScript Number::toString algorithm
//     (shortest round-trip, exponent forms like 1e+21 below 1e-6 and at or above
//     1e+21); Go prints floats its own way. Numbers we re-serialize are integral
//     values that arrived through JSON, so appendJSNumber reproduces the
//     ECMAScript form for them.
//   - Object key order: JSON.stringify keeps insertion order, Go maps sort keys.
//     Every object we re-serialize is rebuilt through ordered pairs.
//
// These helpers are deliberately local: other Go packages need no JS-exact JSON,
// and duplicating a short, well-tested pair of functions is cheaper than a
// shared package whose only caller is this one.

// orderedMember is one key/value pair kept in insertion order.
type orderedMember struct {
	Key string
	Val JSONValue
}

// JSONValue is a JSON value that preserves object key order and integer-typed
// numbers. Construct it with Obj/Arr/Str/Num/Bool/Null/Raw.
type JSONValue struct {
	kind  jvKind
	str   string
	num   float64
	int   int64
	isInt bool
	b     bool
	arr   []JSONValue
	obj   []orderedMember
	raw   json.RawMessage
}

type jvKind uint8

const (
	jvNull jvKind = iota
	jvBool
	jvInt
	jvFloat
	jvString
	jvArray
	jvObject
	jvRaw
)

// Null is the JSON null value.
func Null() JSONValue { return JSONValue{kind: jvNull} }

// Bool builds a JSON boolean.
func Bool(v bool) JSONValue { return JSONValue{kind: jvBool, b: v} }

// Int builds a JSON number that renders as an integer (or exponent form when
// |v| >= 1e21, matching Number::toString).
func Int(v int64) JSONValue { return JSONValue{kind: jvInt, int: v, isInt: true} }

// Float builds a JSON number from a float64.
func Float(v float64) JSONValue {
	if v == math.Trunc(v) && !math.IsInf(v, 0) && !math.IsNaN(v) {
		if v >= -9.007199254740992e15 && v <= 9.007199254740992e15 {
			return JSONValue{kind: jvInt, int: int64(v), isInt: true}
		}
	}
	return JSONValue{kind: jvFloat, num: v}
}

// Str builds a JSON string.
func Str(v string) JSONValue { return JSONValue{kind: jvString, str: v} }

// Arr builds a JSON array, preserving the given order.
func Arr(items ...JSONValue) JSONValue { return JSONValue{kind: jvArray, arr: items} }

// Obj builds a JSON object; members keep the given order.
func Obj(members ...orderedMember) JSONValue { return JSONValue{kind: jvObject, obj: members} }

// Member builds an ordered object member.
func Member(key string, v JSONValue) orderedMember { return orderedMember{Key: key, Val: v} }

// Raw wraps already-serialised JSON bytes; they are emitted verbatim.
func Raw(b []byte) JSONValue { return JSONValue{kind: jvRaw, raw: append([]byte(nil), b...)} }

// RawString wraps already-serialised JSON given as a string.
func RawString(s string) JSONValue { return JSONValue{kind: jvRaw, raw: []byte(s)} }

// RawMessageOf wraps a json.RawMessage verbatim. A nil or empty message renders
// as null, matching JSON.stringify(undefined) in the aggregate helpers.
func RawMessageOf(m json.RawMessage) JSONValue {
	if len(m) == 0 {
		return Null()
	}
	return JSONValue{kind: jvRaw, raw: append([]byte(nil), m...)}
}

// MarshalJSON renders the value with JSON.stringify-compatible bytes.
func (v JSONValue) MarshalJSON() ([]byte, error) { return v.appendJSON(nil), nil }

func (v JSONValue) appendJSON(b []byte) []byte {
	switch v.kind {
	case jvNull:
		return append(b, "null"...)
	case jvBool:
		if v.b {
			return append(b, "true"...)
		}
		return append(b, "false"...)
	case jvInt:
		return appendJSInt(b, v.int)
	case jvFloat:
		return appendJSFloat(b, v.num)
	case jvString:
		return appendJSString(b, v.str)
	case jvRaw:
		if len(v.raw) == 0 {
			return append(b, "null"...)
		}
		return append(b, v.raw...)
	case jvArray:
		b = append(b, '[')
		for i, item := range v.arr {
			if i > 0 {
				b = append(b, ',')
			}
			b = item.appendJSON(b)
		}
		return append(b, ']')
	case jvObject:
		b = append(b, '{')
		first := true
		for _, m := range v.obj {
			// JSON.stringify drops keys whose value is undefined; our raws carry
			// no undefined marker, so every member is emitted.
			if !first {
				b = append(b, ',')
			}
			first = false
			b = appendJSString(b, m.Key)
			b = append(b, ':')
			b = m.Val.appendJSON(b)
		}
		return append(b, '}')
	}
	return append(b, "null"...)
}

// appendJSInt prints an integral value. int64 cannot exceed 2^63-1 (~9.2e18),
// well below the 1e21 exponent threshold, so the digits are always printed in
// full; the 1e21 case only arises for Float values, handled by appendJSFloat.
func appendJSInt(b []byte, v int64) []byte {
	return strconv.AppendInt(b, v, 10)
}

// appendJSFloat reproduces Number::toString for a float64: shortest round-trip
// digits, fixed notation for exponents in [-6, 21), exponent form otherwise.
func appendJSFloat(b []byte, v float64) []byte {
	if math.IsNaN(v) || math.IsInf(v, 0) {
		return append(b, "null"...)
	}
	if v == 0 {
		return append(b, '0')
	}
	neg := math.Signbit(v)
	if neg {
		v = -v
	}
	// Shortest decimal digits; exp is the power of ten of the leading digit.
	// ECMAScript prints fixed notation when 10^(exp+1) does not overflow the
	// decimal representation, i.e. for -6 < exp < 21.
	digits, exp := shortestDigits(v)
	if exp <= -7 || exp >= 21 {
		return appendExponent(b, neg, digits, exp)
	}
	var out []byte
	n := len(digits)
	switch {
	case exp < 0:
		// 0.00ddd: -exp-1 leading zeros after the point, then the digits.
		out = append(out, '0', '.')
		for i := 0; i < -exp-1; i++ {
			out = append(out, '0')
		}
		out = append(out, digits...)
	case exp+1 >= n:
		// 0.0000ddd
		out = append(out, digits...)
		for i := 0; i < exp+1-n; i++ {
			out = append(out, '0')
		}
	default:
		// ddd.ddd
		out = append(out, digits[:exp+1]...)
		out = append(out, '.')
		out = append(out, digits[exp+1:]...)
	}
	if neg {
		b = append(b, '-')
	}
	return append(b, out...)
}

// shortestDigits returns the shortest decimal digit string whose value, read as
// d[0].d[1..] * 10^n (n = len(digits)+e-1), round-trips to v, plus the ECMAScript
// exponent e (the power of ten of the leading digit). strconv's 'e' format gives
// "d.dddde±XX", i.e. d.dddd * 10^e directly.
func shortestDigits(v float64) (digits string, exp int) {
	s := strconv.FormatFloat(v, 'e', -1, 64)
	mant := s
	expPart := ""
	for i := 0; i < len(s); i++ {
		if s[i] == 'e' || s[i] == 'E' {
			mant = s[:i]
			expPart = s[i+1:]
			break
		}
	}
	e, _ := strconv.Atoi(expPart)
	var ds []byte
	for i := 0; i < len(mant); i++ {
		if mant[i] == '.' {
			continue
		}
		ds = append(ds, mant[i])
	}
	return string(ds), e
}

// appendExponent renders d[0].d[1..]e±XX as ECMAScript does: at least one
// exponent digit, explicit sign, and a decimal point only when there is more
// than one significant digit.
func appendExponent(b []byte, neg bool, digits string, exp int) []byte {
	if neg {
		b = append(b, '-')
	}
	b = append(b, digits[0])
	if len(digits) > 1 {
		b = append(b, '.')
		b = append(b, digits[1:]...)
	}
	b = append(b, 'e')
	if exp < 0 {
		b = append(b, '-')
		exp = -exp
	} else {
		b = append(b, '+')
	}
	return strconv.AppendInt(b, int64(exp), 10)
}

// appendJSString writes a JSON string the way JSON.stringify does: it escapes
// the mandated control characters plus the quote and backslash, and passes
// through everything else (notably U+2028/U+2029 and <, >, &) unescaped.
func appendJSString(b []byte, s string) []byte {
	b = append(b, '"')
	for _, r := range s {
		switch r {
		case '"':
			b = append(b, '\\', '"')
		case '\\':
			b = append(b, '\\', '\\')
		case '\n':
			b = append(b, '\\', 'n')
		case '\r':
			b = append(b, '\\', 'r')
		case '\t':
			b = append(b, '\\', 't')
		case '\b':
			b = append(b, '\\', 'b')
		case '\f':
			b = append(b, '\\', 'f')
		default:
			switch {
			case r < 0x20:
				b = append(b, '\\', 'u', '0', '0', hexDigit(byte(r>>4)), hexDigit(byte(r)))
			case r == utf8.RuneError:
				// Preserve lone surrogates / invalid UTF-8 as U+FFFD, which is
				// what a JS string holding a lone surrogate serialises to via
				// the UTF-8 encoder boundary.
				b = append(b, "\uFFFD"...)
			default:
				b = utf8.AppendRune(b, r)
			}
		}
	}
	return append(b, '"')
}

func hexDigit(v byte) byte {
	const hex = "0123456789abcdef"
	return hex[v&0xf]
}

// MarshalJSONString returns JSON.stringify(value) for a JSONValue as a string.
func MarshalJSONString(v JSONValue) (string, error) {
	return string(v.appendJSON(nil)), nil
}
