package modelclient

import "strings"

// textDecoder reproduces the WHATWG TextDecoder('utf-8') used by the SDK's
// TextDecoderStream, in streaming mode ({stream: true}). It buffers a partial
// multi-byte sequence at the end of a chunk and emits it once the remaining bytes
// arrive; on the final flush any incomplete sequence becomes U+FFFD. Invalid
// sequences are replaced with U+FFFD per the WHATWG replacement rules.
//
// This layer exists because the SDK pipes bytes through TextDecoderStream before
// the SSE parser. Decoding bytes-to-string at the SSE layer would (a) mangle
// multi-byte UTF-8 split across reads and (b) defeat the leading-BOM strip, which
// only sees the BOM when the whole 3-byte sequence is present in one decoded
// string.
type textDecoder struct {
	pending []byte
}

// decode converts chunk bytes to a string, holding back any incomplete trailing
// sequence. flush reports end-of-stream.
func (d *textDecoder) decode(chunk []byte, flush bool) string {
	var buf []byte
	if len(d.pending) > 0 {
		buf = append(d.pending, chunk...)
		d.pending = nil
	} else {
		buf = chunk
	}
	var out strings.Builder
	i := 0
	for i < len(buf) {
		b := buf[i]
		if b < 0x80 {
			out.WriteByte(b)
			i++
			continue
		}
		var size int
		switch {
		case b >= 0xC2 && b <= 0xDF:
			size = 2
		case b >= 0xE0 && b <= 0xEF:
			size = 3
		case b >= 0xF0 && b <= 0xF4:
			size = 4
		default:
			out.WriteRune('\uFFFD')
			i++
			continue
		}
		if i+size > len(buf) {
			if flush {
				out.WriteRune('\uFFFD')
				i = len(buf)
				continue
			}
			// hold the incomplete sequence for the next chunk
			d.pending = append(d.pending, buf[i:]...)
			break
		}
		seq := buf[i : i+size]
		if r, ok := decodeUTF8Seq(seq); ok {
			out.WriteRune(r)
		} else {
			out.WriteRune('\uFFFD')
		}
		i += size
	}
	return out.String()
}

// decodeUTF8Seq validates and decodes one full sequence, matching WHATWG UTF-8
// replacement (invalid → 0xFFFD, but we consume the whole size bytes as the
// decoder does for the common overlong/surrogate/range cases).
func decodeUTF8Seq(seq []byte) (rune, bool) {
	switch len(seq) {
	case 2:
		if seq[0] < 0xC2 || seq[0] > 0xDF || seq[1]&0xC0 != 0x80 {
			return 0, false
		}
		return rune(seq[0]&0x1F)<<6 | rune(seq[1]&0x3F), true
	case 3:
		if seq[0] == 0xE0 && seq[1] < 0xA0 {
			return 0, false
		}
		if seq[0] == 0xED && seq[1] > 0x9F {
			return 0, false // surrogates
		}
		if seq[1]&0xC0 != 0x80 || seq[2]&0xC0 != 0x80 {
			return 0, false
		}
		return rune(seq[0]&0x0F)<<12 | rune(seq[1]&0x3F)<<6 | rune(seq[2]&0x3F), true
	case 4:
		if seq[0] == 0xF0 && seq[1] < 0x90 {
			return 0, false
		}
		if seq[0] == 0xF4 && seq[1] > 0x8F {
			return 0, false
		}
		if seq[1]&0xC0 != 0x80 || seq[2]&0xC0 != 0x80 || seq[3]&0xC0 != 0x80 {
			return 0, false
		}
		return rune(seq[0]&0x07)<<18 | rune(seq[1]&0x3F)<<12 | rune(seq[2]&0x3F)<<6 | rune(seq[3]&0x3F), true
	}
	return 0, false
}

// pendingLen reports the bytes held back awaiting the rest of a sequence (tests).
func (d *textDecoder) pendingLen() int { return len(d.pending) }
