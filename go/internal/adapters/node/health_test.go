package node

// Health tests: the readiness probe's contract with the real @nex/server, plus the
// parsing of the line that reveals its port.
//
// The probe is the thing the supervisor decides "the upstream is up" on, so each of
// its properties is asserted separately: which route, what counts as up, what a 401
// means, and that a 5xx does not.

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// TestProbePortReadsTheListenLine covers the format createHttpServer's listen callback
// logs: `[nex-server:http][<pid>] http://127.0.0.1:<port>`. The prefix is the service
// logger's (formatLogPrefix("nex-server:http", pid)), and the tail is the origin.
func TestProbePortReadsTheListenLine(t *testing.T) {
	cases := []struct {
		name string
		line string
		want int
	}{
		{"real prefix", "[nex-server:http][12345] http://127.0.0.1:41234", 41234},
		{"bare origin", "http://127.0.0.1:8080", 8080},
		{"trailing whitespace", "http://127.0.0.1:8080   ", 8080},
		{"crlf", "http://127.0.0.1:8080\r", 8080},
		{"ipv6", "[nex-server:http][1] http://[::1]:9001", 9001},
		{"hostname", "http://localhost:3030", 3030},
		// The negative cases matter more: a startup banner that happens to contain a
		// number must not be mistaken for the port.
		{"banner", "Nex Server starting...", 0},
		{"warning with a number", "[nex-server:http][7] workspace 2 failed to load", 0},
		{"empty", "", 0},
		{"no port", "http://127.0.0.1", 0},
		{"port zero", "http://127.0.0.1:0", 0},
		{"port too large", "http://127.0.0.1:99999", 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := ProbePort(tc.line); got != tc.want {
				t.Errorf("ProbePort(%q) = %d, want %d", tc.line, got, tc.want)
			}
		})
	}
}

// TestHealthProbeRequiresTheSecret: 200 with the right token, 401 with the wrong one.
// The 401 is treated as "the server is up": a protected route rejecting an unauthenticated
// probe proves the middleware is loaded, which is what readiness means here.
func TestHealthProbeRequiresTheSecret(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != HealthPath {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		if r.URL.Query().Get("token") != "the-secret" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"authRequired":true,"authenticated":false}`))
	}))
	defer server.Close()

	if ready := NewHealthChecker(server.URL, "the-secret").Check(context.Background()); !ready.OK || ready.StatusCode != 200 {
		t.Errorf("probe with the right token = %+v, want 200", ready)
	}
	if ready := NewHealthChecker(server.URL, "wrong").Check(context.Background()); !ready.OK || ready.StatusCode != 401 {
		t.Errorf("probe with a wrong token = %+v, want 401 and OK", ready)
	}
	// Without a token the route is unprotected and answers 200 anyway — which is why
	// the supervisor always sends one when it has a secret.
	if ready := NewHealthChecker(server.URL, "").Check(context.Background()); !ready.OK {
		t.Errorf("probe without a token = %+v, want OK", ready)
	}
}

// TestHealthProbeRejectsServerErrors: a process that is listening but answering 500 is
// NOT ready. Reporting it ready would let the gateway open upstream legs that fail on
// the first frame, which is a much worse failure than waiting.
func TestHealthProbeRejectsServerErrors(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer server.Close()

	ready := NewHealthChecker(server.URL, "t").Check(context.Background())
	if ready.OK {
		t.Fatal("a 500 was reported as ready")
	}
	if ready.StatusCode != 500 {
		t.Errorf("StatusCode = %d, want 500 to be reported for diagnostics", ready.StatusCode)
	}
	if ready.Err == nil {
		t.Error("a 5xx must carry an error explaining why it is not ready")
	}
}

// TestHealthProbeReportsConnectionRefused: the normal state while a child is still
// booting. It must be a failed probe, not a panic and not a false "ready".
func TestHealthProbeReportsConnectionRefused(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	url := server.URL
	server.Close() // nothing is listening any more

	ready := NewHealthChecker(url, "t").Check(context.Background())
	if ready.OK {
		t.Fatal("a closed port was reported as ready")
	}
	if ready.Err == nil {
		t.Error("a refused connection must produce an error")
	}
}

// TestHealthProbeWithoutBaseURL: a checker with no address reports a failed probe
// rather than panicking on a malformed request URL.
func TestHealthProbeWithoutBaseURL(t *testing.T) {
	ready := NewHealthChecker("  ", "t").Check(context.Background())
	if ready.OK || ready.Err == nil {
		t.Errorf("probe without a base URL = %+v, want a failed probe with an error", ready)
	}
}

// TestWaitReadyRetriesUntilTheServerAppears is the loop the supervisor uses: a server
// that starts late must still be waited for, and the poll seam is what makes the
// "several probes failed first" case observable without a real sleep.
func TestWaitReadyRetriesUntilTheServerAppears(t *testing.T) {
	var up atomic.Bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if !up.Load() {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		_, _ = w.Write([]byte("{}"))
	}))
	defer server.Close()

	checker := NewHealthChecker(server.URL, "t")
	checker.Interval = time.Millisecond
	polls := 0
	err := checker.WaitReady(context.Background(), 5*time.Second, func(_ context.Context, _ time.Duration) error {
		polls++
		if polls == 3 {
			up.Store(true) // the server "finishes booting"
		}
		return nil
	})
	if err != nil {
		t.Fatalf("WaitReady: %v", err)
	}
	if polls < 3 {
		t.Errorf("WaitReady gave up after %d probes; it should have retried until the server answered", polls)
	}
}

// TestWaitReadyGivesUpAtTheDeadline: a server that never appears must fail rather than
// block the supervisor forever.
func TestWaitReadyGivesUpAtTheDeadline(t *testing.T) {
	// 127.0.0.1:1 is reserved and never served.
	checker := NewHealthChecker("http://127.0.0.1:1", "t")
	checker.Interval = time.Millisecond
	err := checker.WaitReady(context.Background(), 150*time.Millisecond, func(context.Context, time.Duration) error {
		return nil
	})
	if err == nil {
		t.Fatal("WaitReady returned nil for a server that never answered")
	}
	if !strings.Contains(err.Error(), "did not become ready") {
		t.Errorf("error = %v, want it to say the upstream did not become ready", err)
	}
}

// TestWaitReadyStopsOnContextCancel: a cancelled context must end the loop instead of
// running more probes against a supervisor that is shutting down.
//
// The poll seam honours ctx, which is the contract sleepCtx implements: a poll that
// ignored cancellation would make this untestable (the loop could only end at its own
// deadline) and would misdescribe what the real sleep does.
func TestWaitReadyStopsOnContextCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	checker := NewHealthChecker("http://127.0.0.1:1", "t")
	checker.Interval = time.Millisecond
	err := checker.WaitReady(ctx, time.Minute, func(ctx context.Context, _ time.Duration) error {
		return ctx.Err()
	})
	if err == nil {
		t.Fatal("WaitReady returned nil after cancellation")
	}
}

// TestWatchHealthReportsTransitions: the liveness loop reports each flip and stops on
// cancellation. The gateway's legs must be closed when the upstream dies, so a flip
// from up to down has to produce exactly one callback.
func TestWatchHealthReportsTransitions(t *testing.T) {
	// atomic, not a plain bool: the handler runs on the server's goroutine while the
	// test flips it, and -race is the whole point of this suite.
	var up atomic.Bool
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if !up.Load() {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		_, _ = w.Write([]byte("{}"))
	}))
	server.Start()
	defer server.Close()

	checker := NewHealthChecker(server.URL, "t")
	checker.Interval = 10 * time.Millisecond

	transitions := make(chan bool, 8)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- checker.WatchHealth(ctx, func(r Ready) { transitions <- r.OK }) }()

	// First callback is the initial state.
	if got := readTransition(t, transitions); got != false {
		t.Fatalf("first transition = %v, want false (the server answers 500)", got)
	}
	up.Store(true)
	if got := readTransition(t, transitions); got != true {
		t.Fatalf("second transition = %v, want true", got)
	}
	up.Store(false)
	if got := readTransition(t, transitions); got != false {
		t.Fatalf("third transition = %v, want false", got)
	}
	cancel()
	if err := <-done; err == nil {
		t.Error("WatchHealth returned nil after cancellation")
	}
}

func readTransition(t *testing.T, ch <-chan bool) bool {
	t.Helper()
	select {
	case v := <-ch:
		return v
	case <-time.After(10 * time.Second):
		t.Fatal("no health transition was reported")
		return false
	}
}

// TestPortOpen is the last-resort liveness helper, and it must be honest: an open port
// says nothing about whether the server can answer.
func TestPortOpen(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	address := strings.TrimPrefix(server.URL, "http://")
	host, portText, found := strings.Cut(address, ":")
	if !found {
		t.Fatalf("unexpected test server URL %q", server.URL)
	}
	port, err := strconv.Atoi(portText)
	if err != nil {
		t.Fatalf("parse %q: %v", portText, err)
	}
	if !PortOpen(host, port) {
		t.Error("PortOpen said a listening server is closed")
	}
	server.Close()
	if PortOpen(host, port) {
		t.Error("PortOpen said a closed port is open")
	}
}
