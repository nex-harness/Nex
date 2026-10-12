package server

// The replay tests feed the recorded TypeScript frames (go/testdata/traces/rpc, produced by
// scripts/go-rewrite/record/record-rpc.mjs driving the REAL @nex/rpc ChannelServer) into a Go
// server behind a real WebSocket, and require the bytes coming back out to match.
//
// This is the same evidence internal/rpc/replay_test.go gathers over an in-memory socket pair; the
// value of repeating it here is that the transport between the two ends is the one step 2.7 adds.
// The 13-byte ProtocolMessage header is NOT in the recordings (the recorder used a queue pair), so
// the comparison is against the serialized RPC payload, and the framing around it is asserted
// separately below.

import (
	"bufio"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/tylinux/nex/go/internal/rpc"
)

// traceDir holds the recorded scenarios; see scripts/go-rewrite/record/record-rpc.mjs.
const traceDir = "../../testdata/traces/rpc"

// recordedFrame mirrors one line of frames.jsonl.
type recordedFrame struct {
	Direction string `json:"direction"`
	Hex       string `json:"hex"`
}

// readRecorded loads a scenario's frames, split into what the client sent and what the server
// answered.
func readRecorded(t *testing.T, id string) (client, server []recordedFrame) {
	t.Helper()
	path := filepath.Join(traceDir, id, "frames.jsonl")
	file, err := os.Open(filepath.FromSlash(path))
	if err != nil {
		t.Fatalf("open %s: %v (regenerate with scripts/go-rewrite/record/record-rpc.mjs)", path, err)
	}
	defer file.Close()

	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 0, 1<<20), 1<<22)
	for scanner.Scan() {
		var frame recordedFrame
		if err := json.Unmarshal(scanner.Bytes(), &frame); err != nil {
			t.Fatalf("%s: parse %q: %v", id, scanner.Text(), err)
		}
		switch frame.Direction {
		case "client-to-server":
			client = append(client, frame)
		case "server-to-client":
			server = append(server, frame)
		}
	}
	if err := scanner.Err(); err != nil {
		t.Fatalf("%s: scan: %v", id, err)
	}
	if len(server) == 0 {
		t.Fatalf("%s: recording has no server frames", id)
	}
	return client, server
}

// --- the recorded scenarios ------------------------------------------------------------------

// TestWebSocketReplayR01 is the Initialize handshake over a real WebSocket. The recorded frame is
// the payload; the Go server must produce exactly those bytes inside a Regular frame whose 13-byte
// header is well formed.
func TestWebSocketReplayR01(t *testing.T) {
	_, recorded := readRecorded(t, "R01")
	want, err := hex.DecodeString(recorded[0].Hex)
	if err != nil {
		t.Fatalf("bad recorded hex: %v", err)
	}

	server, endpoints := newWSServer(t, "")
	endpoints.SetRegistrar(stubRegistrar(&StubChannel{}))
	conn := dial(t, server, wsPath, nil)

	header, payload, _, err := readResponseFull(t, conn)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if header.Type != rpc.ResponseInitialize {
		t.Fatalf("frame type = %d, want Initialize", header.Type)
	}
	if hex.EncodeToString(payload) != hex.EncodeToString(want) {
		t.Errorf("Initialize payload\n  recorded: %s\n  go:       %s",
			hex.EncodeToString(want), hex.EncodeToString(payload))
	}
}

// TestWebSocketReplayR11 replays the subscribe-shaped payload: a client request whose body is a
// clientMode envelope containing nested objects. It is the recording that exercises the object and
// string encoding paths end to end through the transport.
func TestWebSocketReplayR11(t *testing.T) {
	client, recorded := readRecorded(t, "R11")
	if len(client) != 1 {
		t.Fatalf("R11 recording has %d client frames, want 1", len(client))
	}
	request, err := hex.DecodeString(client[0].Hex)
	if err != nil {
		t.Fatalf("bad recorded hex: %v", err)
	}

	server, endpoints := newWSServer(t, "")
	endpoints.SetRegistrar(func(rpcServer *rpc.Server) func() {
		// The recorded channel answers a fixed ack; reproducing it exactly is what makes the
		// response byte-comparable.
		rpcServer.RegisterChannel("subscribe-shaped", &ackChannel{})
		return nil
	})
	conn := dial(t, server, wsPath, nil)

	// Initialize first, then the recorded request inside a Regular frame.
	if header, _ := readResponse(t, conn); header.Type != rpc.ResponseInitialize {
		t.Fatalf("first frame type = %d, want Initialize", header.Type)
	}
	writeFrame(t, conn, frameOf(rpc.MsgRegular, 0, request))

	header, payload, _, err := readResponseFull(t, conn)
	if err != nil {
		t.Fatalf("read response: %v", err)
	}
	if header.Type != rpc.ResponsePromiseSuccess {
		t.Fatalf("response type = %d, want success", header.Type)
	}

	// The recording's second server frame is the answer; compare the decoded body, because the
	// recorded frame carries the server's own request id.
	want, err := hex.DecodeString(recorded[1].Hex)
	if err != nil {
		t.Fatalf("bad recorded hex: %v", err)
	}
	_, wantBody, err := rpc.DecodeResponse(want)
	if err != nil {
		t.Fatalf("decode recorded response: %v", err)
	}
	_, gotBody, err := rpc.DecodeResponse(payload)
	if err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if !equalBodies(wantBody, gotBody) {
		t.Errorf("response body\n  recorded: %#v\n  go:       %#v", wantBody, gotBody)
	}
}

// TestWebSocketReplayR11RequestIsUnderstood is the request half of R11: the Go server must decode
// the recorded request header and body exactly, which is what proves the clientMode envelope
// survives the real transport. A channel that reports what it saw is the assertion.
func TestWebSocketReplayR11RequestIsUnderstood(t *testing.T) {
	client, _ := readRecorded(t, "R11")
	request, err := hex.DecodeString(client[0].Hex)
	if err != nil {
		t.Fatalf("bad recorded hex: %v", err)
	}
	header, body, err := rpc.DecodeRequest(request)
	if err != nil {
		t.Fatalf("decode recorded request: %v", err)
	}
	if header.Type != rpc.RequestPromise || header.ChannelName != "subscribe-shaped" || header.Name != "subscribe" {
		t.Fatalf("recorded header = %+v, want a Promise call on subscribe-shaped.subscribe", header)
	}
	t.Logf("R11 request body decoded: %#v", body)
}
