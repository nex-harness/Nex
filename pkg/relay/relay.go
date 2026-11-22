package relay

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/coder/websocket"
	"github.com/google/uuid"
)

// Config configures a stateless Nex zero-trust blind relay server.
type Config struct {
	AuthToken         string
	AcceptTimeout     time.Duration
	MaxPendingPerHost int
	MaxTunnelsPerHost int
	KeepAliveInterval time.Duration
}

// Stats summarizes live relay connections without inspecting encrypted payloads.
type Stats struct {
	OnlineHosts    int  `json:"online_hosts"`
	ActiveTunnels  int  `json:"active_tunnels"`
	PendingTunnels int  `json:"pending_tunnels"`
	AuthRequired   bool `json:"auth_required"`
}

// ControlMessage is exchanged over the host control WebSocket (/relay/v1/host).
type ControlMessage struct {
	Op        string `json:"op"`
	SessionID string `json:"session_id,omitempty"`
	Error     string `json:"error,omitempty"`
	TS        int64  `json:"ts,omitempty"`
}

const maxRelayReadLimit = 16 << 20 // 16 MiB frames (supports large file & screencast payloads)

type hostEntry struct {
	nodeID  string
	ws      *websocket.Conn
	sendMu  sync.Mutex
	pending int
	tunnels int
	ctx     context.Context
	cancel  context.CancelFunc
}

type pendingSession struct {
	id        string
	nodeID    string
	host      *hostEntry
	hostReady chan *websocket.Conn
	done      chan struct{}
	doneOnce  sync.Once
}

func (p *pendingSession) closeDone() {
	p.doneOnce.Do(func() {
		close(p.done)
	})
}

// Server implements a stateless WebSocket byte-pipe relay for zero-trust mTLS tunnels.
type Server struct {
	cfg Config
	mux *http.ServeMux

	mu            sync.Mutex
	hosts         map[string]*hostEntry
	pending       map[string]*pendingSession
	activeTunnels int
}

// New constructs a Server with sensible defaults and registers /relay/v1/* handlers.
func New(cfg Config) *Server {
	if cfg.AcceptTimeout <= 0 {
		cfg.AcceptTimeout = 15 * time.Second
	}
	if cfg.MaxPendingPerHost <= 0 {
		cfg.MaxPendingPerHost = 32
	}
	if cfg.MaxTunnelsPerHost <= 0 {
		cfg.MaxTunnelsPerHost = 64
	}
	if cfg.KeepAliveInterval <= 0 {
		cfg.KeepAliveInterval = 25 * time.Second
	}
	s := &Server{
		cfg:     cfg,
		mux:     http.NewServeMux(),
		hosts:   make(map[string]*hostEntry),
		pending: make(map[string]*pendingSession),
	}
	s.Mount(s.mux)
	return s
}

// Handler returns the standalone HTTP handler for cmd/nex-relay.
func (s *Server) Handler() http.Handler {
	return s.mux
}

// Mount attaches all /relay/v1/* routes onto an existing ServeMux (e.g. nex-server).
func (s *Server) Mount(mux *http.ServeMux) {
	mux.HandleFunc("/relay/v1/health", s.handleHealth)
	mux.HandleFunc("/relay/v1/host", s.handleHost)
	mux.HandleFunc("/relay/v1/connect", s.handleConnect)
	mux.HandleFunc("/relay/v1/accept", s.handleAccept)
}

// Stats returns a thread-safe snapshot of current host and tunnel counts.
func (s *Server) Stats() Stats {
	s.mu.Lock()
	defer s.mu.Unlock()
	return Stats{
		OnlineHosts:    len(s.hosts),
		ActiveTunnels:  s.activeTunnels,
		PendingTunnels: len(s.pending),
		AuthRequired:   s.cfg.AuthToken != "",
	}
}

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	st := s.Stats()
	writeRelayJSON(w, http.StatusOK, map[string]any{
		"ok":              true,
		"online_hosts":    st.OnlineHosts,
		"active_tunnels":  st.ActiveTunnels,
		"pending_tunnels": st.PendingTunnels,
		"auth_required":   st.AuthRequired,
	})
}

func (s *Server) handleHost(w http.ResponseWriter, r *http.Request) {
	if !s.authorize(r) {
		writeRelayJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
		return
	}
	nodeID := strings.TrimSpace(r.URL.Query().Get("node_id"))
	if !isValidNodeID(nodeID) {
		writeRelayJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid node_id"})
		return
	}

	ws, err := websocket.Accept(w, r, &websocket.AcceptOptions{
		InsecureSkipVerify: true,
	})
	if err != nil {
		return
	}
	ws.SetReadLimit(64 << 10)

	hostCtx, cancel := context.WithCancel(r.Context())
	entry := &hostEntry{
		nodeID: nodeID,
		ws:     ws,
		ctx:    hostCtx,
		cancel: cancel,
	}

	var replaced *hostEntry
	s.mu.Lock()
	replaced = s.hosts[nodeID]
	s.hosts[nodeID] = entry
	s.mu.Unlock()

	if replaced != nil {
		replaced.cancel()
		_ = replaced.ws.Close(websocket.StatusGoingAway, "replaced by newer host connection")
	}

	defer func() {
		cancel()
		s.mu.Lock()
		if s.hosts[nodeID] == entry {
			delete(s.hosts, nodeID)
		}
		s.mu.Unlock()
		_ = ws.Close(websocket.StatusNormalClosure, "")
	}()

	if err := entry.sendJSON(hostCtx, ControlMessage{
		Op: "registered",
		TS: time.Now().UnixMilli(),
	}); err != nil {
		return
	}

	go func() {
		ticker := time.NewTicker(s.cfg.KeepAliveInterval)
		defer ticker.Stop()
		for {
			select {
			case <-hostCtx.Done():
				return
			case <-ticker.C:
				pCtx, pCancel := context.WithTimeout(hostCtx, 5*time.Second)
				err := ws.Ping(pCtx)
				pCancel()
				if err != nil {
					cancel()
					return
				}
			}
		}
	}()

	for {
		if _, _, err := ws.Read(hostCtx); err != nil {
			return
		}
	}
}

func (s *Server) handleConnect(w http.ResponseWriter, r *http.Request) {
	if !s.authorize(r) {
		writeRelayJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
		return
	}
	nodeID := strings.TrimSpace(r.URL.Query().Get("node_id"))
	if !isValidNodeID(nodeID) {
		writeRelayJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid node_id"})
		return
	}

	sessionID := "rly_" + uuid.NewString()
	pending := &pendingSession{
		id:        sessionID,
		nodeID:    nodeID,
		hostReady: make(chan *websocket.Conn, 1),
		done:      make(chan struct{}),
	}

	s.mu.Lock()
	host := s.hosts[nodeID]
	if host == nil {
		s.mu.Unlock()
		writeRelayJSON(w, http.StatusNotFound, map[string]string{"error": "host offline"})
		return
	}
	if host.pending >= s.cfg.MaxPendingPerHost || host.tunnels >= s.cfg.MaxTunnelsPerHost {
		s.mu.Unlock()
		writeRelayJSON(w, http.StatusTooManyRequests, map[string]string{"error": "host tunnel limit reached"})
		return
	}
	host.pending++
	pending.host = host
	s.pending[sessionID] = pending
	s.mu.Unlock()

	cleanupPending := func() {
		s.mu.Lock()
		if cur, ok := s.pending[sessionID]; ok && cur == pending {
			delete(s.pending, sessionID)
			if host.pending > 0 {
				host.pending--
			}
		}
		s.mu.Unlock()
		pending.closeDone()
	}

	clientWS, err := websocket.Accept(w, r, &websocket.AcceptOptions{
		InsecureSkipVerify: true,
	})
	if err != nil {
		cleanupPending()
		return
	}
	clientWS.SetReadLimit(maxRelayReadLimit)

	ctrlCtx, ctrlCancel := context.WithTimeout(host.ctx, 5*time.Second)
	err = host.sendJSON(ctrlCtx, ControlMessage{
		Op:        "accept",
		SessionID: sessionID,
		TS:        time.Now().UnixMilli(),
	})
	ctrlCancel()
	if err != nil {
		cleanupPending()
		_ = clientWS.Close(websocket.StatusBadGateway, "host control unreachable")
		return
	}

	timer := time.NewTimer(s.cfg.AcceptTimeout)
	defer timer.Stop()

	var hostWS *websocket.Conn
	select {
	case hostWS = <-pending.hostReady:
		s.mu.Lock()
		if host.pending > 0 {
			host.pending--
		}
		host.tunnels++
		s.activeTunnels++
		s.mu.Unlock()
	case <-timer.C:
		cleanupPending()
		_ = clientWS.Close(websocket.StatusTryAgainLater, "host accept timeout")
		return
	case <-host.ctx.Done():
		cleanupPending()
		_ = clientWS.Close(websocket.StatusGoingAway, "host disconnected")
		return
	case <-r.Context().Done():
		cleanupPending()
		_ = clientWS.Close(websocket.StatusGoingAway, "client cancelled")
		return
	}

	defer func() {
		s.mu.Lock()
		if host.tunnels > 0 {
			host.tunnels--
		}
		if s.activeTunnels > 0 {
			s.activeTunnels--
		}
		s.mu.Unlock()
		pending.closeDone()
	}()

	bridgeWS(r.Context(), clientWS, hostWS)
}

func (s *Server) handleAccept(w http.ResponseWriter, r *http.Request) {
	if !s.authorize(r) {
		writeRelayJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
		return
	}
	sessionID := strings.TrimSpace(r.URL.Query().Get("session_id"))
	if sessionID == "" {
		writeRelayJSON(w, http.StatusBadRequest, map[string]string{"error": "session_id required"})
		return
	}

	s.mu.Lock()
	pending := s.pending[sessionID]
	if pending != nil {
		delete(s.pending, sessionID)
	}
	s.mu.Unlock()
	if pending == nil {
		writeRelayJSON(w, http.StatusNotFound, map[string]string{"error": "unknown or expired session_id"})
		return
	}

	hostWS, err := websocket.Accept(w, r, &websocket.AcceptOptions{
		InsecureSkipVerify: true,
	})
	if err != nil {
		s.mu.Lock()
		if pending.host != nil && pending.host.pending > 0 {
			pending.host.pending--
		}
		s.mu.Unlock()
		pending.closeDone()
		return
	}
	hostWS.SetReadLimit(maxRelayReadLimit)

	select {
	case pending.hostReady <- hostWS:
	case <-pending.done:
		_ = hostWS.Close(websocket.StatusGoingAway, "session ended")
		return
	}

	// Keep this HTTP handler alive until the bidirectional pump completes.
	select {
	case <-pending.done:
	case <-r.Context().Done():
	}
}

func bridgeWS(parent context.Context, a, b *websocket.Conn) {
	ctx, cancel := context.WithCancel(parent)
	defer cancel()

	var wg sync.WaitGroup
	wg.Add(2)
	pump := func(dst, src *websocket.Conn) {
		defer wg.Done()
		defer cancel()
		for {
			typ, data, err := src.Read(ctx)
			if err != nil {
				return
			}
			if err := dst.Write(ctx, typ, data); err != nil {
				return
			}
		}
	}

	go pump(b, a)
	go pump(a, b)

	<-ctx.Done()
	_ = a.Close(websocket.StatusNormalClosure, "")
	_ = b.Close(websocket.StatusNormalClosure, "")
	wg.Wait()
}

func (e *hostEntry) sendJSON(ctx context.Context, msg ControlMessage) error {
	raw, err := json.Marshal(msg)
	if err != nil {
		return err
	}
	e.sendMu.Lock()
	defer e.sendMu.Unlock()
	return e.ws.Write(ctx, websocket.MessageText, raw)
}

func (s *Server) authorize(r *http.Request) bool {
	if s.cfg.AuthToken == "" {
		return true
	}
	tok := ""
	authHeader := strings.TrimSpace(r.Header.Get("Authorization"))
	if strings.HasPrefix(strings.ToLower(authHeader), "bearer ") {
		tok = strings.TrimSpace(authHeader[7:])
	}
	if tok == "" {
		tok = strings.TrimSpace(r.URL.Query().Get("token"))
	}
	if len(tok) != len(s.cfg.AuthToken) {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(tok), []byte(s.cfg.AuthToken)) == 1
}

func isValidNodeID(nodeID string) bool {
	if len(nodeID) < 4 || len(nodeID) > 96 {
		return false
	}
	for i := 0; i < len(nodeID); i++ {
		c := nodeID[i]
		if (c >= 'a' && c <= 'z') ||
			(c >= 'A' && c <= 'Z') ||
			(c >= '0' && c <= '9') ||
			c == '_' || c == '-' || c == '.' {
			continue
		}
		return false
	}
	return true
}

func writeRelayJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}
