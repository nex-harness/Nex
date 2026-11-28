package browser

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"sync"
	"sync/atomic"
	"time"

	"github.com/coder/websocket"
)

// DefaultBridgeListenAddr is the default loopback address for the Nex Browser Bridge WebSocket server.
const DefaultBridgeListenAddr = "127.0.0.1:19222"

type bridgeRequest struct {
	ID     string         `json:"id"`
	Action string         `json:"action"`
	Args   map[string]any `json:"args,omitempty"`
}

type bridgeResponse struct {
	ID    string          `json:"id"`
	Type  string          `json:"type,omitempty"`
	OK    bool            `json:"ok"`
	Data  json.RawMessage `json:"data,omitempty"`
	Error string          `json:"error,omitempty"`
}

// BridgeServer accepts WebSocket connections from the Nex Browser Bridge MV3 extension
// (running either in the user's daily Chrome or inside managed --headless=new Chromium)
// and dispatches fast in-page DOM/tab commands over RPC.
type BridgeServer struct {
	mu      sync.Mutex
	srv     *http.Server
	ln      net.Listener
	addr    string
	conn    *websocket.Conn
	pending map[string]chan bridgeResponse
	seq     atomic.Int64
	closed  bool
}

// NewBridgeServer starts the loopback WebSocket server on listenAddr (e.g. "127.0.0.1:19222" or "127.0.0.1:0").
func NewBridgeServer(listenAddr string) (*BridgeServer, error) {
	if listenAddr == "" {
		listenAddr = DefaultBridgeListenAddr
	}
	ln, err := net.Listen("tcp", listenAddr)
	if err != nil {
		return nil, fmt.Errorf("listen browser bridge on %s: %w", listenAddr, err)
	}
	b := &BridgeServer{
		ln:      ln,
		addr:    ln.Addr().String(),
		pending: make(map[string]chan bridgeResponse),
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/ws/bridge", b.handleWS)
	mux.HandleFunc("/health", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"ok":        true,
			"connected": b.Connected(),
			"addr":      b.addr,
		})
	})
	b.srv = &http.Server{Handler: mux}
	go func() {
		_ = b.srv.Serve(ln)
	}()
	return b, nil
}

// Addr returns the actual bound host:port (useful when listening on :0 in tests).
func (b *BridgeServer) Addr() string {
	return b.addr
}

// Connected reports whether a browser extension client is currently attached.
func (b *BridgeServer) Connected() bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.conn != nil
}

// Close shuts down the bridge server and fails any pending RPCs.
func (b *BridgeServer) Close() {
	b.mu.Lock()
	if b.closed {
		b.mu.Unlock()
		return
	}
	b.closed = true
	conn := b.conn
	b.conn = nil
	pending := b.pending
	b.pending = make(map[string]chan bridgeResponse)
	srv := b.srv
	b.mu.Unlock()

	if conn != nil {
		_ = conn.Close(websocket.StatusNormalClosure, "bridge closing")
	}
	for _, ch := range pending {
		close(ch)
	}
	if srv != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = srv.Shutdown(ctx)
	}
}

func (b *BridgeServer) handleWS(w http.ResponseWriter, r *http.Request) {
	c, err := websocket.Accept(w, r, &websocket.AcceptOptions{
		InsecureSkipVerify: true, // Extension origin is chrome-extension://<id>
	})
	if err != nil {
		return
	}
	c.SetReadLimit(16 << 20)

	b.mu.Lock()
	if b.conn != nil {
		_ = b.conn.Close(websocket.StatusGoingAway, "replaced by new extension connection")
	}
	b.conn = c
	b.mu.Unlock()

	defer func() {
		b.mu.Lock()
		if b.conn == c {
			b.conn = nil
		}
		b.mu.Unlock()
		_ = c.Close(websocket.StatusNormalClosure, "")
	}()

	for {
		_, data, err := c.Read(r.Context())
		if err != nil {
			return
		}
		var resp bridgeResponse
		if json.Unmarshal(data, &resp) != nil {
			continue
		}
		if resp.Type == "hello" || resp.ID == "" {
			continue
		}
		b.mu.Lock()
		ch := b.pending[resp.ID]
		delete(b.pending, resp.ID)
		b.mu.Unlock()
		if ch != nil {
			ch <- resp
			close(ch)
		}
	}
}

// Call dispatches an action to the connected browser extension and waits for the response.
func (b *BridgeServer) Call(ctx context.Context, action string, args map[string]any) (json.RawMessage, error) {
	b.mu.Lock()
	c := b.conn
	if c == nil {
		b.mu.Unlock()
		return nil, newError(ErrBackendUnavailable, "none", "Nex Browser Bridge extension is not connected")
	}
	id := fmt.Sprintf("br_%d", b.seq.Add(1))
	ch := make(chan bridgeResponse, 1)
	b.pending[id] = ch
	b.mu.Unlock()

	defer func() {
		b.mu.Lock()
		delete(b.pending, id)
		b.mu.Unlock()
	}()

	raw, err := json.Marshal(bridgeRequest{ID: id, Action: action, Args: args})
	if err != nil {
		return nil, err
	}
	writeCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	if err := c.Write(writeCtx, websocket.MessageText, raw); err != nil {
		return nil, newError(ErrBackendUnavailable, "uncertain", fmt.Sprintf("send to browser bridge: %v", err))
	}

	select {
	case resp, ok := <-ch:
		if !ok {
			return nil, newError(ErrBackendUnavailable, "uncertain", "browser bridge connection closed")
		}
		if !resp.OK {
			msg := resp.Error
			if msg == "" {
				msg = "browser extension returned error"
			}
			return nil, newError(ErrExecutionError, "uncertain", msg)
		}
		return resp.Data, nil
	case <-ctx.Done():
		return nil, errors.New("browser bridge call timed out or cancelled")
	}
}
