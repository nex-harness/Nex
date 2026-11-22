package browser

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"

	"github.com/coder/websocket"
)

type cdpRequest struct {
	ID        int64  `json:"id"`
	Method    string `json:"method"`
	Params    any    `json:"params,omitempty"`
	SessionID string `json:"sessionId,omitempty"`
}

type cdpErrorObj struct {
	Code    int64  `json:"code"`
	Message string `json:"message"`
}

type cdpEnvelope struct {
	ID        int64           `json:"id,omitempty"`
	SessionID string          `json:"sessionId,omitempty"`
	Method    string          `json:"method,omitempty"`
	Params    json.RawMessage `json:"params,omitempty"`
	Result    json.RawMessage `json:"result,omitempty"`
	Error     *cdpErrorObj    `json:"error,omitempty"`
}

// CDPEventHandler receives CDP event payloads along with their flat target sessionID.
type CDPEventHandler func(sessionID string, params json.RawMessage)

// CDPClient is a zero-CGO JSON-RPC client over WebSocket supporting flat target multiplexing.
type CDPClient struct {
	conn      *websocket.Conn
	nextID    atomic.Int64
	writeMu   sync.Mutex
	pendingMu sync.Mutex
	pending   map[int64]chan cdpEnvelope
	eventsMu  sync.RWMutex
	handlers  map[string][]CDPEventHandler
	closeOnce sync.Once
	closedCh  chan struct{}
}

// DialCDP connects to a Chromium WebSocket debugger endpoint (browser or page target).
func DialCDP(ctx context.Context, wsURL string) (*CDPClient, error) {
	conn, _, err := websocket.Dial(ctx, wsURL, nil)
	if err != nil {
		return nil, newError(ErrBackendUnavailable, "none", fmt.Sprintf("dial CDP %s: %v", wsURL, err))
	}
	// Allow large screencast/screenshot payloads (up to 32 MB).
	conn.SetReadLimit(32 * 1024 * 1024)
	c := &CDPClient{
		conn:     conn,
		pending:  make(map[int64]chan cdpEnvelope),
		handlers: make(map[string][]CDPEventHandler),
		closedCh: make(chan struct{}),
	}
	go c.readLoop()
	return c, nil
}

func (c *CDPClient) OnEvent(method string, fn CDPEventHandler) {
	c.eventsMu.Lock()
	defer c.eventsMu.Unlock()
	c.handlers[method] = append(c.handlers[method], fn)
}

func (c *CDPClient) Call(ctx context.Context, sessionID, method string, params any, out any) error {
	select {
	case <-c.closedCh:
		return newError(ErrBackendUnavailable, "none", "CDP connection closed")
	default:
	}

	id := c.nextID.Add(1)
	waitCh := make(chan cdpEnvelope, 1)
	c.pendingMu.Lock()
	c.pending[id] = waitCh
	c.pendingMu.Unlock()

	defer func() {
		c.pendingMu.Lock()
		delete(c.pending, id)
		c.pendingMu.Unlock()
	}()

	payload, err := json.Marshal(cdpRequest{
		ID:        id,
		Method:    method,
		Params:    params,
		SessionID: sessionID,
	})
	if err != nil {
		return err
	}

	c.writeMu.Lock()
	wErr := c.conn.Write(ctx, websocket.MessageText, payload)
	c.writeMu.Unlock()
	if wErr != nil {
		if errors.Is(wErr, context.DeadlineExceeded) {
			return newError(ErrTimeout, "uncertain", fmt.Sprintf("CDP %s write timeout: %v", method, wErr))
		}
		return newError(ErrBackendUnavailable, "uncertain", fmt.Sprintf("CDP %s write failed: %v", method, wErr))
	}

	select {
	case env, ok := <-waitCh:
		if !ok {
			return newError(ErrBackendUnavailable, "uncertain", "CDP connection closed mid-call")
		}
		if env.Error != nil {
			code := ErrExecutionError
			if errors.Is(ctx.Err(), context.DeadlineExceeded) {
				code = ErrTimeout
			}
			return newError(code, "uncertain", fmt.Sprintf("CDP %s failed (%d): %s", method, env.Error.Code, env.Error.Message))
		}
		if out != nil && len(env.Result) > 0 {
			if err := json.Unmarshal(env.Result, out); err != nil {
				return fmt.Errorf("decode CDP %s result: %w", method, err)
			}
		}
		return nil
	case <-ctx.Done():
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return newError(ErrTimeout, "uncertain", fmt.Sprintf("CDP %s timed out", method))
		}
		return ctx.Err()
	case <-c.closedCh:
		return newError(ErrBackendUnavailable, "uncertain", "CDP connection closed")
	}
}

func (c *CDPClient) readLoop() {
	defer c.Close()
	for {
		_, data, err := c.conn.Read(context.Background())
		if err != nil {
			return
		}
		var env cdpEnvelope
		if json.Unmarshal(data, &env) != nil {
			continue
		}
		if env.ID != 0 {
			c.pendingMu.Lock()
			ch := c.pending[env.ID]
			c.pendingMu.Unlock()
			if ch != nil {
				select {
				case ch <- env:
				default:
				}
			}
			continue
		}
		if env.Method != "" {
			c.eventsMu.RLock()
			list := append([]CDPEventHandler(nil), c.handlers[env.Method]...)
			c.eventsMu.RUnlock()
			for _, fn := range list {
				fn(env.SessionID, env.Params)
			}
		}
	}
}

func (c *CDPClient) Closed() <-chan struct{} {
	return c.closedCh
}

func (c *CDPClient) Close() {
	c.closeOnce.Do(func() {
		close(c.closedCh)
		_ = c.conn.Close(websocket.StatusNormalClosure, "bye")
		c.pendingMu.Lock()
		for _, ch := range c.pending {
			close(ch)
		}
		c.pending = map[int64]chan cdpEnvelope{}
		c.pendingMu.Unlock()
	})
}
