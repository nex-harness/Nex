package rpc

import (
	"bytes"
	"context"
	"errors"
	"io"
	"testing"
)

// drainMessages reads until the reader stops producing, tolerating only the two
// terminal conditions a byte stream can legitimately reach: end of stream and an
// over-limit frame. It is bounded so a pathological corpus cannot spin forever.
func drainMessages(t *testing.T, data []byte, chunkSize int, limit int) [][]byte {
	t.Helper()
	var chunks [][]byte
	if chunkSize <= 0 {
		chunks = [][]byte{data}
	} else {
		for i := 0; i < len(data); i += chunkSize {
			end := min(i+chunkSize, len(data))
			chunks = append(chunks, data[i:end])
		}
	}
	r := NewMessageReader(&chunkSocket{chunks: chunks}, limit)
	var out [][]byte
	for {
		msg, err := r.Read(context.Background())
		if err != nil {
			if !errors.Is(err, io.EOF) && !errors.Is(err, ErrFrameTooLong) {
				t.Fatalf("unexpected error: %v", err)
			}
			return out
		}
		out = append(out, msg)
		if len(out) > 1000 {
			return out
		}
	}
}

// FuzzFrameReader asserts two invariants for arbitrary byte input:
//
//  1. The reader never panics and only ever terminates with end-of-stream or an
//     over-limit protocol error — never with an unexpected failure.
//  2. The decoded message sequence is independent of the chunking. A frame that
//     spans chunks and the same frame delivered whole must decode identically;
//     this is the ChunkStream contract from packages/rpc/src/protocol.ts.
func FuzzFrameReader(f *testing.F) {
	f.Add([]byte{1, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 3, 'a', 'b', 'c'})
	f.Add([]byte{1, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0})                   // zero-length Regular
	f.Add([]byte{3, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 2, 0xff, 0xff})       // non-Regular, ignored
	f.Add([]byte{1, 0xff, 0xff, 0xff, 0xff, 0, 0, 0, 0, 0, 0, 0xff, 0xff}) // over-limit length
	f.Add([]byte("short"))
	f.Add([]byte{})
	f.Fuzz(func(t *testing.T, data []byte) {
		const limit = 1 << 16
		whole := drainMessages(t, data, 0, limit)
		split := drainMessages(t, data, 1, limit)
		if len(whole) != len(split) {
			t.Fatalf("chunking changed message count: %d vs %d", len(whole), len(split))
		}
		for i := range whole {
			if !bytes.Equal(whole[i], split[i]) {
				t.Fatalf("chunking changed message %d", i)
			}
		}
	})
}
