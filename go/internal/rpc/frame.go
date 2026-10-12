package rpc

import "encoding/binary"

// frameHeaderSize is HEADER_SIZE from packages/rpc/src/protocol.ts.
const frameHeaderSize = 13

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

// DecodeFrame parses one complete frame. It returns (frame, true) on success,
// (zero, false) when more bytes are needed, and an error for a malformed header.
func DecodeFrame(buf []byte) (Frame, bool, error) {
	if len(buf) < frameHeaderSize {
		return Frame{}, false, nil
	}
	length := binary.BigEndian.Uint32(buf[9:])
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
