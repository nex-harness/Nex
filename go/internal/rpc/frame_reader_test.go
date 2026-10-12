package rpc

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"io"
	"math/rand"
	"testing"
)

// chunkSocket is an in-memory Socket whose Read results are pre-scripted: each
// element of chunks becomes one Read call, so a test can reproduce an exact
// chunk boundary pattern (frames split across reads, frames sharing one read).
type chunkSocket struct {
	chunks  [][]byte
	err     error // returned after the last chunk, together with it
	reads   int
	closed  bool
	written [][]byte
}

func (s *chunkSocket) Read(ctx context.Context) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if s.reads >= len(s.chunks) {
		if s.err != nil {
			return nil, s.err
		}
		return nil, io.EOF
	}
	chunk := s.chunks[s.reads]
	s.reads++
	// Surface the terminal error together with the last chunk, so tests can model a
	// socket that fails right after delivering its final bytes.
	if s.reads == len(s.chunks) && s.err != nil {
		return chunk, s.err
	}
	return chunk, nil
}

func (s *chunkSocket) Write(ctx context.Context, data []byte) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	s.written = append(s.written, append([]byte(nil), data...))
	return nil
}

func (s *chunkSocket) Close() error {
	s.closed = true
	return nil
}

// readAll drains the reader until the socket reports its terminal error.
func readAll(t *testing.T, sock Socket, max int) ([]string, error) {
	t.Helper()
	r := NewMessageReader(sock, max)
	var out []string
	for {
		msg, err := r.Read(context.Background())
		if err != nil {
			return out, err
		}
		out = append(out, string(msg))
	}
}

// readN reads exactly n Regular messages.
func readN(t *testing.T, r *MessageReader, n int) [][]byte {
	t.Helper()
	out := make([][]byte, 0, n)
	for i := 0; i < n; i++ {
		msg, err := r.Read(context.Background())
		if err != nil {
			t.Fatalf("message %d: %v", i, err)
		}
		out = append(out, msg)
	}
	return out
}

// TestMessageReaderHeaderSplitAtEveryOffset feeds a single frame split at every
// byte offset 1..12 of the 13-byte header (and also inside the body), proving the
// reader never consumes a header it has not seen in full.
func TestMessageReaderHeaderSplitAtEveryOffset(t *testing.T) {
	body := []byte("hello")
	full := EncodeFrame(Frame{Type: MsgRegular, ID: 0x01020304, Ack: 0xaabbccdd, Data: body})
	for split := 1; split < len(full); split++ {
		sock := &chunkSocket{chunks: [][]byte{full[:split], full[split:]}}
		got, err := readAll(t, sock, 0)
		if !errors.Is(err, io.EOF) {
			t.Fatalf("split at %d: terminal error = %v, want io.EOF", split, err)
		}
		if len(got) != 1 || got[0] != string(body) {
			t.Fatalf("split at %d: got %q", split, got)
		}
	}
	// Splitting inside the header specifically, byte by byte (offset 1..12).
	for split := 1; split < frameHeaderSize; split++ {
		sock := &chunkSocket{chunks: [][]byte{full[:split], full[split:]}}
		got, err := readAll(t, sock, 0)
		if !errors.Is(err, io.EOF) || len(got) != 1 || got[0] != string(body) {
			t.Fatalf("header split at %d: got %q err=%v", split, got, err)
		}
	}
}

// TestMessageReaderOneByteAtATime is the pathological boundary case: every Read
// returns a single byte, so the header is split 13 ways.
func TestMessageReaderOneByteAtATime(t *testing.T) {
	full := EncodeFrame(Frame{Type: MsgRegular, ID: 1, Ack: 2, Data: []byte("payload")})
	chunks := make([][]byte, 0, len(full))
	for i := range full {
		chunks = append(chunks, full[i:i+1])
	}
	got, err := readAll(t, &chunkSocket{chunks: chunks}, 0)
	if !errors.Is(err, io.EOF) || len(got) != 1 || got[0] != "payload" {
		t.Fatalf("got %q err=%v", got, err)
	}
}

// TestMessageReaderZeroLengthRegular covers the empty-message contract: a
// zero-length Regular fires an empty message, a zero-length non-Regular fires
// nothing. A zero-length Regular followed by a zero-length non-Regular fires
// exactly one empty message and consumes both frames.
func TestMessageReaderZeroLengthRegular(t *testing.T) {
	cases := []struct {
		name string
		typ  uint8
		// want is the expected message sequence for: [zero-len Regular][zero-len typ]
		want []string
	}{
		{"regular", MsgRegular, []string{"", ""}},
		{"ack", MsgAck, []string{""}},
		{"keepalive", MsgKeepAlive, []string{""}},
		{"control", MsgControl, []string{""}},
		{"disconnect", MsgDisconnect, []string{""}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			stream := append(
				EncodeFrame(Frame{Type: MsgRegular, ID: 3, Ack: 0, Data: nil}),
				EncodeFrame(Frame{Type: tc.typ, ID: 4, Ack: 0, Data: nil})...,
			)
			got, err := readAll(t, &chunkSocket{chunks: [][]byte{stream}}, 0)
			if !errors.Is(err, io.EOF) {
				t.Fatalf("terminal error = %v, want io.EOF", err)
			}
			if len(got) != len(tc.want) {
				t.Fatalf("got %q, want %q", got, tc.want)
			}
			for i := range tc.want {
				if got[i] != tc.want[i] {
					t.Fatalf("message %d: got %q want %q", i, got[i], tc.want[i])
				}
			}
		})
	}
}

// TestMessageReaderNonRegularIgnored proves a non-Regular frame is read (its body
// bytes consumed, so the following frame still decodes) and emits nothing.
func TestMessageReaderNonRegularIgnored(t *testing.T) {
	payload := []byte("ignored payload")
	stream := []byte{}
	stream = append(stream, EncodeFrame(Frame{Type: MsgAck, ID: 1, Ack: 9, Data: payload})...)
	stream = append(stream, EncodeFrame(Frame{Type: MsgRegular, ID: 2, Ack: 0, Data: []byte("after")})...)
	got, err := readAll(t, &chunkSocket{chunks: [][]byte{stream}}, 0)
	if !errors.Is(err, io.EOF) {
		t.Fatalf("terminal error = %v, want io.EOF", err)
	}
	if len(got) != 1 || got[0] != "after" {
		t.Fatalf("got %q, want [after]", got)
	}
}

// TestMessageReaderTwoFramesInOneChunk covers two frames arriving in a single Read.
func TestMessageReaderTwoFramesInOneChunk(t *testing.T) {
	a := EncodeFrame(Frame{Type: MsgRegular, ID: 1, Ack: 0, Data: []byte("first")})
	b := EncodeFrame(Frame{Type: MsgRegular, ID: 2, Ack: 0, Data: []byte("second")})
	one := append(append([]byte{}, a...), b...)
	got, err := readAll(t, &chunkSocket{chunks: [][]byte{one}}, 0)
	if !errors.Is(err, io.EOF) {
		t.Fatalf("terminal error = %v, want io.EOF", err)
	}
	if len(got) != 2 || got[0] != "first" || got[1] != "second" {
		t.Fatalf("got %q, want [first second]", got)
	}
}

// TestMessageReaderOversizeFrame rejects a frame whose declared length exceeds
// the limit, without ever allocating the body. The socket must be closed.
func TestMessageReaderOversizeFrame(t *testing.T) {
	const limit = 1024
	huge := 1 << 30 // 1 GiB declared, way over the limit
	header := make([]byte, frameHeaderSize)
	header[0] = MsgRegular
	binary.BigEndian.PutUint32(header[9:], uint32(huge))
	// A handful of body bytes follow; the reader must refuse before waiting for
	// the rest, and must not attempt to buffer `huge` bytes.
	sock := &chunkSocket{chunks: [][]byte{header, []byte("partial body")}}
	r := NewMessageReader(sock, limit)
	if _, err := r.Read(context.Background()); !errors.Is(err, ErrFrameTooLong) {
		t.Fatalf("err = %v, want ErrFrameTooLong", err)
	}
	if !sock.closed {
		t.Fatal("socket must be closed on a protocol error")
	}
	// Nothing was buffered beyond what was read.
	if r.Buffered() > len(header)+len("partial body") {
		t.Fatalf("buffered %d bytes for an over-limit frame", r.Buffered())
	}
}

// TestMessageReaderExactlyAtLimit accepts a frame whose body is exactly the limit.
func TestMessageReaderExactlyAtLimit(t *testing.T) {
	const limit = 4096
	body := bytes.Repeat([]byte{0xab}, limit)
	stream := EncodeFrame(Frame{Type: MsgRegular, ID: 1, Ack: 0, Data: body})
	got, err := readAll(t, &chunkSocket{chunks: [][]byte{stream}}, limit)
	if !errors.Is(err, io.EOF) || len(got) != 1 || !bytes.Equal([]byte(got[0]), body) {
		t.Fatalf("limit-sized frame: %d bytes, err=%v", len(got[0]), err)
	}
}

// TestMessageReaderDefaultLimit verifies the default is 64 MiB.
func TestMessageReaderDefaultLimit(t *testing.T) {
	r := NewMessageReader(&chunkSocket{}, 0)
	if r.MaxFrameBytes() != DefaultMaxFrameBytes {
		t.Fatalf("default max = %d, want %d", r.MaxFrameBytes(), DefaultMaxFrameBytes)
	}
	if DefaultMaxFrameBytes != 64<<20 {
		t.Fatalf("DefaultMaxFrameBytes = %d, want 64 MiB", DefaultMaxFrameBytes)
	}
}

// TestMessageReaderReadErrorMidFramePropagates: a socket error arriving while a
// frame is partially buffered must surface unchanged to the caller.
func TestMessageReaderReadErrorMidFramePropagates(t *testing.T) {
	full := EncodeFrame(Frame{Type: MsgRegular, ID: 1, Ack: 0, Data: []byte("abcdefgh")})
	// Deliver 10 bytes (header + part of body), then fail.
	sock := &chunkSocket{chunks: [][]byte{full[:10]}, err: errors.New("connection reset")}
	r := NewMessageReader(sock, 0)
	if _, err := r.Read(context.Background()); !errors.Is(err, sock.err) {
		t.Fatalf("err = %v, want the socket error", err)
	}
}

// TestMessageReaderReadErrorAfterFrames delivers a couple of good frames, then a
// socket error; the frames must be delivered before the error surfaces.
func TestMessageReaderReadErrorAfterFrames(t *testing.T) {
	a := EncodeFrame(Frame{Type: MsgRegular, ID: 1, Ack: 0, Data: []byte("a")})
	b := EncodeFrame(Frame{Type: MsgRegular, ID: 2, Ack: 0, Data: []byte("b")})
	one := append(append([]byte{}, a...), b...)
	boom := errors.New("socket died")
	sock := &chunkSocket{chunks: [][]byte{one}, err: boom}
	r := NewMessageReader(sock, 0)
	msgs := readN(t, r, 2)
	if string(msgs[0]) != "a" || string(msgs[1]) != "b" {
		t.Fatalf("got %q %q", msgs[0], msgs[1])
	}
	if _, err := r.Read(context.Background()); !errors.Is(err, boom) {
		t.Fatalf("err = %v, want boom", err)
	}
}

// TestMessageReaderContextCancelWhileWaiting: with nothing buffered, a cancelled
// context aborts the wait with the context error.
func TestMessageReaderContextCancelWhileWaiting(t *testing.T) {
	// A socket that returns io.EOF after chunks: we supply no chunks, so the very
	// first Read is terminal. To test cancellation we instead use a socket whose
	// Read observes ctx.
	sock := &chunkSocket{} // returns io.EOF immediately
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	r := NewMessageReader(sock, 0)
	if _, err := r.Read(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
}

// TestMessageReaderWritePassesThrough proves the socket write path is usable by
// callers building on Socket (the framing layer itself only reads).
func TestMessageReaderWritePassesThrough(t *testing.T) {
	sock := &chunkSocket{}
	if err := sock.Write(context.Background(), []byte("x")); err != nil {
		t.Fatal(err)
	}
	if len(sock.written) != 1 || string(sock.written[0]) != "x" {
		t.Fatalf("written = %q", sock.written)
	}
}

// TestMessageReaderRandomSplitsProperty is the property test: for a valid
// concatenation of frames, any random chunking of that byte stream must decode to
// the same sequence of Regular payloads (non-Regular frames dropped).
func TestMessageReaderRandomSplitsProperty(t *testing.T) {
	rng := rand.New(rand.NewSource(20240613))
	for trial := 0; trial < 200; trial++ {
		// Build a random sequence of frames.
		var stream []byte
		var want []string
		frameCount := 1 + rng.Intn(6)
		for i := 0; i < frameCount; i++ {
			typ := uint8(MsgRegular)
			if rng.Intn(3) == 0 {
				typ = []uint8{MsgAck, MsgControl, MsgKeepAlive}[rng.Intn(3)]
			}
			body := make([]byte, rng.Intn(8))
			rng.Read(body)
			stream = append(stream, EncodeFrame(Frame{Type: typ, ID: uint32(i), Ack: uint32(rng.Intn(5)), Data: body})...)
			if typ == MsgRegular {
				want = append(want, string(body))
			}
		}
		// Randomly split the stream into chunks.
		var chunks [][]byte
		rest := stream
		for len(rest) > 0 {
			n := 1 + rng.Intn(len(rest))
			chunks = append(chunks, rest[:n])
			rest = rest[n:]
		}
		got, err := readAll(t, &chunkSocket{chunks: chunks}, 0)
		if !errors.Is(err, io.EOF) {
			t.Fatalf("trial %d: terminal err = %v, want io.EOF", trial, err)
		}
		if len(got) != len(want) {
			t.Fatalf("trial %d: got %d msgs %q, want %d %q", trial, len(got), got, len(want), want)
		}
		for i := range want {
			if got[i] != want[i] {
				t.Fatalf("trial %d: msg %d got %q want %q", trial, i, got[i], want[i])
			}
		}
	}
}

// TestFrameRoundTripR01 replays R01 at the byte level: the Initialize response
// body `04 01 06 c8 01 00` wrapped in a Regular frame must round-trip.
func TestFrameRoundTripR01(t *testing.T) {
	body, err := hex.DecodeString("040106c80100")
	if err != nil {
		t.Fatal(err)
	}
	stream := EncodeFrame(Frame{Type: MsgRegular, ID: 0, Ack: 0, Data: body})
	got, err := readAll(t, &chunkSocket{chunks: [][]byte{stream}}, 0)
	if !errors.Is(err, io.EOF) || len(got) != 1 || got[0] != string(body) {
		t.Fatalf("R01 round trip: %q err=%v", got, err)
	}
}
