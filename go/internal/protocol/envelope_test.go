package protocol

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
)

type envelopeVector struct {
	Input        string `json:"input"`
	Accept       bool   `json:"accept"`
	NormalizedID any    `json:"normalizedId"`
}

// TestEnvelopeAgreesWithZod checks ParseMessage against verdicts produced by the
// real nexProtocolMessageSchema (scripts/go-rewrite/gen-envelope-vectors.mts).
func TestEnvelopeAgreesWithZod(t *testing.T) {
	raw, err := os.ReadFile("../../testdata/vectors/envelope.json")
	if err != nil {
		t.Fatal(err)
	}
	var vectors []envelopeVector
	if err := json.Unmarshal(raw, &vectors); err != nil {
		t.Fatal(err)
	}
	if len(vectors) < 50 {
		t.Fatalf("vector file looks truncated: %d", len(vectors))
	}
	for _, v := range vectors {
		msg, err := ParseMessage([]byte(v.Input))
		got := err == nil
		if got != v.Accept {
			t.Errorf("input %s: go accept=%v (err=%v), zod accept=%v", v.Input, got, err, v.Accept)
			continue
		}
		if !got || v.NormalizedID == nil {
			continue
		}
		// zod yields a JS number or string; compare the textual form.
		want := ""
		switch id := v.NormalizedID.(type) {
		case string:
			want = id
		case float64:
			want = RequestID{num: int64(id)}.String()
		}
		if msg.ID.String() != want {
			t.Errorf("input %s: id %q, want %q", v.Input, msg.ID.String(), want)
		}
	}
}

func TestEncodeRoundTrip(t *testing.T) {
	cases := []Message{
		{Kind: KindRequest, ID: IntID(3), Method: "session/list", Params: json.RawMessage(`{"a":[1,2]}`), HasParams: true},
		{Kind: KindRequest, ID: StringID("x"), Method: "m"},
		{Kind: KindNotification, Method: "v4/conversation/frame", Params: json.RawMessage(`{"k":"\u2028"}`), HasParams: true},
		{Kind: KindResponse, ID: IntID(9), Result: json.RawMessage(`null`)},
		{Kind: KindResponse, ID: IntID(9)}, // missing result must still encode as null
		{Kind: KindError, ID: StringID("e"), Error: &RPCError{Code: CodeMethodNotFound, Message: "Method not found: x"}},
	}
	for _, in := range cases {
		line, err := Encode(in)
		if err != nil {
			t.Fatalf("encode %+v: %v", in, err)
		}
		if strings.ContainsRune(string(line), '\n') {
			t.Fatalf("encoded line contains LF: %q", line)
		}
		out, err := ParseMessage(line)
		if err != nil {
			t.Fatalf("own output rejected: %s: %v", line, err)
		}
		if out.Kind != in.Kind || out.Method != in.Method {
			t.Errorf("round trip changed message: %s -> %+v", line, out)
		}
	}
}

func TestEncodeFieldOrderMatchesHost(t *testing.T) {
	line, _ := Encode(Message{Kind: KindRequest, ID: IntID(1), Method: "m", Params: json.RawMessage(`{}`), HasParams: true,
		Trace: &Trace{TraceID: "t"}})
	if got, want := string(line), `{"id":1,"method":"m","params":{},"trace":{"traceId":"t"}}`; got != want {
		t.Errorf("got %s want %s", got, want)
	}
	line, _ = Encode(Message{Kind: KindResponse, ID: IntID(1), Result: json.RawMessage(`{"ok":true}`)})
	if got, want := string(line), `{"id":1,"result":{"ok":true}}`; got != want {
		t.Errorf("got %s want %s", got, want)
	}
}

func FuzzParseMessage(f *testing.F) {
	f.Add([]byte(`{"id":1,"method":"a"}`))
	f.Add([]byte(`{"id":1,"result":null}`))
	f.Add([]byte(`{"method":"a","params":{"x":1}}`))
	f.Add([]byte(`{"id":1,"error":{"code":1,"message":"m"}}`))
	f.Fuzz(func(t *testing.T, data []byte) {
		msg, err := ParseMessage(data)
		if err != nil {
			return
		}
		// Anything accepted must re-encode, and the re-encoding must be accepted.
		line, err := Encode(msg)
		if err != nil {
			t.Fatalf("accepted message cannot be encoded: %q: %v", data, err)
		}
		if _, err := ParseMessage(line); err != nil {
			t.Fatalf("re-encoded message rejected: %q -> %q: %v", data, line, err)
		}
	})
}
