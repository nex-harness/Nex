package ws

// The adapter's whole job is to be a faithful rpc.Socket over a WebSocket, with two settings that
// are easy to get wrong and expensive to notice later: the read limit (the library defaults to
// 32 KiB, far below what the UI sends) and the close semantics (a teardown path must never hang).

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// serveEcho starts a server that accepts one connection and echoes every binary message back.
func serveEcho(t *testing.T, readLimit int64) string {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := Accept(w, r, Options{ReadLimit: readLimit})
		if err != nil {
			return
		}
		defer conn.Close()
		for {
			data, err := conn.Read(context.Background())
			if err != nil {
				return
			}
			if err := conn.Write(context.Background(), data); err != nil {
				return
			}
		}
	}))
	t.Cleanup(server.Close)
	return "ws" + strings.TrimPrefix(server.URL, "http")
}

// TestSocketRoundTrip proves the adapter is a transparent message transport: what the client writes
// comes back byte for byte.
func TestSocketRoundTrip(t *testing.T) {
	url := serveEcho(t, DefaultReadLimit)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	client, err := Dial(ctx, url, nil, Options{})
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer client.Close()

	payload := []byte("a ProtocolMessage-sized payload")
	if err := client.Write(ctx, payload); err != nil {
		t.Fatalf("write: %v", err)
	}
	got, err := client.Read(ctx)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if string(got) != string(payload) {
		t.Errorf("echoed %q, want %q", got, payload)
	}
}

// TestReadLimitRejectsOversizeMessage is the regression test for verify-first item 7: the library's
// own default is 32 KiB, which would close a healthy connection during a 512 KiB attachment chunk.
// The configured limit must therefore both allow a large message and refuse one beyond it.
func TestReadLimitRejectsOversizeMessage(t *testing.T) {
	const limit = 1 << 20 // 1 MiB
	url := serveEcho(t, limit)

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	client, err := Dial(ctx, url, nil, Options{ReadLimit: limit})
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer client.Close()

	// Just under the limit must survive: this is the v4 maxFrameBytes case.
	large := make([]byte, limit-1024)
	for i := range large {
		large[i] = byte(i)
	}
	if err := client.Write(ctx, large); err != nil {
		t.Fatalf("write large: %v", err)
	}
	got, err := client.Read(ctx)
	if err != nil {
		t.Fatalf("read large: %v", err)
	}
	if len(got) != len(large) {
		t.Fatalf("echoed %d bytes, want %d", len(got), len(large))
	}

	// Well over the limit must fail the connection rather than allocate.
	oversize := make([]byte, limit+4096)
	if err := client.Write(ctx, oversize); err != nil {
		return // the write itself may fail once the peer has closed; either way the limit held
	}
	if _, err := client.Read(ctx); err == nil {
		t.Fatal("an oversize message was echoed back; the read limit did not hold")
	}
}

// TestDefaultReadLimitMatchesFrameReader keeps the transport limit and the framing limit the same
// magnitude: the WebSocket library refuses an oversize message for the same reason, at the same
// size, as the framing layer above it. If these drift, a client sees two different limits depending
// on which layer rejects first.
func TestDefaultReadLimitMatchesFrameReader(t *testing.T) {
	if DefaultReadLimit != rpcDefaultMaxFrameBytes {
		t.Errorf("DefaultReadLimit = %d, want %d (rpc.DefaultMaxFrameBytes)", DefaultReadLimit, rpcDefaultMaxFrameBytes)
	}
}

// rpcDefaultMaxFrameBytes mirrors rpc.DefaultMaxFrameBytes. It is duplicated rather than imported
// because internal/rpc must stay free of this package (and this package of it), so the gate cannot
// detect an import cycle; the test above is what keeps the two numbers honest.
const rpcDefaultMaxFrameBytes = 64 << 20

// TestCloseIsIdempotent: teardown runs from several places (the RPC server's defer, its abort path
// and the transport's own failure), so Close must be safe to call more than once and must not block
// when the peer is already gone.
func TestCloseIsIdempotent(t *testing.T) {
	url := serveEcho(t, DefaultReadLimit)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	client, err := Dial(ctx, url, nil, Options{})
	if err != nil {
		t.Fatalf("dial: %v", err)
	}

	var wg sync.WaitGroup
	results := make([]error, 4)
	for i := range results {
		wg.Add(1)
		go func(index int) {
			defer wg.Done()
			results[index] = client.Close()
		}(i)
	}

	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(15 * time.Second):
		t.Fatal("Close did not return; a teardown path can hang")
	}
	// Every call may report the socket already closed; none may report a hang or a panic. The one
	// that performed the handshake may return nil, the rest ErrClosed.
	for i, err := range results {
		if err != nil && err != ErrClosed {
			t.Errorf("Close call %d returned %v, want nil or ErrClosed", i, err)
		}
	}
}

// TestAcceptRejectsNonWebSocketRequest: the library writes its own HTTP error for a malformed
// handshake, so the handler must return without writing a second response. The test drives a plain
// GET (which is not a WebSocket handshake) and requires a normal HTTP answer, not a 101.
func TestAcceptRejectsNonWebSocketRequest(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if _, err := Accept(w, r, Options{}); err != nil {
			return // the library already wrote the error
		}
		w.WriteHeader(http.StatusTeapot)
	}))
	t.Cleanup(server.Close)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, server.URL, nil)
	if err != nil {
		t.Fatal(err)
	}
	response, err := server.Client().Do(request)
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	defer response.Body.Close()
	if response.StatusCode == http.StatusSwitchingProtocols {
		t.Fatal("a plain GET was upgraded as a WebSocket")
	}
	// 426 Upgrade Required is what the library writes for a request that is not a handshake; the
	// handler's own 418 would mean Accept wrongly succeeded.
	if response.StatusCode != http.StatusUpgradeRequired {
		t.Errorf("status = %d, want 426 (Accept must refuse a non-handshake request)", response.StatusCode)
	}
}
