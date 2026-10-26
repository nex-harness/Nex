package local_test

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/nex-harness/nex/server/internal/local"
	"github.com/nex-harness/nex/server/internal/rpcwire"
)

func getSettings(t *testing.T, settings *local.Settings) map[string]json.RawMessage {
	t.Helper()
	value, err := settings.Call(context.Background(), &local.Env{}, "get", rpcwire.Undefined{})
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	raw, ok := value.(json.RawMessage)
	if !ok {
		t.Fatalf("get returned %T, want json.RawMessage", value)
	}
	var decoded map[string]json.RawMessage
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatalf("decoding settings: %v", err)
	}
	return decoded
}

func TestSettingsDefaultsAndPatchPersistence(t *testing.T) {
	dataDir := t.TempDir()
	routes := []string{"/workspace/a", "/workspace/b"}
	settings := local.NewSettings(dataDir, func() []string { return routes })

	defaults := getSettings(t, settings)
	if string(defaults["locale"]) != `"zh-CN"` {
		t.Fatalf("locale = %s, want zh-CN", defaults["locale"])
	}
	if string(defaults["nexInteractionBehavior"]) != `"queue"` {
		t.Fatalf("nexInteractionBehavior = %s", defaults["nexInteractionBehavior"])
	}
	// Routed workspace paths are injected into recentProjects at read time.
	var recent []string
	if err := json.Unmarshal(defaults["recentProjects"], &recent); err != nil {
		t.Fatalf("recentProjects: %v", err)
	}
	if len(recent) != 2 || recent[0] != "/workspace/a" || recent[1] != "/workspace/b" {
		t.Fatalf("recentProjects = %v, want routed paths", recent)
	}

	update := json.RawMessage(`{"recentProjects":["/user/one","/workspace/a"],"locale":"en-US"}`)
	if _, err := settings.Call(context.Background(), &local.Env{}, "update", update); err != nil {
		t.Fatalf("update: %v", err)
	}

	effective := getSettings(t, settings)
	if string(effective["locale"]) != `"en-US"` {
		t.Fatalf("locale after update = %s", effective["locale"])
	}
	if err := json.Unmarshal(effective["recentProjects"], &recent); err != nil {
		t.Fatalf("recentProjects: %v", err)
	}
	want := []string{"/workspace/a", "/workspace/b", "/user/one"}
	if len(recent) != len(want) {
		t.Fatalf("recentProjects = %v, want %v", recent, want)
	}
	for i := range want {
		if recent[i] != want[i] {
			t.Fatalf("recentProjects = %v, want %v", recent, want)
		}
	}

	// Persisted file contains the user value only (no routed paths).
	raw, err := os.ReadFile(filepath.Join(dataDir, "settings.json"))
	if err != nil {
		t.Fatalf("reading settings file: %v", err)
	}
	var persisted map[string]json.RawMessage
	if err := json.Unmarshal(raw, &persisted); err != nil {
		t.Fatalf("decoding settings file: %v", err)
	}
	if _, ok := persisted["locale"]; !ok {
		t.Fatalf("settings file missing locale: %s", raw)
	}
	var persistedRecent []string
	if err := json.Unmarshal(persisted["recentProjects"], &persistedRecent); err != nil {
		t.Fatalf("persisted recentProjects: %v", err)
	}
	if len(persistedRecent) != 2 || persistedRecent[0] != "/user/one" {
		t.Fatalf("persisted recentProjects = %v, want user-only list", persistedRecent)
	}

	// A new instance reads the same file (persistence survives restarts).
	restarted := local.NewSettings(dataDir, func() []string { return nil })
	reloaded := getSettings(t, restarted)
	if string(reloaded["locale"]) != `"en-US"` {
		t.Fatalf("restarted locale = %s, want en-US", reloaded["locale"])
	}
	if string(reloaded["terminalInheritSystemProfile"]) != "true" {
		t.Fatalf("defaults are not merged over persisted values")
	}
}

func TestSettingsUnknownMethod(t *testing.T) {
	settings := local.NewSettings(t.TempDir(), nil)
	_, err := settings.Call(context.Background(), &local.Env{}, "nope", rpcwire.Undefined{})
	if err == nil {
		t.Fatal("expected an error for an unknown method")
	}
	var notFound *local.MethodNotFoundError
	if _, ok := err.(*local.MethodNotFoundError); !ok {
		t.Fatalf("error = %T (%v), want *MethodNotFoundError", err, notFound)
	}
}

func TestBroadcastFanOutAndClaims(t *testing.T) {
	channel := local.NewBroadcast()
	env := &local.Env{ConnectionID: "conn-1"}
	received := make(chan rpcwire.Value, 1)
	if _, err := channel.Listen(context.Background(), env, "onMessage", rpcwire.Undefined{}, func(value rpcwire.Value) {
		received <- value
	}); err != nil {
		t.Fatalf("listen: %v", err)
	}

	if _, err := channel.Call(context.Background(), env, "send", json.RawMessage(`{"channel":"state:theme","payload":"dark"}`)); err != nil {
		t.Fatalf("send: %v", err)
	}
	select {
	case value := <-received:
		raw, ok := value.(json.RawMessage)
		if !ok {
			t.Fatalf("event value = %T", value)
		}
		var message local.BroadcastMessage
		if err := json.Unmarshal(raw, &message); err != nil {
			t.Fatalf("decoding message: %v", err)
		}
		if message.Channel != "state:theme" || string(message.Payload) != `"dark"` {
			t.Fatalf("message = %+v", message)
		}
	default:
		t.Fatal("broadcast event was not delivered")
	}

	// The client sends positional args for claims: acquireClaim("k1") arrives
	// as ["k1"].
	first, err := channel.Call(context.Background(), env, "tryClaim", json.RawMessage(`["k1"]`))
	if err != nil {
		t.Fatalf("tryClaim: %v", err)
	}
	if first != true {
		t.Fatalf("first tryClaim = %v, want true", first)
	}
	second, err := channel.Call(context.Background(), env, "tryClaim", json.RawMessage(`["k1"]`))
	if err != nil {
		t.Fatalf("tryClaim: %v", err)
	}
	if second != false {
		t.Fatalf("second tryClaim = %v, want false", second)
	}

	// Unknown events are an empty event (no error).
	if _, err := channel.Listen(context.Background(), env, "onNope", rpcwire.Undefined{}, func(rpcwire.Value) {}); err != nil {
		t.Fatalf("unknown event: %v", err)
	}
}

func TestWindowControllerSubscribeEmitsEmptySnapshot(t *testing.T) {
	controller := local.NewWindowController(nil, nil)
	env := &local.Env{ConnectionID: "conn-1"}
	frames := make(chan rpcwire.Value, 4)
	if _, err := controller.Listen(context.Background(), env, "onDynamicControllerFrame", rpcwire.Undefined{}, func(value rpcwire.Value) {
		frames <- value
	}); err != nil {
		t.Fatalf("listen: %v", err)
	}

	ack, err := controller.Call(context.Background(), env, "subscribeControllerV4",
		json.RawMessage(`[{"topic":"controller/tasks-index","visibility":"foreground"}]`))
	if err != nil {
		t.Fatalf("subscribe: %v", err)
	}
	ackRaw, ok := ack.(json.RawMessage)
	if !ok || !json.Valid(ackRaw) {
		t.Fatalf("ack = %T %v", ack, ack)
	}
	var parsedAck struct {
		Ack struct {
			SubscriptionID string `json:"subscriptionId"`
			Mode           string `json:"mode"`
			LogEpoch       string `json:"logEpoch"`
		} `json:"ack"`
	}
	if err := json.Unmarshal(ackRaw, &parsedAck); err != nil {
		t.Fatalf("decoding ack: %v", err)
	}
	if parsedAck.Ack.SubscriptionID == "" || parsedAck.Ack.Mode != "snapshot" || parsedAck.Ack.LogEpoch == "" {
		t.Fatalf("ack = %+v", parsedAck)
	}

	select {
	case value := <-frames:
		frameRaw, ok := value.(json.RawMessage)
		if !ok {
			t.Fatalf("frame = %T", value)
		}
		var frame struct {
			Topic          string `json:"topic"`
			SubscriptionID string `json:"subscriptionId"`
			FromSeq        int    `json:"fromSeq"`
			Payload        struct {
				Kind     string `json:"kind"`
				Snapshot struct {
					ProtocolVersion int   `json:"protocolVersion"`
					Tasks           []any `json:"tasks"`
				} `json:"snapshot"`
			} `json:"payload"`
		}
		if err := json.Unmarshal(frameRaw, &frame); err != nil {
			t.Fatalf("decoding frame: %v", err)
		}
		if frame.Topic != "controller/tasks-index" || frame.SubscriptionID != parsedAck.Ack.SubscriptionID {
			t.Fatalf("frame = %+v", frame)
		}
		if frame.FromSeq != 0 || frame.Payload.Kind != "snapshot" || frame.Payload.Snapshot.ProtocolVersion != 1 {
			t.Fatalf("frame payload = %+v", frame.Payload)
		}
	default:
		t.Fatal("no controller snapshot was emitted")
	}

	// Subscribing without a listen is a no-op for frames, but still ACKs.
	if _, err := controller.Call(context.Background(), &local.Env{ConnectionID: "other"}, "subscribeControllerV4",
		json.RawMessage(`[{"topic":"controller/workspaces"}]`)); err != nil {
		t.Fatalf("subscribe without listen: %v", err)
	}
}

// A resync carries only the subscriptionId; the controller must remember which
// topic that subscription belongs to instead of defaulting to tasks-index.
func TestWindowControllerResyncKeepsTopic(t *testing.T) {
	controller := local.NewWindowController(nil, nil)
	env := &local.Env{ConnectionID: "conn-resync"}
	frames := make(chan rpcwire.Value, 8)
	if _, err := controller.Listen(context.Background(), env, "onDynamicControllerFrame", rpcwire.Undefined{}, func(value rpcwire.Value) {
		frames <- value
	}); err != nil {
		t.Fatalf("listen: %v", err)
	}

	ack, err := controller.Call(context.Background(), env, "subscribeControllerV4",
		json.RawMessage(`[{"topic":"controller/workspaces"}]`))
	if err != nil {
		t.Fatalf("subscribe: %v", err)
	}
	ackRaw := ack.(json.RawMessage)
	var parsed struct {
		Ack struct {
			SubscriptionID string `json:"subscriptionId"`
		} `json:"ack"`
	}
	if err := json.Unmarshal(ackRaw, &parsed); err != nil {
		t.Fatalf("decode ack: %v", err)
	}
	<-frames // initial snapshot

	if _, err := controller.Call(context.Background(), env, "resyncControllerV4",
		json.RawMessage(`[{"subscriptionId":"`+parsed.Ack.SubscriptionID+`","base":null,"forceSnapshot":true}]`)); err != nil {
		t.Fatalf("resync: %v", err)
	}
	var frame struct {
		Topic string `json:"topic"`
	}
	if err := json.Unmarshal(framesValue(t, frames), &frame); err != nil {
		t.Fatalf("decode frame: %v", err)
	}
	if frame.Topic != "controller/workspaces" {
		t.Fatalf("resync topic = %q, want controller/workspaces", frame.Topic)
	}
}

func framesValue(t *testing.T, frames chan rpcwire.Value) json.RawMessage {
	t.Helper()
	select {
	case value := <-frames:
		raw, ok := value.(json.RawMessage)
		if !ok {
			t.Fatalf("frame = %T", value)
		}
		return raw
	case <-time.After(time.Second):
		t.Fatal("no frame emitted")
		return nil
	}
}

// TestRootServiceStubs covers the channels Root mounts unconditionally:
// `onboarding-record.getLatestEntry` (OccupationOnboarding prefill) and
// `bots.syncAppRuntimePreferences` (settings sync). Both used to be unknown
// channels, which logged production warnings on every page load.
func TestRootServiceStubs(t *testing.T) {
	channels := local.StubChannels()
	record, ok := channels["onboarding-record"]
	if !ok {
		t.Fatal("onboarding-record stub is not registered")
	}
	value, err := record.Call(context.Background(), &local.Env{}, "getLatestEntry", rpcwire.Undefined{})
	if err != nil {
		t.Fatalf("getLatestEntry: %v", err)
	}
	if string(value.(json.RawMessage)) != "null" {
		t.Fatalf("getLatestEntry = %s, want null", value)
	}
	if _, err := record.Call(context.Background(), &local.Env{}, "appendRecord",
		json.RawMessage(`["device",{"occupation":"developer"}]`)); err == nil {
		t.Fatal("appendRecord should be rejected on a server with no record store")
	}

	bots, ok := channels["bots"]
	if !ok {
		t.Fatal("bots stub is not registered")
	}
	if _, err := bots.Call(context.Background(), &local.Env{}, "syncAppRuntimePreferences",
		json.RawMessage(`[{"askUserQuestionAutoResolutionEnabled":true}]`)); err != nil {
		t.Fatalf("syncAppRuntimePreferences: %v", err)
	}

	// Unknown methods on these channels stay distinguishable from "channel missing".
	if _, err := bots.Call(context.Background(), &local.Env{}, "saveBot", rpcwire.Undefined{}); err == nil {
		t.Fatal("unknown bots method should fail")
	}
}
