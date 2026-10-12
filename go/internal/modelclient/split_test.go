package modelclient

import (
	"os"
	"path/filepath"
	"testing"
)

// TestDecoderSplitAtEveryByte feeds each SSE fixture one byte at a time and
// asserts it decodes to the same events as the vector's 1-byte split (which was
// produced by the real SDK pipeline). This is the property the SSE parser must
// hold: chunks may split at any byte boundary, including inside multi-byte UTF-8
// sequences.
//
// It deliberately compares against the SDK's own 1-byte result rather than
// against the whole-text result, because the reference parser itself differs
// between whole and split on pathological lone-CR / NUL inputs.
func TestDecoderSplitAtEveryByte(t *testing.T) {
	var vectors []sseVector
	readJSONFile(t, filepath.Join(vectorDir(t), "vectors", "model-sse.json"), &vectors)
	for _, v := range vectors {
		want, ok := v.Split["1"]
		if !ok {
			continue
		}
		t.Run(v.Name, func(t *testing.T) {
			events, err := decodeChunked(v.Text, 1)
			assertSplitResult(t, events, err, want.Events, want.Error)
		})
	}
}

// TestDecoderSplitAtEveryBoundary feeds each fixture split at every possible
// two-part boundary.
func TestDecoderSplitAtEveryBoundary(t *testing.T) {
	files, err := filepath.Glob(filepath.Join(vectorDir(t), "sse", "*.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if len(files) == 0 {
		t.Skip("no sse fixtures")
	}
	var vectors []sseVector
	readJSONFile(t, filepath.Join(vectorDir(t), "vectors", "model-sse.json"), &vectors)
	byName := map[string]sseVector{}
	for _, v := range vectors {
		byName[v.Name] = v
	}
	for _, file := range files {
		name := filepath.Base(file)
		name = name[:len(name)-len(".txt")]
		v, ok := byName[name]
		if !ok {
			continue
		}
		t.Run(name, func(t *testing.T) {
			whole, wholeErr := decodeChunked(v.Text, 1<<30)
			wholeSerialized := serializeEvents(whole)
			for cut := 0; cut <= len(v.Text); cut++ {
				d := NewDecoder()
				out := []Event{d.Start()}
				var e error
				out, e = d.Decode([]byte(v.Text[:cut]), out)
				if e == nil {
					out, e = d.Decode([]byte(v.Text[cut:]), out)
				}
				if e == nil {
					out, e = d.Finish(out)
				}
				// A two-part split must not produce MORE events than a whole read
				// (trailing buffered text can be dropped at EOF, as in the SDK).
				got := serializeEvents(out)
				if len(got) > len(wholeSerialized) {
					t.Fatalf("cut %d produced %d events, more than whole read's %d", cut, len(got), len(wholeSerialized))
				}
				// When a two-part split agrees on the event count with the whole
				// read, the events must be identical.
				if len(got) == len(wholeSerialized) && wholeErr == nil && e == nil {
					for i := range got {
						if !eventsEqual(got[i], wholeSerialized[i]) {
							t.Fatalf("cut %d event %d differs from whole read", cut, i)
						}
					}
				}
			}
		})
	}
}

func assertSplitResult(t *testing.T, events []Event, err error, wantEvents []decodedEvent, wantErr string) {
	t.Helper()
	if wantErr != "" {
		if err == nil {
			t.Fatalf("expected terminal error containing %q, got none", wantErr)
		}
		if !contains(err.Error(), wantErr) {
			t.Fatalf("terminal error %q does not contain %q", err.Error(), wantErr)
		}
		return
	}
	if err != nil {
		t.Fatalf("unexpected terminal error: %v", err)
	}
	got := serializeEvents(events)
	if len(got) != len(wantEvents) {
		t.Fatalf("event count mismatch\n got %s\nwant %s", mustJSONStr(got), mustJSONStr(wantEvents))
	}
	for i := range got {
		if !eventsEqual(got[i], wantEvents[i]) {
			t.Fatalf("event %d mismatch\n got %s\nwant %s", i, mustJSONStr(got[i]), mustJSONStr(wantEvents[i]))
		}
	}
}

// FuzzDecoderNeverPanics is a fuzz target: decoding arbitrary bytes (whole and
// byte-by-byte) must never panic, and the byte-by-byte decode must not produce
// more events than the whole decode.
func FuzzDecoderNeverPanics(f *testing.F) {
	files, _ := filepath.Glob(filepath.Join(vectorDir(f), "sse", "*.txt"))
	for _, file := range files {
		if b, err := os.ReadFile(file); err == nil {
			f.Add(string(b))
		}
	}
	f.Add("data: {\"choices\":[{\"index\":0,\"delta\":{\"content\":\"x\"},\"finish_reason\":null}]}\n\n")
	f.Add(": comment\ndata: [DONE]\n\n")
	f.Add("data:\n\n")
	f.Add("data: [DONE]")
	f.Add("\r\ndata: x\r\n\r\n")
	f.Add("data:0\x00\r\r0")
	f.Fuzz(func(t *testing.T, text string) {
		whole, _ := decodeAll(text, -1)
		split, _ := decodeAll(text, 1)
		if len(split) > len(whole) {
			t.Fatalf("byte-split produced more events (%d) than whole (%d) for %q", len(split), len(whole), text)
		}
	})
}

// decodeAll decodes text; when step <= 0 it feeds everything at once, else it
// feeds step bytes at a time.
func decodeAll(text string, step int) ([]Event, error) {
	d := NewDecoder()
	out := []Event{d.Start()}
	feed := func(chunk []byte) error {
		var e error
		out, e = d.Decode(chunk, out)
		return e
	}
	if step <= 0 {
		if err := feed([]byte(text)); err != nil {
			return out, err
		}
	} else {
		for i := 0; i < len(text); i += step {
			end := i + step
			if end > len(text) {
				end = len(text)
			}
			if err := feed([]byte(text[i:end])); err != nil {
				return out, err
			}
		}
	}
	res, err := d.Finish(out)
	return res, err
}
