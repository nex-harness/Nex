package agent

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/tylinux/nex/go/internal/protocol"
)

func TestUnknownMethodIsMethodNotFound(t *testing.T) {
	_, err := NewHandler().HandleRequest(context.Background(), protocol.Message{Method: "session/create"})
	rpc, ok := err.(*protocol.RPCError)
	if !ok || rpc.Code != protocol.CodeMethodNotFound || rpc.Message != "Method not found: session/create" {
		t.Fatalf("got %#v", err)
	}
}

func TestCapabilitiesAnswer(t *testing.T) {
	out, err := NewHandler().HandleRequest(context.Background(), protocol.Message{Method: MethodRuntimeCapabilities})
	if err != nil || string(out) != `{"independentPlanState":true}` {
		t.Fatalf("got %s, %v", out, err)
	}
}

func TestStorageNotifierEnvelopeAndSequence(t *testing.T) {
	n := &StorageStateNotifier{AttemptID: "attempt-1", DatabaseID: "db-1"}
	var seqs []int
	for i := 0; i < 3; i++ {
		msg, err := n.Notification(map[string]any{"phase": "checking", "elapsedMs": 0})
		if err != nil {
			t.Fatal(err)
		}
		if msg.Kind != protocol.KindNotification || msg.Method != NotifyStorageState {
			t.Fatalf("not a storageState notification: %+v", msg)
		}
		var p map[string]any
		if err := json.Unmarshal(msg.Params, &p); err != nil {
			t.Fatal(err)
		}
		if p["attemptId"] != "attempt-1" || p["databaseId"] != "db-1" || p["databaseKind"] != "session" || p["schemaVersion"] != float64(1) {
			t.Fatalf("envelope fields wrong: %v", p)
		}
		seqs = append(seqs, int(p["sequence"].(float64)))
	}
	if seqs[0] != 1 || seqs[1] != 2 || seqs[2] != 3 {
		t.Errorf("sequence must count up from 1: %v", seqs)
	}
	// The line the notifier produces must pass the same strict envelope check as the host's.
	msg, _ := n.Notification(map[string]any{"phase": "ready", "elapsedMs": 1})
	line, err := protocol.Encode(msg)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := protocol.ParseMessage(line); err != nil {
		t.Fatalf("own notification rejected by the envelope parser: %v", err)
	}
}
