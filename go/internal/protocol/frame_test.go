package protocol

import (
	"bytes"
	"errors"
	"io"
	"strings"
	"sync"
	"testing"
	"testing/iotest"
)

func readAll(t *testing.T, r io.Reader, max int) []string {
	t.Helper()
	fr := NewFrameReader(r, max)
	var out []string
	for {
		f, err := fr.Next()
		if errors.Is(err, io.EOF) {
			return out
		}
		if err != nil {
			t.Fatalf("Next: %v", err)
		}
		out = append(out, string(f))
	}
}

func TestFrameReaderSplitsOnLFOnly(t *testing.T) {
	// U+2028/U+2029 inside a JSON string must not split the frame: Node's readline
	// would, which is exactly why the host does not use it.
	in := "{\"a\":\"x\u2028y\u2029z\"}\n{\"b\":1}\n"
	got := readAll(t, strings.NewReader(in), 0)
	if len(got) != 2 || got[0] != "{\"a\":\"x\u2028y\u2029z\"}" || got[1] != `{"b":1}` {
		t.Fatalf("got %q", got)
	}
}

func TestFrameReaderEveryReadBoundary(t *testing.T) {
	in := "{\"a\":1}\r\n\n   \n{\"b\":\"\u2028\"}\n{\"c\":3}"
	want := []string{`{"a":1}`, "{\"b\":\"\u2028\"}", `{"c":3}`}
	readers := map[string]func() io.Reader{
		"onebyte": func() io.Reader { return iotest.OneByteReader(strings.NewReader(in)) },
		"half":    func() io.Reader { return iotest.HalfReader(strings.NewReader(in)) },
		"dataerr": func() io.Reader { return iotest.DataErrReader(strings.NewReader(in)) },
	}
	for name, mk := range readers {
		got := readAll(t, mk(), 0)
		if strings.Join(got, "|") != strings.Join(want, "|") {
			t.Errorf("%s: got %q want %q", name, got, want)
		}
	}
	// A split at every possible offset of a two-chunk stream.
	for i := 0; i <= len(in); i++ {
		r := io.MultiReader(strings.NewReader(in[:i]), strings.NewReader(in[i:]))
		got := readAll(t, r, 0)
		if strings.Join(got, "|") != strings.Join(want, "|") {
			t.Fatalf("split at %d: got %q", i, got)
		}
	}
}

func TestFrameReaderSkipsBlankAndTrimsOneCR(t *testing.T) {
	got := readAll(t, strings.NewReader("\n\r\n \t \n{\"x\":1}\r\r\n"), 0)
	// Only one trailing CR is stripped, as in the host; the extra CR stays.
	if len(got) != 1 || got[0] != "{\"x\":1}\r" {
		t.Fatalf("got %q", got)
	}
}

func TestFrameReaderLargeFrame(t *testing.T) {
	big := strings.Repeat("a", 8<<20)
	got := readAll(t, strings.NewReader(`"`+big+`"`+"\n"), 0)
	if len(got) != 1 || len(got[0]) != len(big)+2 {
		t.Fatalf("large frame mangled: %d frames", len(got))
	}
}

func TestFrameReaderTooLong(t *testing.T) {
	fr := NewFrameReader(strings.NewReader(strings.Repeat("a", 1000)+"\n"), 100)
	if _, err := fr.Next(); !errors.Is(err, ErrFrameTooLong) {
		t.Fatalf("want ErrFrameTooLong, got %v", err)
	}
	// Exactly at the limit is accepted.
	fr = NewFrameReader(strings.NewReader(strings.Repeat("a", 100)+"\n"), 100)
	if f, err := fr.Next(); err != nil || len(f) != 100 {
		t.Fatalf("limit-sized frame: %d, %v", len(f), err)
	}
	// An unterminated line over the limit must also fail instead of buffering forever.
	fr = NewFrameReader(strings.NewReader(strings.Repeat("a", 1000)), 100)
	if _, err := fr.Next(); !errors.Is(err, ErrFrameTooLong) {
		t.Fatalf("unterminated: want ErrFrameTooLong, got %v", err)
	}
}

func TestFrameReaderPropagatesReadError(t *testing.T) {
	boom := errors.New("boom")
	fr := NewFrameReader(io.MultiReader(strings.NewReader("{\"a\":1}\n"), iotest.ErrReader(boom)), 0)
	if f, err := fr.Next(); err != nil || string(f) != `{"a":1}` {
		t.Fatalf("first: %q %v", f, err)
	}
	if _, err := fr.Next(); !errors.Is(err, boom) {
		t.Fatalf("want boom, got %v", err)
	}
}

func TestFrameWriterRejectsRawLF(t *testing.T) {
	var b bytes.Buffer
	fw := NewFrameWriter(&b)
	if err := fw.WriteFrame([]byte("a\nb")); err == nil {
		t.Fatal("expected error for payload with LF")
	}
	if b.Len() != 0 {
		t.Fatalf("partial write leaked: %q", b.String())
	}
}

// countingWriter records each Write call so the test can prove one frame is one Write.
type countingWriter struct {
	mu     sync.Mutex
	writes [][]byte
}

func (c *countingWriter) Write(p []byte) (int, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.writes = append(c.writes, append([]byte(nil), p...))
	return len(p), nil
}

func TestFrameWriterConcurrentFramesNeverInterleave(t *testing.T) {
	cw := &countingWriter{}
	fw := NewFrameWriter(cw)
	var wg sync.WaitGroup
	for i := 0; i < 64; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 50; j++ {
				_ = fw.WriteFrame([]byte(`{"id":1,"result":"` + strings.Repeat("x", 200) + `"}`))
			}
		}()
	}
	wg.Wait()
	if len(cw.writes) != 64*50 {
		t.Fatalf("writes: %d", len(cw.writes))
	}
	for _, w := range cw.writes {
		if bytes.Count(w, []byte{'\n'}) != 1 || w[len(w)-1] != '\n' {
			t.Fatalf("a Write was not exactly one frame: %q", w)
		}
	}
}

func TestEncodedMessagesSurviveReader(t *testing.T) {
	var b bytes.Buffer
	fw := NewFrameWriter(&b)
	msg := Message{Kind: KindNotification, Method: "m", Params: []byte("{\"t\":\"a\u2028b\u2029c\\n\"}"), HasParams: true}
	for i := 0; i < 3; i++ {
		if err := fw.WriteMessage(msg); err != nil {
			t.Fatal(err)
		}
	}
	got := readAll(t, &b, 0)
	if len(got) != 3 {
		t.Fatalf("expected 3 frames, got %d", len(got))
	}
	for _, f := range got {
		if _, err := ParseMessage([]byte(f)); err != nil {
			t.Fatalf("frame %q: %v", f, err)
		}
	}
}

func FuzzFrameReaderMatchesSplit(f *testing.F) {
	f.Add([]byte("a\nb\r\n\nc"))
	f.Add([]byte("\u2028\n\u2029"))
	f.Fuzz(func(t *testing.T, data []byte) {
		// Reference: split on LF, strip one CR, drop blank lines.
		var want []string
		for _, line := range bytes.Split(data, []byte{'\n'}) {
			line = bytes.TrimSuffix(line, []byte{'\r'})
			if len(bytes.TrimSpace(line)) > 0 {
				want = append(want, string(line))
			}
		}
		got := readAll(t, iotest.OneByteReader(bytes.NewReader(data)), 0)
		if strings.Join(got, "\x00") != strings.Join(want, "\x00") {
			t.Fatalf("data %q: got %q want %q", data, got, want)
		}
	})
}
