// Package runnerws implements the server side of the runner link protocol:
// one WebSocket per runner, JSON text frames, hello/welcome handshake, call
// forwarding, dynamic listen streams and WebSocket-level ping/pong.
package runnerws

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"sync"
	"sync/atomic"
	"time"

	"github.com/coder/websocket"

	"github.com/tylinux/nex-next/server/internal/auth"
	"github.com/tylinux/nex-next/server/internal/runners"
)

// Link timing. PingInterval/PingTimeout follow docs/next/README.md.
const (
	PingInterval = 15 * time.Second
	PingTimeout  = 45 * time.Second
	// writeTimeout bounds a single frame write.
	writeTimeout = 10 * time.Second
	// callTimeout is the default deadline for one forwarded call. A caller's
	// context still cancels it earlier.
	callTimeout = 10 * time.Minute
	// maxReadLimit bounds a single runner frame.
	maxReadLimit = 32 << 20
)

// DefaultServerVersion is reported in the welcome frame when Serve is called
// without an explicit version.
const DefaultServerVersion = "0.0.0-dev"

// handshakeTimeout bounds how long a peer may hold /runner before sending
// hello. Without it a token holder (or a stalled TCP peer that authenticated)
// can pin a goroutine and socket forever, since keepalive only starts after the
// handshake.
var handshakeTimeout = 10 * time.Second

type helloFrame struct {
	RunnerID   string              `json:"runnerId"`
	Name       string              `json:"name"`
	Version    string              `json:"version"`
	OS         string              `json:"os"`
	Arch       string              `json:"arch"`
	Workspaces []runners.Workspace `json:"workspaces"`
	// LastAppliedVersion is the config version the runner last applied
	// (docs/next/config-mcp-skills.md decision 4). Optional; 0 means "never
	// applied" and triggers an immediate push.
	LastAppliedVersion uint64 `json:"lastAppliedVersion,omitempty"`
}

// ConfigHandler receives runner acknowledgements and connect events. The
// server-side implementation (internal/local) owns the config doc, the push
// scheduling and the per-runner facts.
type ConfigHandler interface {
	// RunnerConnected is called after registration with the runner id and the
	// version it last applied.
	RunnerConnected(runnerID string, lastAppliedVersion uint64)
	// RunnerDisconnected forgets per-runner applied state.
	RunnerDisconnected(runnerID string)
	// ConfigApplied records one `configApplied` frame.
	ConfigApplied(runnerID string, version uint64, ok bool, errors []map[string]any, facts map[string]any)
	// OnLinkReady lets the handler attach a push function for this runner.
	OnLinkReady(runnerID string, push func(version uint64, payload any) error)
}

type runnerError struct {
	Message string `json:"message"`
	Name    string `json:"name,omitempty"`
}

type callResult struct {
	value json.RawMessage
	err   error
}

type pendingCall struct {
	ch chan callResult
}

// Conn is one live runner connection. It implements runners.Link.
type Conn struct {
	logger  *slog.Logger
	reg     *runners.Registry
	config  ConfigHandler
	ws      *websocket.Conn
	version string

	writeMu sync.Mutex

	mu      sync.Mutex
	pending map[string]*pendingCall
	listens map[string]func(json.RawMessage)
	closed  bool
	info    runners.Info

	done chan struct{}
	// doneOnce guards closing done; shutdownOnce guards the pending-call drain.
	// They are separate so Close() (duplicate runner, failed ping) cannot
	// suppress shutdown()'s failure of pending calls.
	doneOnce     sync.Once
	shutdownOnce sync.Once
}

// errConnClosed is the stable browser-facing error after a runner goes away.
var errConnClosed = runners.OfflineError{}

var sequence atomic.Uint64

// Serve accepts /runner requests, validates the bearer token, performs the
// hello/welcome handshake and runs the link until the peer disconnects.
func Serve(logger *slog.Logger, reg *runners.Registry, runnerToken, serverVersion string, w http.ResponseWriter, r *http.Request) {
	ServeWithConfig(logger, reg, runnerToken, serverVersion, nil, w, r)
}

// ServeWithConfig is Serve plus the server-owned config push (docs/next/
// config-mcp-skills.md decision 4). config may be nil, which disables the push
// (the client then keeps its local fallback config).
func ServeWithConfig(logger *slog.Logger, reg *runners.Registry, runnerToken, serverVersion string, config ConfigHandler, w http.ResponseWriter, r *http.Request) {
	if !auth.TokensEqual(runnerToken, bearerToken(r)) {
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}
	if serverVersion == "" {
		serverVersion = DefaultServerVersion
	}

	ws, err := websocket.Accept(w, r, &websocket.AcceptOptions{
		// Runners are authenticated by bearer token; Origin carries no session
		// semantics for a non-browser client.
		InsecureSkipVerify: true,
		CompressionMode:    websocket.CompressionDisabled,
	})
	if err != nil {
		logger.Warn("runner websocket upgrade failed", "error", err)
		return
	}
	ws.SetReadLimit(maxReadLimit)

	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()

	conn := &Conn{
		logger:  logger.With("component", "runner-link"),
		reg:     reg,
		config:  config,
		ws:      ws,
		version: serverVersion,
		pending: make(map[string]*pendingCall),
		listens: make(map[string]func(json.RawMessage)),
		done:    make(chan struct{}),
	}

	handshakeCtx, handshakeCancel := context.WithTimeout(ctx, handshakeTimeout)
	err = conn.handshake(handshakeCtx)
	handshakeCancel()
	if err != nil {
		conn.logger.Warn("runner handshake failed", "error", err)
		_ = ws.Close(websocket.StatusPolicyViolation, "handshake failed")
		return
	}
	defer conn.Close()

	err = conn.readLoop(ctx)
	conn.shutdown()
	reg.Unregister(conn.info.RunnerID, conn)
	if config != nil {
		config.RunnerDisconnected(conn.info.RunnerID)
	}
	if err != nil && !isExpectedClose(err) {
		conn.logger.Info("runner link closed", "runnerId", conn.info.RunnerID, "error", err)
	}
}

func bearerToken(r *http.Request) string {
	const prefix = "Bearer "
	header := r.Header.Get("Authorization")
	if len(header) > len(prefix) && header[:len(prefix)] == prefix {
		return header[len(prefix):]
	}
	return ""
}

func isExpectedClose(err error) bool {
	var closeErr websocket.CloseError
	if errors.As(err, &closeErr) {
		return true
	}
	return errors.Is(err, context.Canceled) || errors.Is(err, errConnClosed) || errors.Is(err, context.DeadlineExceeded)
}

func (c *Conn) handshake(ctx context.Context) error {
	typ, data, err := c.ws.Read(ctx)
	if err != nil {
		return fmt.Errorf("reading hello: %w", err)
	}
	if typ != websocket.MessageText {
		return errors.New("hello must be a text frame")
	}
	var probe struct {
		T string `json:"t"`
	}
	if err := json.Unmarshal(data, &probe); err != nil {
		return fmt.Errorf("decoding hello envelope: %w", err)
	}
	if probe.T != "hello" {
		return fmt.Errorf("expected hello, got %q", probe.T)
	}
	var hello helloFrame
	if err := json.Unmarshal(data, &hello); err != nil {
		return fmt.Errorf("decoding hello payload: %w", err)
	}
	if hello.RunnerID == "" {
		return errors.New("hello.runnerId is required")
	}
	// The runner id is embedded in every workspace identity
	// (`runner:<runnerId>:<path>`), so the separator and whitespace would make
	// identities ambiguous. Reject them at the handshake.
	if !runners.ValidRunnerID(hello.RunnerID) {
		return fmt.Errorf("hello.runnerId %q must not contain ':' or whitespace", hello.RunnerID)
	}

	c.info = runners.Info{
		RunnerID:   hello.RunnerID,
		Name:       hello.Name,
		Version:    hello.Version,
		OS:         hello.OS,
		Arch:       hello.Arch,
		Workspaces: hello.Workspaces,
	}

	welcome, err := json.Marshal(map[string]any{"t": "welcome", "serverVersion": c.version})
	if err != nil {
		return err
	}
	if err := c.write(welcome); err != nil {
		return fmt.Errorf("writing welcome: %w", err)
	}

	// Register after welcome so the runner never sees `listen` before it has
	// been welcomed. Register closes a displaced duplicate itself.
	c.reg.Register(c.info, c)
	c.logger.Info("runner connected", "runnerId", hello.RunnerID, "name", hello.Name,
		"workspaces", len(hello.Workspaces), "version", hello.Version)
	if c.config != nil {
		c.config.OnLinkReady(hello.RunnerID, c.pushConfig)
		c.config.RunnerConnected(hello.RunnerID, hello.LastAppliedVersion)
	}
	return nil
}

// pushConfig writes one S→R `config` frame. The payload is the JSON body of the
// frame minus envelope fields ({"t","version","doc","secrets","bundles"}).
func (c *Conn) pushConfig(version uint64, payload any) error {

	var body []byte
	switch typed := payload.(type) {
	case nil:
		body = []byte("null")
	case []byte:
		body = typed
	case json.RawMessage:
		body = typed
	default:
		encoded, err := json.Marshal(typed)
		if err != nil {
			return fmt.Errorf("encoding config push: %w", err)
		}
		body = encoded
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(body, &fields); err != nil {
		return fmt.Errorf("config push body must be an object: %w", err)
	}
	frame := make(map[string]json.RawMessage, len(fields)+2)
	for key, value := range fields {
		frame[key] = value
	}
	t, _ := json.Marshal("config")
	versionValue, _ := json.Marshal(version)
	frame["t"] = t
	frame["version"] = versionValue
	if _, ok := frame["bundles"]; !ok {
		frame["bundles"], _ = json.Marshal([]any{})
	}
	if _, ok := frame["secrets"]; !ok {
		frame["secrets"], _ = json.Marshal(map[string]string{})
	}
	encoded, err := json.Marshal(frame)
	if err != nil {
		return err
	}
	return c.write(encoded)
}

// keepAlive pings the runner so an idle link is reaped by PingTimeout.
//
// It uses the application-level `ping` frame, not a WebSocket Ping: the
// library's Conn.Ping blocks until a pong arrives, and the pong is only
// delivered by the read loop — so a ping issued while the read loop waits
// out a caller deadline (or between reads) is never answered and would
// tear down a perfectly healthy link. Application pings are ordinary text
// frames: they are read like any other frame and `pong` is dispatched from
// the read loop, so any read activity keeps the link alive and silence is
// detected by the same path that detects a dead TCP peer.
func (c *Conn) keepAlive(ctx context.Context) {
	ticker := time.NewTicker(PingInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-c.done:
			return
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := c.sendControl("ping"); err != nil {
				c.logger.Info("runner link ping failed, closing", "runnerId", c.info.RunnerID, "error", err)
				c.Close()
				return
			}
		}
	}
}

func (c *Conn) readLoop(ctx context.Context) error {
	for {
		typ, data, err := c.ws.Read(ctx)
		if err != nil {
			return err
		}
		if typ != websocket.MessageText {
			continue
		}
		c.dispatch(data)
	}
}

func (c *Conn) dispatch(data []byte) {
	var env struct {
		T   string          `json:"t"`
		ID  string          `json:"id"`
		OK  *bool           `json:"ok"`
		Val json.RawMessage `json:"value"`
		Err *runnerError    `json:"error"`
	}
	if err := json.Unmarshal(data, &env); err != nil {
		c.logger.Warn("dropping malformed runner frame", "error", err)
		return
	}

	switch env.T {
	case "result":
		c.mu.Lock()
		pending := c.pending[env.ID]
		delete(c.pending, env.ID)
		c.mu.Unlock()
		if pending == nil {
			return
		}
		if env.OK != nil && *env.OK {
			pending.ch <- callResult{value: env.Val}
			return
		}
		remote := &runners.RemoteError{Message: "runner call failed"}
		if env.Err != nil && env.Err.Message != "" {
			remote.Message = env.Err.Message
			remote.Name = env.Err.Name
		}
		pending.ch <- callResult{err: remote}
	case "event":
		c.mu.Lock()
		emit := c.listens[env.ID]
		c.mu.Unlock()
		if emit != nil {
			emit(env.Val)
		}
	case "configApplied":
		var ack struct {
			Version uint64           `json:"version"`
			OK      bool             `json:"ok"`
			Errors  []map[string]any `json:"errors"`
			Facts   map[string]any   `json:"facts"`
		}
		if err := json.Unmarshal(data, &ack); err != nil {
			c.logger.Warn("dropping malformed configApplied", "error", err)
			return
		}
		if c.config != nil {
			c.config.ConfigApplied(c.info.RunnerID, ack.Version, ack.OK, ack.Errors, ack.Facts)
		}
	case "ping":
		_ = c.sendControl("pong")
	case "pong":
		// Application-level pong: nothing to do.
	default:
		c.logger.Debug("ignoring unknown runner frame", "t", env.T)
	}
}

func (c *Conn) sendControl(t string) error {
	data, err := json.Marshal(map[string]string{"t": t})
	if err != nil {
		return err
	}
	return c.write(data)
}

func (c *Conn) write(data []byte) error {
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), writeTimeout)
	defer cancel()
	return c.ws.Write(ctx, websocket.MessageText, data)
}

// Call forwards one channel call; it implements runners.Link.
func (c *Conn) Call(ctx context.Context, channel, method string, arg json.RawMessage) (json.RawMessage, error) {
	if len(arg) == 0 {
		arg = json.RawMessage("null")
	}
	id := fmt.Sprintf("srv-%d", sequence.Add(1))
	payload, err := json.Marshal(map[string]any{
		"t":       "call",
		"id":      id,
		"channel": channel,
		"method":  method,
		"arg":     arg,
	})
	if err != nil {
		return nil, fmt.Errorf("encoding call: %w", err)
	}

	pending := &pendingCall{ch: make(chan callResult, 1)}
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return nil, errConnClosed
	}
	c.pending[id] = pending
	c.mu.Unlock()

	if err := c.write(payload); err != nil {
		c.mu.Lock()
		delete(c.pending, id)
		c.mu.Unlock()
		return nil, errConnClosed
	}

	callCtx, cancel := context.WithTimeout(ctx, callTimeout)
	defer cancel()
	select {
	case result := <-pending.ch:
		if result.err != nil {
			return nil, result.err
		}
		return result.value, nil
	case <-callCtx.Done():
		c.mu.Lock()
		delete(c.pending, id)
		c.mu.Unlock()
		return nil, callCtx.Err()
	case <-c.done:
		return nil, errConnClosed
	}
}

// Listen starts a dynamic event stream; it implements runners.Link.
func (c *Conn) Listen(id, channel, event string, arg json.RawMessage, emit func(json.RawMessage)) error {
	if len(arg) == 0 {
		arg = json.RawMessage("null")
	}
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return errConnClosed
	}
	c.listens[id] = emit
	c.mu.Unlock()

	payload, err := json.Marshal(map[string]any{
		"t":       "listen",
		"id":      id,
		"channel": channel,
		"event":   event,
		"arg":     arg,
	})
	if err != nil {
		return fmt.Errorf("encoding listen: %w", err)
	}
	if err := c.write(payload); err != nil {
		c.mu.Lock()
		delete(c.listens, id)
		c.mu.Unlock()
		return errConnClosed
	}
	return nil
}

// Unlisten stops a stream; it implements runners.Link.
func (c *Conn) Unlisten(id string) {
	c.mu.Lock()
	_, ok := c.listens[id]
	delete(c.listens, id)
	closed := c.closed
	c.mu.Unlock()
	if !ok || closed {
		return
	}
	payload, err := json.Marshal(map[string]string{"t": "unlisten", "id": id})
	if err != nil {
		return
	}
	_ = c.write(payload)
}

// Close terminates the connection without a close handshake; it implements
// runners.Link. Pending calls are failed immediately as well, so a duplicate
// runner replacement never leaves a caller waiting on a dead socket.
func (c *Conn) Close() {
	c.doneOnce.Do(func() {
		close(c.done)
		_ = c.ws.CloseNow()
	})
	c.failPending()
}

// Done reports closure; it implements runners.Link.
func (c *Conn) Done() <-chan struct{} { return c.done }

// shutdown fails pending calls and drops listen sinks. The registry keeps the
// browser listen specs so a reconnecting runner can be re-subscribed.
func (c *Conn) shutdown() {
	c.doneOnce.Do(func() { close(c.done) })
	c.failPending()
}

// failPending drains the pending call map exactly once and delivers the
// offline error to every waiter.
func (c *Conn) failPending() {
	c.shutdownOnce.Do(func() {
		c.mu.Lock()
		c.closed = true
		pending := c.pending
		c.pending = make(map[string]*pendingCall)
		c.listens = make(map[string]func(json.RawMessage))
		c.mu.Unlock()

		for _, call := range pending {
			call.ch <- callResult{err: errConnClosed}
		}
	})
}
