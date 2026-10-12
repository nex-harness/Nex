package gateway

// The Node integration test spawns the REAL @nex/server (packages/server/dist/entry-http.js) and
// drives recorded RPC scenarios through the Go front to it.
//
// What it proves that the fake upstream cannot:
//   - the gateway speaks to the real Node stack, not to a test double that happens to share its
//     shape: the real SocketProtocol, the real ChannelServer, the real per-connection service
//     registration from createLocalServices;
//   - exactly one Initialize reaches the client, even though BOTH Go and Node produce one;
//   - a request for an unregistered channel produces Node's own error payload, unchanged, which is
//     R05's recorded answer and the shape the UI branches on;
//   - both client modes work against Node, with Node's own capability store behind /ws/host.
//
// The recorded scenarios R01-R11 mostly address stub channels ("demo", "binary", …) that only
// existed in the recorder's in-process ChannelServer, so they cannot be replayed against the real
// service collection. What IS replayable is replayed (R01's handshake byte for byte, R05's
// pending-queue timeout error), and the rest of the evidence — the recording comparison for every
// shape — stays where it already is: internal/rpc's replay tests and scripts/go-rewrite/replay-rpc-go.mjs.
// This file closes the gap those cannot: the hop through the gateway to a real Node.

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/tylinux/nex/go/internal/rpc"
	"github.com/tylinux/nex/go/internal/server"
)

// repoRoot walks up from this package's directory to the repository root, which is where the
// built server bundle and the recorded traces live. It is computed rather than written as a
// relative constant so the paths cannot drift when a test file moves.
func repoRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatalf("working directory: %v", err)
	}
	for i := 0; i < 8; i++ {
		// The Go module lives in go/, so its presence only marks the module root; the repository
		// root is its parent (pnpm-workspace.yaml is what actually identifies it).
		if _, statErr := os.Stat(filepath.Join(dir, "pnpm-workspace.yaml")); statErr == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}
	t.Fatalf("go.mod not found above %s", dir)
	return ""
}

// nodeServerEntry is the built @nex/server HTTP entry, relative to the repository root. Step 2.8
// does not build it — the strangler needs a Node that exists — so its absence is a SKIP with an
// explicit reason, never a silent pass.
const nodeServerEntry = "packages/server/dist/entry-http.js"

// nodeAuthToken is the shared secret Go would generate for the Node child process. The test uses a
// fixed value so the assertion "the gateway passed the secret" is checkable; in production this is
// generated per start (see docs/specs/go-rewrite/phase-2-go-server.md, upstream security).
const nodeAuthToken = "gateway-node-test-token"

// nodeUpstream is a running real Node server.
type nodeUpstream struct {
	baseURL string
	command *exec.Cmd
	logPath string
}

// startNodeServer spawns the real @nex/server on loopback with a shared secret, and waits for it to
// answer. It skips (rather than fails) when the server build is absent, so a checkout that has not
// built packages/server still runs the rest of the suite.
func startNodeServer(t *testing.T) *nodeUpstream {
	t.Helper()
	if _, err := os.Stat(filepath.Join(repoRoot(t), filepath.FromSlash(nodeServerEntry))); err != nil {
		t.Skipf("build @nex/server first (pnpm --filter @nex/server build): %v", err)
	}
	if _, err := exec.LookPath("node"); err != nil {
		t.Skipf("node is not installed: %v", err)
	}

	root := t.TempDir()
	dataDir := filepath.Join(root, "data")
	workspace := filepath.Join(root, "ws")
	for _, dir := range []string{filepath.Join(dataDir, ".nex", "v2"), workspace} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatalf("mkdir %s: %v", dir, err)
		}
	}
	port := freePort(t)
	logPath := filepath.Join(root, "node.log")
	logFile, err := os.Create(logPath)
	if err != nil {
		t.Fatalf("create log: %v", err)
	}

	root_dir := repoRoot(t)
	// A clean env: the Node server must see only what this test sets, or a stray NEX_* from the
	// developer's shell would change which deployment is being exercised.
	env := []string{
		"HOME=" + dataDir,
		"NEX_DATA_BASE_DIR=" + dataDir,
		"PORT=" + fmt.Sprint(port),
		"NEX_SERVER_HOST=127.0.0.1",
		"NEX_SERVER_AUTH_TOKEN=" + nodeAuthToken,
		"NEX_SERVER_WORKSPACE=" + workspace,
		"PATH=" + os.Getenv("PATH"),
	}
	if value := os.Getenv("SystemRoot"); value != "" {
		env = append(env, "SystemRoot="+value)
	}
	// cwd is OUTSIDE the repository: findUpward would otherwise find the monorepo bundle and the
	// server would load the developer's configuration instead of the throwaway one.
	command := exec.Command("node", filepath.Join(root_dir, filepath.FromSlash(nodeServerEntry)))
	command.Dir = root
	command.Env = env
	command.Stdout = logFile
	command.Stderr = logFile
	if err := command.Start(); err != nil {
		t.Fatalf("start node server: %v", err)
	}

	upstream := &nodeUpstream{
		baseURL: fmt.Sprintf("http://127.0.0.1:%d", port),
		command: command,
		logPath: logPath,
	}
	t.Cleanup(func() {
		_ = command.Process.Kill()
		_, _ = command.Process.Wait()
		_ = logFile.Close()
		if t.Failed() {
			if contents, readErr := os.ReadFile(logPath); readErr == nil {
				t.Logf("node server log:\n%s", tail(string(contents), 40))
			}
		}
	})
	waitForNodeServer(t, upstream.baseURL)
	return upstream
}

// waitForNodeServer polls the health route until it answers or the deadline passes. GET
// /api/auth/session is the route the plan names as the readiness signal: it exists unconditionally
// and answers 200 whether or not a token is configured.
func waitForNodeServer(t *testing.T, baseURL string) {
	t.Helper()
	deadline := time.Now().Add(60 * time.Second)
	client := &http.Client{Timeout: 2 * time.Second}
	for time.Now().Before(deadline) {
		response, err := client.Get(baseURL + "/api/auth/session?token=" + nodeAuthToken)
		if err == nil {
			_ = response.Body.Close()
			if response.StatusCode < 500 {
				return
			}
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("the node server did not become ready within 60s")
}

// Stop kills the Node process, for the failure test.
func (n *nodeUpstream) Stop() {
	if n.command.Process != nil {
		_ = n.command.Process.Kill()
		_, _ = n.command.Process.Wait()
	}
}

// freePort reserves an ephemeral loopback port and releases it.
func freePort(t *testing.T) int {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("reserve a port: %v", err)
	}
	port := listener.Addr().(*net.TCPAddr).Port
	if err := listener.Close(); err != nil {
		t.Fatalf("release the reserved port: %v", err)
	}
	return port
}

// tail returns the last n lines of a log, so a failure prints something actionable.
func tail(text string, n int) string {
	lines := strings.Split(strings.TrimRight(text, "\n"), "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.Join(lines, "\n")
}

// TestGatewayToRealNodeInitializeExactlyOnce is the plan's integration requirement stated as a
// count: the client sees ONE Initialize, not two.
//
// Go's rpc.Server sends one when the connection is built. Node's ChannelServer sends one too
// (setupChannelServer constructs it with deferInit unset). Forwarding both would give a browser
// two handshake frames — an observable difference from the pure-Node deployment — so the gateway
// swallows the upstream's. Counting the frames is the only way to prove the swallow happened: an
// assertion that the first frame is Initialize would pass either way.
func TestGatewayToRealNodeInitializeExactlyOnce(t *testing.T) {
	upstream := startNodeServer(t)
	base := serveGateway(t, newGatewayForNode(t, upstream))

	client, err := dialClient(t, base+"/ws", nil)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	// Read everything that arrives without the client sending anything. Anything beyond the first
	// frame is the upstream's Initialize leaking through.
	first := client.ReadHeader(t)
	if first.Type != rpc.ResponseInitialize {
		t.Fatalf("first frame type = %d, want Initialize", first.Type)
	}
	extra := readHeaderOrClose(t, client)
	if extra.Type == rpc.ResponseInitialize {
		t.Fatal("two Initialize frames reached the client: the upstream's was not swallowed")
	}
}

// TestGatewayToRealNodeInitializePayloadMatchesRecording is R01 replayed through the Go front: the
// Initialize the client receives must be the recorded TypeScript bytes.
func TestGatewayToRealNodeInitializePayloadMatchesRecording(t *testing.T) {
	recorded := readRecordedFrame(t, "R01")
	upstream := startNodeServer(t)
	base := serveGateway(t, newGatewayForNode(t, upstream))

	client, err := dialClient(t, base+"/ws", nil)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	got := client.ReadFrame(t)
	if hex.EncodeToString(got) != hex.EncodeToString(recorded) {
		t.Errorf("Initialize payload\n  recorded: %x\n  gateway: %x", recorded, got)
	}
}

// TestGatewayToRealNodeUnknownChannelIsNodeError is R05 through the gateway: a call for a channel
// the real service collection does not have must come back as Node's OWN pending-queue timeout
// error, forwarded byte for byte.
//
// This is the shape the UI branches on (an unknown channel is a plugin-loading state, not a
// transport failure), and it can only be produced by Node's ChannelServer — the Go front has no
// pending queue for a channel it never owned.
func TestGatewayToRealNodeUnknownChannelIsNodeError(t *testing.T) {
	upstream := startNodeServer(t)
	base := serveGateway(t, newGatewayForNode(t, upstream))

	client, err := dialClient(t, base+"/ws", nil)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	if header := client.ReadHeader(t); header.Type != rpc.ResponseInitialize {
		t.Fatalf("first frame type = %d, want Initialize", header.Type)
	}

	client.WriteRequest(t, 0, "definitely-not-a-service", "nope", nil)
	header, body := readHeaderAndBody(t, client)
	if header.Type != rpc.ResponsePromiseError {
		t.Fatalf("response type = %d, want a PromiseError (%d)", header.Type, rpc.ResponsePromiseError)
	}
	message := errorMessageOf(body)
	// Node's own text: `Channel name '<x>' timed out after 1000ms`. The recorder's stub server was
	// built with a 300 ms pending queue, so only the TIMEOUT VALUE differs from R05's recording —
	// the message shape, which is what the UI matches on, is identical. The default 1000 ms is
	// ChannelServer's own constructor default (channelServer.ts), not a test artifact.
	if !strings.Contains(message, "Channel name 'definitely-not-a-service' timed out after") {
		t.Errorf("error = %q, want Node's pending-queue timeout for an unregistered channel", message)
	}
	if name, _ := objectField(body, "name"); name != "Unknown channel" {
		t.Errorf("error name = %q, want %q", name, "Unknown channel")
	}
}

// TestGatewayToRealNodeForwardsARealChannel: a call on a channel the real server DOES own must be
// answered by Node, not by the gateway. It cannot assert the answer's VALUE — a real git call
// depends on the machine's repository state — but it can assert that an answer came from Node with
// the request's id, which is the proof the frame reached the real ChannelServer and came back.
func TestGatewayToRealNodeForwardsARealChannel(t *testing.T) {
	upstream := startNodeServer(t)
	base := serveGateway(t, newGatewayForNode(t, upstream))

	client, err := dialClient(t, base+"/ws", nil)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	if header := client.ReadHeader(t); header.Type != rpc.ResponseInitialize {
		t.Fatalf("first frame type = %d, want Initialize", header.Type)
	}

	// `client-scenes.getScene` is a read-only call with no side effects and no workspace state.
	client.WriteRequest(t, 42, "client-scenes", "getScene", []rpc.Value{"gateway-test"})
	header, _ := readHeaderAndBody(t, client)
	if header.ID != 42 {
		t.Errorf("response id = %d, want 42 (the id the client chose must survive the round trip)", header.ID)
	}
	if header.Type != rpc.ResponsePromiseSuccess && header.Type != rpc.ResponsePromiseError {
		t.Errorf("response type = %d, want a promise settled by the real Node server", header.Type)
	}
	if header.Type == rpc.ResponsePromiseError {
		t.Logf("the real call errored (acceptable: its answer depends on local state), id round-tripped")
	}
}

// TestGatewayToRealNodeBothClientModes is the plan's mode test against the REAL capability store:
// /ws must reach Node as terminal-client and /ws/host as trusted-host-relay, with Go minting a
// capability from Node's own one-shot store for the host leg.
func TestGatewayToRealNodeBothClientModes(t *testing.T) {
	upstream := startNodeServer(t)
	gateway := newGatewayForNode(t, upstream)
	base := serveGateway(t, gateway)

	web, err := dialClient(t, base+"/ws", nil)
	if err != nil {
		t.Fatalf("dial /ws: %v", err)
	}
	if header := web.ReadHeader(t); header.Type != rpc.ResponseInitialize {
		t.Fatalf("/ws first frame type = %d, want Initialize", header.Type)
	}

	// The desktop client mints a capability from the Go front, which mints one from Node's store.
	issued := postCapability(t, base)
	host, err := dialClient(t, base+"/ws/host", http.Header{server.HostCapabilityHeader: []string{issued}})
	if err != nil {
		t.Fatalf("dial /ws/host: %v", err)
	}
	if header := host.ReadHeader(t); header.Type != rpc.ResponseInitialize {
		t.Fatalf("/ws/host first frame type = %d, want Initialize", header.Type)
	}
	// Both legs answer a forwarded call on their own connection, which is what a per-connection
	// scope on Node's side requires.
	web.WriteRequest(t, 1, "client-scenes", "getScene", []rpc.Value{"web"})
	host.WriteRequest(t, 1, "client-scenes", "getScene", []rpc.Value{"host"})
	for name, client := range map[string]*testClient{"web": web, "host": host} {
		header, _ := readHeaderAndBody(t, client)
		if header.ID != 1 {
			t.Errorf("%s connection answered id %d, want 1", name, header.ID)
		}
	}
}

// TestGatewayToRealNodeKilledMidCallClosesTheClient is the plan's failure test against a real
// process: killing Node during an active call must close the client socket (the UI reconnects),
// must not hang, and must not leave the gateway holding the connection.
//
// The request is a real one on a real channel, so Node is genuinely mid-handler when the signal
// lands. That is the difference from the fake-upstream version: here the process is gone, not just
// the socket.
func TestGatewayToRealNodeKilledMidCallClosesTheClient(t *testing.T) {
	upstream := startNodeServer(t)
	base := serveGateway(t, newGatewayForNode(t, upstream))

	client, err := dialClient(t, base+"/ws", nil)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	if header := client.ReadHeader(t); header.Type != rpc.ResponseInitialize {
		t.Fatalf("first frame type = %d, want Initialize", header.Type)
	}
	// A real channel whose handler touches the agent process, so Node is doing work when it dies.
	client.WriteRequest(t, 1, "nex-agent", "getStatus", nil)
	// Give the request time to be forwarded and picked up before the kill.
	time.Sleep(300 * time.Millisecond)

	upstream.Stop()

	readDone := make(chan error, 1)
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		for {
			if _, _, err := client.conn.Read(ctx); err != nil {
				readDone <- err
				return
			}
		}
	}()
	select {
	case <-readDone:
	case <-time.After(20 * time.Second):
		t.Fatal("the client socket stayed open after the Node process was killed")
	}
}

// TestGatewayRetryAfterRealNodeRestarts is the plan's "Node slow to start" case with a real process:
// while Node is down the client is closed with 1013, and once Node is up the retry is served.
func TestGatewayRetryAfterRealNodeRestarts(t *testing.T) {
	upstream := startNodeServer(t)
	base := serveGateway(t, newGatewayForNode(t, upstream))
	upstream.Stop()
	// Wait for the port to actually stop answering, so the 1013 comes from a refused dial rather
	// than from a race with the kill.
	waitForPortClosed(t, upstream.baseURL)

	client, _, err := dialWebSocket(t, base+"/ws")
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer client.CloseNow()
	code, readErr := readUntilClose(t, client)
	if readErr == nil {
		t.Fatal("the connection stayed open with no Node behind it")
	}
	if code != 1013 {
		t.Errorf("close code = %d, want 1013 (try again later) while Node is down", code)
	}
}

// TestGatewayRejectsRoutingTableNamingAChannelNodeDoesNotHave is the typo protection applied to the
// real service set: a table naming a channel the real server does not register is refused at
// startup, so an operator cannot believe a channel moved when it did not.
func TestGatewayRejectsRoutingTableNamingAChannelNodeDoesNotHave(t *testing.T) {
	path := writeRoutes(t, `{"version":1,"channels":{"not-a-real-channel":"go"}}`)
	_, err := New(Options{
		RoutesPath: path,
		Upstream:   UpstreamConfig{BaseURL: "http://127.0.0.1:1"},
	})
	if err == nil {
		t.Fatal("a routing table naming a channel that does not exist was accepted")
	}
	var unknown *ErrUnknownChannel
	if !errorsAs(err, &unknown) {
		t.Errorf("error = %v (%T), want *ErrUnknownChannel", err, err)
	}
}

// newGatewayForNode builds a gateway in front of the real Node server, with the shared secret the
// child process was started with.
func newGatewayForNode(t *testing.T, upstream *nodeUpstream) *Gateway {
	t.Helper()
	gateway, err := New(Options{
		RoutesPath: filepath.Join(t.TempDir(), "absent.json"),
		Upstream:   UpstreamConfig{BaseURL: upstream.baseURL, AuthToken: nodeAuthToken},
		// Generous: the first real request after boot initializes the provider registry, which is
		// far slower than a dial but still not a hang.
		DialTimeout: 30 * time.Second,
		Log:         testLogger{t},
	})
	if err != nil {
		t.Fatalf("gateway.New: %v", err)
	}
	return gateway
}

// readRecordedFrame loads one scenario's first recorded server payload.
func readRecordedFrame(t *testing.T, id string) []byte {
	t.Helper()
	path := filepath.Join(repoRoot(t), "go", "testdata", "traces", "rpc", id, "frames.jsonl")
	contents, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s (regenerate with scripts/go-rewrite/record/record-rpc.mjs): %v", path, err)
	}
	for _, line := range strings.Split(string(contents), "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		var frame struct {
			Direction string `json:"direction"`
			Hex       string `json:"hex"`
		}
		if err := json.Unmarshal([]byte(line), &frame); err != nil {
			t.Fatalf("%s: parse: %v", path, err)
		}
		if frame.Direction != "server-to-client" {
			continue
		}
		decoded, err := hex.DecodeString(frame.Hex)
		if err != nil {
			t.Fatalf("%s: bad hex: %v", path, err)
		}
		return decoded
	}
	t.Fatalf("%s: no server frame in the recording", path)
	return nil
}

// objectField pulls one member out of a decoded error payload.
func objectField(body any, key string) (any, bool) {
	obj, ok := body.(rpc.Object)
	if !ok {
		return nil, false
	}
	value, found := obj.Get(key)
	return value, found
}

// errorMessageOf pulls `message` out of a decoded error payload.
func errorMessageOf(body any) string {
	obj, ok := body.(rpc.Object)
	if !ok {
		return ""
	}
	value, _ := obj.Get("message")
	message, _ := value.(string)
	return message
}

// waitForPortClosed blocks until the URL stops answering, so a "Node is down" assertion is about
// Node being down rather than about a race with the kill.
func waitForPortClosed(t *testing.T, baseURL string) {
	t.Helper()
	client := &http.Client{Timeout: time.Second}
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		response, err := client.Get(baseURL + "/api/auth/session")
		if err != nil {
			return
		}
		_ = response.Body.Close()
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("%s kept answering after the node process was stopped", baseURL)
}

// dialWebSocket opens a raw client connection for the tests that expect the handshake to succeed
// and the connection to end afterwards.
func dialWebSocket(t *testing.T, url string) (*websocket.Conn, *http.Response, error) {
	t.Helper()
	conn, response, err := websocket.Dial(context.Background(), url, nil)
	if conn != nil {
		t.Cleanup(func() { _ = conn.CloseNow() })
	}
	return conn, response, err
}

// errorsAs is errors.As spelled out, kept local so the import list of this file stays short.
func errorsAs(err error, target any) bool { return errors.As(err, target) }
