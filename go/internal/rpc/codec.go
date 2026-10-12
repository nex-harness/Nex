// Package rpc ports the UI<->server RPC wire format from packages/rpc.
//
// Two layers matter for byte compatibility:
//
//	serialization.go (TS): a tagged, VQL-length-prefixed value encoding.
//	protocol.ts      (TS): a 13-byte frame header (type/id/ack/length, big-endian).
//
// The Go port is a straight transliteration: the TypeScript is the specification, and
// go/internal/rpc/rpc_test.go checks byte-for-byte equality against vectors produced by
// the real serialize()/deserialize().
package rpc

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
)

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

// nestedKey marks a Uint8Array embedded inside an object's JSON fallback, exactly as
// RPC_NESTED_UINT8_ARRAY_MARKER in the TypeScript codec.
const (
	nestedMarkerKey = "__nex_rpc_nested_uint8array_v1"
	nestedBase64Key = "base64"
)

// ErrTruncated is returned when the reader runs out of bytes mid-value.
var ErrTruncated = errors.New("rpc: truncated value")

// writeVQL appends the TypeScript VQL encoding of a 32-bit integer.
//
// The TS loop is `for (v = value; v !== 0; v = v >>> 7)`. `>>>` is a LOGICAL shift, so a
// negative value never reaches 0 in 32 bits: the loop runs until the value is 0 *as a
// uint32*, which for a negative int32 always takes 5 bytes (indices 0..4). Length
// computation and encoding therefore both operate on uint32.
func (w *Writer) writeInt32VQL(value int32) {
	if value == 0 {
		w.buf = append(w.buf, 0)
		return
	}
	v := uint32(value)
	length := 0
	for probe := v; probe != 0; probe >>= 7 {
		length++
	}
	scratch := make([]byte, length)
	cur := v
	for i := 0; cur != 0; i++ {
		scratch[i] = byte(cur & 0x7f)
		cur >>= 7
		if cur > 0 {
			scratch[i] |= 0x80
		}
	}
	w.buf = append(w.buf, scratch...)
}

// readIntVQL reads a VQL integer. The TS reader accumulates with `value |= (byte &
// 0x7f) << n` where `value` is a signed 32-bit JS number, so the result is truncated to
// 32 bits and reinterpreted as signed. Go reproduces this with uint32 accumulation and a
// final int32 conversion.
func (r *Reader) readIntVQL() (int32, error) {
	var value uint32
	for n := 0; ; n += 7 {
		next, err := r.readByte()
		if err != nil {
			return 0, err
		}
		value |= uint32(next&0x7f) << n
		if next&0x80 == 0 {
			return int32(value), nil
		}
		if n >= 28 {
			// The TS loop would keep reading on malformed input; cap at 5 groups (32 bits)
			// to avoid an unbounded loop while matching all valid encodings.
			return int32(value), nil
		}
	}
}

// Writer accumulates serialized bytes. It mirrors BufferWriter.
type Writer struct {
	buf []byte
}

// NewWriter returns an empty writer.
func NewWriter() *Writer { return &Writer{} }

// Bytes returns the accumulated serialization.
func (w *Writer) Bytes() []byte { return w.buf }

// Serialize appends the RPC encoding of value, which must be one of:
// nil (JSON null), bool, string, int/int32/int64/float64, []byte, []any, or
// map[string]any / json.RawMessage. The TypeScript original is dynamically typed;
// callers convert protocol values to these Go shapes first.
func (w *Writer) Serialize(value any) error {
	switch v := value.(type) {
	case nil:
		// JSON null goes through the object branch in TS.
		return w.serializeJSON("null")
	case undefinedValue:
		w.buf = append(w.buf, TypeUndefined)
		return nil
	case string:
		w.buf = append(w.buf, TypeString)
		w.writeInt32VQL(int32(len(v)))
		w.buf = append(w.buf, v...)
		return nil
	case []byte:
		w.buf = append(w.buf, TypeBuffer)
		w.writeInt32VQL(int32(len(v)))
		w.buf = append(w.buf, v...)
		return nil
	case bool:
		if v {
			return w.serializeJSON("true")
		}
		return w.serializeJSON("false")
	case []any:
		w.buf = append(w.buf, TypeArray)
		w.writeInt32VQL(int32(len(v)))
		for _, item := range v {
			if err := w.Serialize(item); err != nil {
				return err
			}
		}
		return nil
	case json.RawMessage:
		w.buf = append(w.buf, TypeObject)
		w.writeInt32VQL(int32(len(v)))
		w.buf = append(w.buf, v...)
		return nil
	default:
		return w.serializeNumberOrObject(v)
	}
}

// undefinedValue marks an explicit TS `undefined` argument.
type undefinedValue struct{}

// Undefined is the sentinel that serializes to the TS Undefined tag.
var Undefined = undefinedValue{}

func (w *Writer) serializeNumberOrObject(value any) error {
	if n, ok := asNumber(value); ok {
		// TS uses the Int tag only when `(data | 0) === data`, i.e. a 32-bit integer.
		if isInt32(n) {
			w.buf = append(w.buf, TypeInt)
			w.writeInt32VQL(int32(n))
			return nil
		}
		// Non-int numbers fall through to the JSON fallback (NaN/Infinity -> null).
		return w.serializeJSON(numberToJSON(n))
	}
	if obj, ok := value.(map[string]any); ok {
		return w.serializeObject(obj)
	}
	if obj, ok := value.(Object); ok {
		return w.serializeOrderedObject(obj)
	}
	// Unknown Go type: encode as JSON to stay total rather than panic.
	return w.serializeJSONValue(value)
}

// SerializeObjectValue encodes a Go map that may contain []byte values, applying the
// nested-Uint8Array marker exactly as the TS `encodeRpcJsonValue` replacer does.
func (w *Writer) SerializeObjectValue(obj map[string]any) error {
	return w.serializeObject(obj)
}

func (w *Writer) serializeObject(obj map[string]any) error {
	buf, err := marshalWithMarker(obj)
	if err != nil {
		return err
	}
	w.buf = append(w.buf, TypeObject)
	w.writeInt32VQL(int32(len(buf)))
	w.buf = append(w.buf, buf...)
	return nil
}

func (w *Writer) serializeOrderedObject(obj Object) error {
	buf, err := marshalWithMarker(obj)
	if err != nil {
		return err
	}
	w.buf = append(w.buf, TypeObject)
	w.writeInt32VQL(int32(len(buf)))
	w.buf = append(w.buf, buf...)
	return nil
}

func (w *Writer) serializeJSONValue(value any) error {
	buf, err := json.Marshal(value)
	if err != nil {
		return err
	}
	w.buf = append(w.buf, TypeObject)
	w.writeInt32VQL(int32(len(buf)))
	w.buf = append(w.buf, buf...)
	return nil
}

func (w *Writer) serializeJSON(raw string) error {
	w.buf = append(w.buf, TypeObject)
	w.writeInt32VQL(int32(len(raw)))
	w.buf = append(w.buf, raw...)
	return nil
}

// Reader consumes serialized bytes. It mirrors BufferReader.
type Reader struct {
	buf []byte
	pos int
}

// NewReader wraps a serialized buffer.
func NewReader(buf []byte) *Reader { return &Reader{buf: buf} }

// Remaining reports how many bytes are left unread.
func (r *Reader) Remaining() int { return len(r.buf) - r.pos }

func (r *Reader) readByte() (byte, error) {
	if r.pos >= len(r.buf) {
		return 0, ErrTruncated
	}
	b := r.buf[r.pos]
	r.pos++
	return b, nil
}

func (r *Reader) readN(n int) ([]byte, error) {
	if n < 0 || r.pos+n > len(r.buf) {
		return nil, ErrTruncated
	}
	out := r.buf[r.pos : r.pos+n]
	r.pos += n
	return out, nil
}

// Deserialize reads one value. The returned value uses: nil for null/undefined, bool,
// string, int32 for Int, []byte for Buffer, []any for Array, and map[string]any (with
// nested markers already decoded) for Object.
func (r *Reader) Deserialize() (any, error) {
	tag, err := r.readByte()
	if err != nil {
		return nil, err
	}
	switch tag {
	case TypeUndefined:
		return nil, nil
	case TypeString:
		n, err := r.readIntVQL()
		if err != nil {
			return nil, err
		}
		raw, err := r.readN(int(n))
		if err != nil {
			return nil, err
		}
		return string(raw), nil
	case TypeBuffer:
		n, err := r.readIntVQL()
		if err != nil {
			return nil, err
		}
		raw, err := r.readN(int(n))
		if err != nil {
			return nil, err
		}
		out := make([]byte, len(raw))
		copy(out, raw)
		return out, nil
	case TypeVSBuffer:
		n, err := r.readIntVQL()
		if err != nil {
			return nil, err
		}
		raw, err := r.readN(int(n))
		if err != nil {
			return nil, err
		}
		out := make([]byte, len(raw))
		copy(out, raw)
		return out, nil
	case TypeArray:
		n, err := r.readIntVQL()
		if err != nil {
			return nil, err
		}
		out := make([]any, 0, int(n))
		for i := int32(0); i < n; i++ {
			item, err := r.Deserialize()
			if err != nil {
				return nil, err
			}
			out = append(out, item)
		}
		return out, nil
	case TypeObject:
		n, err := r.readIntVQL()
		if err != nil {
			return nil, err
		}
		raw, err := r.readN(int(n))
		if err != nil {
			return nil, err
		}
		return decodeObjectJSON(raw)
	case TypeInt:
		return r.readIntVQL()
	default:
		return nil, fmt.Errorf("rpc: unknown data type tag %d", tag)
	}
}

// asNumber converts the numeric Go types a caller may pass.
func asNumber(value any) (float64, bool) {
	switch n := value.(type) {
	case int:
		return float64(n), true
	case int32:
		return float64(n), true
	case int64:
		return float64(n), true
	case float32:
		return float64(n), true
	case float64:
		return n, true
	}
	return 0, false
}

// isInt32 mirrors `(data | 0) === data`. Negative int32 values qualify (JS `|0` keeps
// them), so ints down to -2^31 use the Int tag.
func isInt32(n float64) bool {
	if math.IsNaN(n) || math.IsInf(n, 0) {
		return false
	}
	if n != math.Trunc(n) {
		return false
	}
	return n >= math.MinInt32 && n <= math.MaxInt32
}

// numberToJSON renders a non-int number as JS JSON.stringify would (NaN/Infinity -> null).
func numberToJSON(n float64) string {
	if math.IsNaN(n) || math.IsInf(n, 0) {
		return "null"
	}
	buf, _ := json.Marshal(n)
	return string(buf)
}
