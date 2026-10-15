// usagestats_test.go (step 3.8): the forwarder contract — exact passthrough
// of range/timeZone, no event surface, and the channel dispatch.
package usagestats

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/tylinux/nex/go/internal/rpc"
)

type fakeAgent struct {
	lastRequest Request
}

func (f *fakeAgent) GetAppUsageStats(_ context.Context, request Request) (json.RawMessage, error) {
	f.lastRequest = request
	return json.RawMessage(`{"summary":{"totalSessions":3}}`), nil
}

func TestGetAppUsageSnapshotForwardsRequest(t *testing.T) {
	agent := &fakeAgent{}
	service := New(agent)
	snapshot, err := service.GetAppUsageSnapshot(context.Background(), Request{Range: "7d", TimeZone: "Asia/Shanghai"})
	if err != nil {
		t.Fatal(err)
	}
	if agent.lastRequest.Range != "7d" || agent.lastRequest.TimeZone != "Asia/Shanghai" {
		t.Fatalf("request = %+v", agent.lastRequest)
	}
	var decoded map[string]any
	if err := json.Unmarshal(snapshot, &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded["summary"] == nil {
		t.Fatalf("snapshot = %s", snapshot)
	}
}

func TestChannelDispatch(t *testing.T) {
	agent := &fakeAgent{}
	channel := &Channel{Service: New(agent)}
	value, err := channel.Call(context.Background(), "getAppUsageSnapshot", []rpc.Value{
		map[string]any{"range": "30d", "timeZone": "UTC"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if agent.lastRequest.Range != "30d" {
		t.Fatalf("request = %+v", agent.lastRequest)
	}
	if value == nil {
		t.Fatal("nil snapshot")
	}
	// unknown method
	if _, err := channel.Call(context.Background(), "unknown", nil); err == nil {
		t.Fatal("unknown method must error")
	}
	// no events
	if _, _, err := channel.Listen(context.Background(), "onDynamicProgress", nil); err == nil {
		t.Fatal("no event surface exists")
	}
}
