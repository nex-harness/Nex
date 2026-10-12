package server

// The WebSocket endpoint tests drive the real HTTP server through a real socket: a handshake, a
// framed RPC round trip, and the capability states /ws/host can be in. Nothing here mocks the
// transport, because the transport is the thing step 2.7 introduces.

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/tylinux/nex/go/internal/rpc"
	"github.com/tylinux/nex/go/internal/server/hostcap"
)

// testToken is the NEX_SERVER_AUTH_TOKEN stand-in for the auth-enabled cases.
const testToken = "test-token-value"

// newWSServer starts a Go server with the given auth token and returns it with its live endpoint
// set. A blank token reproduces the auth-disabled deployment.
func newWSServer(t *testing.T, authToken string) (*httptest.Server, *WebSocket) {
	t.Helper()
	return newWSServerWithConfig(t, authToken, nil)
}

// newWSServerWithConfig is newWSServer with an injected endpoint configuration, which is how the
// expiry test gets a store whose clock it controls.
func newWSServerWithConfig(t *testing.T, authToken string, wsConfig *WSConfig) (*httptest.Server, *WebSocket) {
	t.Helper()
	handler, cfg, err := NewHandler(Options{AuthToken: authToken, WSConfig: wsConfig})
	if err != nil {
		t.Fatalf("NewHandler: %v", err)
	}
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	return server, cfg.WS
}

// wsURL converts an httptest URL to its ws:// form for a path.
func wsURL(server *httptest.Server, path string) string {
	return "ws" + strings.TrimPrefix(server.URL, "http") + path
}

// dial opens a WebSocket to path and fails the test on error.
func dial(t *testing.T, server *httptest.Server, path string, header http.Header) *websocket.Conn {
	t.Helper()
	conn, _, err := tryDial(t, server, path, header)
	if err != nil {
		t.Fatalf("dial %s: %v", path, err)
	}
	return conn
}

// tryDial is dial for the cases that EXPECT failure; it returns the handshake response so the test
// can assert the status code.
func tryDial(t *testing.T, server *httptest.Server, path string, header http.Header) (*websocket.Conn, *http.Response, error) {
	t.Helper()
	conn, response, err := websocket.Dial(context.Background(), wsURL(server, path), &websocket.DialOptions{
		HTTPHeader: header,
	})
	if conn != nil {
		t.Cleanup(func() { _ = conn.CloseNow() })
	}
	return conn, response, err
}

// readResponse reads the next RPC frame and decodes its response header and body. The 13 bytes of
// ProtocolMessage header are stripped exactly as MessageReader does on the server side.
func readResponse(t *testing.T, conn *websocket.Conn) (rpc.ResponseHeader, any) {
	t.Helper()
	header, _, body, err := readResponseFull(t, conn)
	if err != nil {
		t.Fatal(err)
	}
	return header, body
}

// readResponseFull is readResponse returning the raw serialized payload too, for the byte-level
// replay assertions.
func readResponseFull(t *testing.T, conn *websocket.Conn) (rpc.ResponseHeader, []byte, any, error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, data, err := conn.Read(ctx)
	if err != nil {
		return rpc.ResponseHeader{}, nil, nil, err
	}
	if len(data) < 13 {
		return rpc.ResponseHeader{}, nil, nil, shortFrameError(len(data))
	}
	payload := data[13:]
	header, body, err := rpc.DecodeResponse(payload)
	if err != nil {
		return rpc.ResponseHeader{}, nil, nil, err
	}
	return header, payload, body, nil
}

// shortFrameError reports a WebSocket message too small to hold a protocol header.
type shortFrameError int

func (n shortFrameError) Error() string {
	return "frame shorter than the 13-byte protocol header: " + strconv.Itoa(int(n)) + " bytes"
}

// capabilityBody mirrors the JSON body of POST /api/rpc-host-capability.
type capabilityBody struct {
	Capability string `json:"capability"`
	ExpiresAt  int64  `json:"expiresAt"`
}

// httpRequest issues a plain HTTP request to the server and returns the response.
func httpRequest(t *testing.T, server *httptest.Server, method, path, body string, header http.Header) *http.Response {
	t.Helper()
	request, err := http.NewRequest(method, server.URL+path, strings.NewReader(body))
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	for key, value := range header {
		for _, v := range value {
			request.Header.Add(key, v)
		}
	}
	response, err := server.Client().Do(request)
	if err != nil {
		t.Fatalf("do request: %v", err)
	}
	return response
}

// postCapability mints a capability through the HTTP route.
func postCapability(t *testing.T, server *httptest.Server) capabilityBody {
	t.Helper()
	response := httpRequest(t, server, http.MethodPost, capabilityPath, "", nil)
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("issue status = %d, want 200", response.StatusCode)
	}
	var issued capabilityBody
	if err := json.NewDecoder(response.Body).Decode(&issued); err != nil {
		t.Fatalf("decode issue body: %v", err)
	}
	return issued
}

// readErrorField decodes the `{"error": "..."}` body both Hono handlers answer with.
func readErrorField(t *testing.T, response *http.Response) string {
	t.Helper()
	var body map[string]string
	if err := json.NewDecoder(response.Body).Decode(&body); err != nil {
		t.Fatalf("decode error body: %v", err)
	}
	return body["error"]
}

// TestWebSocketConnectWithoutAuth: with auth disabled, /ws upgrades and immediately sends the
// Initialize frame. This is R01 over a real socket.
func TestWebSocketConnectWithoutAuth(t *testing.T) {
	server, _ := newWSServer(t, "")
	conn := dial(t, server, wsPath, nil)

	header, _ := readResponse(t, conn)
	if header.Type != rpc.ResponseInitialize {
		t.Fatalf("first frame type = %d, want %d (Initialize)", header.Type, rpc.ResponseInitialize)
	}
}

// TestWebSocketConnectWithAuth covers R10's transport half: with a token configured, /ws is a
// protected path, so an unauthenticated upgrade is refused 401 by the auth middleware — BEFORE the
// capability logic — while a ?token= upgrade succeeds.
func TestWebSocketConnectWithAuth(t *testing.T) {
	server, _ := newWSServer(t, testToken)

	t.Run("without a token is 401", func(t *testing.T) {
		_, response, err := tryDial(t, server, wsPath, nil)
		if err == nil {
			t.Fatal("expected the unauthenticated upgrade to be refused")
		}
		if response == nil || response.StatusCode != http.StatusUnauthorized {
			t.Fatalf("response = %#v, want status 401", response)
		}
	})

	t.Run("with ?token= upgrades", func(t *testing.T) {
		conn := dial(t, server, wsPath+"?token="+testToken, nil)
		header, _ := readResponse(t, conn)
		if header.Type != rpc.ResponseInitialize {
			t.Fatalf("first frame type = %d, want Initialize", header.Type)
		}
	})
}

// TestWebSocketConnectWithCookie is the browser path: log in once, then present the session cookie
// on /ws. The client never puts ?token= on the socket.
func TestWebSocketConnectWithCookie(t *testing.T) {
	server, _ := newWSServer(t, testToken)

	login := httpRequest(t, server, http.MethodPost, "/api/auth/login", `{"token":"`+testToken+`"}`, nil)
	defer login.Body.Close()
	if login.StatusCode != http.StatusOK {
		t.Fatalf("login status = %d, want 200", login.StatusCode)
	}
	var cookies []string
	for _, cookie := range login.Cookies() {
		cookies = append(cookies, cookie.Name+"="+cookie.Value)
	}
	if len(cookies) == 0 {
		t.Fatal("login returned no session cookie")
	}

	conn := dial(t, server, wsPath, http.Header{"Cookie": []string{strings.Join(cookies, "; ")}})
	header, _ := readResponse(t, conn)
	if header.Type != rpc.ResponseInitialize {
		t.Fatalf("first frame type = %d, want Initialize", header.Type)
	}
}

// TestCapabilityIssueRoute: POST /api/rpc-host-capability returns {capability, expiresAt} and the
// ticket works exactly once on /ws/host.
func TestCapabilityIssueRoute(t *testing.T) {
	server, endpoints := newWSServer(t, "")

	issued := postCapability(t, server)
	if issued.Capability == "" || issued.ExpiresAt == 0 {
		t.Fatalf("issue response = %+v, want a capability and an expiry", issued)
	}

	conn := dial(t, server, wsHostPath, http.Header{HostCapabilityHeader: []string{issued.Capability}})
	header, _ := readResponse(t, conn)
	if header.Type != rpc.ResponseInitialize {
		t.Fatalf("first frame type = %d, want Initialize", header.Type)
	}
	if endpoints.Capabilities().Consume(issued.Capability) {
		t.Error("the capability must be single-use even after a successful connect")
	}
}

// TestWebSocketHostCapabilityTable is the /ws/host matrix: missing, unknown, valid and reused
// capabilities. Each row is one dial attempt against a fresh server.
func TestWebSocketHostCapabilityTable(t *testing.T) {
	cases := []struct {
		name    string
		prepare func(endpoints *WebSocket) http.Header
		wantOK  bool
	}{
		{
			name:    "no capability header is refused",
			prepare: func(*WebSocket) http.Header { return nil },
			wantOK:  false,
		},
		{
			name: "an unknown capability is refused",
			prepare: func(*WebSocket) http.Header {
				return http.Header{HostCapabilityHeader: []string{"not-a-real-ticket"}}
			},
			wantOK: false,
		},
		{
			name: "a valid capability connects",
			prepare: func(endpoints *WebSocket) http.Header {
				return http.Header{HostCapabilityHeader: []string{endpoints.Capabilities().Issue().Capability}}
			},
			wantOK: true,
		},
		{
			name: "a capability already spent by an earlier connect is refused",
			prepare: func(endpoints *WebSocket) http.Header {
				capability := endpoints.Capabilities().Issue().Capability
				if !endpoints.Capabilities().Consume(capability) {
					t.Fatal("the priming consume should have succeeded")
				}
				return http.Header{HostCapabilityHeader: []string{capability}}
			},
			wantOK: false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			server, endpoints := newWSServer(t, "")
			header := tc.prepare(endpoints)
			conn, response, err := tryDial(t, server, wsHostPath, header)
			if tc.wantOK {
				if err != nil {
					t.Fatalf("expected the upgrade to succeed, got %v (response %#v)", err, response)
				}
				responseHeader, _ := readResponse(t, conn)
				if responseHeader.Type != rpc.ResponseInitialize {
					t.Fatalf("first frame type = %d, want Initialize", responseHeader.Type)
				}
				return
			}
			if err == nil {
				_ = conn.CloseNow()
				t.Fatal("expected the upgrade to be refused")
			}
			if response == nil || response.StatusCode != http.StatusUnauthorized {
				t.Fatalf("response = %#v, want status 401", response)
			}
		})
	}
}

// TestWebSocketHostExpiredCapability uses an injected store whose clock the test owns, so the
// expiry is exact rather than a sleep. expiresAt == now is the boundary, and it must be refused.
func TestWebSocketHostExpiredCapability(t *testing.T) {
	now := int64(1_700_000_000_000)
	store := hostcap.New(hostcap.Options{Now: func() int64 { return now }})
	server, _ := newWSServerWithConfig(t, "", &WSConfig{Capabilities: store})

	issued := store.Issue()
	// One millisecond inside the TTL the ticket still works.
	now = issued.ExpiresAt - 1
	live := dial(t, server, wsHostPath, http.Header{HostCapabilityHeader: []string{issued.Capability}})
	if header, _ := readResponse(t, live); header.Type != rpc.ResponseInitialize {
		t.Fatalf("frame type = %d, want Initialize one millisecond before expiry", header.Type)
	}

	// At exactly expiresAt it is expired. Issue a second ticket and step the clock onto it.
	second := store.Issue()
	now = second.ExpiresAt
	_, response, err := tryDial(t, server, wsHostPath, http.Header{HostCapabilityHeader: []string{second.Capability}})
	if err == nil {
		t.Fatal("expected the expired capability to be refused")
	}
	if response == nil || response.StatusCode != http.StatusUnauthorized {
		t.Fatalf("response = %#v, want status 401 for an expired capability", response)
	}
}

// TestWebSocketHostCapability401Body pins the exact JSON body the TypeScript returns from the
// /ws/host capability middleware. The desktop client matches on it, so the string is a contract.
// A plain HTTP GET (no upgrade) reaches the same middleware without a handshake, so the body is
// directly readable.
func TestWebSocketHostCapability401Body(t *testing.T) {
	server, _ := newWSServer(t, "")

	response := httpRequest(t, server, http.MethodGet, wsHostPath, "", nil)
	defer response.Body.Close()
	if response.StatusCode != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", response.StatusCode)
	}
	if got := readErrorField(t, response); got != "Invalid or expired host capability" {
		t.Errorf("error field = %q, want \"Invalid or expired host capability\"", got)
	}
}

// TestCapabilityCheckRunsAfterAuth: with a token configured, /ws/host without credentials is 401
// "Unauthorized" (the auth middleware) — NOT the capability 401 — proving the capability check
// runs after auth, exactly as app.use("/ws/host", …) sits behind app.use("*", middleware()).
func TestCapabilityCheckRunsAfterAuth(t *testing.T) {
	server, _ := newWSServer(t, testToken)

	response := httpRequest(t, server, http.MethodGet, wsHostPath, "", nil)
	defer response.Body.Close()
	if response.StatusCode != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", response.StatusCode)
	}
	if got := readErrorField(t, response); got != "Unauthorized" {
		t.Errorf("error field = %q, want the AUTH 401 \"Unauthorized\", proving auth runs first", got)
	}
}

// TestCapabilityRouteRequiresAuth: POST /api/rpc-host-capability is an /api/* path, so it is
// protected exactly as it is in Node.
func TestCapabilityRouteRequiresAuth(t *testing.T) {
	server, _ := newWSServer(t, testToken)

	noCreds := httpRequest(t, server, http.MethodPost, capabilityPath, "", nil)
	defer noCreds.Body.Close()
	if noCreds.StatusCode != http.StatusUnauthorized {
		t.Errorf("issue without credentials status = %d, want 401", noCreds.StatusCode)
	}

	withToken := httpRequest(t, server, http.MethodPost, capabilityPath+"?token="+testToken, "", nil)
	defer withToken.Body.Close()
	if withToken.StatusCode != http.StatusOK {
		t.Errorf("issue with ?token= status = %d, want 200", withToken.StatusCode)
	}
}

// TestCapabilityRouteRejectsNonPost mirrors Hono's app.post: a GET on the route is not the route.
func TestCapabilityRouteRejectsNonPost(t *testing.T) {
	server, _ := newWSServer(t, "")

	response := httpRequest(t, server, http.MethodGet, capabilityPath, "", nil)
	defer response.Body.Close()
	if response.StatusCode != http.StatusNotFound {
		t.Errorf("GET /api/rpc-host-capability status = %d, want 404 (only POST is registered)", response.StatusCode)
	}
}

// TestPlainWebSocketPathIsNotUpgraded pins the guard at the top of Hono's upgradeWebSocket helper:
// a plain GET /ws is not a handshake and must fall through to the static handler, which refuses a
// token-protected path. The recorded TypeScript responses already pin this as 404.
//
// /ws/host is deliberately NOT in this table: its capability middleware runs for every request to
// the path, so a plain GET there is answered 401 by the capability check before the route is ever
// consulted. TestWebSocketHostCapability401Body pins that case.
func TestPlainWebSocketPathIsNotUpgraded(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "index.html"), []byte("<!doctype html><title>app</title>"), 0o600); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	handler, _, err := NewHandler(Options{StaticRoot: root, SPAFallback: true})
	if err != nil {
		t.Fatalf("NewHandler: %v", err)
	}
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)

	response := httpRequest(t, server, http.MethodGet, wsPath, "", nil)
	body, _ := io.ReadAll(response.Body)
	response.Body.Close()
	if response.StatusCode != http.StatusNotFound {
		t.Errorf("GET %s status = %d, want 404 (a plain GET is not an upgrade)", wsPath, response.StatusCode)
	}
	if strings.Contains(string(body), "<title>app</title>") {
		t.Errorf("GET %s served the SPA fallback; a protected path must not fall back", wsPath)
	}
}
