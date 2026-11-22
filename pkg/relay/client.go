package relay

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"

	"github.com/coder/websocket"
)

type cancelOnCloseConn struct {
	net.Conn
	cancel context.CancelFunc
	once   sync.Once
}

func (c *cancelOnCloseConn) Close() error {
	err := c.Conn.Close()
	c.once.Do(c.cancel)
	return err
}

func wrapNetConn(ws *websocket.Conn) net.Conn {
	ws.SetReadLimit(maxRelayReadLimit)
	connCtx, cancel := context.WithCancel(context.Background())
	nc := websocket.NetConn(connCtx, ws, websocket.MessageBinary)
	return &cancelOnCloseConn{
		Conn:   nc,
		cancel: cancel,
	}
}

// NormalizeRelayWSURL builds a ws:// or wss:// endpoint URL for the given relay route path.
func NormalizeRelayWSURL(baseURL, routePath string, query url.Values) (string, error) {
	raw := strings.TrimSpace(baseURL)
	if raw == "" {
		return "", errors.New("relay URL is required")
	}
	u, err := url.Parse(raw)
	if err != nil {
		return "", fmt.Errorf("invalid relay URL: %w", err)
	}
	switch strings.ToLower(u.Scheme) {
	case "http", "ws":
		u.Scheme = "ws"
	case "https", "wss":
		u.Scheme = "wss"
	default:
		return "", fmt.Errorf("unsupported relay URL scheme %q", u.Scheme)
	}
	if u.Host == "" {
		return "", errors.New("relay URL must include a host")
	}
	basePath := strings.TrimRight(u.Path, "/")
	basePath = strings.TrimSuffix(basePath, "/relay/v1")
	u.Path = basePath + routePath
	q := u.Query()
	for k, vals := range query {
		for _, v := range vals {
			q.Set(k, v)
		}
	}
	u.RawQuery = q.Encode()
	return u.String(), nil
}

func dialOptions(authToken string) *websocket.DialOptions {
	if strings.TrimSpace(authToken) == "" {
		return nil
	}
	return &websocket.DialOptions{
		HTTPHeader: http.Header{
			"Authorization": []string{"Bearer " + strings.TrimSpace(authToken)},
		},
	}
}

// DialHostControl connects an intranet host to the relay control endpoint and invokes onAccept
// for each incoming client session with a bridged net.Conn ready for TLS / mux handling.
func DialHostControl(
	ctx context.Context,
	relayBaseURL string,
	nodeID string,
	authToken string,
	onAccept func(ctx context.Context, sessionID string, conn net.Conn),
) error {
	ctrlURL, err := NormalizeRelayWSURL(relayBaseURL, "/relay/v1/host", url.Values{
		"node_id": {strings.TrimSpace(nodeID)},
	})
	if err != nil {
		return err
	}
	opts := dialOptions(authToken)
	ws, _, err := websocket.Dial(ctx, ctrlURL, opts)
	if err != nil {
		return err
	}
	defer ws.Close(websocket.StatusNormalClosure, "")
	ws.SetReadLimit(64 << 10)

	var wg sync.WaitGroup
	defer wg.Wait()

	for {
		_, data, err := ws.Read(ctx)
		if err != nil {
			return err
		}
		var msg ControlMessage
		if json.Unmarshal(data, &msg) != nil {
			continue
		}
		if msg.Op != "accept" || strings.TrimSpace(msg.SessionID) == "" {
			continue
		}
		sid := strings.TrimSpace(msg.SessionID)
		wg.Add(1)
		go func() {
			defer wg.Done()
			acceptURL, uErr := NormalizeRelayWSURL(relayBaseURL, "/relay/v1/accept", url.Values{
				"session_id": {sid},
			})
			if uErr != nil {
				return
			}
			dataWS, _, dErr := websocket.Dial(ctx, acceptURL, opts)
			if dErr != nil {
				return
			}
			nc := wrapNetConn(dataWS)
			if onAccept != nil {
				onAccept(ctx, sid, nc)
			} else {
				_ = nc.Close()
			}
		}()
	}
}

// DialClientTunnel opens an end-to-end byte stream through relayBaseURL to host nodeID.
// The provided ctx governs dialing only; the returned net.Conn remains open until Close().
func DialClientTunnel(ctx context.Context, relayBaseURL, nodeID, authToken string) (net.Conn, error) {
	connURL, err := NormalizeRelayWSURL(relayBaseURL, "/relay/v1/connect", url.Values{
		"node_id": {strings.TrimSpace(nodeID)},
	})
	if err != nil {
		return nil, err
	}
	ws, resp, err := websocket.Dial(ctx, connURL, dialOptions(authToken))
	if err != nil {
		if resp != nil {
			return nil, fmt.Errorf("relay connect HTTP %d: %w", resp.StatusCode, err)
		}
		return nil, err
	}
	return wrapNetConn(ws), nil
}
