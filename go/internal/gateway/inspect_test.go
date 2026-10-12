package gateway

// The frame-inspection tests pin the cost and the correctness of the router's only look at a
// frame's contents.
//
// The cost claim is not measurable in a unit test, so what is pinned here is the CONSEQUENCE: the
// router reads the header array and nothing else, which is only true if decoding a request with a
// large body succeeds while the body bytes are never touched. That is asserted by a body the
// codec would reject if it were decoded — a truncated large value — so a future change that
// decoded the body would fail here rather than silently getting slower.

import (
	"strings"
	"testing"

	"github.com/tylinux/nex/go/internal/rpc"
)

// TestInspectPayloadReadsOnlyTheHeader: a valid header with a body the codec could not decode is
// still routed, because the body is never read.
func TestInspectPayloadReadsOnlyTheHeader(t *testing.T) {
	// A header, then a body whose declared length runs past the end of the buffer. DecodeRequest
	// would fail on it; the router must not notice, because it stops after the header array.
	header := mustSerialize(t, []any{int64(rpc.RequestPromise), int64(7), "git", "status"})
	truncatedBody := []byte{0x01, 0xff, 0xff, 0xff, 0x0f} // a string tag with a huge length
	payload := append(append([]byte{}, header...), truncatedBody...)

	info := inspectPayload(payload)
	if !info.OK {
		t.Fatal("inspectPayload rejected a frame whose header is fine: the body must not be decoded")
	}
	if info.ChannelName != "git" || info.MethodName != "status" {
		t.Errorf("inspected %q.%s, want git.status", info.ChannelName, info.MethodName)
	}
	if info.RequestID != 7 || info.RequestType != rpc.RequestPromise {
		t.Errorf("id/type = %d/%d, want 7/%d", info.RequestID, info.RequestType, rpc.RequestPromise)
	}
}

// TestInspectPayloadRejectsNonChannelTraffic: a payload that is not a request header at all must
// come back not-OK, so the frame is dispatched locally and the codec decides — never forwarded on
// a guess.
func TestInspectPayloadRejectsNonChannelTraffic(t *testing.T) {
	cases := map[string][]byte{
		"empty":             {},
		"one byte":          {0x04},
		"undefined tag":     {0x00},
		"truncated arr":     {0x04, 0x02, 0x06},
		"array of one":      mustSerialize(t, []any{int64(rpc.RequestPromise)}),
		"not even an array": mustSerialize(t, "just a string"),
	}
	for name, payload := range cases {
		if info := inspectPayload(payload); info.OK {
			t.Errorf("%s: inspectPayload = %+v, want OK=false", name, info)
		}
	}
}

// TestInspectPayloadCancelCarriesNoChannel is the property the cancel routing depends on: a 101
// frame has [type, id] and NOTHING else, so the router cannot re-derive the channel and must use
// the id→route map instead.
func TestInspectPayloadCancelCarriesNoChannel(t *testing.T) {
	for _, kind := range []int{rpc.RequestPromiseCancel, rpc.RequestEventDispose} {
		payload, err := rpc.EncodeCancelOrDispose(kind, 12)
		if err != nil {
			t.Fatalf("encode %d: %v", kind, err)
		}
		info := inspectPayload(payload)
		if !info.OK {
			t.Fatalf("kind %d: inspectPayload rejected a cancel", kind)
		}
		if info.RequestType != kind || info.RequestID != 12 {
			t.Errorf("kind %d: id/type = %d/%d, want 12/%d", kind, info.RequestID, info.RequestType, kind)
		}
		if info.ChannelName != "" {
			t.Errorf("kind %d: channel = %q, want empty (a cancel carries no channel)", kind, info.ChannelName)
		}
	}
}

// TestIsInitializeMessageSwallowsOnlyTheHandshake: the filter that keeps the client from seeing
// two Initialize frames must not swallow anything else. A success, an event and a non-Regular
// frame all have to pass.
func TestIsInitializeMessageSwallowsOnlyTheHandshake(t *testing.T) {
	swallowed := mustFrame(t, mustEncodeResponse(t, rpc.ResponseInitialize, 0, nil))
	if !isInitializeMessage(swallowed) {
		t.Error("an Initialize frame was not recognized, so the upstream's would reach the client")
	}

	passed := map[string][]byte{
		"success": mustFrame(t, mustEncodeResponse(t, rpc.ResponsePromiseSuccess, 1, "ok")),
		"promise error": mustFrame(t, mustEncodeResponse(t, rpc.ResponsePromiseError, 1,
			rpc.NewObject("message", "boom"))),
		"event":      mustFrame(t, mustEncodeResponse(t, rpc.ResponseEventFire, 2, "tick")),
		"ack":        encodeFrameBytes(rpc.MsgAck, 3, []byte{1, 2, 3}),
		"keep alive": encodeFrameBytes(rpc.MsgKeepAlive, 0, nil),
		"empty":      {},
		"short":      {1, 2},
	}
	for name, frame := range passed {
		if isInitializeMessage(frame) {
			t.Errorf("%s: swallowed as an Initialize", name)
		}
	}
}

// TestRawObjectEntriesFindsRepeats is the duplicate-key detector's own test: encoding/json keeps
// the last of two identical keys silently, so this is the only place that knows both were there.
func TestRawObjectEntriesFindsRepeats(t *testing.T) {
	cases := map[string]struct {
		raw     string
		want    int
		wantErr bool
	}{
		"absent field":  {raw: "", want: 0},
		"null":          {raw: "null", want: 0},
		"one key":       {raw: `{"file":"go"}`, want: 1},
		"repeated key":  {raw: `{"file":"go","file":"node"}`, want: 2},
		"distinct keys": {raw: `{"file":"go","git":"node"}`, want: 2},
		"nested value":  {raw: `{"http":{"a":"go"}}`, want: 1},
		"not an object": {raw: `[1,2]`, wantErr: true},
		// "trailing value" is NOT rejected here: a trailing token inside the scanned field is
		// impossible for a well-formed document, and the enclosing parse (json.Unmarshal into the
		// raw map in checkDuplicateKeys) is what rejects it. This scanner only has to report keys.
		"escaped key": {raw: `{"we\"ird":"go"}`, want: 1},
	}
	for name, tc := range cases {
		got, err := rawObjectEntries([]byte(tc.raw))
		if tc.wantErr {
			if err == nil {
				t.Errorf("%s: rawObjectEntries(%q) was accepted", name, tc.raw)
			}
			continue
		}
		if err != nil {
			t.Errorf("%s: rawObjectEntries(%q): %v", name, tc.raw, err)
			continue
		}
		if len(got) != tc.want {
			t.Errorf("%s: rawObjectEntries(%q) = %v (%d keys), want %d", name, tc.raw, got, len(got), tc.want)
		}
	}
}

// TestRawObjectEntriesKeepsNestedStringsWhole: a key containing a quote must survive, so a
// duplicate report names the key the operator actually wrote.
func TestRawObjectEntriesKeepsNestedStringsWhole(t *testing.T) {
	got, err := rawObjectEntries([]byte(`{"we\"ird":"go"}`))
	if err != nil {
		t.Fatalf("rawObjectEntries: %v", err)
	}
	if len(got) != 1 || !strings.Contains(got[0], "ird") {
		t.Errorf("rawObjectEntries = %v, want the escaped key decoded", got)
	}
}

// mustSerialize encodes a value the way a request header is serialized.
func mustSerialize(t *testing.T, value any) []byte {
	t.Helper()
	payload, err := rpc.Marshal(value)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return payload
}

// mustEncodeResponse encodes a response payload.
func mustEncodeResponse(t *testing.T, kind, id int, body any) []byte {
	t.Helper()
	payload, err := rpc.EncodeResponse(rpc.ResponseHeader{Type: kind, ID: id}, body)
	if err != nil {
		t.Fatalf("encode response: %v", err)
	}
	return payload
}

// mustFrame wraps a payload in a Regular ProtocolMessage frame.
func mustFrame(t *testing.T, payload []byte) []byte {
	t.Helper()
	return encodeFrameBytes(rpc.MsgRegular, 0, payload)
}
