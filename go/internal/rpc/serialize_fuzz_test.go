package rpc

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"reflect"
	"strconv"
	"testing"
)

// FuzzUnmarshal feeds arbitrary bytes to the decoder. The contract that matters for a
// network-facing parser: no panic, and every rejection is an error rather than a fault.
//
// When a value does decode, it is re-marshaled and decoded again: the second decode must
// produce an equal value. That catches decoders that are merely safe but lossy or unstable.
func FuzzUnmarshal(f *testing.F) {
	// Seeds: one per tag, plus the VQL and depth edge cases the hardening exists for.
	f.Add([]byte{TypeUndefined})
	f.Add([]byte{TypeString, 0x00})
	f.Add([]byte{TypeString, 0x05, 'h', 'e', 'l', 'l', 'o'})
	f.Add([]byte{TypeString, 0xff, 0xff, 0xff, 0xff, 0x0f}) // negative int32 length
	f.Add([]byte{TypeBuffer, 0x03, 0x01, 0x02, 0x03})
	f.Add([]byte{TypeVSBuffer, 0x00})
	f.Add([]byte{TypeArray, 0x00})
	f.Add([]byte{TypeArray, 0x02, TypeInt, 0x01, TypeString, 0x01, 'x'})
	f.Add([]byte{TypeObject, 0x02, '{', '}'})
	f.Add([]byte{TypeObject, 0x04, 'n', 'u', 'l', 'l'})
	f.Add([]byte{TypeInt, 0x00})
	f.Add([]byte{TypeInt, 0xff, 0xff, 0xff, 0xff, 0x0f}) // -1
	f.Add([]byte{TypeInt, 0x7f})
	f.Add([]byte{0x07})                                       // unknown tag
	f.Add([]byte{})                                           // empty input
	f.Add([]byte{TypeString})                                 // tag with no length
	f.Add([]byte{TypeInt, 0x80})                              // truncated VQL: continuation bit with no payload
	f.Add(append([]byte{TypeArray, 0x01}, deepNested(80)...)) // nesting past the depth limit

	f.Fuzz(func(t *testing.T, data []byte) {
		value, err := NewReader(data).Deserialize()
		if err != nil {
			if !isDeclaredDecodeError(err) {
				t.Fatalf("undeclared error for %x: %v", data, err)
			}
			return
		}
		// A decoded value must be a FIXED POINT of encode/decode, checked over two hops:
		// one hop is not enough because a JSON integer legitimately canonicalizes to the
		// Int tag (the JS tag rule picks tag 6 by numeric value), which is a change of
		// representation, not of value. The second hop must then agree with the first.
		first, err := Marshal(value)
		if err != nil {
			t.Fatalf("marshal of %#v (from %x): %v", value, data, err)
		}
		decodedAgain, err := Unmarshal(first)
		if err != nil {
			t.Fatalf("re-decode of %x (encoded %x): %v", data, first, err)
		}
		second, err := Marshal(decodedAgain)
		if err != nil {
			t.Fatalf("re-marshal of %#v (from %x): %v", decodedAgain, data, err)
		}
		if !valuesEqual(decodedAgain, mustUnmarshal(t, second)) {
			t.Fatalf("decode/encode is not a fixed point for %x:\n value %#v\nre-encodes to %x\nthen %#v",
				data, value, first, decodedAgain)
		}
	})
}

// isDeclaredDecodeError reports whether err is one the decoder is allowed to produce. The
// tag-5 branch delegates to encoding/json, so a malformed JSON payload surfaces that
// package's errors; everything else is one of this package's sentinels.
func isDeclaredDecodeError(err error) bool {
	if errors.Is(err, ErrTruncated) || errors.Is(err, ErrTooDeep) ||
		errors.Is(err, ErrValueTooLarge) || errors.Is(err, ErrVQLTooLong) ||
		errors.Is(err, ErrUnknownTag) {
		return true
	}
	var syntaxErr *json.SyntaxError
	if errors.As(err, &syntaxErr) {
		return true
	}
	return errors.Is(err, io.ErrUnexpectedEOF) || errors.Is(err, io.EOF)
}

// deepNested returns `depth` nested one-element array headers, the innermost holding a
// single Undefined so the value is otherwise complete.
func deepNested(depth int) []byte {
	out := make([]byte, 0, depth*2+1)
	for i := 0; i < depth; i++ {
		out = append(out, TypeArray, 0x01)
	}
	return append(out, TypeUndefined)
}

// valuesEqual is the strict codec-level equality used by the round-trip property. It is
// deliberately separate from valuesMatch, which folds the Undefined sentinel and JSON
// numbers into the shapes the recorded-vector fixture uses.
//
// It encodes the four documented JS behaviours that make a tree change SHAPE (never value)
// on its way through the JSON fallback:
//
//  1. JSON.parse yields an IEEE-754 double, so an integer past 2^53 comes back rounded.
//  2. An integer in int32 range is written bare and re-read as tag 6 (int32).
//  3. JSON.stringify DROPS an object member whose value is undefined, but turns undefined
//     inside an ARRAY into null.
//  4. A VSBuffer nested in the JSON fallback is not a Uint8Array, so JSON.stringify emits
//     its two own enumerable properties ({buffer, byteLength}) rather than the marker.
//
// The comparison is run on the DECODED form of both sides, so that a value which changes
// representation on the way through the JSON fallback is compared like for like. That is
// what makes the property a statement about the codec rather than about which side of the
// comparison happened to be the decoded one.
func valuesEqual(a, b Value) bool {
	return valuesEqualDecoded(a, b)
}

// valuesEqualDecoded is the codec-level equality used by the round-trip property. It is
// deliberately separate from valuesMatch, which folds the Undefined sentinel and JSON
// numbers into the shapes the recorded-vector fixture uses.
//
// It encodes the documented JS behaviours that make a tree change SHAPE (never value) on
// its way through the JSON fallback:
//
//  1. JSON.parse yields an IEEE-754 double, so an integer past 2^53 comes back rounded and
//     an exponent is renormalized (`1e21` is written `1e+21`).
//  2. An integer in int32 range is written bare and re-read as tag 6 (int32).
//  3. JSON.stringify DROPS an object member whose value is undefined, but turns undefined
//     inside an ARRAY into null.
//  4. A VSBuffer nested in the JSON fallback is not a Uint8Array, so JSON.stringify emits
//     its two own enumerable properties ({buffer, byteLength}) rather than the marker.
//
// `inJSON` says whether the value sits inside the JSON fallback, where rules 3 and 4 apply;
// at the top level a Buffer is tag 3 and an Undefined is tag 0.
func valuesEqualDecoded(a, b Value) bool {
	return valuesEqualIn(a, b, true)
}

func valuesEqualIn(a, b Value, inJSON bool) bool {
	// `-0` and `0` are the same JSON number: JSON.stringify(-0) is "0" and JSON.parse("0")
	// is +0, so a value written as -0 reads back as 0.
	if n, ok := a.(json.Number); ok && n.String() == "-0" {
		if f, isFloat := b.(float64); isFloat {
			return f == 0
		}
		if z, isInt := b.(int32); isInt {
			return z == 0
		}
	}
	if n, ok := b.(json.Number); ok && n.String() == "-0" {
		if f, isFloat := a.(float64); isFloat {
			return f == 0
		}
		if z, isInt := a.(int32); isInt {
			return z == 0
		}
	}
	// An Undefined survives as Undefined only at the top level, where the writer emits
	// tag 0. Inside the JSON fallback it does not: JSON.stringify drops it as an object
	// member (handled in objectSurvivesEqual) and writes null for it in an array (handled
	// in the []Value case below).
	if IsUndefined(a) || IsUndefined(b) {
		return IsUndefined(a) && IsUndefined(b)
	}
	// A JSON null arrives from the generator as a typed nil pointer (reflect cannot carry
	// an untyped nil into a function call) and from the decoder as an untyped nil. Both
	// encode to the JSON literal `null`, so they denote the same value.
	if isJSONNull(a) || isJSONNull(b) {
		return isJSONNull(a) && isJSONNull(b)
	}
	switch av := a.(type) {
	case nil:
		return false // unreachable: isJSONNull already handled it
	case string:
		bv, ok := b.(string)
		return ok && av == bv
	case bool:
		bv, ok := b.(bool)
		return ok && av == bv
	case int32:
		if bv, ok := b.(int32); ok {
			return av == bv
		}
		// Inside the JSON fallback an integer is written bare and read back as
		// json.Number; the decoded side then carries the same value in the other type.
		if bn, ok := b.(json.Number); ok && inJSON {
			return bn.String() == strconv.FormatInt(int64(av), 10)
		}
		return false
	case json.Number:
		// A JSON number is re-read through JSON.parse, which in JS yields an IEEE-754
		// double, so an integer past 2^53 comes back rounded and the exponent is
		// renormalized (`1e21` is written `1e+21`). Comparing as float64 mirrors the
		// reference implementation; comparing text would flag a JS-faithful decode.
		af, aerr := av.Float64()
		switch bv := b.(type) {
		case json.Number:
			bfValue, berr := bv.Float64()
			if aerr != nil || berr != nil {
				return av.String() == bv.String()
			}
			return af == bfValue
		case int32:
			// Inside the JSON fallback a bare integer comes back as tag 6 (int32).
			n, ok := numberAsInt32(av)
			return ok && n == bv
		}
		return false
	case float64:
		bf, ok := b.(json.Number)
		if !ok {
			return false
		}
		bfValue, err := bf.Float64()
		return err == nil && av == bfValue
	case []byte:
		bv, ok := b.([]byte)
		return ok && bytesEqual(av, bv)
	case Buffer:
		if bv, ok := b.(Buffer); ok {
			return bytesEqual(av, bv)
		}
		// Inside the JSON fallback a VSBuffer is written as {buffer, byteLength}.
		if obj, ok := b.(Object); ok && inJSON {
			return bufferJSONViewMatches(av, obj)
		}
		return false
	case []Value:
		bv, ok := b.([]Value)
		if !ok || len(av) != len(bv) {
			return false
		}
		for i := range av {
			// An undefined element inside a JSON array is written as null by
			// JSON.stringify. Either side may therefore show Undefined or null for it.
			if IsUndefined(av[i]) || IsUndefined(bv[i]) {
				if !(IsUndefined(av[i]) || isJSONNull(av[i])) || !(IsUndefined(bv[i]) || isJSONNull(bv[i])) {
					return false
				}
				continue
			}
			if !valuesEqualIn(av[i], bv[i], inJSON) {
				return false
			}
		}
		return true
	case Object:
		bv, ok := b.(Object)
		if !ok {
			// A VSBuffer nested in the JSON fallback decodes to its own enumerable
			// properties, so the roles of the two sides are reversed here.
			if buf, isBuffer := b.(Buffer); isBuffer && inJSON {
				return bufferJSONViewMatches(buf, av)
			}
			return false
		}
		// JSON.stringify DROPS an object member whose value is undefined, so the decoded
		// object legitimately has fewer members than the input. Comparing the surviving
		// members only is the faithful expectation, not a tolerance for loss elsewhere.
		return objectSurvivesEqual(av, bv, inJSON)
	}
	return reflect.DeepEqual(a, b)
}

// objectSurvivesEqual compares a decoded object against an encoded one, honouring the two
// documented JS losses of the JSON fallback:
//
//   - JSON.stringify drops a member whose value is undefined, so the decoded object has
//     fewer keys than the input;
//   - every integer that fits in int32 is written by JSON.stringify as a bare number and
//     read back by the tag rule as tag 6 (int32), so its Go type changes while its value
//     does not.
//
// Every other member must survive unchanged.
func objectSurvivesEqual(decoded, encoded Object, inJSON bool) bool {
	if decoded.Len() > encoded.Len() {
		return false // the codec must never invent a member
	}
	used := make([]bool, encoded.Len())
	for i, key := range decoded.Keys {
		slot := -1
		for j, candidate := range encoded.Keys {
			if !used[j] && candidate == key {
				slot = j
				break
			}
		}
		if slot < 0 {
			return false
		}
		used[slot] = true
		if !valuesEqualIn(decoded.Values[i], encoded.Values[slot], inJSON) {
			return false
		}
	}
	for j := range encoded.Keys {
		if !used[j] && !IsUndefined(encoded.Values[j]) {
			return false // a defined member was dropped
		}
	}
	return true
}

// bufferJSONViewMatches checks the {buffer, byteLength} object JSON.stringify produces for
// a VSBuffer that is nested inside the JSON fallback instead of serialized on its own.
func bufferJSONViewMatches(buf Buffer, decoded Object) bool {
	if decoded.Len() != 2 {
		return false
	}
	data, ok := decoded.Get("buffer")
	if !ok {
		return false
	}
	raw, ok := data.([]byte)
	if !ok || !bytesEqual(buf, raw) {
		return false
	}
	length, ok := decoded.Get("byteLength")
	if !ok {
		return false
	}
	n, ok := numberAsInt32(json.Number(fmt.Sprintf("%v", length)))
	return ok && int(n) == len(buf)
}

// isNilPointer reports whether v is a typed nil pointer. Only the generator produces one:
// reflect cannot pass an untyped nil through reflect.Call, so a generated JSON null reaches
// the property as a nil *Value. Marshal writes `null` for it, exactly as for an untyped nil.
func isNilPointer(v Value) bool {
	if v == nil {
		return false
	}
	rv := reflect.ValueOf(v)
	return rv.Kind() == reflect.Pointer && rv.IsNil()
}

// isJSONNull reports whether v denotes the JSON null: either the untyped nil the decoder
// produces or the typed nil pointer the generator must use to get one through reflect.
func isJSONNull(v Value) bool { return v == nil || isNilPointer(v) }

func bytesEqual(a, b []byte) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// numberAsInt32 reports the int32 a json.Number takes when the tag rule chooses the Int
// branch for it, and whether it does.
func numberAsInt32(n json.Number) (int32, bool) {
	i, err := n.Int64()
	if err != nil {
		return 0, false
	}
	if i < math.MinInt32 || i > math.MaxInt32 {
		return 0, false
	}
	return int32(i), true
}

func mustUnmarshal(t *testing.T, data []byte) Value {
	t.Helper()
	value, err := Unmarshal(data)
	if err != nil {
		t.Fatalf("unmarshal %x: %v", data, err)
	}
	return value
}
