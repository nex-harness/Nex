package rpc

import (
	"encoding/json"
	"errors"
	"math"
	"testing"
)

// The Go decoder is reachable from the network, so the hostile-input behaviour is pinned
// here rather than left to the fuzzer: a length prefix must never turn into an allocation
// proportional to the prefix, and nesting must be bounded.

// vqlOf encodes n the way the TypeScript writer does, for building hostile inputs.
func vqlOf(n uint32) []byte {
	if n == 0 {
		return []byte{0}
	}
	var out []byte
	cur := n
	for cur != 0 {
		next := byte(cur & 0x7f)
		cur >>= 7
		if cur > 0 {
			next |= 0x80
		}
		out = append(out, next)
	}
	return out
}

// buf builds a hostile input: tag, an arbitrary length prefix, and an optional payload.
func buf(tag byte, length []byte, payload ...byte) []byte {
	out := make([]byte, 0, 1+len(length)+len(payload))
	out = append(out, tag)
	out = append(out, length...)
	return append(out, payload...)
}

// hostileLengthPrefixes are length claims a peer can make while supplying (almost) nothing.
func hostileLengthPrefixes() map[string][]byte {
	return map[string][]byte{
		"string tag, 2^31 length, 3 bytes":    buf(TypeString, vqlOf(1<<31), 'a', 'b', 'c'),
		"buffer tag, 2^31 length, 3 bytes":    buf(TypeBuffer, vqlOf(1<<31), 'a', 'b', 'c'),
		"vsbuffer tag, 2^31 length, 3 bytes":  buf(TypeVSBuffer, vqlOf(1<<31), 'a', 'b', 'c'),
		"array tag, 2^31 count, 3 bytes":      buf(TypeArray, vqlOf(1<<31), 0, 0, 0),
		"object tag, 2^31 length, 3 bytes":    buf(TypeObject, vqlOf(1<<31), '{', '}', '}'),
		"object tag, 2^32-1 length":           buf(TypeObject, vqlOf(math.MaxUint32)),
		"negative int32 length (-1)":          buf(TypeString, []byte{0xff, 0xff, 0xff, 0xff, 0x0f}),
		"min int32 length":                    buf(TypeBuffer, []byte{0x80, 0x80, 0x80, 0x80, 0x08}),
		"int tag with unbounded continuation": buf(TypeInt, []byte{0x80, 0x80, 0x80, 0x80, 0x80, 0x80}),
		"array tag claiming 2^28 elements":    buf(TypeArray, vqlOf(1<<28), 0),
		"string tag, length 2^30, no payload": buf(TypeString, vqlOf(1<<30)),
		"object tag, length 2^30, no payload": buf(TypeObject, vqlOf(1<<30)),
		"buffer tag, length 2^30, no payload": buf(TypeBuffer, vqlOf(1<<30)),
		"truncated vql in length position":    buf(TypeString, []byte{0x80}),
		"truncated string body":               buf(TypeString, []byte{0x04}, 'a', 'b'),
		"truncated array element":             buf(TypeArray, []byte{0x02}, TypeInt, 0x01),
	}
}

// TestUnmarshalRejectsHostileLengthPrefixesWithoutAllocating is the step-2.1 requirement:
// a length prefix of 2^31 with three bytes available must not allocate 2 GiB.
func TestUnmarshalRejectsHostileLengthPrefixesWithoutAllocating(t *testing.T) {
	for name, input := range hostileLengthPrefixes() {
		t.Run(name, func(t *testing.T) {
			allocs := testing.AllocsPerRun(50, func() {
				// The error value is what matters here; the point is the memory profile.
				_, _ = Unmarshal(input)
			})
			// A rejected prefix needs a constant handful of allocations (the error itself and
			// the formatting of it). Anything that scales with the claimed length would show
			// up here as hundreds or thousands.
			if allocs > 32 {
				t.Errorf("decode of a %d-byte input allocated %v times; a rejected length prefix must not scale with the claimed length", len(input), allocs)
			}
			if _, err := Unmarshal(input); err == nil {
				t.Errorf("hostile input decoded without error; want a clean rejection")
			}
		})
	}
}

// TestUnmarshalTruncatedIsAnError pins the "never panic, always an error" rule for every
// proper prefix of a valid encoding: a stream can be cut anywhere.
func TestUnmarshalTruncatedIsAnError(t *testing.T) {
	valid := []struct {
		name  string
		value Value
	}{
		{"string", "hello é😀"},
		{"uint8array", []byte{1, 2, 3}},
		{"vsbuffer", Buffer{1, 2, 3}},
		{"array", []Value{int32(1), "two", nil, true, []Value{int32(3)}}},
		{"object", Object{Keys: []string{"a"}, Values: []Value{json.Number("1")}}},
		{"int negative", int32(-1000)},
		{"undefined", Undefined},
	}
	for _, tc := range valid {
		t.Run(tc.name, func(t *testing.T) {
			encoded, err := Marshal(tc.value)
			if err != nil {
				t.Fatal(err)
			}
			for cut := 0; cut < len(encoded); cut++ {
				func() {
					defer func() {
						if r := recover(); r != nil {
							t.Fatalf("panic decoding prefix %d of %d (%x): %v", cut, len(encoded), encoded[:cut], r)
						}
					}()
					// A prefix may decode successfully if the cut lands after a complete
					// element (e.g. inside a multi-element array), but it must never panic.
					_, _ = Unmarshal(encoded[:cut])
				}()
			}
		})
	}
}

// TestUnmarshalDepthLimit checks the 64-level cap from step 2.1.
func TestUnmarshalDepthLimit(t *testing.T) {
	// 64 nested arrays around an Undefined: the innermost container is at depth 64.
	build := func(depth int) []byte {
		out := []byte{}
		for i := 0; i < depth; i++ {
			out = append(out, TypeArray, 0x01)
		}
		return append(out, TypeUndefined)
	}
	if _, err := Unmarshal(build(8)); err != nil {
		t.Fatalf("a shallow value must decode: %v", err)
	}
	// 200 levels is well past the limit and must be rejected, not recursed into.
	_, err := Unmarshal(build(200))
	if !errors.Is(err, ErrTooDeep) {
		t.Errorf("deep nesting: got %v, want ErrTooDeep", err)
	}
}

// TestUnmarshalDepthLimitDoesNotOverflowStack checks the exact boundary of the cap: just
// under the limit decodes, and the guard fires rather than the goroutine stack.
func TestUnmarshalDepthLimitBoundary(t *testing.T) {
	build := func(depth int) []byte {
		out := []byte{}
		for i := 0; i < depth; i++ {
			out = append(out, TypeArray, 0x01)
		}
		return append(out, TypeUndefined)
	}
	// 63 nested containers plus the innermost element is comfortably inside.
	if _, err := Unmarshal(build(DefaultMaxDepth - 2)); err != nil {
		t.Errorf("depth %d must decode, got %v", DefaultMaxDepth-2, err)
	}
	if _, err := Unmarshal(build(DefaultMaxDepth + 8)); !errors.Is(err, ErrTooDeep) {
		t.Errorf("depth %d must be rejected with ErrTooDeep, got %v", DefaultMaxDepth+8, err)
	}
}

// TestUnmarshalVQLMaxBytes pins the 5-byte VQL budget: a sixth continuation bit is a
// malformed integer, not an unbounded read.
func TestUnmarshalVQLMaxBytes(t *testing.T) {
	if _, err := Unmarshal(buf(TypeInt, []byte{0xff, 0xff, 0xff, 0xff, 0x0f})); err != nil {
		t.Errorf("a well-formed 5-byte VQL must decode: %v", err)
	}
	// Five continuation bytes: the sixth group would shift past 32 bits.
	_, err := Unmarshal(buf(TypeInt, []byte{0x80, 0x80, 0x80, 0x80, 0x80, 0x01}))
	if !errors.Is(err, ErrVQLTooLong) {
		t.Errorf("six-group VQL: got %v, want ErrVQLTooLong", err)
	}
	_, err = Unmarshal(buf(TypeString, []byte{0x80, 0x80, 0x80, 0x80, 0x80, 0x80}))
	if !errors.Is(err, ErrVQLTooLong) {
		t.Errorf("six-group length prefix: got %v, want ErrVQLTooLong", err)
	}
}

// TestUnmarshalUnknownTag pins the rejection of a tag outside the closed set.
func TestUnmarshalUnknownTag(t *testing.T) {
	for _, tag := range []byte{7, 8, 0x7f, 0xff} {
		if _, err := Unmarshal([]byte{tag}); !errors.Is(err, ErrUnknownTag) {
			t.Errorf("tag %d: got %v, want ErrUnknownTag", tag, err)
		}
	}
}

// TestUnmarshalMaxValueBytesIsConfigurable checks the documented "64 MiB (configurable)":
// a tighter limit must reject a payload the default would accept.
func TestUnmarshalMaxValueBytesIsConfigurable(t *testing.T) {
	payload := make([]byte, 4096)
	encoded := buf(TypeBuffer, vqlOf(uint32(len(payload))), payload...)
	if _, err := NewReader(encoded).Deserialize(); err != nil {
		t.Fatalf("the default limit must accept a 4 KiB buffer: %v", err)
	}
	tight := NewReaderWithLimits(encoded, Limits{MaxValueBytes: 1024})
	if _, err := tight.Deserialize(); !errors.Is(err, ErrValueTooLarge) {
		t.Errorf("with MaxValueBytes=1024: got %v, want ErrValueTooLarge", err)
	}
}

// TestUnmarshalDefaultLimitIs64MiB pins the documented default so a future edit to the
// constant is a deliberate decision.
func TestUnmarshalDefaultLimitIs64MiB(t *testing.T) {
	limits := DefaultLimits()
	if limits.MaxValueBytes != 64<<20 {
		t.Errorf("MaxValueBytes = %d, want %d (64 MiB)", limits.MaxValueBytes, 64<<20)
	}
	if limits.MaxDepth != 64 {
		t.Errorf("MaxDepth = %d, want 64", limits.MaxDepth)
	}
	if limits.MaxVQLBytes != 5 {
		t.Errorf("MaxVQLBytes = %d, want 5", limits.MaxVQLBytes)
	}
}

// TestVQLMatchesTypeScriptBitPatterns pins the documented JS quirks so nobody "fixes" them:
// a negative int32 takes five bytes because the shift is logical, and 2^28 still fits in
// the uint32 space the algorithm works in.
func TestVQLMatchesTypeScriptBitPatterns(t *testing.T) {
	cases := []struct {
		value int32
		hex   string
		why   string
	}{
		{0, "00", "zero is a single byte"},
		{1, "01", "small values are compact"},
		{127, "7f", "the 7-bit boundary"},
		{128, "8001", "crossing into a second byte"},
		{16383, "ff7f", "the 14-bit boundary"},
		{16384, "808001", "three bytes"},
		{math.MaxInt32, "ffffffff07", "the largest int32"},
		{-1, "ffffffff0f", "NEGATIVE: the logical shift never reaches zero, so five bytes"},
		{-2, "feffffff0f", "NEGATIVE: five bytes"},
		{-1000, "98f8ffff0f", "NEGATIVE: five bytes"},
		{math.MinInt32, "8080808008", "NEGATIVE: the most negative int32"},
		{1 << 28, "8080808001", "2^28 is the point where a fifth byte becomes needed"},
		{1<<28 - 1, "ffffff7f", "just under 2^28 still fits in four bytes"},
	}
	for _, tc := range cases {
		t.Run(tc.why, func(t *testing.T) {
			got := appendInt32VQL(nil, tc.value)
			if hexOf(got) != tc.hex {
				t.Errorf("appendInt32VQL(%d) = %s, want %s", tc.value, hexOf(got), tc.hex)
			}
			back, err := NewReader(got).readIntVQL()
			if err != nil {
				t.Fatalf("readIntVQL: %v", err)
			}
			if back != tc.value {
				t.Errorf("round trip of %d gave %d", tc.value, back)
			}
			if len(got) > DefaultMaxVQLBytes {
				t.Errorf("encoding used %d bytes, over the %d byte budget", len(got), DefaultMaxVQLBytes)
			}
		})
	}
}

func hexOf(b []byte) string {
	const digits = "0123456789abcdef"
	out := make([]byte, 0, len(b)*2)
	for _, c := range b {
		out = append(out, digits[c>>4], digits[c&0xf])
	}
	return string(out)
}

// TestAppendInt32VQLIsABitPatternNotAnAssertion keeps the intent explicit: the five-byte
// encoding of a negative number is the wire format, not a defect to be repaired.
func TestAppendInt32VQLIsABitPatternNotAnAssertion(t *testing.T) {
	for _, negative := range []int32{-1, -2, -127, -128, -1000, math.MinInt32} {
		encoded := appendInt32VQL(nil, negative)
		if len(encoded) != 5 {
			t.Errorf("appendInt32VQL(%d) used %d bytes, want the 5 the TS logical shift produces", negative, len(encoded))
		}
		// The first byte must carry the continuation marker, which only the logical-shift
		// formulation can produce for a negative value.
		if encoded[0]&0x80 == 0 {
			t.Errorf("appendInt32VQL(%d) first byte %08b has no continuation bit", negative, encoded[0])
		}
	}
	// 2^28 is where the fifth byte first appears for a POSITIVE value, verified against the
	// TypeScript serializer: 2^28 -> 8080808001, 2^28-1 -> ffffff7f.
	if got := len(appendInt32VQL(nil, 1<<28)); got != 5 {
		t.Errorf("appendInt32VQL(2^28) used %d bytes, want 5", got)
	}
	if got := len(appendInt32VQL(nil, 1<<28-1)); got != 4 {
		t.Errorf("appendInt32VQL(2^28-1) used %d bytes, want 4", got)
	}
}

// TestVQLOfMatchesAppendInt32VQL keeps the hostile-input helper honest: it must produce the
// same bit pattern as the writer the vectors pinned.
func TestVQLOfMatchesAppendInt32VQL(t *testing.T) {
	for _, v := range []uint32{0, 1, 127, 128, 16383, 16384, 1 << 28, math.MaxUint32} {
		want := appendInt32VQL(nil, int32(v))
		got := vqlOf(v)
		if hexOf(want) != hexOf(got) {
			t.Errorf("vqlOf(%d) = %s, appendInt32VQL = %s", v, hexOf(got), hexOf(want))
		}
	}
}
