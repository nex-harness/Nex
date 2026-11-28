package browser

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/coder/websocket"
)

func TestBridgeServerRPC(t *testing.T) {
	srv, err := NewBridgeServer("127.0.0.1:0")
	if err != nil {
		t.Fatalf("start bridge server: %v", err)
	}
	defer srv.Close()

	if srv.Connected() {
		t.Fatalf("expected disconnected before extension connects")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// Simulate MV3 extension connecting to /ws/bridge
	wsURL := "ws://" + srv.Addr() + "/ws/bridge"
	conn, _, err := websocket.Dial(ctx, wsURL, nil)
	if err != nil {
		t.Fatalf("dial bridge: %v", err)
	}
	defer conn.Close(websocket.StatusNormalClosure, "")

	// Send hello
	_ = conn.Write(ctx, websocket.MessageText, []byte(`{"type":"hello","client":"nex-browser-ext"}`))

	// Wait briefly for server to register connection
	deadline := time.Now().Add(2 * time.Second)
	for !srv.Connected() && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if !srv.Connected() {
		t.Fatalf("expected bridge server to report connected")
	}

	// Extension responder loop
	go func() {
		_, data, err := conn.Read(ctx)
		if err != nil {
			return
		}
		var req bridgeRequest
		if err := json.Unmarshal(data, &req); err != nil {
			return
		}
		resp := bridgeResponse{
			ID:   req.ID,
			OK:   true,
			Data: json.RawMessage(`{"title":"Nex Test","elements":[{"ref":"e1","role":"button","name":"Go"}]}`),
		}
		raw, _ := json.Marshal(resp)
		_ = conn.Write(ctx, websocket.MessageText, raw)
	}()

	out, err := srv.Call(ctx, "snapshot", map[string]any{"max_elements": 50})
	if err != nil {
		t.Fatalf("bridge Call failed: %v", err)
	}
	var parsed map[string]any
	if err := json.Unmarshal(out, &parsed); err != nil {
		t.Fatalf("unmarshal result: %v", err)
	}
	if parsed["title"] != "Nex Test" {
		t.Fatalf("unexpected title: %v", parsed["title"])
	}
}
