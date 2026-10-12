package rpc

import (
	"bytes"
	"encoding/json"
	"math/rand"
	"reflect"
	"sort"
	"testing"
	"testing/quick"
)

// The recorded vectors (rpc_test.go) pin the byte-exact agreement with the TypeScript
// codec for 35 hand-picked shapes. This file covers the space between them: random value
// trees, generated with testing/quick (stdlib — the Go rewrite adds no test dependency).
//
// The property is stated on the CANONICAL form. A decoded value must be a fixed point of
// encode/decode:
//
//	canonical(v) = decode(encode(decode(encode(v))))
//
// One hop is deliberately not the assertion. A JSON integer legitimately changes
// representation on the way out — the JS tag rule picks tag 6 by numeric VALUE, so the
// JSON number 0 (json.Number) is re-encoded as Int 0 (int32). That is a change of Go type
// carrying the same value, and it is exactly what Node does. Two hops are stable.

// valueSource is the parameter type of the property functions. It is not itself the
// generated value: quick synthesizes one of these per iteration and the test derives a
// random tree from it, which is how the depth budget is threaded through.
//
// Because `Value` is an alias for `any`, the tree is passed to the property as a plain
// Value argument — see configFor below, which installs the custom Values hook.
type valueSource struct {
	depth    int
	maxDepth int
}

const (
	propertyMaxDepth = 4
	propertyMaxWidth = 4
)

// configFor returns a quick.Config whose Values hook fills the single `Value` parameter of
// the property function with a generated tree. quick.Generator cannot be used directly
// here: it must return the argument type itself, and the argument type is the open `Value`
// alias, which quick cannot construct. The hook uses the *rand.Rand that quick supplies, so
// there is no shared state to race on.
func configFor(seed int64, count int) *quick.Config {
	return &quick.Config{
		MaxCount: count,
		Rand:     rand.New(rand.NewSource(seed)), // fixed seed: a failure is reproducible
		Values: func(args []reflect.Value, rnd *rand.Rand) {
			if len(args) != 1 {
				panic("property takes exactly one argument")
			}
			source := valueSource{maxDepth: propertyMaxDepth}
			// reflect.ValueOf(nil) is an INVALID value and reflect.Call panics on it, so a
			// generated JSON null is carried as a typed nil pointer instead. Marshal routes
			// any nil pointer through the JSON fallback and writes "null", which is exactly
			// what a plain nil produces, so the codec sees the same value either way.
			if tree := source.tree(rnd); tree == nil {
				args[0] = reflect.ValueOf((*Value)(nil))
			} else {
				args[0] = reflect.ValueOf(tree)
			}
		},
	}
}

// tree picks one leaf or composite shape. Composites recurse with depth+1, so generation
// always terminates regardless of the random stream.
func (v valueSource) tree(rnd *rand.Rand) Value {
	canNest := v.depth < v.maxDepth
	// undefined, string, uint8array, vsbuffer, int, json number, bool, null
	leaves := 8
	composites := 0
	if canNest {
		composites = 2 // array, object
	}
	switch rnd.Intn(leaves + composites) {
	case 0:
		return Undefined
	case 1:
		return randomString(rnd)
	case 2:
		return randomBytes(rnd)
	case 3:
		return Buffer(randomBytes(rnd))
	case 4:
		return randomInt32(rnd)
	case 5:
		return randomJSONNumber(rnd)
	case 6:
		return rnd.Intn(2) == 0
	case 7:
		return nil // JSON null
	case 8:
		if canNest {
			return randomArray(valueSource{depth: v.depth + 1, maxDepth: v.maxDepth}, rnd)
		}
		return "leaf"
	default:
		if canNest {
			return randomObject(valueSource{depth: v.depth + 1, maxDepth: v.maxDepth}, rnd)
		}
		return "leaf"
	}
}

func randomArray(source valueSource, rnd *rand.Rand) Value {
	n := rnd.Intn(propertyMaxWidth + 1)
	out := make([]Value, 0, n)
	for i := 0; i < n; i++ {
		out = append(out, source.tree(rnd))
	}
	return out
}

// randomObject builds an Object with sorted keys, so re-marshaling is deterministic and a
// failure is reproducible rather than dependent on map iteration order.
func randomObject(source valueSource, rnd *rand.Rand) Value {
	n := rnd.Intn(propertyMaxWidth + 1)
	keys := make([]string, 0, n)
	for i := 0; i < n; i++ {
		keys = append(keys, randomString(rnd))
	}
	sort.Strings(keys)
	obj := Object{}
	for _, key := range keys {
		obj.Set(key, source.tree(rnd))
	}
	return obj
}

func randomString(rnd *rand.Rand) string {
	n := rnd.Intn(8)
	const alphabet = "aé中😀 <>&\"\\\n\t\x00{}"
	runes := []rune(alphabet)
	var sb []rune
	for i := 0; i < n; i++ {
		sb = append(sb, runes[rnd.Intn(len(runes))])
	}
	return string(sb)
}

func randomBytes(rnd *rand.Rand) []byte {
	n := rnd.Intn(16)
	out := make([]byte, n)
	for i := range out {
		out[i] = byte(rnd.Intn(256))
	}
	return out
}

// randomInt32 spans the boundaries the VQL quirks live at, not just the easy middle.
func randomInt32(rnd *rand.Rand) int32 {
	specials := []int32{
		0, 1, -1, 127, 128, -127, -128, 16383, 16384, -16383, -16384,
		1 << 20, -(1 << 20), 1<<28 - 1, 1 << 28, -(1 << 28),
		2147483647, -2147483648, 2147483646, -2147483647,
	}
	if rnd.Intn(2) == 0 {
		return specials[rnd.Intn(len(specials))]
	}
	return int32(rnd.Uint64())
}

// randomJSONNumber produces literal text that stays a legal JSON number and is stable
// through a decode/re-encode, so the property is about the codec and not about float
// formatting noise. Literals that are integral and fit in int32 are deliberately included:
// they are the documented case where a JSON number canonicalizes to the Int tag.
func randomJSONNumber(rnd *rand.Rand) json.Number {
	literals := []string{
		"0", "-0", "1", "-1", "3", "1.5", "-1.5", "1e21", "1e-7", "0.1",
		"1000000000000", "2147483648", "123.456", "5e-324", "1.7976931348623157e308",
		"1e400", "-1e400", "9007199254740993", "779074521",
	}
	return json.Number(literals[rnd.Intn(len(literals))])
}

// TestPropertyValueRoundTrip is the step-2.1 property test: random value trees survive
// marshal -> unmarshal -> equal.
func TestPropertyValueRoundTrip(t *testing.T) {
	err := quick.Check(func(input Value) bool {
		encoded, err := Marshal(input)
		if err != nil {
			t.Errorf("marshal %#v: %v", input, err)
			return false
		}
		decoded, err := Unmarshal(encoded)
		if err != nil {
			t.Errorf("unmarshal %x (from %#v): %v", encoded, input, err)
			return false
		}
		if !valuesEqual(decoded, input) {
			t.Errorf("round trip changed the value\n input   %#v\n encoded %x\n decoded %#v", input, encoded, decoded)
			return false
		}
		return true
	}, configFor(20241012, 2000))
	if err != nil {
		t.Fatal(err)
	}
}

// TestPropertyEncodingIsIdempotentAfterOneHop is the second half of the story. A JSON
// integer changes representation on the way out (tag 5 json.Number becomes tag 6 int32,
// because the JS tag rule picks by numeric VALUE), but the encoding after that must be
// frozen — that stability is exactly what FuzzUnmarshal relies on.
func TestPropertyEncodingIsIdempotentAfterOneHop(t *testing.T) {
	err := quick.Check(func(input Value) bool {
		first, err := Marshal(input)
		if err != nil {
			t.Errorf("marshal %#v: %v", input, err)
			return false
		}
		decoded, err := Unmarshal(first)
		if err != nil {
			t.Errorf("unmarshal %x: %v", first, err)
			return false
		}
		second, err := Marshal(decoded)
		if err != nil {
			t.Errorf("re-marshal %#v: %v", decoded, err)
			return false
		}
		if !bytes.Equal(first, second) {
			t.Errorf("encoding is not idempotent\n input  %#v\n first  %x\n second %x", input, first, second)
			return false
		}
		return true
	}, configFor(20241013, 1000))
	if err != nil {
		t.Fatal(err)
	}
}
