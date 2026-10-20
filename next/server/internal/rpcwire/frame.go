// Package rpcwire implements the binary wire format spoken by the Nex web UI's
// Channel RPC layer: a 13-byte frame header wrapping a two-value payload
// (header array + body) encoded with the VS Code style type-tag serializer.
//
// The format is derived from the TypeScript implementation in
// packages/rpc/src/protocol.ts and packages/rpc/src/serialization.ts.
package rpcwire

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"
)

// HeaderSize is the fixed frame header size: type(1) + id(4) + ack(4) + length(4).
//
// Source: packages/rpc/src/protocol.ts:207 (`export const HEADER_SIZE = 13`).
const HeaderSize = 13

// MessageType identifies the kind of a protocol frame.
//
// Values mirror ProtocolMessageType in packages/rpc/src/protocol.ts:195-205.
type MessageType uint8

// Protocol frame types. The browser path (SocketProtocol) only ever sends
// Regular frames; the remaining values exist because the TS enum defines them
// and because a frame header may carry any of them.
const (
	MessageNone          MessageType = 0
	MessageRegular       MessageType = 1
	MessageControl       MessageType = 2
	MessageAck           MessageType = 3
	MessageDisconnect    MessageType = 5
	MessageReplayRequest MessageType = 6
	MessagePause         MessageType = 7
	MessageResume        MessageType = 8
	MessageKeepAlive     MessageType = 9
)

// String returns the TypeScript enum member name, or a numeric fallback.
func (t MessageType) String() string {
	switch t {
	case MessageNone:
		return "None"
	case MessageRegular:
		return "Regular"
	case MessageControl:
		return "Control"
	case MessageAck:
		return "Ack"
	case MessageDisconnect:
		return "Disconnect"
	case MessageReplayRequest:
		return "ReplayRequest"
	case MessagePause:
		return "Pause"
	case MessageResume:
		return "Resume"
	case MessageKeepAlive:
		return "KeepAlive"
	default:
		return fmt.Sprintf("MessageType(%d)", uint8(t))
	}
}

// Frame is one protocol message: a 13-byte header plus an opaque payload.
//
// ID and Ack are carried verbatim because the wire format requires them, but
// SocketProtocol never assigns them meaning: send() always writes 0/0
// (packages/rpc/src/protocol.ts:252-254) and readMessages() discards both
// (packages/rpc/src/protocol.ts:267-270).
type Frame struct {
	Type    MessageType
	ID      uint32
	Ack     uint32
	Payload []byte
}

// EncodeFrame serializes f into the exact bytes writeProtocolMessage() emits
// (packages/rpc/src/protocol.ts:222-230): big-endian uint32 ID, Ack and length.
//
// EncodeFrame allocates a fresh slice; the returned bytes do not alias f.Payload.
func EncodeFrame(f Frame) []byte {
	out := make([]byte, HeaderSize+len(f.Payload))
	out[0] = byte(f.Type)
	binary.BigEndian.PutUint32(out[1:5], f.ID)
	binary.BigEndian.PutUint32(out[5:9], f.Ack)
	binary.BigEndian.PutUint32(out[9:13], uint32(len(f.Payload)))
	copy(out[HeaderSize:], f.Payload)
	return out
}

// ErrShortFrame is returned when a frame payload is longer than the bytes
// available from the underlying reader.
var ErrShortFrame = errors.New("rpcwire: truncated frame payload")

// ErrPayloadTooLarge is returned when a frame declares a payload size that
// exceeds the reader's configured limit.
var ErrPayloadTooLarge = errors.New("rpcwire: frame payload exceeds limit")

// DefaultMaxPayload is the per-frame payload limit applied by NewFrameReader
// when maxPayload <= 0. The browser and server both run inside Node/Electron
// with a 4 GiB uint32 length field, so a 64 MiB ceiling is comfortably above
// real traffic while still bounding allocation from a hostile peer.
const DefaultMaxPayload = 64 << 20

// FrameReader incrementally decodes frames from a byte stream.
//
// A WebSocket binary message is not required to contain exactly one frame.
// The browser client hands each WS message to VSBuffer.wrap and feeds it into
// ChunkStream (packages/client/src/websocket.ts:29-31), while the server does
// the same in packages/server/src/http.ts:53-56. ChunkStream then peeks a
// 13-byte header and only consumes it once the whole body is buffered
// (packages/rpc/src/protocol.ts:260-296). Nothing in either direction splits or
// concatenates frames at the WS layer, but the receiving ChunkStream tolerates
// both, so a peer is free to put several frames in one WS message or split one
// frame across messages. FrameReader models exactly that: it accepts arbitrary
// byte chunks and yields whole frames.
type FrameReader struct {
	r          io.Reader
	maxPayload uint32
	header     [HeaderSize]byte
}

// NewFrameReader returns a FrameReader reading from r.
// A maxPayload <= 0 selects DefaultMaxPayload.
func NewFrameReader(r io.Reader, maxPayload int) *FrameReader {
	if maxPayload <= 0 {
		maxPayload = DefaultMaxPayload
	}
	return &FrameReader{r: r, maxPayload: uint32(maxPayload)}
}

// Next reads the next complete frame. It blocks until a full header and body
// have been read. io.EOF is returned only when the stream ends on a frame
// boundary; a stream that ends mid-frame yields io.ErrUnexpectedEOF (or
// ErrShortFrame) so callers never mistake a truncated frame for a clean close.
func (fr *FrameReader) Next() (Frame, error) {
	if _, err := io.ReadFull(fr.r, fr.header[:]); err != nil {
		if errors.Is(err, io.EOF) {
			return Frame{}, io.EOF
		}
		return Frame{}, fmt.Errorf("rpcwire: reading frame header: %w", err)
	}

	length := binary.BigEndian.Uint32(fr.header[9:13])
	if length > fr.maxPayload {
		return Frame{}, fmt.Errorf("%w: %d > %d", ErrPayloadTooLarge, length, fr.maxPayload)
	}

	payload := make([]byte, length)
	if _, err := io.ReadFull(fr.r, payload); err != nil {
		if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
			return Frame{}, fmt.Errorf("%w: want %d bytes: %w", ErrShortFrame, length, io.ErrUnexpectedEOF)
		}
		return Frame{}, fmt.Errorf("rpcwire: reading frame payload: %w", err)
	}

	return Frame{
		Type:    MessageType(fr.header[0]),
		ID:      binary.BigEndian.Uint32(fr.header[1:5]),
		Ack:     binary.BigEndian.Uint32(fr.header[5:9]),
		Payload: payload,
	}, nil
}

// ReadFrame decodes exactly one frame from r and reports whether trailing
// bytes remain. It is a convenience wrapper for callers that already hold a
// complete WS message. Any bytes after the first frame are returned in rest so
// a concatenated message can be drained with another call.
func ReadFrame(data []byte, maxPayload int) (Frame, []byte, error) {
	br := NewFrameReader(&byteSliceReader{data: data}, maxPayload)
	f, err := br.Next()
	if err != nil {
		if errors.Is(err, io.EOF) {
			return Frame{}, nil, io.ErrUnexpectedEOF
		}
		return Frame{}, nil, err
	}
	return f, data[HeaderSize+len(f.Payload):], nil
}

type byteSliceReader struct{ data []byte }

func (r *byteSliceReader) Read(p []byte) (int, error) {
	if len(r.data) == 0 {
		return 0, io.EOF
	}
	n := copy(p, r.data)
	r.data = r.data[n:]
	return n, nil
}
