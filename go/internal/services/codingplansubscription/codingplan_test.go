// channel.go + tests for the codingplansubscription package.

package codingplansubscription

import (
	"context"
	"errors"
	"net/url"
	"sync/atomic"
	"testing"
	"time"
)

// ---------------------------------------------------------------------------
// tests
// ---------------------------------------------------------------------------

type fakeClient struct {
	calls  int32
	status int
	body   []byte
	err    error
}

func (f *fakeClient) GetJSON(ctx context.Context, target *url.URL) (int, []byte, error) {
	atomic.AddInt32(&f.calls, 1)
	return f.status, f.body, f.err
}

func newService(client HTTPClient, env map[string]string) *Service {
	return NewService(Options{
		Client:     client,
		Env:        func() map[string]string { return env },
		AppVersion: "1.0.0",
		Platform:   "linux-amd64",
	})
}

func TestOverrideEnvWinsBeforeNetwork(t *testing.T) {
	client := &fakeClient{}
	svc := newService(client, map[string]string{DynamicWorkflowModeEnv: "alwaysOn"})
	config, err := svc.GetDynamicWorkflowClientConfig(context.Background(), false)
	if err != nil {
		t.Fatal(err)
	}
	if config.Mode != "alwaysOn" || !config.Enabled || config.Source != "override" {
		t.Fatalf("override wrong: %+v", config)
	}
	if calls := atomic.LoadInt32(&client.calls); calls != 0 {
		t.Fatalf("override must not hit the network, calls=%d", calls)
	}
	// Invalid override values are ignored (normalizeDynamicWorkflowMode).
	svc = newService(client, map[string]string{DynamicWorkflowModeEnv: "sometimes"})
	config, err = svc.GetDynamicWorkflowClientConfig(context.Background(), false)
	if err != nil {
		t.Fatal(err)
	}
	if calls := atomic.LoadInt32(&client.calls); calls != 1 {
		t.Fatalf("invalid override must fall through to remote, calls=%d", calls)
	}
	_ = config
}

func TestRemoteModeAndCache(t *testing.T) {
	client := &fakeClient{status: 200, body: []byte(`{"data":{"configs":{"dynamicWorkflow":{"mode":"onDemand"}}}}`)}
	svc := newService(client, map[string]string{})
	config, err := svc.GetDynamicWorkflowClientConfig(context.Background(), false)
	if err != nil {
		t.Fatal(err)
	}
	if config.Mode != "onDemand" || !config.Enabled || config.Source != "remote" {
		t.Fatalf("remote wrong: %+v", config)
	}
	// Cached: no second call.
	if _, err := svc.GetDynamicWorkflowClientConfig(context.Background(), false); err != nil {
		t.Fatal(err)
	}
	if calls := atomic.LoadInt32(&client.calls); calls != 1 {
		t.Fatalf("cache miss: calls=%d", calls)
	}
	// forceRefresh re-fetches.
	if _, err := svc.GetDynamicWorkflowClientConfig(context.Background(), true); err != nil {
		t.Fatal(err)
	}
	if calls := atomic.LoadInt32(&client.calls); calls != 2 {
		t.Fatalf("forceRefresh: calls=%d", calls)
	}
}

func TestRemoteDisabledAndMissingKey(t *testing.T) {
	// Explicit disabled.
	client := &fakeClient{status: 200, body: []byte(`{"data":{"configs":{"dynamicWorkflow":{"mode":"disabled"}}}}`)}
	svc := newService(client, map[string]string{})
	config, _ := svc.GetDynamicWorkflowClientConfig(context.Background(), false)
	if config.Mode != "disabled" || config.Enabled {
		t.Fatalf("disabled wrong: %+v", config)
	}
	// Missing key = disabled (服务端撤掉 key 等于关闭).
	client2 := &fakeClient{status: 200, body: []byte(`{"data":{"configs":{}}}`)}
	svc2 := newService(client2, map[string]string{})
	config2, _ := svc2.GetDynamicWorkflowClientConfig(context.Background(), false)
	if config2.Mode != "disabled" || config2.Source != "default" {
		t.Fatalf("missing key wrong: %+v", config2)
	}
	// Illegal remote value = default.
	client3 := &fakeClient{status: 200, body: []byte(`{"data":{"configs":{"dynamicWorkflow":{"mode":"sometimes"}}}}`)}
	svc3 := newService(client3, map[string]string{})
	config3, _ := svc3.GetDynamicWorkflowClientConfig(context.Background(), false)
	if config3.Mode != "disabled" || config3.Source != "default" {
		t.Fatalf("illegal remote wrong: %+v", config3)
	}
}

func TestFailClosedOnRequestError(t *testing.T) {
	client := &fakeClient{err: errors.New("network down")}
	svc := newService(client, map[string]string{})
	config, err := svc.GetDynamicWorkflowClientConfig(context.Background(), false)
	if err != nil {
		t.Fatalf("fail-closed must not propagate the error: %v", err)
	}
	if config.Mode != "disabled" || config.Source != "default" {
		t.Fatalf("fail-closed wrong: %+v", config)
	}
	// HTTP errors also fail closed.
	client2 := &fakeClient{status: 503, body: []byte(`{}`)}
	svc2 := newService(client2, map[string]string{})
	config2, err := svc2.GetDynamicWorkflowClientConfig(context.Background(), false)
	if err != nil || config2.Mode != "disabled" {
		t.Fatalf("HTTP fail-closed wrong: %+v %v", config2, err)
	}
}

func TestSnapshotTTLExpiry(t *testing.T) {
	// One service, a moving clock: the first fetch caches; reads before the
	// TTL hit the cache; after the TTL the next read re-fetches.
	client := &fakeClient{status: 200, body: []byte(`{"data":{"configs":{"dynamicWorkflow":{"mode":"alwaysOn"}}}}`)}
	now := time.Now()
	svc := NewService(Options{
		Client:     client,
		Env:        func() map[string]string { return map[string]string{} },
		AppVersion: "1",
		Platform:   "p",
		Now:        func() time.Time { return now },
	})
	if _, err := svc.GetDynamicWorkflowClientConfig(context.Background(), false); err != nil {
		t.Fatal(err)
	}
	if calls := atomic.LoadInt32(&client.calls); calls != 1 {
		t.Fatalf("first fetch: calls=%d", calls)
	}
	// Advance the clock past the TTL (1h): the next read re-fetches.
	svc.SetNowForTests(func() time.Time { return now.Add(2 * time.Hour) })
	if _, err := svc.GetDynamicWorkflowClientConfig(context.Background(), false); err != nil {
		t.Fatal(err)
	}
	if calls := atomic.LoadInt32(&client.calls); calls != 2 {
		t.Fatalf("TTL expiry must re-fetch: calls=%d", calls)
	}
}

func TestConcurrentRequestsSingleFlight(t *testing.T) {
	client := &fakeClient{status: 200, body: []byte(`{"data":{"configs":{"dynamicWorkflow":{"mode":"onDemand"}}}}`)}
	svc := newService(client, map[string]string{})
	done := make(chan ClientConfig, 4)
	for i := 0; i < 4; i++ {
		go func() {
			config, err := svc.GetDynamicWorkflowClientConfig(context.Background(), false)
			if err != nil {
				t.Errorf("get: %v", err)
			}
			done <- config
		}()
	}
	for i := 0; i < 4; i++ {
		select {
		case config := <-done:
			if config.Mode != "onDemand" {
				t.Fatalf("config wrong: %+v", config)
			}
		case <-time.After(3 * time.Second):
			t.Fatal("single-flight get hung")
		}
	}
}
