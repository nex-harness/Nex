package rpc

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
)

// FrameHeaderSize is HEADER_SIZE from packages/rpc/src/protocol.ts, exported: a caller that must
// inspect a frame WITHOUT decoding it — the gateway router of step 2.8, which forwards the
// client's bytes verbatim — still needs to know where the payload begins.
const FrameHeaderSize = frameHeaderSize

// frameHeaderSize is HEADER_SIZE from packages/rpc/src/protocol.ts.
const frameHeaderSize = 13

// DefaultMaxFrameBytes bounds the body of a single frame. WebSocket binary messages
// are bounded by the transport in practice, but this package is also fed by
// TCP/stdio streams later (step 2.4) and by the network in general, so a peer must
// not be able to make the reader allocate an arbitrary amount of memory.
const DefaultMaxFrameBytes = 64 << 20 // 64 MiB

// ErrFrameTooLong is returned when a frame header declares a body larger than the
// reader's limit. The stream cannot be resynchronised after it (the bytes of that
// body may never arrive and the next header offset is unknown), so the connection
// must be closed — see MessageReader.Read.
var ErrFrameTooLong = errors.New("rpc: frame exceeds maximum length")

// Frame is a ProtocolMessage: a 13-byte header plus a body.
type Frame struct {
	Type uint8
	ID   uint32
	Ack  uint32
	Data []byte
}

// ProtocolMessageType mirrors packages/rpc/src/protocol.ts.
const (
	MsgNone          uint8 = 0
	MsgRegular       uint8 = 1
	MsgControl       uint8 = 2
	MsgAck           uint8 = 3
	MsgDisconnect    uint8 = 5
	MsgReplayRequest uint8 = 6
	MsgPause         uint8 = 7
	MsgResume        uint8 = 8
	MsgKeepAlive     uint8 = 9
)

// Socket is the transport the framing layer sits on. It is message-oriented to
// match WebSocket binary messages, but the reader deliberately treats it as a
// byte stream (see MessageReader): a frame may span several Read results or share
// one with its neighbours. net imports stay out of this package so it remains
// transport-agnostic (gate G2.11).
type Socket interface {
	Read(ctx context.Context) ([]byte, error)
	Write(ctx context.Context, data []byte) error
	Close() error
}

// EncodeFrame writes the 13-byte big-endian header followed by the body.
func EncodeFrame(f Frame) []byte {
	out := make([]byte, frameHeaderSize+len(f.Data))
	out[0] = f.Type
	binary.BigEndian.PutUint32(out[1:], f.ID)
	binary.BigEndian.PutUint32(out[5:], f.Ack)
	binary.BigEndian.PutUint32(out[9:], uint32(len(f.Data)))
	copy(out[frameHeaderSize:], f.Data)
	return out
}

// DecodeFrame parses one complete frame using DefaultMaxFrameBytes. It returns
// (frame, true) on success, (zero, false) when more bytes are needed, and an error
// for a malformed header or an over-limit length.
func DecodeFrame(buf []byte) (Frame, bool, error) {
	return DecodeFrameLimit(buf, DefaultMaxFrameBytes)
}

// DecodeFrameLimit is DecodeFrame with an explicit per-frame body limit. A
// non-positive max means DefaultMaxFrameBytes. The length check happens before any
// allocation, so an over-limit header costs nothing.
func DecodeFrameLimit(buf []byte, max int) (Frame, bool, error) {
	if max <= 0 {
		max = DefaultMaxFrameBytes
	}
	if len(buf) < frameHeaderSize {
		return Frame{}, false, nil
	}
	length := binary.BigEndian.Uint32(buf[9:])
	if length > uint32(max) {
		return Frame{}, false, fmt.Errorf("%w: %d bytes (limit %d)", ErrFrameTooLong, length, max)
	}
	total := frameHeaderSize + int(length)
	if len(buf) < total {
		return Frame{}, false, nil
	}
	f := Frame{
		Type: buf[0],
		ID:   binary.BigEndian.Uint32(buf[1:]),
		Ack:  binary.BigEndian.Uint32(buf[5:]),
	}
	if length > 0 {
		f.Data = make([]byte, length)
		copy(f.Data, buf[frameHeaderSize:total])
	}
	return f, true, nil
}

// MessageReader reassembles frames from a byte stream, mirroring SocketProtocol +
// ChunkStream in packages/rpc/src/protocol.ts.
//
// Semantics taken verbatim from the TypeScript source:
//
//   - The 13-byte header is peeked, not consumed, until the whole frame is present.
//     Consuming the header early loses the length information for a body that
//     arrives in a later chunk, and the caller's promise then never resolves.
//   - Only MsgRegular produces a message. Every other type is read — its body
//     bytes are consumed so the stream stays aligned — and then ignored.
//   - A zero-length MsgRegular is a valid, empty message.
//
// It is not safe for concurrent use; exactly one goroutine may call Read.
type MessageReader struct {
	sock Socket
	max  int

	// buf holds bytes that have been read from the socket but not yet consumed by a
	// frame. A single flat buffer is used instead of a chunk list: frames are
	// appended in order and consumed from the front, so compaction is just a copy
	// of the (small) unconsumed remainder.
	buf []byte

	// pendingErr is a socket error returned together with data. It is held until the
	// buffered bytes have been decoded, because a socket may legitimately return
	// (chunk, err) and the chunk is still valid input.
	pendingErr error
}

// NewMessageReader wraps a socket. A non-positive maxFrameBytes selects
// DefaultMaxFrameBytes. The socket is closed when the reader hits an unrecoverable
// protocol error, because the byte stream cannot be resynchronised.
func NewMessageReader(sock Socket, maxFrameBytes int) *MessageReader {
	if maxFrameBytes <= 0 {
		maxFrameBytes = DefaultMaxFrameBytes
	}
	return &MessageReader{sock: sock, max: maxFrameBytes}
}

// MaxFrameBytes reports the configured per-frame limit.
func (r *MessageReader) MaxFrameBytes() int { return r.max }

// Buffered reports how many bytes have been read from the socket but not yet
// consumed by a completed frame. It is a diagnostic aid for tests.
func (r *MessageReader) Buffered() int { return len(r.buf) }

// Read returns the next MsgRegular payload, skipping every other frame type.
//
// It blocks until a complete Regular frame is available, the socket returns an
// error, or ctx is done. ctx cancellation only applies while waiting on the
// socket; bytes already buffered are still decoded, so a frame that is already
// complete is never lost because the context was cancelled a moment earlier.
//
// Errors:
//   - ErrFrameTooLong: the declared length is over the limit, or ctx is done (both
//     leave the stream unusable). The socket is closed before returning.
//   - any error from Socket.Read, unwrapped, so callers can use errors.Is on
//     io.EOF or the transport's own sentinel.
func (r *MessageReader) Read(ctx context.Context) ([]byte, error) {
	for {
		frame, err := r.nextFrame(ctx)
		if err != nil {
			return nil, err
		}
		if frame.Type != MsgRegular {
			// Non-Regular types (Ack, KeepAlive, Control, …) carry no channel payload
			// here; the body has already been consumed, so the stream stays aligned.
			continue
		}
		return frame.Data, nil
	}
}

// ReadFrame is Read, but it returns the frame WITH its 13-byte ProtocolMessage header instead of
// just the payload, and it does not skip non-Regular types — the caller decides.
//
// It exists for the gateway (step 2.8), which forwards a client's frame to the upstream as the
// client's own bytes. Read hands back only the payload, and rebuilding a header from it would
// reconstruct the id and ack fields rather than preserve the ones the client actually sent.
//
// The returned bytes are freshly allocated, so a caller may keep them past the next Read.
// MessageReader's contract is unchanged: exactly one goroutine may read from it.
func (r *MessageReader) ReadFrame(ctx context.Context) ([]byte, error) {
	frame, err := r.nextFrame(ctx)
	if err != nil {
		return nil, err
	}
	return encodeFrame(frame), nil
}

// encodeFrame rebuilds the on-wire bytes of a frame: the 13-byte header followed by the body.
//
// The header fields are recoverable from the Frame the reader produced — it copied them out before
// consuming the buffer — so this reproduces exactly what the peer sent rather than re-encoding the
// payload. The ProtocolMessage id and ack are always 0 on the WebSocket paths (SocketProtocol.send
// hardcodes them), and they are carried through unchanged so a frame from a protocol that uses them
// survives the round trip.
func encodeFrame(f Frame) []byte {
	out := make([]byte, frameHeaderSize+len(f.Data))
	out[0] = f.Type
	binary.BigEndian.PutUint32(out[1:], f.ID)
	binary.BigEndian.PutUint32(out[5:], f.Ack)
	binary.BigEndian.PutUint32(out[9:], uint32(len(f.Data)))
	copy(out[frameHeaderSize:], f.Data)
	return out
}

// nextFrame returns the next complete frame of any type, reading from the socket as
// needed. A zero-length frame has a nil Data.
func (r *MessageReader) nextFrame(ctx context.Context) (Frame, error) {
	for {
		frame, ok, err := DecodeFrameLimit(r.buf, r.max)
		if err != nil {
			r.close()
			return Frame{}, err
		}
		if ok {
			total := frameHeaderSize + len(frame.Data)
			r.consume(total)
			return frame, nil
		}

		// DecodeFrameLimit reports an over-limit declared length as an error even
		// when the body has not arrived, so a hostile header cannot make the reader
		// wait for a body that will never be accepted.
		if r.pendingErr != nil {
			err := r.pendingErr
			r.pendingErr = nil
			return Frame{}, err
		}
		if err := r.fill(ctx); err != nil {
			return Frame{}, err
		}
	}
}

// consume drops the first n consumed bytes, compacting so that a long stream does
// not keep re-slicing forward into an ever-growing allocation.
func (r *MessageReader) consume(n int) {
	if n >= len(r.buf) {
		r.buf = r.buf[:0]
		return
	}
	rest := len(r.buf) - n
	copy(r.buf, r.buf[n:])
	r.buf = r.buf[:rest]
}

// fill performs one Socket.Read, appending to the buffer. An error returned
// alongside data is deferred in pendingErr so the data is decoded first.
func (r *MessageReader) fill(ctx context.Context) error {
	chunk, err := r.sock.Read(ctx)
	if len(chunk) > 0 {
		r.buf = append(r.buf, chunk...)
	}
	if err != nil && len(chunk) > 0 {
		r.pendingErr = err
		return nil
	}
	return err
}

// close tears the socket down after an unrecoverable protocol error. A close
// failure cannot be reported on top of the original error and is ignored.
func (r *MessageReader) close() {
	_ = r.sock.Close()
}
