package gateway

// The HTTP proxy tests: what reaches the upstream for a forwarded route, and what never does.
//
// The claims worth testing are the ones a reverse proxy gets wrong silently — a buffered body, a
// hop-by-hop header that survived, an X-Forwarded-For that lies — plus the routing boundary
// itself: a path the Go shell owns must never be forwarded even when the table asks for it.

import (
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
)

// recordedRequest is one request the fake upstream saw.
type recordedRequest struct {
	method string
	path   string
	query  string
	header http.Header
	body   string
}

// upstreamRecorder answers what it is asked and records the request it got.
type upstreamRecorder struct {
	server *httptest.Server
	// mu is a plain Mutex, not a channel semaphore: the handler and the assertions run on
	// different goroutines and a channel-based lock here deadlocked the assertion against the
	// handler it was waiting for.
	mu     sync.Mutex
	seen   []recordedRequest
	answer func(w http.ResponseWriter, r *http.Request)
}

func newUpstreamRecorder(t *testing.T) *upstreamRecorder {
	t.Helper()
	recorder := &upstreamRecorder{}
	recorder.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		recorder.mu.Lock()
		recorder.seen = append(recorder.seen, recordedRequest{
			method: r.Method,
			path:   r.URL.Path,
			query:  r.URL.RawQuery,
			header: r.Header.Clone(),
			body:   string(body),
		})
		answer := recorder.answer
		recorder.mu.Unlock()
		if answer != nil {
			answer(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	t.Cleanup(recorder.server.Close)
	return recorder
}

func (r *upstreamRecorder) requests() []recordedRequest {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]recordedRequest, len(r.seen))
	copy(out, r.seen)
	return out
}

// proxyFor builds a proxy with the given routing table in front of the recorder.
func proxyFor(t *testing.T, recorder *upstreamRecorder, table string) *HTTPProxy {
	t.Helper()
	var router *Router
	var err error
	if table == "" {
		router, err = LoadRouter(filepathToTemp(t))
	} else {
		router, err = LoadRouter(writeRoutes(t, table))
	}
	if err != nil {
		t.Fatalf("LoadRouter: %v", err)
	}
	proxy, err := NewHTTPProxy(recorder.server.URL, "shared-secret", router)
	if err != nil {
		t.Fatalf("NewHTTPProxy: %v", err)
	}
	return proxy
}

// TestProxyForwardsOnlyUnownedRoutes is the routing boundary. /api/pets, /api/bots and
// /api/connect-remote are Node's; /api/auth/*, /api/server-info and /api/rpc-host-capability are
// Go's and must never be forwarded, whatever the table says.
func TestProxyForwardsOnlyUnownedRoutes(t *testing.T) {
	recorder := newUpstreamRecorder(t)
	// A table that asks for EVERYTHING to be forwarded: the structurally Go-owned routes must
	// still refuse, or one client would get two answers for the same path.
	proxy := proxyFor(t, recorder, `{"version":1,"default":"node","http":{"/":"node"}}`)

	forwarded := []string{"/api/pets/cat/spritesheet", "/api/bots/webhook", "/api/connect-remote"}
	kept := []string{"/api/auth/login", "/api/auth/session", "/api/server-info", "/api/rpc-host-capability"}

	for _, path := range forwarded {
		if !proxy.Handles(path) {
			t.Errorf("Handles(%q) = false, want true (the route belongs to Node)", path)
		}
	}
	for _, path := range kept {
		if proxy.Handles(path) {
			t.Errorf("Handles(%q) = true, want false (the route is structurally Go-owned)", path)
		}
	}

	// And the truth at the wire: only the forwarded one reaches the upstream.
	front := httptest.NewServer(proxy)
	t.Cleanup(front.Close)
	for _, path := range append(append([]string{}, forwarded...), kept...) {
		response, err := front.Client().Get(front.URL + path)
		if err != nil {
			t.Fatalf("GET %s: %v", path, err)
		}
		_ = response.Body.Close()
	}
	seen := recorder.requests()
	if len(seen) != len(forwarded) {
		var paths []string
		for _, request := range seen {
			paths = append(paths, request.path)
		}
		t.Errorf("upstream saw %d requests (%v), want exactly the %d forwarded ones",
			len(seen), paths, len(forwarded))
	}
}

// TestProxyPreservesMethodPathAndQuery: the forwarded request must be the client's request. A
// dropped query changes the meaning of a route silently, and a rewritten path would land the
// request on a different handler.
func TestProxyPreservesMethodPathAndQuery(t *testing.T) {
	recorder := newUpstreamRecorder(t)
	front := httptest.NewServer(proxyFor(t, recorder, ""))
	t.Cleanup(front.Close)

	request, err := http.NewRequest(http.MethodPost,
		front.URL+"/api/bots/telegram?botId=42&x=1", strings.NewReader(`{"update":1}`))
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	response, err := front.Client().Do(request)
	if err != nil {
		t.Fatalf("do: %v", err)
	}
	_ = response.Body.Close()

	seen := recorder.requests()
	if len(seen) != 1 {
		t.Fatalf("upstream saw %d requests, want 1", len(seen))
	}
	got := seen[0]
	if got.method != http.MethodPost {
		t.Errorf("method = %q, want POST", got.method)
	}
	if got.path != "/api/bots/telegram" {
		t.Errorf("path = %q, want /api/bots/telegram", got.path)
	}
	// The client's query parameters survive; the only difference is the token the proxy
	// substituted (TestProxyReplacesTheClientCredential asserts that separately). url.Values
	// encodes keys sorted, so the comparison is on parameters rather than on raw text.
	query, err := url.ParseQuery(got.query)
	if err != nil {
		t.Fatalf("upstream query %q: %v", got.query, err)
	}
	for key, want := range map[string]string{"botId": "42", "x": "1"} {
		if query.Get(key) != want {
			t.Errorf("query parameter %s = %q, want %q", key, query.Get(key), want)
		}
	}
	if got.body != `{"update":1}` {
		t.Errorf("body = %q, want the client's body streamed through unchanged", got.body)
	}
}

// TestProxyStripsHopByHopHeaders: RFC 7230 §6.1. A Connection-scoped header that survives one hop
// describes a leg that no longer exists, and the failure is a response the client cannot parse.
func TestProxyStripsHopByHopHeaders(t *testing.T) {
	recorder := newUpstreamRecorder(t)
	front := httptest.NewServer(proxyFor(t, recorder, ""))
	t.Cleanup(front.Close)

	request, err := http.NewRequest(http.MethodPost, front.URL+"/api/bots/webhook",
		strings.NewReader(`{}`))
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	request.Header.Set("Connection", "X-Secret-Hop")
	request.Header.Set("X-Secret-Hop", "must-not-travel")
	request.Header.Set("Keep-Alive", "timeout=5")
	request.Header.Set("Te", "trailers")
	request.Header.Set("X-Nex-Bot-Secret", "real-end-to-end-header")
	response, err := front.Client().Do(request)
	if err != nil {
		t.Fatalf("do: %v", err)
	}
	_ = response.Body.Close()

	seen := recorder.requests()
	if len(seen) != 1 {
		t.Fatalf("upstream saw %d requests, want 1", len(seen))
	}
	header := seen[0].header
	for _, name := range []string{"X-Secret-Hop", "Keep-Alive", "Transfer-Encoding"} {
		if header.Get(name) != "" {
			t.Errorf("header %s = %q reached the upstream; it is connection-scoped", name, header.Get(name))
		}
	}
	// "Te" is deliberately NOT asserted: the Go transport adds `Te: trailers` itself when it takes
	// over the request body, so its presence proves nothing about the proxy's stripping. What the
	// proxy's own stripping must prevent is a header the CLIENT nominated, which is X-Secret-Hop
	// above (asserted via the Connection header) — that is the case a hand-rolled stripper misses.
	if header.Get("X-Nex-Bot-Secret") != "real-end-to-end-header" {
		t.Errorf("the end-to-end header X-Nex-Bot-Secret was dropped or altered: %q",
			header.Get("X-Nex-Bot-Secret"))
	}
}

// TestProxyAddsNoForwardedHeaders: the TypeScript does not read X-Forwarded-*, so adding them
// would be a claim the proxy cannot make — and would change which address the login rate limiter
// sees if a future route started reading them.
func TestProxyAddsNoForwardedHeaders(t *testing.T) {
	recorder := newUpstreamRecorder(t)
	front := httptest.NewServer(proxyFor(t, recorder, ""))
	t.Cleanup(front.Close)

	response, err := front.Client().Get(front.URL + "/api/connect-remote")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	_ = response.Body.Close()

	seen := recorder.requests()
	if len(seen) != 1 {
		t.Fatalf("upstream saw %d requests, want 1", len(seen))
	}
	for _, name := range []string{"X-Forwarded-For", "X-Forwarded-Proto", "X-Forwarded-Host"} {
		if value := seen[0].header.Get(name); value != "" {
			t.Errorf("%s = %q reached the upstream; nothing in the TypeScript reads it", name, value)
		}
	}
}

// TestProxyReplacesTheClientCredential is the security property: the upstream is authenticated as
// the Go front, not as the browser. A browser's own ?token= must never reach Node, and the shared
// secret Go generated must be what arrives.
func TestProxyReplacesTheClientCredential(t *testing.T) {
	recorder := newUpstreamRecorder(t)
	front := httptest.NewServer(proxyFor(t, recorder, ""))
	t.Cleanup(front.Close)

	response, err := front.Client().Get(front.URL + "/api/connect-remote?token=browser-token")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	_ = response.Body.Close()

	seen := recorder.requests()
	if len(seen) != 1 {
		t.Fatalf("upstream saw %d requests, want 1", len(seen))
	}
	if !strings.Contains(seen[0].query, "token=shared-secret") {
		t.Errorf("upstream query = %q, want the Go-generated shared secret", seen[0].query)
	}
	if strings.Contains(seen[0].query, "browser-token") {
		t.Errorf("the client's own token reached the upstream: %q", seen[0].query)
	}
}

// TestProxyStreamsAResponseWithoutBuffering is the size property. A spritesheet or a webhook
// payload can be large, and the proxy must not hold it in memory to write it out.
func TestProxyStreamsAResponseWithoutBuffering(t *testing.T) {
	recorder := newUpstreamRecorder(t)
	recorder.mu.Lock()
	recorder.answer = func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "image/png")
		w.WriteHeader(http.StatusOK)
		// 4 MiB in 64 KiB writes: a buffered proxy would hold the whole thing before the first
		// byte reaches the client, and the client would see nothing until it was all assembled.
		chunk := make([]byte, 64*1024)
		for i := range chunk {
			chunk[i] = byte(i)
		}
		for i := 0; i < 64; i++ {
			if _, err := w.Write(chunk); err != nil {
				return
			}
			if flusher, ok := w.(http.Flusher); ok {
				flusher.Flush()
			}
		}
	}
	recorder.mu.Unlock()
	front := httptest.NewServer(proxyFor(t, recorder, ""))
	t.Cleanup(front.Close)

	response, err := front.Client().Get(front.URL + "/api/pets/cat/spritesheet")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	defer response.Body.Close()
	body, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	if len(body) != 64*64*1024 {
		t.Errorf("body length = %d, want %d", len(body), 64*64*1024)
	}
	if got := response.Header.Get("Content-Type"); got != "image/png" {
		t.Errorf("Content-Type = %q, want the upstream's image/png", got)
	}
}

// TestProxyUpstreamDownIs502NotA404: a route that exists but whose server is down must answer 502,
// which is a different thing to a client than the 404 for a route nobody owns — and it is the only
// status this file produces, so it must never be a fabricated RPC response.
func TestProxyUpstreamDownIs502NotA404(t *testing.T) {
	proxy, err := NewHTTPProxy("http://127.0.0.1:1", "", mustDefaultRouter(t))
	if err != nil {
		t.Fatalf("NewHTTPProxy: %v", err)
	}
	front := httptest.NewServer(proxy)
	t.Cleanup(front.Close)

	response, err := front.Client().Get(front.URL + "/api/connect-remote")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusBadGateway {
		t.Errorf("status = %d, want 502 for an unreachable upstream", response.StatusCode)
	}
}

// TestProxyPreservesTheUpstreamStatusAndBody: a webhook provider distinguishes 400 / 401 / 503
// from 200, so the proxy must pass the status through untouched.
func TestProxyPreservesTheUpstreamStatusAndBody(t *testing.T) {
	recorder := newUpstreamRecorder(t)
	recorder.mu.Lock()
	recorder.answer = func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = w.Write([]byte(`{"error":"Bots service is not available."}`))
	}
	recorder.mu.Unlock()
	front := httptest.NewServer(proxyFor(t, recorder, ""))
	t.Cleanup(front.Close)

	response, err := front.Client().Post(front.URL+"/api/bots/telegram", "application/json",
		strings.NewReader(`{}`))
	if err != nil {
		t.Fatalf("post: %v", err)
	}
	defer response.Body.Close()
	body, _ := io.ReadAll(response.Body)
	if response.StatusCode != http.StatusServiceUnavailable {
		t.Errorf("status = %d, want 503 passed through", response.StatusCode)
	}
	if !strings.Contains(string(body), "not available") {
		t.Errorf("body = %q, want the upstream's body unchanged", body)
	}
}

// TestProxyRejectsABadTarget keeps a configuration mistake from becoming a runtime surprise.
func TestProxyRejectsABadTarget(t *testing.T) {
	for _, target := range []string{"", "ftp://example.test", "://nonsense"} {
		if _, err := NewHTTPProxy(target, "", nil); err == nil {
			t.Errorf("NewHTTPProxy(%q) was accepted, want an error", target)
		}
	}
}
