package rpc

import (
	"encoding/json"
	"errors"
	"math"
	"strconv"
	"strings"
)

// Value is one RPC value: the Go transcription of the dynamic `any` that
// packages/rpc/src/serialization.ts serializes.
//
// The set is CLOSED. Only these Go types get a wire tag of their own:
//
//	Undefined                         -> tag 0   (TS `undefined`)
//	string                            -> tag 1
//	[]byte                            -> tag 2   (Uint8Array)
//	Buffer                            -> tag 3   (VSBuffer)
//	[]Value                           -> tag 4   (Array)
//	int32                             -> tag 6   (Int; VQL)
//	nil, bool, json.Number, []any,
//	Object                            -> tag 5   (JSON fallback)
//
// Everything else is encoded as JSON, which is exactly what the TS `else` branch does for
// any non-array/non-string/non-binary value. Note two consequences of the JS model:
//
//   - There is ONE number type in JS, so the tag is chosen by the numeric VALUE, not by
//     the Go type: float64(3.0) and int(3) both take the Int tag, and int64 outside int32
//     range falls through to JSON, as 2**31 does in the recorded vectors.
//   - Object is the ordered representation of a JSON object inside a JSON tree. JS object
//     key order is insertion order and `JSON.stringify` preserves it, so a Go map (which
//     has no order) cannot round-trip protocol payloads byte-for-byte.
//
// `Value` is an ALIAS for `any` on purpose: the TS type is `any`, and an alias keeps
// []Value identical to []any so decoded trees need no conversion at call sites.
type Value = any

// DataType tags mirror the `enum DataType` in packages/rpc/src/serialization.ts.
const (
	TypeUndefined = 0
	TypeString    = 1
	TypeBuffer    = 2 // Uint8Array, not VSBuffer
	TypeVSBuffer  = 3
	TypeArray     = 4
	TypeObject    = 5 // JSON fallback
	TypeInt       = 6 // VQL-encoded integer
)

// Buffer is a VSBuffer (packages/rpc/src/buffer.ts): binary data that is NOT a Uint8Array
// subclass in JS. It is a named []byte so a Go type switch can tell tags 2 and 3 apart —
// a plain []byte always means Uint8Array.
type Buffer []byte

// undefinedValue is the sentinel for the TS `undefined`. A distinct empty struct type makes
// `case undefinedValue:` unambiguous, which a bare nil would not be.
type undefinedValue struct{}

// Undefined serializes to the Undefined tag. It is distinct from a nil Value, which is JS
// `null` and therefore the JSON fallback.
var Undefined = undefinedValue{}

// IsUndefined reports whether v is the Undefined sentinel.
func IsUndefined(v Value) bool { _, ok := v.(undefinedValue); return ok }

// Decode errors. Every one of them is returned instead of panicking: the decoder reads
// attacker-controlled bytes.
var (
	// ErrTruncated means the reader ran out of bytes mid-value.
	ErrTruncated = errors.New("rpc: truncated value")
	// ErrTooDeep means the value nests deeper than Limits.MaxDepth.
	ErrTooDeep = errors.New("rpc: value nests too deeply")
	// ErrValueTooLarge means a length prefix exceeded Limits.MaxValueBytes.
	ErrValueTooLarge = errors.New("rpc: value exceeds the configured maximum length")
	// ErrVQLTooLong means a VQL integer used more than Limits.MaxVQLBytes bytes.
	ErrVQLTooLong = errors.New("rpc: VQL integer too long")
	// ErrUnknownTag means a type tag outside 0..6.
	ErrUnknownTag = errors.New("rpc: unknown data type tag")
)

// Limits bounds what a decoder will accept. The Go decoder is network-facing, so every
// length and depth is checked BEFORE anything is allocated.
type Limits struct {
	// MaxDepth is the maximum array/object nesting (default 64).
	MaxDepth int
	// MaxValueBytes is the maximum length prefix of a single string, buffer, array or
	// JSON payload (default 64 MiB).
	MaxValueBytes int
	// MaxVQLBytes is the maximum number of bytes one VQL integer may occupy. The TS
	// encoding needs at most 5 for a uint32; more is malformed (default 5).
	MaxVQLBytes int
}

// Default decode limits, matching step 2.1 of docs/specs/go-rewrite/phase-2-go-server.md.
const (
	DefaultMaxDepth      = 64
	DefaultMaxValueBytes = 64 << 20 // 64 MiB
	DefaultMaxVQLBytes   = 5
	// defaultMaxPrealloc caps how many elements a decoder reserves before it has seen any:
	// an array length prefix must never translate into an allocation proportional to the
	// prefix alone.
	defaultMaxPrealloc = 256
)

// DefaultLimits returns the limits used by NewReader.
func DefaultLimits() Limits {
	return Limits{
		MaxDepth:      DefaultMaxDepth,
		MaxValueBytes: DefaultMaxValueBytes,
		MaxVQLBytes:   DefaultMaxVQLBytes,
	}
}

// withDefaults fills unset fields so a zero Limits is usable.
func (l Limits) withDefaults() Limits {
	if l.MaxDepth <= 0 {
		l.MaxDepth = DefaultMaxDepth
	}
	if l.MaxValueBytes <= 0 {
		l.MaxValueBytes = DefaultMaxValueBytes
	}
	if l.MaxVQLBytes <= 0 {
		l.MaxVQLBytes = DefaultMaxVQLBytes
	}
	return l
}

// asNumber reports the numeric value of v, mirroring the JS `typeof data === "number"`
// test. Go types are accepted for convenience; the TAG is decided from the value.
func asNumber(value Value) (float64, bool) {
	switch n := value.(type) {
	case int:
		return float64(n), true
	case int8:
		return float64(n), true
	case int16:
		return float64(n), true
	case int32:
		return float64(n), true
	case int64:
		return float64(n), true
	case uint:
		return float64(n), true
	case uint8:
		return float64(n), true
	case uint16:
		return float64(n), true
	case uint32:
		return float64(n), true
	case uint64:
		return float64(n), true
	case float32:
		return float64(n), true
	case float64:
		return n, true
	case json.Number:
		f, err := n.Float64()
		return f, err == nil
	}
	return 0, false
}

// isInt32 mirrors the TS `(data | 0) === data`: true only for an integral value that
// survives a ToInt32 cast, so negative values (down to -2^31) DO take the Int tag and
// 2^31 does not.
func isInt32(n float64) bool {
	if math.IsNaN(n) || math.IsInf(n, 0) {
		return false
	}
	if n != math.Trunc(n) {
		return false
	}
	return n >= math.MinInt32 && n <= math.MaxInt32
}

// appendJSNumber appends n the way `JSON.stringify(n)` would. Go's encoding/json disagrees
// with ECMAScript on both the exponent thresholds (JS switches to exponential form only
// outside 1e-6 <= |n| < 1e21) and the spelling of the exponent, so the ECMAScript
// Number::toString algorithm (ECMA-262 6.1.6.1.20) is implemented here over the shortest
// round-tripping decimal Go can give us.
func appendJSNumber(dst []byte, n float64) []byte {
	switch {
	case math.IsNaN(n) || math.IsInf(n, 0):
		// JSON.stringify(NaN) and JSON.stringify(Infinity) are both "null".
		return append(dst, 'n', 'u', 'l', 'l')
	case n == 0:
		// Covers -0, which JSON.stringify renders as "0".
		return append(dst, '0')
	}
	if n < 0 {
		dst = append(dst, '-')
		n = -n
	}
	// Shortest round-tripping digits, as "d.dddde±dd".
	sci := strconv.AppendFloat(nil, n, 'e', -1, 64)
	mantissa, exp, _ := strings.Cut(string(sci), "e")
	expValue, _ := strconv.Atoi(exp)
	digits := strings.Replace(mantissa, ".", "", 1)
	// n is the position of the decimal point relative to the first digit.
	point := expValue + 1
	switch {
	case len(digits) <= point && point <= 21:
		dst = append(dst, digits...)
		for i := len(digits); i < point; i++ {
			dst = append(dst, '0')
		}
	case point > 0 && point <= 21:
		dst = append(dst, digits[:point]...)
		dst = append(dst, '.')
		dst = append(dst, digits[point:]...)
	case point > -6 && point <= 0:
		dst = append(dst, '0', '.')
		for i := 0; i < -point; i++ {
			dst = append(dst, '0')
		}
		dst = append(dst, digits...)
	default:
		dst = append(dst, digits[0])
		if len(digits) > 1 {
			dst = append(dst, '.')
			dst = append(dst, digits[1:]...)
		}
		dst = append(dst, 'e')
		e := point - 1
		if e >= 0 {
			dst = append(dst, '+')
		} else {
			dst = append(dst, '-')
			e = -e
		}
		dst = strconv.AppendInt(dst, int64(e), 10)
	}
	return dst
}
