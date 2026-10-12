package gateway

// Small helpers the forwarding tests share: reading one frame with a bounded wait, decoding a
// response into its header and body, and the few conversions that keep assertions readable.
//
// They are here rather than inline because each one is used by more than one test, and a helper
// that re-implements framing in three places is a helper that will eventually disagree with the
// real reader.

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/tylinux/nex/go/internal/rpc"
)

// dialClientErr opens a client WebSocket without failing the test, for the tests that drive
// connections concurrently and report their own errors.
func dialClientErr(url string, header http.Header) (*testClient, error) {
	conn, _, err := websocket.Dial(context.Background(), url, &websocket.DialOptions{HTTPHeader: header})
	if err != nil {
		return nil, err
	}
	return &testClient{conn: conn}, nil
}

// closeQuietly ends a client connection without asserting, for teardown paths where the close may
// legitimately race the server's own close.
func (c *testClient) closeQuietly() { _ = c.conn.CloseNow() }

// readHeaderOrClose reads the next frame's header, or reports that the connection ended. It exists
// for the "nothing more should arrive" assertions: a second Initialize is an error, and a close is
// the other acceptable outcome.
func readHeaderOrClose(t *testing.T, client *testClient) rpc.ResponseHeader {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()
	_, data, err := client.conn.Read(ctx)
	if err != nil {
		return rpc.ResponseHeader{Type: -1}
	}
	decoded, ok, decodeErr := rpc.DecodeFrame(data)
	if decodeErr != nil || !ok {
		t.Fatalf("decode frame: ok=%v err=%v", ok, decodeErr)
	}
	header, decodeErr := rpc.DecodeResponseHeader(decoded.Data)
	if decodeErr != nil {
		t.Fatalf("decode response header: %v", decodeErr)
	}
	return header
}

// readHeaderAndBody reads one frame and decodes both halves.
func readHeaderAndBody(t *testing.T, client *testClient) (rpc.ResponseHeader, any) {
	t.Helper()
	payload := client.ReadFrame(t)
	header, body, err := rpc.DecodeResponse(payload)
	if err != nil {
		t.Fatalf("decode response: %v", err)
	}
	return header, body
}

// thirdField pulls the connection index out of the fake upstream's [channel, method, conn] echo.
func thirdField(t *testing.T, body any) int {
	t.Helper()
	fields, ok := body.([]any)
	if !ok || len(fields) != 3 {
		t.Fatalf("body = %#v, want the upstream's three-field echo", body)
	}
	conn, ok := fields[2].(int32)
	if !ok {
		t.Fatalf("echo connection = %#v (%T), want an int", fields[2], fields[2])
	}
	return int(conn)
}

// requestType decodes only the header of an upstream frame, for asserting what the gateway
// forwarded without decoding its body.
func requestType(t *testing.T, payload []byte) int {
	t.Helper()
	header, err := rpc.DecodeRequestHeader(payload)
	if err != nil {
		t.Fatalf("decode forwarded request header: %v", err)
	}
	return header.Type
}

// mustEncodeRequest encodes a 100 (Promise) request or fails the test.
func mustEncodeRequest(t *testing.T, id int, channel, method string) []byte {
	t.Helper()
	payload, err := rpc.EncodeRequest(rpc.RequestHeader{
		Type: rpc.RequestPromise, ID: id, ChannelName: channel, Name: method,
	}, nil)
	if err != nil {
		t.Fatalf("encode request: %v", err)
	}
	return payload
}

// postCapability mints a host capability through the Go front, the way a desktop client does.
func postCapability(t *testing.T, base string) string {
	t.Helper()
	// base is a ws:// URL; the capability route is plain HTTP on the same listener.
	httpBase := "http" + strings.TrimPrefix(base, "ws")
	request, err := http.NewRequest(http.MethodPost, httpBase+"/api/rpc-host-capability", nil)
	if err != nil {
		t.Fatalf("capability request: %v", err)
	}
	client := &http.Client{Timeout: 5 * time.Second}
	response, err := client.Do(request)
	if err != nil {
		t.Fatalf("capability request: %v", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(response.Body)
		t.Fatalf("capability status = %d, want 200: %s", response.StatusCode, body)
	}
	var issued struct {
		Capability string `json:"capability"`
	}
	if err := json.NewDecoder(response.Body).Decode(&issued); err != nil {
		t.Fatalf("decode capability: %v", err)
	}
	if issued.Capability == "" {
		t.Fatal("capability route returned an empty capability")
	}
	return issued.Capability
}

// waitForConnections blocks until the fake upstream has accepted n connections.
func waitForConnections(t *testing.T, fake *fakeUpstream, n int) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if fake.Connections() >= n {
			return
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatalf("upstream accepted %d connections, want %d", fake.Connections(), n)
}

// itoa formats an int for a test message without a fmt.Sprintf at each call site.
func itoa(n int) string { return strconv.Itoa(n) }

// asCloseError is errors.As specialized for the WebSocket close error, so each caller reads as one
// line instead of a type assertion dance.
func asCloseError(err error, target **websocket.CloseError) bool {
	return errors.As(err, target)
}

// itoa formats an int without pulling strconv in for two call sites.
// newGatewayAtDeadUpstream builds a gateway whose upstream is a real TCP port nothing listens on.
// That is the state a machine is in while Node is starting — or after it crashed — and it is what
// the 1013 failure semantics are written for. Using a closed port rather than a mock that returns
// an error keeps the failure inside the real dial path (TCP connect refused).
func newGatewayAtDeadUpstream(t *testing.T) *Gateway {
	t.Helper()
	// Bind a listener, learn its address, then release it: nothing is listening by the time the
	// gateway dials, and the OS will not have handed the port to anyone else in between on the
	// loopback interface this test uses.
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("reserve a port: %v", err)
	}
	address := listener.Addr().String()
	if err := listener.Close(); err != nil {
		t.Fatalf("release the reserved port: %v", err)
	}

	gateway, err := New(Options{
		RoutesPath: filepath.Join(t.TempDir(), "absent.json"),
		Upstream:   UpstreamConfig{BaseURL: "http://" + address},
		// Short: the connect is refused immediately, and a slow default would only make a broken
		// deployment look like a slow one.
		DialTimeout: 2 * time.Second,
		Log:         testLogger{t},
	})
	if err != nil {
		t.Fatalf("gateway.New: %v", err)
	}
	return gateway
}

// readUntilClose reads until the connection ends and returns the close code the SERVER sent.
//
// coder/websocket wraps the peer's close frame in its own "failed to get reader: received close
// frame: status = ..." error rather than returning a *websocket.CloseError, so the code has to be
// read back out of that message. A code of 0 means the socket ended without one (an abrupt drop),
// which the caller treats as different from a deliberate code-carrying close.
func readUntilClose(t *testing.T, client *websocket.Conn) (websocket.StatusCode, error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	for {
		_, _, err := client.Read(ctx)
		if err == nil {
			continue
		}
		return closeCodeFromError(err), err
	}
}

// closeCodeFromError pulls a numeric close code out of a coder/websocket read error.
//
// The library wraps the peer's close frame in its own reader error ("failed to get reader:
// received close frame: status = StatusTryAgainLater and reason = ...") rather than returning a
// *websocket.CloseError, so the code is read back out of the message. Both phrasings are handled:
// the symbolic one ("status = StatusInternalError") and the numeric one ("status = 1013"). A code
// of 0 means the socket ended without one, which the caller treats as an abrupt drop.
var statusCodePattern = regexp.MustCompile(`status = (?:Status([A-Za-z]+)|(\d+))`)

func closeCodeFromError(err error) websocket.StatusCode {
	if err == nil {
		return 0
	}
	var closeErr *websocket.CloseError
	if asCloseError(err, &closeErr) {
		return closeErr.Code
	}
	match := statusCodePattern.FindStringSubmatch(err.Error())
	if match == nil {
		return 0
	}
	if match[2] != "" {
		code, convErr := strconv.Atoi(match[2])
		if convErr != nil {
			return 0
		}
		return websocket.StatusCode(code)
	}
	// The symbolic names coder/websocket prints are its own constants, so a lookup table keyed by
	// name is exact; an unknown name is an abrupt drop.
	if code, ok := namedStatusCodes[match[1]]; ok {
		return code
	}
	return 0
}

// namedStatusCodes maps the status names coder/websocket prints to their codes. Only the codes a
// failure path can produce are listed — the ones the plan names, plus the abnormal-closure
// variants a dropped upstream produces.
var namedStatusCodes = map[string]websocket.StatusCode{
	"NormalClosure":           websocket.StatusNormalClosure,
	"GoingAway":               websocket.StatusGoingAway,
	"AbnormalClosure":         websocket.StatusAbnormalClosure,
	"InternalError":           websocket.StatusInternalError,
	"TryAgainLater":           websocket.StatusTryAgainLater,
	"ServiceRestart":          websocket.StatusServiceRestart,
	"UnsupportedData":         websocket.StatusUnsupportedData,
	"PolicyViolation":         websocket.StatusPolicyViolation,
	"MessageTooBig":           websocket.StatusMessageTooBig,
	"MissingExtension":        1012,
	"InternalServerError":     1011,
	"TLSHandshake":            1015,
	"ApplicationError":        1011,
	"NetworkError":            1006,
	"ConnectionClosedLocally": 1006,
}

// leadingStatus finds a bare 3-digit status in a message, for the "status = 1013" phrasing.
func leadingStatus(message string) (websocket.StatusCode, bool) {
	marker := strings.LastIndex(message, "status = ")
	if marker < 0 {
		return 0, false
	}
	rest := message[marker+len("status = "):]
	digits := 0
	for digits < len(rest) && rest[digits] >= '0' && rest[digits] <= '9' {
		digits++
	}
	if digits == 0 {
		return 0, false
	}
	code, err := strconv.Atoi(rest[:digits])
	if err != nil {
		return 0, false
	}
	return websocket.StatusCode(code), true
}

// filepathToTemp returns a path inside a fresh temp dir that does not exist, for the tests that
// want the "no routing table" default without writing a file.
func filepathToTemp(t *testing.T) string {
	t.Helper()
	return filepath.Join(t.TempDir(), RoutesFileName)
}

// mustDefaultRouter loads the default (no-file) routing table or fails the test.
func mustDefaultRouter(t *testing.T) *Router {
	t.Helper()
	router, err := LoadRouter(filepathToTemp(t))
	if err != nil {
		t.Fatalf("LoadRouter: %v", err)
	}
	return router
}
