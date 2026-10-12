package rpc

import (
	"bytes"
	"testing"
)

// TestFrameRoundTrip checks the 13-byte header layout from packages/rpc/src/protocol.ts:
// type(1) | id(4 BE) | ack(4 BE) | length(4 BE), followed by the body.
func TestFrameRoundTrip(t *testing.T) {
	cases := []Frame{
		{Type: MsgRegular, ID: 0, Ack: 0, Data: []byte{1, 2, 3}},
		{Type: MsgRegular, ID: 0x01020304, Ack: 0xdeadbeef, Data: nil},
		{Type: MsgKeepAlive, ID: 1, Ack: 2, Data: bytes.Repeat([]byte{0xab}, 300)},
	}
	for _, in := range cases {
		encoded := EncodeFrame(in)
		if len(encoded) != frameHeaderSize+len(in.Data) {
			t.Fatalf("frame %+v: encoded length %d", in, len(encoded))
		}
		// Header fields are big-endian at fixed offsets.
		if encoded[0] != in.Type {
			t.Errorf("type byte: got %d want %d", encoded[0], in.Type)
		}
		out, ok, err := DecodeFrame(encoded)
		if err != nil || !ok {
			t.Fatalf("frame %+v: decode ok=%v err=%v", in, ok, err)
		}
		if out.Type != in.Type || out.ID != in.ID || out.Ack != in.Ack {
			t.Errorf("frame header changed: %+v -> %+v", in, out)
		}
		if !bytes.Equal(out.Data, in.Data) {
			t.Errorf("frame body changed: %v -> %v", in.Data, out.Data)
		}
	}
}

// TestDecodeFrameNeedsMoreBytes checks the incremental-read contract used by SocketProtocol:
// a partial header or body reports ok=false rather than an error.
func TestDecodeFrameNeedsMoreBytes(t *testing.T) {
	full := EncodeFrame(Frame{Type: MsgRegular, ID: 7, Ack: 0, Data: []byte("hello")})
	for _, n := range []int{0, 5, 12, 13, 14, len(full) - 1} {
		_, ok, err := DecodeFrame(full[:n])
		if err != nil {
			t.Fatalf("prefix %d: unexpected error %v", n, err)
		}
		if ok {
			t.Fatalf("prefix %d: decoded a frame from incomplete bytes", n)
		}
	}
	_, ok, err := DecodeFrame(full)
	if err != nil || !ok {
		t.Fatalf("full frame: ok=%v err=%v", ok, err)
	}
}

// TestRequestHeaderRoundTrip checks the channel request header [type, id, channel, name]
// plus body, as ChannelClient/ChannelServer serialize them.
func TestRequestHeaderRoundTrip(t *testing.T) {
	header := RequestHeader{Type: RequestPromise, ID: 42, ChannelName: "sessions", Name: "list"}
	body := []any{int32(1), "x"}
	encoded, err := EncodeRequest(header, body)
	if err != nil {
		t.Fatal(err)
	}
	got, gotBody, err := DecodeRequest(encoded)
	if err != nil {
		t.Fatal(err)
	}
	if got != header {
		t.Errorf("header: got %+v want %+v", got, header)
	}
	if !valuesMatch(gotBody, body) {
		t.Errorf("body: got %#v want %#v", gotBody, body)
	}
}

// TestResponseHeaderRoundTrip covers both response header shapes: Initialize is [type]
// only (no id), everything else is [type, id].
func TestResponseHeaderRoundTrip(t *testing.T) {
	init, err := EncodeResponse(ResponseHeader{Type: ResponseInitialize}, nil)
	if err != nil {
		t.Fatal(err)
	}
	header, body, err := DecodeResponse(init)
	if err != nil {
		t.Fatal(err)
	}
	if header.Type != ResponseInitialize || header.ID != 0 {
		t.Errorf("initialize header: got %+v", header)
	}
	if body != nil {
		t.Errorf("initialize body should be nil, got %#v", body)
	}

	success, err := EncodeResponse(ResponseHeader{Type: ResponsePromiseSuccess, ID: 9}, map[string]any{"ok": true})
	if err != nil {
		t.Fatal(err)
	}
	header, body, err = DecodeResponse(success)
	if err != nil {
		t.Fatal(err)
	}
	if header.Type != ResponsePromiseSuccess || header.ID != 9 {
		t.Errorf("success header: got %+v", header)
	}
	if !valuesMatch(body, Object{Keys: []string{"ok"}, Values: []any{true}}) {
		t.Errorf("success body: got %#v", body)
	}
}

// TestCancelOrDispose checks that cancel/dispose frames carry an undefined body.
func TestCancelOrDispose(t *testing.T) {
	encoded, err := EncodeCancelOrDispose(RequestPromiseCancel, 5)
	if err != nil {
		t.Fatal(err)
	}
	header, body, err := DecodeRequest(encoded)
	if err != nil {
		t.Fatal(err)
	}
	if header.Type != RequestPromiseCancel || header.ID != 5 {
		t.Errorf("header: got %+v", header)
	}
	if body != nil {
		t.Errorf("cancel body should decode to nil, got %#v", body)
	}
}
