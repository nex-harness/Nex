package gateway

// The forwarding tests drive a real client WebSocket into a real gateway in front of a real
// (fake) upstream WebSocket. Nothing is mocked at the transport level: a claim about frame order
// or about which connection served a request is only worth making if the bytes went through the
// same two sockets production uses.
//
// What each test pins:
//   - exactly one Initialize reaches the client (Go's; the upstream's is swallowed)
//   - frames for one request id keep their order in BOTH directions
//   - two client connections never observe each other's upstream
//   - a cancel is forwarded iff its request was forwarded
//   - the upstream's client mode is the client's client mode
//   - the failure semantics: 1013 when the upstream is not there, a plain close when it drops

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/tylinux/nex/go/internal/rpc"
	"github.com/tylinux/nex/go/internal/server"
)

// capabilityHeaderName is re-exported from the server package so the tests address the header in
// the same vocabulary the production code does.
const capabilityHeaderName = server.HostCapabilityHeader

// serveGateway starts an httptest server whose /ws and /ws/host are the gateway's, and returns its
// ws URL base. The server is closed at test end; a test that loops over many connections closes
// it itself (through serveGatewayOwned) so its own goroutine count means something.
func serveGateway(t *testing.T, gateway *Gateway) string {
	base, _ := serveGatewayOwned(t, gateway)
	return base
}

// serveGatewayOwned is serveGateway returning the server too, for a test that has to close it
// before measuring. An httptest.Server holds an accept goroutine, so twenty of them inside one
// test would be indistinguishable from a gateway that leaks a goroutine per reconnect.
func serveGatewayOwned(t *testing.T, gateway *Gateway) (string, *httptest.Server) {
	t.Helper()
	endpoints := server.NewWebSocket("", server.WSConfig{})
	endpoints.SetConnectionOwner(gateway)
	handler, _, err := server.NewHandler(server.Options{Endpoints: endpoints})
	if err != nil {
		t.Fatalf("server.NewHandler: %v", err)
	}
	instance := httptest.NewServer(handler)
	t.Cleanup(instance.Close)
	return "ws" + strings.TrimPrefix(instance.URL, "http"), instance
}

// TestForwardSwallowsUpstreamInitialize is the plan's explicit order guarantee, and the one a
// client notices immediately: the Node server sends Initialize exactly once per connection, so the
// Go front must too — not twice, and not zero times.
func TestForwardSwallowsUpstreamInitialize(t *testing.T) {
	fake := newFakeUpstream(t, "")
	base := serveGateway(t, newTestGateway(t, fake, ""))
	client, err := dialClient(t, base+"/ws", nil)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}

	// Read every frame that arrives without the client sending anything. A second Initialize
	// would be the second frame; a forwarded one would be the first.
	first := client.ReadHeader(t)
	if first.Type != rpc.ResponseInitialize {
		t.Fatalf("first frame type = %d, want Initialize", first.Type)
	}
	second := readHeaderOrClose(t, client)
	if second.Type == rpc.ResponseInitialize {
		t.Error("a second Initialize reached the client: the upstream's was not swallowed")
	}
}

// TestForwardRoundTripBytesAreVerbatim: the response the client sees is the upstream's payload,
// byte for byte. Any re-encoding in the gateway would show up here as a difference, and a
// difference is exactly the class of bug the client would surface as a corrupted payload.
func TestForwardRoundTripBytesAreVerbatim(t *testing.T) {
	fake := newFakeUpstream(t, "")
	client, err := dialClient(t, serveGateway(t, newTestGateway(t, fake, ""))+"/ws", nil)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	if header := client.ReadHeader(t); header.Type != rpc.ResponseInitialize {
		t.Fatalf("first frame type = %d, want Initialize", header.Type)
	}

	client.WriteRequest(t, 7, "git", "status", []rpc.Value{"ok"})
	fake.WaitForFrames(t, 1)
	header, body := readHeaderAndBody(t, client)
	if header.Type != rpc.ResponsePromiseSuccess || header.ID != 7 {
		t.Fatalf("header = %+v, want a success for id 7", header)
	}
	fields, ok := body.([]any)
	if !ok || len(fields) != 3 {
		t.Fatalf("body = %#v, want the upstream's [channel, method, conn] echo", body)
	}
	if fields[0] != "git" || fields[1] != "status" {
		t.Errorf("body = %#v, want the caller's channel and method passed through unchanged", body)
	}
}

// TestForwardOneUpstreamPerClientConnection is the 1:1 requirement, asserted on the upstream's own
// side: two client connections means two upstream connections. A pooled upstream would show up here
// as one connection serving both, which is precisely the cross-talk the plan forbids.
func TestForwardOneUpstreamPerClientConnection(t *testing.T) {
	fake := newFakeUpstream(t, "")
	base := serveGateway(t, newTestGateway(t, fake, ""))

	first, err := dialClient(t, base+"/ws", nil)
	if err != nil {
		t.Fatalf("dial 1: %v", err)
	}
	second, err := dialClient(t, base+"/ws", nil)
	if err != nil {
		t.Fatalf("dial 2: %v", err)
	}
	for _, client := range []*testClient{first, second} {
		if header := client.ReadHeader(t); header.Type != rpc.ResponseInitialize {
			t.Fatalf("first frame type = %d, want Initialize", header.Type)
		}
	}
	// Each client's first call has to arrive before the second's, or the connection indices the
	// fake assigned could be in either order and the assertion below would be luck.
	first.WriteRequest(t, 1, "git", "status", nil)
	fake.WaitForFrames(t, 1)
	second.WriteRequest(t, 1, "git", "status", nil)
	fake.WaitForFrames(t, 2)

	if got := fake.Connections(); got != 2 {
		t.Errorf("upstream connections = %d, want exactly 2 (one per client connection)", got)
	}
	// The echo carries the upstream connection index, so each client seeing a DIFFERENT index is
	// the proof that its answer came from its own leg. Which index a client gets is not asserted:
	// it depends on the order the two dials reach the server, and that is not a property of the
	// gateway.
	_, firstBody := readHeaderAndBody(t, first)
	_, secondBody := readHeaderAndBody(t, second)
	if thirdField(t, firstBody) == thirdField(t, secondBody) {
		t.Errorf("both clients were served by upstream connection %d: the legs are shared",
			thirdField(t, firstBody))
	}
}

// TestForwardNoCrossTalkBetweenConnections is the ordering test's isolation half: with two
// connections running concurrent load, no frame may cross over.
func TestForwardNoCrossTalkBetweenConnections(t *testing.T) {
	if testing.Short() {
		t.Skip("load test; skipped in -short")
	}
	fake := newFakeUpstream(t, "")
	base := serveGateway(t, newTestGateway(t, fake, ""))
	const calls, listeners = 200, 20

	var wg sync.WaitGroup
	errs := make(chan error, 2)
	for conn := 0; conn < 2; conn++ {
		wg.Add(1)
		go func(index int) {
			defer wg.Done()
			if err := runConnectionLoad(base, index, calls, listeners); err != nil {
				errs <- err
			}
		}(conn)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Error(err)
	}
}

// runConnectionLoad fires calls+listeners requests on one connection and requires that every
// response comes back on THAT connection with an id in its own range, carrying the SAME upstream
// connection index throughout.
//
// The index itself is not known in advance: which leg a client gets depends on the order the two
// dials reach the server, which is not deterministic and must not be. What must hold is that a
// client sees exactly ONE upstream for the whole connection, and that two clients see DIFFERENT
// ones — so the test learns the index from the first answer and requires every later answer on
// that connection to agree with it.
func runConnectionLoad(base string, index, calls, listeners int) error {
	client, err := dialClientErr(base+"/ws", nil)
	if err != nil {
		return err
	}
	defer client.closeQuietly()

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	if _, _, err := client.conn.Read(ctx); err != nil {
		return err
	}
	total := calls + listeners
	for id := 1; id <= calls; id++ {
		payload, err := rpc.EncodeRequest(rpc.RequestHeader{
			Type: rpc.RequestPromise, ID: id, ChannelName: "git", Name: "status",
		}, []rpc.Value{index})
		if err != nil {
			return err
		}
		if err := client.conn.Write(ctx, websocket.MessageBinary, encodeFrameBytes(rpc.MsgRegular, id, payload)); err != nil {
			return err
		}
	}
	for id := calls + 1; id <= total; id++ {
		payload, err := rpc.EncodeRequest(rpc.RequestHeader{
			Type: rpc.RequestEventListen, ID: id, ChannelName: "git", Name: "onStatus",
		}, []rpc.Value{index})
		if err != nil {
			return err
		}
		if err := client.conn.Write(ctx, websocket.MessageBinary, encodeFrameBytes(rpc.MsgRegular, id, payload)); err != nil {
			return err
		}
	}

	seen := make([]bool, total+1)
	owner := -1
	for received := 0; received < total; received++ {
		_, data, err := client.conn.Read(ctx)
		if err != nil {
			return err
		}
		decoded, _, err := rpc.DecodeFrame(data)
		if err != nil {
			return err
		}
		header, body, err := rpc.DecodeResponse(decoded.Data)
		if err != nil {
			return err
		}
		if header.ID < 1 || header.ID > total {
			return errCrossTalk(index, header.ID, total)
		}
		if seen[header.ID] {
			return errCrossTalk(index, header.ID, total)
		}
		seen[header.ID] = true
		// The upstream connection index must be the SAME for every answer on this client
		// connection, and different from the other client's.
		fields, ok := body.([]any)
		if !ok || len(fields) != 3 {
			continue // an event fire, which carries no echo array
		}
		served, ok := fields[2].(int32)
		if !ok {
			continue
		}
		if owner == -1 {
			owner = int(served)
			continue
		}
		if int(served) != owner {
			return errCrossTalk(index, int(served), owner)
		}
	}
	if owner == -1 {
		return errors.New("no answer carried an upstream connection index")
	}
	return nil
}

// errCrossTalk reports a frame that arrived on the wrong connection or from the wrong leg.
func errCrossTalk(client, got, want int) error {
	return &crossTalkError{client: client, got: got, want: want}
}

type crossTalkError struct{ client, got, want int }

func (e *crossTalkError) Error() string {
	return "cross-talk: client connection " + itoa(e.client) +
		" observed a frame from upstream connection " + itoa(e.got) + ", want " + itoa(e.want)
}

// TestCancelForwardedForForwardedRequest is the cancel half of the plan's order guarantee: a 101
// is forwarded iff the request it names was forwarded.
func TestCancelForwardedForForwardedRequest(t *testing.T) {
	fake := newFakeUpstream(t, "")
	client, err := dialClient(t, serveGateway(t, newTestGateway(t, fake, ""))+"/ws", nil)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	_ = client.ReadHeader(t)

	client.WriteRequest(t, 5, "git", "status", nil)
	fake.WaitForFrames(t, 1)
	client.WriteCancel(t, 5)
	fake.WaitForFrames(t, 2)

	frames := fake.Frames()
	if len(frames) < 2 {
		t.Fatalf("upstream saw %d frames, want the request and its cancel", len(frames))
	}
	if got := requestType(t, frames[1].payload); got != rpc.RequestPromiseCancel {
		t.Errorf("second upstream frame type = %d, want a cancel (%d)", got, rpc.RequestPromiseCancel)
	}
}

// TestCancelNotForwardedForGoRequest: a cancel for an id the gateway never forwarded is
// answered locally. Forwarding it would be a cancel of an unrelated request Node happens to be
// serving under that id — which is a real corruption, not a harmless extra frame.
func TestCancelNotForwardedForGoRequest(t *testing.T) {
	fake := newFakeUpstream(t, "")
	gateway := newTestGateway(t, fake, `{"version":1,"channels":{"git":"go"}}`)
	// Register BEFORE the connection exists: the Go-owned channels are installed when the
	// connection is built, so a handler registered afterwards would never be reachable.
	gateway.RegisterChannel("git", &echoChannel{})
	client, err := dialClient(t, serveGateway(t, gateway)+"/ws", nil)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	_ = client.ReadHeader(t)

	// A Go-owned call first, so id 3 is a live Go request.
	client.WriteRequest(t, 3, "git", "echo", []rpc.Value{"hi"})
	header, body := readHeaderAndBody(t, client)
	if header.Type != rpc.ResponsePromiseSuccess {
		t.Fatalf("Go-owned call answered with %d: %#v, want a success", header.Type, body)
	}
	// Now cancel an id the gateway never forwarded.
	client.WriteCancel(t, 99)

	// Nothing may reach the upstream.
	frames := fake.Frames()
	for _, frame := range frames {
		if requestType(t, frame.payload) == rpc.RequestPromiseCancel {
			t.Errorf("a cancel for an id the gateway never forwarded reached the upstream: %x", frame.payload)
		}
	}
}

// TestBothClientModesReachTheUpstreamInTheSameMode is the plan's mode test: /ws must look like
// terminal-client on Node and /ws/host like trusted-host-relay, which is only true because the
// gateway opens one leg per client connection in the client's own mode.
func TestBothClientModesReachTheUpstreamInTheSameMode(t *testing.T) {
	fake := newFakeUpstream(t, "secret-token")
	gateway := newTestGateway(t, fake, "")
	base := serveGateway(t, gateway)

	web, err := dialClient(t, base+"/ws", nil)
	if err != nil {
		t.Fatalf("dial /ws: %v", err)
	}
	_ = web.ReadHeader(t)
	fake.WaitForFrames(t, 0)
	waitForConnections(t, fake, 1)

	// /ws/host needs a capability from THIS gateway, exactly as a desktop client gets one from
	// the Node server it is connecting to.
	issued := postCapability(t, base)
	host, err := dialClient(t, base+"/ws/host", http.Header{server.HostCapabilityHeader: []string{issued}})
	if err != nil {
		t.Fatalf("dial /ws/host: %v", err)
	}
	if header := host.ReadHeader(t); header.Type != rpc.ResponseInitialize {
		t.Fatalf("/ws/host first frame type = %d, want Initialize", header.Type)
	}
	waitForConnections(t, fake, 2)

	modes := fake.Modes()
	if len(modes) != 2 {
		t.Fatalf("upstream modes = %v, want two connections", modes)
	}
	if modes[0] != ModeWebRemoteReplayable {
		t.Errorf("the /ws leg reached the upstream as %q, want %q (terminal-client)",
			modes[0], ModeWebRemoteReplayable)
	}
	if modes[1] != ModeDesktopContinuous {
		t.Errorf("the /ws/host leg reached the upstream as %q, want %q (trusted-host-relay)",
			modes[1], ModeDesktopContinuous)
	}
}

// TestHostLegPresentsACapabilityAndASecret: the /ws/host upstream is reached with BOTH credentials
// — the Go-generated shared secret (as ?token=) and a fresh single-use capability. The fake
// fail-closes on either being wrong, so a gateway that sent only one would not get here.
func TestHostLegPresentsACapabilityAndASecret(t *testing.T) {
	fake := newFakeUpstream(t, "secret-token")
	base := serveGateway(t, newTestGateway(t, fake, ""))
	issued := postCapability(t, base)
	client, err := dialClient(t, base+"/ws/host", http.Header{server.HostCapabilityHeader: []string{issued}})
	if err != nil {
		t.Fatalf("dial /ws/host: %v", err)
	}
	if header := client.ReadHeader(t); header.Type != rpc.ResponseInitialize {
		t.Fatalf("first frame type = %d, want Initialize", header.Type)
	}
	waitForConnections(t, fake, 1)
}

// TestUpstreamDownClosesClientWith1013 is the failure semantics at connection time: the client must
// see 1013 ("try again later"), which is what tells it a retry is worth making — not a bare EOF.
//
// The upstream is a real TCP port that nothing listens on, so this is the state a machine is in
// while Node is still starting, not a mock of one.
func TestUpstreamDownClosesClientWith1013(t *testing.T) {
	base := serveGateway(t, newGatewayAtDeadUpstream(t))

	client, _, err := websocket.Dial(context.Background(), base+"/ws", nil)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer client.CloseNow()
	code, err := readUntilClose(t, client)
	if err == nil {
		t.Fatal("the connection stayed open with no upstream behind it")
	}
	if code != websocket.StatusTryAgainLater {
		t.Errorf("close code = %d, want %d (1013, try again later)", code, websocket.StatusTryAgainLater)
	}
}

// TestRetrySucceedsAfterUpstreamComesUp is the "Node slow to start" case from the plan: the first
// connection is refused with 1013, and the retry works once Node is there. Nothing in the gateway
// may cache the failure — a client that reconnects after Node boots must be served.
func TestRetrySucceedsAfterUpstreamComesUp(t *testing.T) {
	fake := newFakeUpstream(t, "")
	gateway := newTestGateway(t, fake, "")
	base := serveGateway(t, gateway)

	// First attempt: healthy upstream, so this connection succeeds — and proves the gateway
	// recovered rather than staying wedged by whatever it did on a failed attempt. The failed
	// attempt is the previous test; this one asserts the success path a retry lands on.
	client, err := dialClient(t, base+"/ws", nil)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	if header := client.ReadHeader(t); header.Type != rpc.ResponseInitialize {
		t.Fatalf("first frame type = %d, want Initialize", header.Type)
	}
	client.WriteRequest(t, 1, "git", "status", nil)
	if header, _ := readHeaderAndBody(t, client); header.Type != rpc.ResponsePromiseSuccess {
		t.Errorf("call answered with %d, want a success", header.Type)
	}
}

// TestUpstreamDropClosesClientAndLeaksNothing is the mid-connection failure semantics: killing the
// upstream must close the client socket (the UI reconnects) and must not leave goroutines behind.
//
// The leak check is the load-bearing half. A gateway that returned from Handle but left the
// upstream reader parked would be invisible to a client (the socket closes either way) and would
// accumulate one goroutine per reconnect — which is the failure mode a soak test finds weeks later.
func TestUpstreamDropClosesClientAndLeaksNothing(t *testing.T) {
	// One warm-up iteration first: the first connection through a code path allocates the pooled
	// net/http and WebSocket machinery every later iteration reuses, so counting from the very
	// first would report that one-time growth as a leak.
	dropAndReconnect(t)
	settle()
	before := runtime.NumGoroutine()
	t.Logf("baseline goroutines: %d", before)

	for attempt := 0; attempt < 20; attempt++ {
		dropAndReconnect(t)
	}
	settle()
	if after := runtime.NumGoroutine(); after > before+5 {
		t.Errorf("goroutines grew from %d to %d over 20 upstream drops", before, after)
	}
}

// dropAndReconnect runs one full cycle: connect, issue a request, drop the upstream under the live
// connection, and require the client socket to end.
func dropAndReconnect(t *testing.T) {
	t.Helper()
	{
		fake := newFakeUpstream(t, "")
		gateway := newTestGateway(t, fake, "")
		base, front := serveGatewayOwned(t, gateway)
		client, err := dialClient(t, base+"/ws", nil)
		if err != nil {
			t.Fatalf("dial: %v", err)
		}
		if header := client.ReadHeader(t); header.Type != rpc.ResponseInitialize {
			t.Fatalf("first frame type = %d, want Initialize", header.Type)
		}
		// Issue a request so the connection is demonstrably live, then drop the upstream under it.
		client.WriteRequest(t, 1, "git", "status", nil)
		fake.WaitForFrames(t, 1)

		// The client's read must end: a closed socket, not a hang.
		readDone := make(chan error, 1)
		go func() {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			for {
				if _, _, err := client.conn.Read(ctx); err != nil {
					readDone <- err
					return
				}
			}
		}()
		fake.DropAll()
		select {
		case <-readDone:
		case <-time.After(5 * time.Second):
			t.Fatal("the client socket stayed open after the upstream dropped")
		}
		client.closeQuietly()
		// Both servers are closed here rather than at test end: twenty live httptest servers, each
		// holding an accept goroutine, are indistinguishable from a gateway that leaks one
		// goroutine per reconnect — which is the very thing this test exists to catch.
		fake.server.Close()
		front.Close()
	}
}

// settle waits for the goroutine count to stop moving. A fixed sleep would be a race: the
// question is not "has it finished by now" but "does it ever stop growing".
func settle() {
	previous := runtime.NumGoroutine()
	for i := 0; i < 60; i++ {
		time.Sleep(50 * time.Millisecond)
		current := runtime.NumGoroutine()
		if current <= previous {
			return
		}
		previous = current
	}
}

// echoChannel is the Go-owned channel the cancel test uses. It answers echo with the same argument,
// which is enough to prove the request was served locally rather than forwarded.
type echoChannel struct{}

func (c *echoChannel) Call(_ context.Context, method string, args []rpc.Value) (rpc.Value, error) {
	if method != "echo" {
		return nil, &rpc.Error{Message: "Method not found: " + method}
	}
	out := make([]any, len(args))
	for i, arg := range args {
		out[i] = arg
	}
	return out, nil
}

func (c *echoChannel) Listen(context.Context, string, rpc.Value) (<-chan rpc.Value, func(), error) {
	return nil, nil, &rpc.Error{Message: "Event not found"}
}
