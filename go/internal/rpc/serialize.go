package rpc

import "fmt"

// This file is the port of `serialize` / `deserialize` from
// packages/rpc/src/serialization.ts. The TS is the specification; the recorded vectors in
// go/testdata/rpc-vectors.json are the byte-level contract and win over any Go judgement.

// Marshal returns the RPC encoding of v (see the Value doc for the closed type set).
func Marshal(v Value) ([]byte, error) {
	w := NewWriter()
	if err := w.Serialize(v); err != nil {
		return nil, err
	}
	return w.Bytes(), nil
}

// Unmarshal decodes one value from data, using DefaultLimits.
func Unmarshal(data []byte) (Value, error) {
	return NewReader(data).Deserialize()
}

// Writer accumulates serialized bytes. It mirrors BufferWriter.
type Writer struct {
	buf []byte
}

// NewWriter returns an empty writer.
func NewWriter() *Writer { return &Writer{} }

// Bytes returns the accumulated serialization.
func (w *Writer) Bytes() []byte { return w.buf }

// Serialize appends the RPC encoding of value.
//
// Branch order follows the TS exactly: undefined, string, VSBuffer, Uint8Array, Array,
// int32-valued number, JSON fallback.
func (w *Writer) Serialize(value Value) error {
	switch v := value.(type) {
	case undefinedValue:
		w.buf = append(w.buf, TypeUndefined)
		return nil
	case string:
		w.buf = append(w.buf, TypeString)
		w.buf = appendInt32VQL(w.buf, int32(len(v)))
		w.buf = append(w.buf, v...)
		return nil
	case Buffer:
		w.buf = append(w.buf, TypeVSBuffer)
		w.buf = appendInt32VQL(w.buf, int32(len(v)))
		w.buf = append(w.buf, v...)
		return nil
	case nullValue:
		return w.writeJSONRaw([]byte("null"))
	case []byte:
		w.buf = append(w.buf, TypeBuffer)
		w.buf = appendInt32VQL(w.buf, int32(len(v)))
		w.buf = append(w.buf, v...)
		return nil
	default:
		if n, ok := asNumber(value); ok {
			// TS takes the Int tag iff `(data | 0) === data`; every other number (a float,
			// 2^31, NaN, Infinity) falls through to the JSON fallback.
			if isInt32(n) {
				w.buf = append(w.buf, TypeInt)
				w.buf = appendInt32VQL(w.buf, int32(n))
				return nil
			}
			return w.writeJSONRaw(appendJSNumber(nil, n))
		}
		if items, ok := asValueSlice(value); ok {
			w.buf = append(w.buf, TypeArray)
			w.buf = appendInt32VQL(w.buf, int32(len(items)))
			for _, item := range items {
				if err := w.Serialize(item); err != nil {
					return err
				}
			}
			return nil
		}
	}
	return w.serializeJSONValue(value)
}

// serializeJSONValue writes the JSON fallback (tag 5), applying the nested-Uint8Array marker
// exactly as `JSON.stringify(data, encodeRpcJsonValue)` does.
func (w *Writer) serializeJSONValue(value Value) error {
	text, err := marshalJSONValue(value)
	if err != nil {
		return err
	}
	return w.writeJSONRaw(text)
}

func (w *Writer) writeJSONRaw(text []byte) error {
	w.buf = append(w.buf, TypeObject)
	w.buf = appendInt32VQL(w.buf, int32(len(text)))
	w.buf = append(w.buf, text...)
	return nil
}

// SerializeObjectValue encodes an ordered Object.
func (w *Writer) SerializeObjectValue(obj Object) error { return w.serializeJSONValue(obj) }

// asValueSlice views a []Value / []any / []string / []int32 / … as []Value. Because
// `type Value = any`, []Value and []any are the same type. The extra cases make []string and
// []int32 take the Array tag as JS would, instead of the JSON fallback. A byte slice is
// binary, not a list of numbers, and is handled by Serialize itself.
func asValueSlice(value Value) ([]Value, bool) {
	switch items := value.(type) {
	case []Value:
		return items, true
	case []string:
		out := make([]Value, len(items))
		for i, item := range items {
			out[i] = item
		}
		return out, true
	case []int32:
		out := make([]Value, len(items))
		for i, item := range items {
			out[i] = item
		}
		return out, true
	}
	return nil, false
}

// Reader consumes serialized bytes. It mirrors BufferReader plus the hardening Go needs:
// this decoder faces the network, so it never trusts a length prefix.
type Reader struct {
	buf    []byte
	pos    int
	limits Limits
	// prealloc caps how many elements are reserved up front for an array, so a 64 MiB
	// length prefix cannot turn into a 64 Mi-element allocation before the first element
	// has even been read.
	prealloc int
}

// NewReader wraps a serialized buffer with the default limits.
func NewReader(buf []byte) *Reader { return NewReaderWithLimits(buf, DefaultLimits()) }

// NewReaderWithLimits wraps a serialized buffer with explicit limits.
func NewReaderWithLimits(buf []byte, limits Limits) *Reader {
	return &Reader{buf: buf, limits: limits.withDefaults(), prealloc: defaultMaxPrealloc}
}

// Remaining reports how many bytes are left unread.
func (r *Reader) Remaining() int { return len(r.buf) - r.pos }

// Limits returns the limits this reader enforces.
func (r *Reader) Limits() Limits { return r.limits }

func (r *Reader) readByte() (byte, error) {
	if r.pos >= len(r.buf) {
		return 0, ErrTruncated
	}
	b := r.buf[r.pos]
	r.pos++
	return b, nil
}

// readLength reads a VQL length prefix and validates it against the limits AND against the
// bytes actually left in the buffer, before any allocation happens.
func (r *Reader) readLength() (int, error) {
	n, err := r.readIntVQL()
	if err != nil {
		return 0, err
	}
	// A negative int32 length (any 5-byte encoding with the top bit set) can never be
	// satisfied; reject it here so it can never reach make() or a slice bound.
	if n < 0 || int64(n) > int64(r.limits.MaxValueBytes) {
		return 0, fmt.Errorf("%w: %d", ErrValueTooLarge, n)
	}
	// Checked against what is left, not against the declared limit: this is what stops a
	// hostile 2^31 length prefix followed by three bytes from allocating anything.
	if int64(n) > int64(len(r.buf)-r.pos) {
		return 0, ErrTruncated
	}
	return int(n), nil
}

func (r *Reader) readN(n int) ([]byte, error) {
	if n < 0 || n > len(r.buf)-r.pos {
		return nil, ErrTruncated
	}
	out := r.buf[r.pos : r.pos+n]
	r.pos += n
	return out, nil
}

// Deserialize reads one value. The returned Value uses: Undefined for the Undefined tag,
// nil for JSON null, string, []byte (tag 2), Buffer (tag 3), []Value (tag 4), int32 (tag 6),
// and bool / json.Number / []any / Object (tag 5).
func (r *Reader) Deserialize() (Value, error) {
	return r.deserialize(r.limits.MaxDepth)
}

func (r *Reader) deserialize(depth int) (Value, error) {
	if depth <= 0 {
		return nil, ErrTooDeep
	}
	tag, err := r.readByte()
	if err != nil {
		return nil, err
	}
	switch tag {
	case TypeUndefined:
		return Undefined, nil
	case TypeString:
		n, err := r.readLength()
		if err != nil {
			return nil, err
		}
		raw, err := r.readN(n)
		if err != nil {
			return nil, err
		}
		return decodeUTF8(raw), nil
	case TypeBuffer:
		n, err := r.readLength()
		if err != nil {
			return nil, err
		}
		raw, err := r.readN(n)
		if err != nil {
			return nil, err
		}
		return cloneBytes(raw), nil
	case TypeVSBuffer:
		n, err := r.readLength()
		if err != nil {
			return nil, err
		}
		raw, err := r.readN(n)
		if err != nil {
			return nil, err
		}
		return Buffer(cloneBytes(raw)), nil
	case TypeArray:
		n, err := r.readLength()
		if err != nil {
			return nil, err
		}
		// Every element needs at least one byte, so the unread remainder bounds how many
		// can possibly follow; `prealloc` caps the up-front reservation on top of that.
		capacity := n
		if remaining := len(r.buf) - r.pos; capacity > remaining {
			capacity = remaining
		}
		if capacity > r.prealloc {
			capacity = r.prealloc
		}
		out := make([]Value, 0, capacity)
		for i := 0; i < n; i++ {
			item, err := r.deserialize(depth - 1)
			if err != nil {
				return nil, err
			}
			out = append(out, item)
		}
		return out, nil
	case TypeObject:
		n, err := r.readLength()
		if err != nil {
			return nil, err
		}
		raw, err := r.readN(n)
		if err != nil {
			return nil, err
		}
		return decodeObjectJSON(raw, r.limits.MaxDepth, r.prealloc)
	case TypeInt:
		return r.readIntVQL()
	default:
		return nil, fmt.Errorf("%w: %d", ErrUnknownTag, tag)
	}
}

// cloneBytes copies a sub-slice out of the reader's buffer, matching the TS
// `reader.read(n).buffer`, which always yields a fresh Uint8Array.
func cloneBytes(raw []byte) []byte {
	out := make([]byte, len(raw))
	copy(out, raw)
	return out
}
