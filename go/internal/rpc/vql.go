package rpc

// VQL (Variable-Length Quantity) is the 7-bit/byte integer codec shared with VS Code.
// This is a byte-exact port of `readIntVQL` / `writeInt32VQL` in
// packages/rpc/src/serialization.ts. Where the JS is surprising — and it IS surprising —
// the JS behavior is reproduced deliberately, not corrected.
//
// The two JS quirks worth spelling out, because they look like bugs but are the wire
// format the existing UI speaks:
//
//   - writeInt32VQL computes the length with `v = v >>> 7`, a LOGICAL shift. For a negative
//     int32 the uint32 reinterpretation never reaches 0, so every negative number — even
//     -1 — is written as 5 bytes (ff ff ff ff 0f). Changing this would desynchronize Go
//     from Node on every negative Int.
//   - writeInt32VQL stores `value & 0x7f` into a Uint8Array element. In JS that truncates
//     to 8 bits, so only the low 7 bits survive; the writer then relies on the logical
//     shift for the rest. The Go port works on uint32 and masks identically.
//
// A value >= 2^28 is NOT an Int: the TS tag rule `(data | 0) === data` sends anything
// outside int32 (including 2**31) to the JSON fallback instead.

// appendInt32VQL appends the TypeScript VQL encoding of value.
func appendInt32VQL(dst []byte, value int32) []byte {
	if value == 0 {
		return append(dst, 0)
	}
	// The JS loop is `for (let v = value; v !== 0; v = v >>> 7)`, i.e. uint32 arithmetic.
	v := uint32(value)
	length := 0
	for probe := v; probe != 0; probe >>= 7 {
		length++
	}
	cur := v
	for i := 0; cur != 0; i++ {
		next := byte(cur & 0x7f)
		cur >>= 7
		if cur > 0 {
			next |= 0x80
		}
		dst = append(dst, next)
	}
	return dst
}

// readIntVQL reads a VQL integer.
//
// The TS reader is `value |= (byte & 0x7f) << n` with `value` a JS number, so every
// intermediate is a ToInt32 and the result is the signed reinterpretation of the low 32
// bits. Go reproduces that with uint32 accumulation and a final int32 conversion.
//
// The only deviation is the byte budget: the JS loop would spin forever on `0x80` repeated,
// so Limits.MaxVQLBytes (5 by default) bounds the continuation bits. Five bytes are enough
// for any uint32, so no well-formed encoding is rejected.
func (r *Reader) readIntVQL() (int32, error) {
	var value uint32
	for n := 0; ; n += 7 {
		next, err := r.readByte()
		if err != nil {
			return 0, err
		}
		// The 32-bit accumulator is full once the group starting at bit 28 has been read, so
		// a sixth group cannot contribute and is malformed input. The check runs AFTER the
		// read so a complete five-byte integer — which is what every negative int32 is —
		// is still accepted rather than rejected one byte early.
		if n >= r.limits.MaxVQLBytes*7 {
			return 0, ErrVQLTooLong
		}
		value |= uint32(next&0x7f) << n
		if next&0x80 == 0 {
			return int32(value), nil
		}
	}
}
