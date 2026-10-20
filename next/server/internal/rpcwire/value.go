package rpcwire

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strconv"
)

// Value is any value that can appear in a Channel RPC body.
//
// The concrete Go types map one-to-one onto the TypeScript serializer's data
// types (packages/rpc/src/serialization.ts:109-117):
//
//	Undefined{}        tag 0, JavaScript undefined
//	string             tag 1, UTF-8 encoded
//	Buffer{VS:false}   tag 2, a JavaScript Uint8Array
//	Buffer{VS:true}    tag 3, a VSBuffer
//	[]any              tag 4, array
//	json.RawMessage    tag 5, JSON fallback object (raw JSON text)
//	int/int64          tag 6, VQL integer (signed 32-bit range)
//
// A nil Value is treated as Undefined by the encoders.
type Value = any

// Undefined is the sentinel for JavaScript undefined (wire tag 0).
type Undefined struct{}

// String implements fmt.Stringer.
func (Undefined) String() string { return "undefined" }

// Buffer is a byte string. VS records which tag the bytes arrived with so a
// decode/encode round trip is byte-identical: tag 3 for VSBuffer, tag 2 for a
// plain Uint8Array.
type Buffer struct {
	Data []byte
	VS   bool
}

// Wire data type tags, from packages/rpc/src/serialization.ts:109-117.
const (
	tagUndefined byte = 0
	tagString    byte = 1
	tagBuffer    byte = 2
	tagVSBuffer  byte = 3
	tagArray     byte = 4
	tagObject    byte = 5
	tagInt       byte = 6
)

// ErrInvalidValue reports an unencodable Go value or a malformed wire value.
var ErrInvalidValue = errors.New("rpcwire: invalid value")

// maxValueNodes bounds the number of decoded values in one frame. A 64 MiB
// frame made only of tag-0 bytes would otherwise decode into ~64M interface
// slots (~1 GiB), letting one authenticated peer amplify a frame into a
// multi-gigabyte allocation. Real channel payloads are orders of magnitude
// smaller.
const maxValueNodes = 1 << 20

// EncodeValue serializes v using the TypeScript serializer's exact layout.
//
// For tag 6 the TypeScript side only takes the integer branch when
// (data | 0) === data (packages/rpc/src/serialization.ts:175), i.e. when the
// value fits a signed 32-bit integer. Values outside that range are emitted as
// tag 5 JSON numbers, exactly like the TypeScript fallback branch.
func EncodeValue(v Value) ([]byte, error) {
	var dst []byte
	return appendValue(dst, v)
}

func appendValue(dst []byte, v Value) ([]byte, error) {
	switch x := v.(type) {
	case nil, Undefined:
		return append(dst, tagUndefined), nil

	case string:
		dst = append(dst, tagString)
		dst = appendVQL(dst, uint32(len(x)))
		return append(dst, x...), nil

	case Buffer:
		if x.VS {
			dst = append(dst, tagVSBuffer)
		} else {
			dst = append(dst, tagBuffer)
		}
		dst = appendVQL(dst, uint32(len(x.Data)))
		return append(dst, x.Data...), nil

	case []byte:
		dst = append(dst, tagBuffer)
		dst = appendVQL(dst, uint32(len(x)))
		return append(dst, x...), nil

	case []any:
		dst = append(dst, tagArray)
		dst = appendVQL(dst, uint32(len(x)))
		for i, el := range x {
			var err error
			dst, err = appendValue(dst, el)
			if err != nil {
				return nil, fmt.Errorf("array element %d: %w", i, err)
			}
		}
		return dst, nil

	case json.RawMessage:
		if !json.Valid(x) {
			return nil, fmt.Errorf("%w: object payload is not valid JSON", ErrInvalidValue)
		}
		dst = append(dst, tagObject)
		dst = appendVQL(dst, uint32(len(x)))
		return append(dst, x...), nil

	case bool:
		text := "false"
		if x {
			text = "true"
		}
		return appendJSONFallback(dst, text)

	case int:
		return appendInteger(dst, int64(x))
	case int8:
		return appendInteger(dst, int64(x))
	case int16:
		return appendInteger(dst, int64(x))
	case int32:
		return appendInteger(dst, int64(x))
	case int64:
		return appendInteger(dst, x)
	case uint:
		return appendUnsigned(dst, uint64(x))
	case uint8:
		return appendUnsigned(dst, uint64(x))
	case uint16:
		return appendUnsigned(dst, uint64(x))
	case uint32:
		return appendUnsigned(dst, uint64(x))
	case uint64:
		return appendUnsigned(dst, x)
	}

	return nil, fmt.Errorf("%w: unsupported Go type %T", ErrInvalidValue, v)
}

func appendInteger(dst []byte, v int64) ([]byte, error) {
	if v >= math.MinInt32 && v <= math.MaxInt32 {
		dst = append(dst, tagInt)
		return appendVQL(dst, uint32(int32(v))), nil
	}
	return appendJSONFallback(dst, strconv.FormatInt(v, 10))
}

func appendUnsigned(dst []byte, v uint64) ([]byte, error) {
	if v <= math.MaxUint32 {
		if v <= math.MaxInt32 {
			dst = append(dst, tagInt)
			return appendVQL(dst, uint32(v)), nil
		}
		// Mirrors TypeScript: (v | 0) !== v, so the JSON fallback applies.
		return appendJSONFallback(dst, strconv.FormatUint(v, 10))
	}
	return appendJSONFallback(dst, strconv.FormatUint(v, 10))
}

func appendJSONFallback(dst []byte, jsonText string) ([]byte, error) {
	dst = append(dst, tagObject)
	dst = appendVQL(dst, uint32(len(jsonText)))
	return append(dst, jsonText...), nil
}

// DecodeValue parses one serialized value and errors if trailing bytes remain.
func DecodeValue(data []byte) (Value, error) {
	r := &valueReader{data: data}
	v, err := decodeValue(r)
	if err != nil {
		return nil, err
	}
	if r.remaining() != 0 {
		return nil, fmt.Errorf("%w: %d trailing bytes after value", ErrInvalidValue, r.remaining())
	}
	return v, nil
}

// DecodeValuePrefix parses one serialized value and returns the unconsumed
// suffix. Channel payloads contain exactly two concatenated values.
func DecodeValuePrefix(data []byte) (Value, []byte, error) {
	r := &valueReader{data: data}
	v, err := decodeValue(r)
	if err != nil {
		return nil, nil, err
	}
	return v, r.data[r.pos:], nil
}

func decodeValue(r *valueReader) (Value, error) {
	r.nodes++
	if r.nodes > maxValueNodes {
		return nil, fmt.Errorf("%w: more than %d values in one frame", ErrInvalidValue, maxValueNodes)
	}
	tag, err := r.byte()
	if err != nil {
		return nil, fmt.Errorf("reading value tag: %w", err)
	}

	switch tag {
	case tagUndefined:
		return Undefined{}, nil

	case tagString:
		n, err := r.vql()
		if err != nil {
			return nil, err
		}
		raw, err := r.take(int(n))
		if err != nil {
			return nil, err
		}
		// Bytes are preserved verbatim: TextDecoder in TypeScript would replace
		// malformed UTF-8, but keeping the raw bytes makes re-encoding exact.
		return string(raw), nil

	case tagBuffer, tagVSBuffer:
		n, err := r.vql()
		if err != nil {
			return nil, err
		}
		raw, err := r.take(int(n))
		if err != nil {
			return nil, err
		}
		return Buffer{Data: append([]byte(nil), raw...), VS: tag == tagVSBuffer}, nil

	case tagArray:
		n, err := r.vql()
		if err != nil {
			return nil, err
		}
		// Guard against absurd element counts before allocating.
		if int(n) > r.remaining() {
			return nil, fmt.Errorf("%w: array length %d exceeds remaining %d bytes", ErrInvalidValue, n, r.remaining())
		}
		// Each element needs at least one tag byte, so n <= remaining, but
		// []any costs 16 bytes per slot: pre-allocating n for a 64 MiB hostile
		// frame would reserve ~1 GiB. Grow instead; valid input still amortizes
		// to O(n).
		capacity := int(n)
		if capacity > 1024 {
			capacity = 1024
		}
		out := make([]any, 0, capacity)
		for i := uint32(0); i < n; i++ {
			el, err := decodeValue(r)
			if err != nil {
				return nil, fmt.Errorf("array element %d: %w", i, err)
			}
			out = append(out, el)
		}
		return out, nil

	case tagObject:
		n, err := r.vql()
		if err != nil {
			return nil, err
		}
		raw, err := r.take(int(n))
		if err != nil {
			return nil, err
		}
		if !json.Valid(raw) {
			return nil, fmt.Errorf("%w: tag 5 payload is not valid JSON", ErrInvalidValue)
		}
		return json.RawMessage(append([]byte(nil), raw...)), nil

	case tagInt:
		n, err := r.vql()
		if err != nil {
			return nil, err
		}
		// readIntVQL in TypeScript accumulates with 32-bit bitwise OR, so the
		// result is signed 32-bit. Values above 0x7fffffff decode negative.
		return int64(int32(n)), nil
	}

	return nil, fmt.Errorf("%w: unknown value tag %d", ErrInvalidValue, tag)
}

// appendVQL encodes v exactly like writeInt32VQL (packages/rpc/src/serialization.ts:84-103).
func appendVQL(dst []byte, v uint32) []byte {
	if v == 0 {
		return append(dst, 0)
	}
	for v != 0 {
		b := byte(v & 0x7f)
		v >>= 7
		if v > 0 {
			b |= 0x80
		}
		dst = append(dst, b)
	}
	return dst
}

type valueReader struct {
	data  []byte
	pos   int
	nodes int
}

func (r *valueReader) remaining() int { return len(r.data) - r.pos }

func (r *valueReader) byte() (byte, error) {
	if r.pos >= len(r.data) {
		return 0, fmt.Errorf("%w: unexpected end of value", ErrInvalidValue)
	}
	b := r.data[r.pos]
	r.pos++
	return b, nil
}

func (r *valueReader) take(n int) ([]byte, error) {
	if n < 0 || n > r.remaining() {
		return nil, fmt.Errorf("%w: need %d bytes, have %d", ErrInvalidValue, n, r.remaining())
	}
	out := r.data[r.pos : r.pos+n]
	r.pos += n
	return out, nil
}

// vql decodes one variable-length quantity. The TypeScript encoder emits at
// most five bytes for a 32-bit value; a longer run is rejected rather than
// silently wrapping.
func (r *valueReader) vql() (uint32, error) {
	var v uint32
	for shift := uint(0); ; shift += 7 {
		b, err := r.byte()
		if err != nil {
			return 0, fmt.Errorf("reading VQL: %w", err)
		}
		v |= uint32(b&0x7f) << shift
		if b&0x80 == 0 {
			return v, nil
		}
		if shift >= 28 {
			return 0, fmt.Errorf("%w: VQL integer longer than 5 bytes", ErrInvalidValue)
		}
	}
}
