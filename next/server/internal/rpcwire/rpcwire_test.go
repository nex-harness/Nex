package rpcwire

import (
	"bytes"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// fixtureMeta mirrors the JSON sidecar written by testdata/gen.ts.
type fixtureMeta struct {
	Name        string      `json:"name"`
	Description string      `json:"description"`
	Direction   string      `json:"direction"`
	Frame       frameHeader `json:"frame"`
	Expected    struct {
		Kind    string        `json:"kind"`
		Type    int64         `json:"type"`
		ID      *uint32       `json:"id"`
		Channel string        `json:"channel"`
		Name    string        `json:"name"`
		Arg     *logical      `json:"arg"`
		Body    *logical      `json:"body"`
		Frames  []frameHeader `json:"frames"`
	} `json:"expected"`
}

type frameHeader struct {
	Type uint8  `json:"type"`
	ID   uint32 `json:"id"`
	Ack  uint32 `json:"ack"`
}

// logical is the fixture-side description of a serialized value.
type logical struct {
	T  string `json:"t"`
	V  any    `json:"v"`
	VS bool   `json:"vs"`
}

func fixtureNames(t *testing.T) []string {
	t.Helper()
	matches, err := filepath.Glob(filepath.Join("testdata", "*.bin"))
	if err != nil {
		t.Fatalf("globbing fixtures: %v", err)
	}
	if len(matches) == 0 {
		t.Fatal("no fixtures found; run the generator (see README.md)")
	}
	names := make([]string, 0, len(matches))
	for _, m := range matches {
		names = append(names, strings.TrimSuffix(filepath.Base(m), ".bin"))
	}
	return names
}

func readFixture(t *testing.T, name string) ([]byte, fixtureMeta) {
	t.Helper()
	bin, err := os.ReadFile(filepath.Join("testdata", name+".bin"))
	if err != nil {
		t.Fatalf("reading %s.bin: %v", name, err)
	}
	rawJSON, err := os.ReadFile(filepath.Join("testdata", name+".json"))
	if err != nil {
		t.Fatalf("reading %s.json: %v", name, err)
	}
	var meta fixtureMeta
	if err := json.Unmarshal(rawJSON, &meta); err != nil {
		t.Fatalf("parsing %s.json: %v", name, err)
	}
	if meta.Name != name {
		t.Fatalf("%s.json name is %q, want %q", name, meta.Name, name)
	}
	return bin, meta
}

// TestGoldenFrames decodes every golden frame, checks the logical content,
// and requires re-encoding (payload for channel messages, frame header for raw
// frames) to be byte-identical to the TypeScript output.
func TestGoldenFrames(t *testing.T) {
	for _, name := range fixtureNames(t) {
		t.Run(name, func(t *testing.T) {
			bin, meta := readFixture(t, name)

			fr := NewFrameReader(bytes.NewReader(bin), 0)
			got, err := fr.Next()
			if err != nil {
				t.Fatalf("decoding frame: %v", err)
			}
			if meta.Expected.Kind != "frames" {
				if _, err := fr.Next(); !errors.Is(err, io.EOF) {
					t.Fatalf("expected exactly one frame, trailing read returned %v", err)
				}
				assertHeader(t, got, meta.Frame)

				// EncodeFrame must reproduce the original bytes exactly.
				if reencoded := EncodeFrame(got); !bytes.Equal(reencoded, bin) {
					t.Fatalf("EncodeFrame mismatch\n got: %x\nwant: %x", reencoded, bin)
				}
			}

			switch meta.Expected.Kind {
			case "request":
				checkRequest(t, got.Payload, meta)
			case "response":
				checkResponse(t, got.Payload, meta)
			case "frame":
				if meta.Expected.Type != 0 {
					t.Fatalf("raw frame fixture must not declare a channel type")
				}
			case "frames":
				// Handled by TestConcatenatedFrames.
			default:
				t.Fatalf("unexpected fixture kind %q", meta.Expected.Kind)
			}
		})
	}
}

func assertHeader(t *testing.T, got Frame, want frameHeader) {
	t.Helper()
	if uint8(got.Type) != want.Type || got.ID != want.ID || got.Ack != want.Ack {
		t.Fatalf("frame header = {%d %d %d}, want {%d %d %d}",
			uint8(got.Type), got.ID, got.Ack, want.Type, want.ID, want.Ack)
	}
}

func checkRequest(t *testing.T, payload []byte, meta fixtureMeta) {
	t.Helper()
	req, err := DecodeRequest(payload)
	if err != nil {
		t.Fatalf("DecodeRequest: %v", err)
	}
	exp := meta.Expected
	if int64(req.Type) != exp.Type {
		t.Fatalf("request type = %d, want %d", req.Type, exp.Type)
	}
	if exp.ID != nil && req.ID != *exp.ID {
		t.Fatalf("request id = %d, want %d", req.ID, *exp.ID)
	}
	if req.Channel != exp.Channel {
		t.Fatalf("channel = %q, want %q", req.Channel, exp.Channel)
	}
	if req.Name != exp.Name {
		t.Fatalf("name = %q, want %q", req.Name, exp.Name)
	}
	if exp.Arg != nil {
		assertLogical(t, req.Arg, *exp.Arg)
	}

	// Re-encode through the typed encoder and require identical payload bytes.
	reencoded, err := EncodeRequest(req.Type, req.ID, req.Channel, req.Name, req.Arg)
	if err != nil {
		t.Fatalf("EncodeRequest: %v", err)
	}
	if !bytes.Equal(reencoded, payload) {
		t.Fatalf("request re-encode mismatch\n got: %x\nwant: %x", reencoded, payload)
	}
}

func checkResponse(t *testing.T, payload []byte, meta fixtureMeta) {
	t.Helper()
	resp, err := DecodeResponse(payload)
	if err != nil {
		t.Fatalf("DecodeResponse: %v", err)
	}
	exp := meta.Expected
	if int64(resp.Type) != exp.Type {
		t.Fatalf("response type = %d, want %d", resp.Type, exp.Type)
	}
	if exp.ID != nil && resp.ID != *exp.ID {
		t.Fatalf("response id = %d, want %d", resp.ID, *exp.ID)
	}
	if exp.Body != nil {
		assertLogical(t, resp.Body, *exp.Body)
	}

	reencoded, err := EncodeResponse(resp)
	if err != nil {
		t.Fatalf("EncodeResponse: %v", err)
	}
	if !bytes.Equal(reencoded, payload) {
		t.Fatalf("response re-encode mismatch\n got: %x\nwant: %x", reencoded, payload)
	}

	// Also exercise the purpose-built encoders where they apply.
	switch resp.Type {
	case ResponseInitialize:
		if b, err := EncodeInitialize(); err != nil || !bytes.Equal(b, payload) {
			t.Fatalf("EncodeInitialize mismatch: %v\n got: %x\nwant: %x", err, b, payload)
		}
	case ResponsePromiseSuccess:
		if b, err := EncodePromiseSuccess(resp.ID, resp.Body); err != nil || !bytes.Equal(b, payload) {
			t.Fatalf("EncodePromiseSuccess mismatch: %v\n got: %x\nwant: %x", err, b, payload)
		}
	}
}

func assertLogical(t *testing.T, got Value, want logical) {
	t.Helper()
	switch want.T {
	case "undef":
		if _, ok := got.(Undefined); !ok {
			t.Fatalf("value = %T(%v), want Undefined", got, got)
		}
	case "str":
		s, ok := got.(string)
		if !ok || s != want.V.(string) {
			t.Fatalf("value = %T(%q), want string %q", got, got, want.V)
		}
	case "int":
		n, ok := got.(int64)
		if !ok || n != int64(want.V.(float64)) {
			t.Fatalf("value = %T(%v), want int64 %v", got, got, want.V)
		}
	case "jsonnum":
		raw, ok := got.(json.RawMessage)
		if !ok || string(raw) != want.V.(string) {
			t.Fatalf("value = %T(%s), want raw JSON %s", got, got, want.V)
		}
	case "buf":
		buf, ok := got.(Buffer)
		if !ok {
			t.Fatalf("value = %T, want Buffer", got)
		}
		data, err := base64.StdEncoding.DecodeString(want.V.(string))
		if err != nil {
			t.Fatalf("fixture base64: %v", err)
		}
		if !bytes.Equal(buf.Data, data) || buf.VS != want.VS {
			t.Fatalf("buffer = {%x vs=%v}, want {%x vs=%v}", buf.Data, buf.VS, data, want.VS)
		}
	case "arr":
		arr, ok := got.([]any)
		if !ok {
			t.Fatalf("value = %T, want []any", got)
		}
		items := want.V.([]any)
		if len(arr) != len(items) {
			t.Fatalf("array length = %d, want %d", len(arr), len(items))
		}
		for i, item := range items {
			raw, err := json.Marshal(item)
			if err != nil {
				t.Fatalf("re-marshalling fixture element: %v", err)
			}
			var el logical
			if err := json.Unmarshal(raw, &el); err != nil {
				t.Fatalf("fixture element %d: %v", i, err)
			}
			assertLogical(t, arr[i], el)
		}
	case "obj":
		raw, ok := got.(json.RawMessage)
		if !ok {
			t.Fatalf("value = %T, want json.RawMessage", got)
		}
		var gotObj, wantObj any
		if err := json.Unmarshal(raw, &gotObj); err != nil {
			t.Fatalf("decoded object is invalid JSON: %v", err)
		}
		wantRaw, err := json.Marshal(want.V)
		if err != nil {
			t.Fatalf("marshalling expected object: %v", err)
		}
		if err := json.Unmarshal(wantRaw, &wantObj); err != nil {
			t.Fatalf("parsing expected object: %v", err)
		}
		gotJSON, _ := json.Marshal(gotObj)
		wantJSON, _ := json.Marshal(wantObj)
		if !bytes.Equal(gotJSON, wantJSON) {
			t.Fatalf("object = %s, want %s", gotJSON, wantJSON)
		}
	default:
		t.Fatalf("unknown fixture value kind %q", want.T)
	}
}

// TestConcatenatedFrames decodes two frames delivered in one byte stream.
func TestConcatenatedFrames(t *testing.T) {
	bin, meta := readFixture(t, "frames-concatenated")
	fr := NewFrameReader(bytes.NewReader(bin), 0)
	for i, want := range meta.Expected.Frames {
		got, err := fr.Next()
		if err != nil {
			t.Fatalf("frame %d: %v", i, err)
		}
		assertHeader(t, got, want)
		if i == 0 {
			resp, err := DecodeResponse(got.Payload)
			if err != nil {
				t.Fatalf("first frame is not an Initialize response: %v", err)
			}
			if resp.Type != ResponseInitialize {
				t.Fatalf("first frame type = %d, want Initialize", resp.Type)
			}
		}
	}
	if _, err := fr.Next(); !errors.Is(err, io.EOF) {
		t.Fatalf("expected EOF after both frames, got %v", err)
	}
}

// TestSplitFrameAcrossChunks feeds one frame one byte at a time, which is the
// worst case SocketProtocol's ChunkStream tolerates.
func TestSplitFrameAcrossChunks(t *testing.T) {
	bin, meta := readFixture(t, "server-promise-error")
	pr, pw := io.Pipe()
	fr := NewFrameReader(pr, 0)

	done := make(chan Frame, 1)
	errc := make(chan error, 1)
	go func() {
		f, err := fr.Next()
		if err != nil {
			errc <- err
			return
		}
		done <- f
	}()

	for i := 0; i < len(bin); i++ {
		if _, err := pw.Write(bin[i : i+1]); err != nil {
			t.Fatalf("writing chunk %d: %v", i, err)
		}
	}
	pw.Close()

	select {
	case err := <-errc:
		t.Fatalf("Next: %v", err)
	case f := <-done:
		assertHeader(t, f, meta.Frame)
		resp, err := DecodeResponse(f.Payload)
		if err != nil {
			t.Fatalf("DecodeResponse: %v", err)
		}
		if resp.Type != ResponsePromiseError {
			t.Fatalf("type = %d, want PromiseError", resp.Type)
		}
	}
}

// TestTruncatedFrame ensures a short body is not reported as a clean EOF.
func TestTruncatedFrame(t *testing.T) {
	bin, _ := readFixture(t, "client-promise-large")
	truncated := bin[:len(bin)-1]
	fr := NewFrameReader(bytes.NewReader(truncated), 0)
	if _, err := fr.Next(); !errors.Is(err, ErrShortFrame) {
		t.Fatalf("Next error = %v, want ErrShortFrame", err)
	}
}

// TestPayloadLimit rejects oversized frames before allocating.
func TestPayloadLimit(t *testing.T) {
	bin, _ := readFixture(t, "client-promise-large")
	fr := NewFrameReader(bytes.NewReader(bin), 1024)
	if _, err := fr.Next(); !errors.Is(err, ErrPayloadTooLarge) {
		t.Fatalf("Next error = %v, want ErrPayloadTooLarge", err)
	}
}

// TestVQLRoundTrip covers boundary values with the encoder/decoder pair and
// pins the exact byte encoding for the well-known boundaries.
func TestVQLRoundTrip(t *testing.T) {
	cases := []struct {
		value uint32
		bytes []byte
	}{
		{0, []byte{0x00}},
		{1, []byte{0x01}},
		{127, []byte{0x7f}},
		{128, []byte{0x80, 0x01}},
		{300, []byte{0xac, 0x02}},
		{1 << 31, []byte{0x80, 0x80, 0x80, 0x80, 0x08}},
		{^uint32(0), []byte{0xff, 0xff, 0xff, 0xff, 0x0f}},
	}
	for _, tc := range cases {
		enc := appendVQL(nil, tc.value)
		if !bytes.Equal(enc, tc.bytes) {
			t.Fatalf("VQL(%d) = %x, want %x", tc.value, enc, tc.bytes)
		}
		r := &valueReader{data: enc}
		got, err := r.vql()
		if err != nil {
			t.Fatalf("decoding VQL(%d): %v", tc.value, err)
		}
		if got != tc.value {
			t.Fatalf("VQL round trip = %d, want %d", got, tc.value)
		}
	}
}

// TestIntValueBoundaries checks tag selection for integers: signed 32-bit
// values use tag 6, everything else falls back to tag 5 JSON like TypeScript.
func TestIntValueBoundaries(t *testing.T) {
	cases := []struct {
		name     string
		value    Value
		wantTag  byte
		wantText string
	}{
		{"zero", 0, tagInt, "\x00"},
		{"one", 1, tagInt, "\x01"},
		{"127", 127, tagInt, "\x7f"},
		{"128", 128, tagInt, "\x80\x01"},
		{"max-int32", int64(2147483647), tagInt, "\xff\xff\xff\xff\x07"},
		{"min-int32", int64(-2147483648), tagInt, "\x80\x80\x80\x80\x08"},
		{"2^31", int64(2147483648), tagObject, "\n2147483648"},
		{"max-uint32", uint64(^uint32(0)), tagObject, "\n4294967295"},
		{"negative", -5, tagInt, "\xfb\xff\xff\xff\x0f"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			enc, err := EncodeValue(tc.value)
			if err != nil {
				t.Fatalf("EncodeValue: %v", err)
			}
			if enc[0] != tc.wantTag {
				t.Fatalf("tag = %d, want %d (%x)", enc[0], tc.wantTag, enc)
			}
			if string(enc[1:]) != tc.wantText {
				t.Fatalf("body = %x, want %x", enc[1:], tc.wantText)
			}
			if _, err := DecodeValue(enc); err != nil {
				t.Fatalf("DecodeValue: %v", err)
			}
		})
	}

	// -1 in VQL: TypeScript converts via >>> 0, so it writes 0xffffffff.
	enc, err := EncodeValue(-1)
	if err != nil {
		t.Fatalf("EncodeValue(-1): %v", err)
	}
	want := append([]byte{tagInt}, 0xff, 0xff, 0xff, 0xff, 0x0f)
	if !bytes.Equal(enc, want) {
		t.Fatalf("EncodeValue(-1) = %x, want %x", enc, want)
	}
	got, err := DecodeValue(enc)
	if err != nil {
		t.Fatalf("DecodeValue(-1): %v", err)
	}
	if got != int64(-1) {
		t.Fatalf("decoded -1 = %v (%T), want int64(-1)", got, got)
	}
}

// TestValueRoundTrip exercises every tag through encode/decode/encode.
func TestValueRoundTrip(t *testing.T) {
	values := []Value{
		Undefined{},
		"",
		"plain",
		"unicode: 你好 😀 \u2028 \u2029 <&>",
		Buffer{Data: []byte{0, 1, 2, 255}},
		Buffer{Data: nil, VS: true},
		[]any{},
		[]any{"a", int64(1), Undefined{}, Buffer{Data: []byte{9}, VS: true}},
		json.RawMessage(`{"a":1,"b":[true,null,"x"]}`),
		json.RawMessage(`[]`),
		json.RawMessage(`null`),
		int64(0),
		int64(-7),
	}
	for i, v := range values {
		enc, err := EncodeValue(v)
		if err != nil {
			t.Fatalf("case %d EncodeValue: %v", i, err)
		}
		dec, err := DecodeValue(enc)
		if err != nil {
			t.Fatalf("case %d DecodeValue: %v", i, err)
		}
		reenc, err := EncodeValue(dec)
		if err != nil {
			t.Fatalf("case %d re-EncodeValue: %v", i, err)
		}
		if !bytes.Equal(enc, reenc) {
			t.Fatalf("case %d not byte-stable\n got: %x\nwant: %x", i, reenc, enc)
		}
	}
}

// TestRejectsMalformedPayloads covers structural validation.
func TestRejectsMalformedPayloads(t *testing.T) {
	if _, err := DecodeRequest([]byte{tagString, 1, 'x', tagUndefined}); err == nil {
		t.Fatal("string header should be rejected")
	}
	if _, err := DecodeRequest([]byte{tagArray, 1, tagInt, 0, tagUndefined}); err == nil {
		t.Fatal("single-element request header should be rejected")
	}
	if _, err := DecodeRequest([]byte{tagArray, 2, tagInt, 199, tagInt, 0, tagUndefined}); err == nil {
		t.Fatal("unknown request type should be rejected")
	}
	if _, err := DecodeValue([]byte{tagInt, 0, 0}); !errors.Is(err, ErrInvalidValue) {
		t.Fatalf("trailing bytes error = %v, want ErrInvalidValue", err)
	}
	if _, err := DecodeValue([]byte{tagObject, 0x02, '{', 'x'}); err == nil {
		t.Fatal("invalid JSON object payload should be rejected")
	}
}

// TestJSONBridge checks the runner-link mapping, including nested buffers and
// the reserved-key escape hatch.
func TestJSONBridge(t *testing.T) {
	cases := []struct {
		name  string
		value Value
		want  string
	}{
		{"undefined", Undefined{}, `{"$undef":true}`},
		{"buffer", Buffer{Data: []byte{1, 2, 3}, VS: true}, `{"$buf":"AQID","$vs":true}`},
		{"plain-buffer", Buffer{Data: []byte{4, 5}, VS: false}, `{"$buf":"BAU=","$vs":false}`},
		{"string", "hi", `"hi"`},
		{"int", int64(12), `12`},
		{"array", []any{"a", Undefined{}, Buffer{Data: []byte{7}, VS: true}}, `["a",{"$undef":true},{"$buf":"Bw==","$vs":true}]`},
		{"object", json.RawMessage(`{"k":1}`), `{"k":1}`},
		{"reserved-key", json.RawMessage(`{"$buf":"x"}`), `{"$esc":{"$buf":"x"}}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			raw, err := ToJSON(tc.value)
			if err != nil {
				t.Fatalf("ToJSON: %v", err)
			}
			if string(raw) != tc.want {
				t.Fatalf("ToJSON = %s, want %s", raw, tc.want)
			}
			back, err := FromJSON(raw)
			if err != nil {
				t.Fatalf("FromJSON: %v", err)
			}
			again, err := ToJSON(back)
			if err != nil {
				t.Fatalf("ToJSON after FromJSON: %v", err)
			}
			if string(again) != tc.want {
				t.Fatalf("round trip = %s, want %s", again, tc.want)
			}
		})
	}
}

// TestLoopback simulates the client/server handshake over an in-process byte
// buffer: the client request is encoded, decoded, answered, and the response
// is decoded back.
func TestLoopback(t *testing.T) {
	const (
		channel   = "demo"
		method    = "echo"
		requestID = 7
	)

	arg := json.RawMessage(`{"message":"ping","count":2}`)
	payload, err := EncodeRequest(RequestPromise, requestID, channel, method, arg)
	if err != nil {
		t.Fatalf("EncodeRequest: %v", err)
	}

	// Wire it as a Regular frame exactly like SocketProtocol.send does.
	var wire bytes.Buffer
	wire.Write(EncodeFrame(Frame{Type: MessageRegular, Payload: payload}))

	fr := NewFrameReader(&wire, 0)
	f, err := fr.Next()
	if err != nil {
		t.Fatalf("server reading frame: %v", err)
	}
	if f.Type != MessageRegular {
		t.Fatalf("frame type = %v, want Regular", f.Type)
	}

	req, err := DecodeRequest(f.Payload)
	if err != nil {
		t.Fatalf("DecodeRequest: %v", err)
	}
	if req.Type != RequestPromise || req.ID != requestID || req.Channel != channel || req.Name != method {
		t.Fatalf("decoded request = %+v", req)
	}

	// Server-side dispatch: echo the argument back.
	respPayload, err := EncodePromiseSuccess(req.ID, req.Arg)
	if err != nil {
		t.Fatalf("EncodePromiseSuccess: %v", err)
	}

	// Client-side decode.
	resp, err := DecodeResponse(respPayload)
	if err != nil {
		t.Fatalf("DecodeResponse: %v", err)
	}
	if resp.Type != ResponsePromiseSuccess || resp.ID != requestID {
		t.Fatalf("decoded response = %+v", resp)
	}
	body, ok := resp.Body.(json.RawMessage)
	if !ok || !bytes.Equal(body, arg) {
		t.Fatalf("response body = %T(%s), want %s", resp.Body, resp.Body, arg)
	}

	// And the reverse direction: server Initialize before any request.
	initPayload, err := EncodeInitialize()
	if err != nil {
		t.Fatalf("EncodeInitialize: %v", err)
	}
	initResp, err := DecodeResponse(initPayload)
	if err != nil {
		t.Fatalf("DecodeResponse(Initialize): %v", err)
	}
	if initResp.Type != ResponseInitialize {
		t.Fatalf("initialize type = %v", initResp.Type)
	}
}

// TestGoldenBodiesSurviveJSONBridge pushes every golden channel body through
// the runner-link bridge and requires the re-encoded payload to stay
// byte-identical. This is the guarantee the Go server relies on when it hands
// decoded arguments to JSON-based code and resumes the TS wire format.
func TestGoldenBodiesSurviveJSONBridge(t *testing.T) {
	for _, name := range fixtureNames(t) {
		bin, meta := readFixture(t, name)
		if meta.Expected.Kind != "request" && meta.Expected.Kind != "response" {
			continue
		}
		t.Run(name, func(t *testing.T) {
			f, err := NewFrameReader(bytes.NewReader(bin), 0).Next()
			if err != nil {
				t.Fatalf("decoding frame: %v", err)
			}

			body, err := decodeBody(f.Payload)
			if err != nil {
				t.Fatalf("decoding body: %v", err)
			}
			bridge, err := ToJSON(body)
			if err != nil {
				t.Fatalf("ToJSON: %v", err)
			}
			back, err := FromJSON(bridge)
			if err != nil {
				t.Fatalf("FromJSON: %v", err)
			}

			// Rebuild the payload with the bridged body and compare.
			var rebuilt []byte
			if meta.Expected.Kind == "request" {
				req, err := DecodeRequest(f.Payload)
				if err != nil {
					t.Fatalf("DecodeRequest: %v", err)
				}
				rebuilt, err = EncodeRequest(req.Type, req.ID, req.Channel, req.Name, back)
				if err != nil {
					t.Fatalf("EncodeRequest: %v", err)
				}
			} else {
				resp, err := DecodeResponse(f.Payload)
				if err != nil {
					t.Fatalf("DecodeResponse: %v", err)
				}
				resp.Body = back
				rebuilt, err = EncodeResponse(resp)
				if err != nil {
					t.Fatalf("EncodeResponse: %v", err)
				}
			}
			if !bytes.Equal(rebuilt, f.Payload) {
				t.Fatalf("bridge round trip changed payload\n got: %x\nwant: %x", rebuilt, f.Payload)
			}
		})
	}
}

// decodeBody returns the second value of a Channel payload.
func decodeBody(payload []byte) (Value, error) {
	_, rest, err := DecodeValuePrefix(payload)
	if err != nil {
		return nil, err
	}
	return DecodeValue(rest)
}

// TestFrameHeaderLayout pins the byte layout documented in protocol.ts.
func TestFrameHeaderLayout(t *testing.T) {
	f := Frame{Type: MessageKeepAlive, ID: 0x01020304, Ack: 0x05060708, Payload: []byte{0xaa, 0xbb}}
	got := EncodeFrame(f)
	want := []byte{
		9,
		1, 2, 3, 4,
		5, 6, 7, 8,
		0, 0, 0, 2,
		0xaa, 0xbb,
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("EncodeFrame = %x, want %x", got, want)
	}
	if binary.BigEndian.Uint32(got[9:13]) != 2 {
		t.Fatalf("length field = %d, want 2", binary.BigEndian.Uint32(got[9:13]))
	}
}
