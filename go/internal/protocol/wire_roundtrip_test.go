package protocol

import (
	"bytes"
	"encoding/json"
	"math/rand"
	"strings"
	"testing"
)

// reassemble is a deliberately small reference decoder used only to check the
// encoder: it takes the physical frames, decodes the fragments in index order
// and verifies count, size and checksum. The full assembler (ordinals, timeouts,
// supersede rules) is host-side and is ported in Phase 3.
func reassemble(t testing.TB, wires []Wire) []byte {
	t.Helper()
	if len(wires) == 1 && !wires[0].Fragment {
		var w struct {
			Frame json.RawMessage `json:"frame"`
		}
		if err := json.Unmarshal(wires[0].JSON, &w); err != nil {
			t.Fatal(err)
		}
		return w.Frame
	}
	type frag struct {
		Kind           string                            `json:"kind"`
		Index          int                               `json:"fragmentIndex"`
		Count          int                               `json:"fragmentCount"`
		LogicalBytes   int                               `json:"logicalBytes"`
		Checksum       struct{ Algorithm, Value string } `json:"checksum"`
		DataBase64     string                            `json:"dataBase64"`
		Ordinal        int64                             `json:"logicalFrameOrdinal"`
		LogicalFrameID string                            `json:"logicalFrameId"`
	}
	parts := make([][]byte, len(wires))
	var first frag
	for i, w := range wires {
		var f frag
		if err := json.Unmarshal(w.JSON, &f); err != nil {
			t.Fatalf("fragment %d: %v", i, err)
		}
		if i == 0 {
			first = f
		}
		if f.Kind != "fragment" || f.Count != len(wires) || f.LogicalBytes != first.LogicalBytes ||
			f.Checksum != first.Checksum || f.Ordinal != first.Ordinal || f.LogicalFrameID != first.LogicalFrameID {
			t.Fatalf("fragment %d metadata inconsistent: %+v vs %+v", i, f, first)
		}
		data, ok := DecodeBase64(f.DataBase64)
		if !ok {
			t.Fatalf("fragment %d base64 rejected", i)
		}
		parts[f.Index] = data
	}
	out := bytes.Join(parts, nil)
	if len(out) != first.LogicalBytes {
		t.Fatalf("length %d, header says %d", len(out), first.LogicalBytes)
	}
	if CRC32Hex(out) != first.Checksum.Value || first.Checksum.Algorithm != "crc32" {
		t.Fatalf("checksum mismatch")
	}
	return out
}

func encodeAndCheck(t testing.TB, frame []byte, limit int) {
	t.Helper()
	opts := EncodeOptions{DeliveryKind: DeliveryOnline, Topic: "conversation/s", SubscriptionID: "sub",
		LogicalFrameID: "lf", LogicalFrameOrdinal: 7, MaxPhysicalFrameBytes: limit}
	wires, err := EncodeTopicWireFrames(frame, opts)
	if err != nil {
		if _, ok := err.(*EncodeError); !ok {
			t.Fatalf("non-EncodeError: %v", err)
		}
		return // a refusal is a legal outcome; the vectors pin which reasons occur
	}
	maxBytes := limit
	if maxBytes <= 0 || maxBytes > MaxFrameBytes {
		maxBytes = MaxFrameBytes
	}
	for i, w := range wires {
		if !json.Valid(w.JSON) {
			t.Fatalf("wire %d is not valid JSON", i)
		}
		if got := MeasureEnvelope(len(w.JSON)).MaxBytes; got > maxBytes {
			t.Fatalf("wire %d measures %d > limit %d", i, got, maxBytes)
		}
		if len(wires) > LogicalFrameAssemblyMaxFragments {
			t.Fatalf("too many fragments: %d", len(wires))
		}
	}
	if got := reassemble(t, wires); !bytes.Equal(got, frame) {
		t.Fatalf("round trip changed the frame (%d vs %d bytes)", len(got), len(frame))
	}
}

func TestEncoderRoundTripRandomSizes(t *testing.T) {
	rng := rand.New(rand.NewSource(1))
	alphabet := []string{"a", "é", "中", "😀", `\n`, `\"`, "\u2028"}
	for i := 0; i < 300; i++ {
		var sb strings.Builder
		for sb.Len() < rng.Intn(60_000) {
			sb.WriteString(alphabet[rng.Intn(len(alphabet))])
		}
		frame := []byte(`{"topic":"conversation/s","subscriptionId":"sub","d":"` + sb.String() + `"}`)
		limit := []int{0, 2048, 3000, 4096, 10_000, 100_000}[rng.Intn(6)]
		encodeAndCheck(t, frame, limit)
	}
}

func TestEncoderNeverEmitsOversizedFragments(t *testing.T) {
	// Sweep the limit across the boundary where "complete" turns into fragments.
	frame := []byte(`{"topic":"conversation/s","subscriptionId":"sub","d":"` + strings.Repeat("x", 3000) + `"}`)
	for limit := 1100; limit < 9000; limit += 37 {
		encodeAndCheck(t, frame, limit)
	}
}

func FuzzEncoderRoundTrip(f *testing.F) {
	f.Add("hello", 2048)
	f.Add(strings.Repeat("中😀", 1000), 4096)
	f.Fuzz(func(t *testing.T, body string, limit int) {
		if limit < 0 || limit > 200_000 {
			return
		}
		quoted, _ := json.Marshal(body) // any valid JSON string body
		frame := append(append([]byte(`{"topic":"conversation/s","subscriptionId":"sub","d":`), quoted...), '}')
		encodeAndCheck(t, frame, limit)
	})
}
