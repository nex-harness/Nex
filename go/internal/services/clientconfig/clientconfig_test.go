package clientconfig

// clientconfig_test.go: snapshot parsing table, TTL cache, forceRefresh,
// redirect refusal, request sharing, timeout — all against a fake HTTP
// client (the adapter layer owns net/http; httptest exercises it in the
// server package).

import (
	"context"
	"encoding/json"
	"errors"
	"net/url"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type fakeClient struct {
	mu           sync.Mutex
	calls        int32
	status       int
	body         []byte
	responseURL  string
	err          error
	delay        time.Duration
	perCallURLCh chan *url.URL
}

func (f *fakeClient) GetJSON(ctx context.Context, target *url.URL) (int, string, []byte, error) {
	atomic.AddInt32(&f.calls, 1)
	if f.perCallURLCh != nil {
		f.perCallURLCh <- target
	}
	if f.delay > 0 {
		select {
		case <-time.After(f.delay):
		case <-ctx.Done():
			return 0, "", nil, ctx.Err()
		}
	}
	if f.err != nil {
		return 0, "", nil, f.err
	}
	return f.status, f.responseURL, f.body, nil
}

func newService(client HTTPClient) *Service {
	return NewService(Options{
		Client: client,
		ResolveRequestContext: func() (RequestContext, error) {
			return RequestContext{EndpointOrigin: "https://example.test", AppVersion: "1.0.0", Platform: "linux-amd64"}, nil
		},
	})
}

func validBody() []byte {
	return []byte(`{"code":0,"data":{"configs":{"pluginStoreOrder":{"code":{"categoryOrder":["a","b","a"]},"work":{"pluginOrder":{"g1":["p2","p1"]}}}}}}`)
}

func TestGetSnapshotParsesAndCaches(t *testing.T) {
	client := &fakeClient{status: 200, body: validBody(), responseURL: "https://example.test/api/v1/client/configs?app_version=1.0.0&platform=linux-amd64"}
	svc := newService(client)
	snap, err := svc.GetSnapshot(context.Background(), false)
	if err != nil {
		t.Fatal(err)
	}
	order := snap.PluginStoreOrder.(map[string]any)
	codeOrder := order["code"].(map[string]any)
	categories := codeOrder["categoryOrder"].([]string)
	if len(categories) != 2 || categories[0] != "a" || categories[1] != "b" {
		t.Fatalf("category order not deduped: %v", categories)
	}
	// Second read hits the cache (one HTTP call).
	if _, err := svc.GetSnapshot(context.Background(), false); err != nil {
		t.Fatal(err)
	}
	if calls := atomic.LoadInt32(&client.calls); calls != 1 {
		t.Fatalf("expected 1 HTTP call, got %d", calls)
	}
	// forceRefresh bypasses the cache.
	if _, err := svc.GetSnapshot(context.Background(), true); err != nil {
		t.Fatal(err)
	}
	if calls := atomic.LoadInt32(&client.calls); calls != 2 {
		t.Fatalf("forceRefresh must re-fetch, calls=%d", calls)
	}
}

func TestGetSnapshotInvalidEnvelope(t *testing.T) {
	cases := []struct {
		name string
		body string
	}{
		{"non-zero code", `{"code":1}`},
		{"missing code", `{"data":null}`},
		{"not json", `nope`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			client := &fakeClient{status: 200, body: []byte(tc.body)}
			svc := newService(client)
			_, err := svc.GetSnapshot(context.Background(), false)
			if err == nil || !errors.Is(err, ErrInvalidResponse) {
				t.Fatalf("want invalid-response error, got %v", err)
			}
		})
	}
}

func TestGetSnapshotHTTPError(t *testing.T) {
	client := &fakeClient{status: 503, body: []byte(`{}`)}
	svc := newService(client)
	_, err := svc.GetSnapshot(context.Background(), false)
	if err == nil || err.Error() != "Public client config HTTP 503" {
		t.Fatalf("wrong HTTP error: %v", err)
	}
}

func TestGetSnapshotRedirectRefused(t *testing.T) {
	client := &fakeClient{
		status:      200,
		body:        validBody(),
		responseURL: "https://other-origin.test/api/v1/client/configs?x=1",
	}
	svc := newService(client)
	_, err := svc.GetSnapshot(context.Background(), false)
	if err == nil || err.Error() != "Public client config request context changed" {
		t.Fatalf("redirect must be refused: %v", err)
	}
}

func TestGetSnapshotTimeout(t *testing.T) {
	// The 15s request timeout applies to the HTTP call. Simulate it with a
	// client that honours ctx cancellation and a delay well under 15s: the
	// CALLER's context (1s) fires first, proving ctx propagation end to end.
	client := &fakeClient{status: 200, body: validBody(), delay: 30 * time.Second}
	svc := newService(client)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	start := time.Now()
	_, err := svc.GetSnapshot(ctx, false)
	if err == nil {
		t.Fatal("expected timeout error")
	}
	if time.Since(start) > 5*time.Second {
		t.Fatal("timeout did not apply")
	}
}

func TestConcurrentGetSharesOneRequest(t *testing.T) {
	client := &fakeClient{status: 200, body: validBody(), delay: 100 * time.Millisecond}
	client.responseURL = ""
	// responseURL empty means "unknown" → allowed; set the real one.
	client.responseURL = "https://example.test/api/v1/client/configs?app_version=1.0.0&platform=linux-amd64"
	svc := newService(client)
	var wg sync.WaitGroup
	for i := 0; i < 5; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := svc.GetSnapshot(context.Background(), false); err != nil {
				t.Errorf("get: %v", err)
			}
		}()
	}
	wg.Wait()
	if calls := atomic.LoadInt32(&client.calls); calls != 1 {
		t.Fatalf("concurrent gets must share one request, calls=%d", calls)
	}
}

func TestRequestURLShape(t *testing.T) {
	// The cache key includes app_version + platform query params.
	urlCh := make(chan *url.URL, 1)
	client := &fakeClient{status: 200, body: validBody()}
	client.responseURL = "" // unknown allowed
	client.perCallURLCh = urlCh
	svc := newService(client)
	if _, err := svc.GetSnapshot(context.Background(), false); err != nil {
		t.Fatal(err)
	}
	target := <-urlCh
	if target.Path != "/api/v1/client/configs" {
		t.Fatalf("path: %s", target.Path)
	}
	if target.Query().Get("app_version") != "1.0.0" || target.Query().Get("platform") != "linux-amd64" {
		t.Fatalf("query: %s", target.RawQuery)
	}
}

func TestPluginStoreOrderBadModeDropped(t *testing.T) {
	// 排序只是展示配置；错误模式独立回退，不能阻止目录浏览或污染另一种模式。
	body := []byte(`{"code":0,"data":{"configs":{"pluginStoreOrder":{"code":{"categoryOrder":["a"]},"work":"not-an-object"}}}}`)
	snapshot, err := ParseClientConfigSnapshot(body)
	if err != nil {
		t.Fatal(err)
	}
	order := snapshot.PluginStoreOrder.(map[string]any)
	if _, has := order["code"]; !has {
		t.Fatal("good mode dropped")
	}
	if _, has := order["work"]; has {
		t.Fatal("bad mode must be dropped")
	}
	_ = json.Marshal
}
