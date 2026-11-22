package remote

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/hashicorp/yamux"
	"github.com/nex-harness/nex/pkg/relay"
)

// PairedHostProfile persists a paired remote Nex host and its reachable endpoints.
type PairedHostProfile struct {
	NodeID        string   `json:"node_id"`
	HostName      string   `json:"host_name"`
	Fingerprint   string   `json:"fingerprint"`
	DeviceID      string   `json:"device_id"`
	RelayURL      string   `json:"relay_url,omitempty"`
	RelayToken    string   `json:"relay_token,omitempty"`
	DirectURLs    []string `json:"direct_urls,omitempty"`
	LANURLs       []string `json:"lan_urls,omitempty"`
	LastTransport string   `json:"last_transport,omitempty"`
	PairedAt      int64    `json:"paired_at"`
}

// TransportStatus describes the active connection path of a client's LocalProxy.
type TransportStatus struct {
	NodeID        string `json:"node_id"`
	HostName      string `json:"host_name"`
	Connected     bool   `json:"connected"`
	Transport     string `json:"transport"` // "lan" | "direct" | "tsnet" | "relay" | "disconnected"
	Target        string `json:"target,omitempty"`
	LocalProxyURL string `json:"local_proxy_url"`
	LatencyMS     int64  `json:"latency_ms,omitempty"`
	Error         string `json:"error,omitempty"`
}

// LocalProxyOptions configures optional Sidecar routing and status callbacks for LocalProxy.
type LocalProxyOptions struct {
	TSNetProxyURL   string
	OnStatusChanged func(TransportStatus)
}

type pairCompleteResponse struct {
	DeviceID    string   `json:"device_id"`
	DeviceToken string   `json:"device_token"`
	NodeID      string   `json:"node_id"`
	Fingerprint string   `json:"fingerprint"`
	HostName    string   `json:"host_name"`
	RelayURL    string   `json:"relay_url"`
	DirectURLs  []string `json:"direct_urls"`
	LANURLs     []string `json:"lan_urls"`
}

// PairWithHost completes pairing against an intranet Nex Server over the fastest available route
// (LAN -> Direct -> Relay mTLS+yamux) and returns the saved profile + device bearer token.
func PairWithHost(
	ctx context.Context,
	clientID *Identity,
	invite PairingInvite,
	deviceName string,
	platform string,
) (PairedHostProfile, string, string, error) {
	if clientID == nil {
		return PairedHostProfile{}, "", "", errors.New("remote: client identity is required")
	}
	if strings.TrimSpace(deviceName) == "" {
		deviceName = "Nex Client"
	}
	reqBody, _ := json.Marshal(map[string]string{
		"pair_token":        invite.Token,
		"device_name":       deviceName,
		"platform":          platform,
		"client_pubkey_pem": clientID.PubKeyPEM,
	})

	tryHTTPPair := func(baseURL, transport string, timeout time.Duration) (*pairCompleteResponse, bool, error) {
		pCtx, cancel := context.WithTimeout(ctx, timeout)
		defer cancel()
		target := strings.TrimRight(baseURL, "/") + "/api/v1/remote/pair/complete"
		req, err := http.NewRequestWithContext(pCtx, http.MethodPost, target, bytes.NewReader(reqBody))
		if err != nil {
			return nil, false, err
		}
		req.Header.Set("Content-Type", "application/json")
		hc := &http.Client{Transport: &http.Transport{ForceAttemptHTTP2: false}}
		resp, err := hc.Do(req)
		if err != nil {
			return nil, false, err
		}
		defer resp.Body.Close()
		if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
			b, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
			return nil, true, fmt.Errorf("pairing rejected (%d): %s", resp.StatusCode, strings.TrimSpace(string(b)))
		}
		if resp.StatusCode != http.StatusOK {
			b, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
			return nil, false, fmt.Errorf("pairing HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(b)))
		}
		var out pairCompleteResponse
		if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
			return nil, false, err
		}
		return &out, false, nil
	}

	var errs []string
	var completed *pairCompleteResponse
	winningTransport := ""

	// 1. LAN URLs (fast local probe)
	for _, u := range invite.LANURLs {
		out, fatal, err := tryHTTPPair(u, "lan", 450*time.Millisecond)
		if err == nil && out != nil {
			completed = out
			winningTransport = "lan"
			break
		}
		if fatal {
			return PairedHostProfile{}, "", "", err
		}
		if err != nil {
			errs = append(errs, fmt.Sprintf("lan(%s): %v", u, err))
		}
	}

	// 2. Direct URLs (Tailscale / Cloudflare / public URL)
	if completed == nil {
		for _, u := range invite.DirectURLs {
			out, fatal, err := tryHTTPPair(u, "direct", 1800*time.Millisecond)
			if err == nil && out != nil {
				completed = out
				winningTransport = "direct"
				break
			}
			if fatal {
				return PairedHostProfile{}, "", "", err
			}
			if err != nil {
				errs = append(errs, fmt.Sprintf("direct(%s): %v", u, err))
			}
		}
	}

	// 3. Zero-trust Relay mTLS + yamux tunnel
	if completed == nil && strings.TrimSpace(invite.RelayURL) != "" {
		rCtx, cancel := context.WithTimeout(ctx, 8*time.Second)
		rawConn, err := relay.DialClientTunnel(rCtx, invite.RelayURL, invite.NodeID, invite.RelayToken)
		if err != nil {
			cancel()
			errs = append(errs, fmt.Sprintf("relay(%s): %v", invite.RelayURL, err))
		} else {
			sess, tErr := DialTunnelSession(rCtx, rawConn, clientID, invite.Fingerprint)
			cancel()
			if tErr != nil {
				_ = rawConn.Close()
				errs = append(errs, fmt.Sprintf("relay tls(%s): %v", invite.RelayURL, tErr))
			} else {
				hc := NewTunnelHTTPClient(sess)
				pCtx, pCancel := context.WithTimeout(ctx, 5*time.Second)
				req, _ := http.NewRequestWithContext(pCtx, http.MethodPost, "http://nex.tunnel/api/v1/remote/pair/complete", bytes.NewReader(reqBody))
				req.Header.Set("Content-Type", "application/json")
				resp, rErr := hc.Do(req)
				pCancel()
				if rErr != nil {
					errs = append(errs, fmt.Sprintf("relay pair request: %v", rErr))
				} else {
					b, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
					_ = resp.Body.Close()
					if resp.StatusCode == http.StatusOK {
						var out pairCompleteResponse
						if dErr := json.Unmarshal(b, &out); dErr == nil {
							completed = &out
							winningTransport = "relay"
						}
					} else {
						errs = append(errs, fmt.Sprintf("relay pair HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(b))))
					}
				}
				_ = sess.Close()
			}
		}
	}

	if completed == nil {
		if len(errs) == 0 {
			return PairedHostProfile{}, "", "", errors.New("pairing failed: no reachable endpoints in invite")
		}
		return PairedHostProfile{}, "", "", fmt.Errorf("pairing failed: %s", strings.Join(errs, "; "))
	}

	hostName := strings.TrimSpace(completed.HostName)
	if hostName == "" {
		hostName = invite.HostName
	}
	relayURL := strings.TrimRight(strings.TrimSpace(completed.RelayURL), "/")
	if relayURL == "" {
		relayURL = strings.TrimRight(strings.TrimSpace(invite.RelayURL), "/")
	}
	directURLs := completed.DirectURLs
	if len(directURLs) == 0 {
		directURLs = invite.DirectURLs
	}
	lanURLs := completed.LANURLs
	if len(lanURLs) == 0 {
		lanURLs = invite.LANURLs
	}
	fp := strings.ToLower(strings.TrimSpace(completed.Fingerprint))
	if fp == "" {
		fp = invite.Fingerprint
	}

	profile := PairedHostProfile{
		NodeID:        invite.NodeID,
		HostName:      hostName,
		Fingerprint:   fp,
		DeviceID:      completed.DeviceID,
		RelayURL:      relayURL,
		RelayToken:    invite.RelayToken,
		DirectURLs:    cleanURLList(directURLs),
		LANURLs:       cleanURLList(lanURLs),
		LastTransport: winningTransport,
		PairedAt:      time.Now().UnixMilli(),
	}
	return profile, completed.DeviceToken, winningTransport, nil
}

// LocalProxy runs a loopback 127.0.0.1 HTTP/WebSocket proxy that routes WebView requests
// cleanly to a paired intranet server via LAN -> Direct -> TSNet -> Relay mTLS+yamux.
type LocalProxy struct {
	clientID    *Identity
	profile     PairedHostProfile
	deviceToken string
	opts        LocalProxyOptions

	ln       net.Listener
	httpSrv  *http.Server
	localURL string

	httpTransport *http.Transport

	connectMu     sync.Mutex
	mu            sync.RWMutex
	mode          string // "http" | "yamux"
	targetURL     *url.URL
	yamuxSess     *yamux.Session
	yamuxRound    *http.Transport
	status        TransportStatus
	closed        bool
}

// NewLocalProxy binds 127.0.0.1:0 and starts serving local proxy traffic for profile.
func NewLocalProxy(clientID *Identity, profile PairedHostProfile, deviceToken string, opts LocalProxyOptions) (*LocalProxy, error) {
	if clientID == nil {
		return nil, errors.New("remote: client identity required")
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, err
	}
	localURL := "http://" + ln.Addr().String()
	lp := &LocalProxy{
		clientID:    clientID,
		profile:     profile,
		deviceToken: deviceToken,
		opts:        opts,
		ln:          ln,
		localURL:    localURL,
		httpTransport: &http.Transport{
			ForceAttemptHTTP2:   false,
			MaxIdleConns:        32,
			IdleConnTimeout:     90 * time.Second,
			TLSHandshakeTimeout: 8 * time.Second,
		},
		status: TransportStatus{
			NodeID:        profile.NodeID,
			HostName:      profile.HostName,
			Connected:     false,
			Transport:     "disconnected",
			LocalProxyURL: localURL,
		},
	}

	lp.yamuxRound = &http.Transport{
		ForceAttemptHTTP2: false,
		MaxIdleConns:      32,
		IdleConnTimeout:   90 * time.Second,
		DialContext:       lp.dialYamuxStream,
	}

	rev := &httputil.ReverseProxy{
		Rewrite: func(pr *httputil.ProxyRequest) {
			lp.mu.RLock()
			mode := lp.mode
			tURL := lp.targetURL
			lp.mu.RUnlock()

			if mode == "http" && tURL != nil {
				pr.SetURL(tURL)
				pr.Out.Host = tURL.Host
				if lp.deviceToken != "" {
					pr.Out.Header.Set("X-Nex-Device-Token", lp.deviceToken)
					if strings.EqualFold(pr.In.Header.Get("Upgrade"), "websocket") {
						q := pr.Out.URL.Query()
						if q.Get("device_token") == "" {
							q.Set("device_token", lp.deviceToken)
							pr.Out.URL.RawQuery = q.Encode()
						}
					}
				}
				return
			}

			// Relay yamux path (authentication relies on pinned mTLS client certificate)
			pr.Out.URL.Scheme = "http"
			pr.Out.URL.Host = "nex.tunnel"
			pr.Out.Host = "nex.tunnel"
			if lp.deviceToken != "" {
				pr.Out.Header.Set("X-Nex-Device-Token", lp.deviceToken)
			}
		},
		Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
			lp.mu.RLock()
			mode := lp.mode
			lp.mu.RUnlock()
			if mode == "http" {
				return lp.httpTransport.RoundTrip(req)
			}
			return lp.yamuxRound.RoundTrip(req)
		}),
		ErrorHandler: func(w http.ResponseWriter, _ *http.Request, err error) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusBadGateway)
			_ = json.NewEncoder(w).Encode(map[string]string{
				"error": err.Error(),
			})
		},
	}

	rootHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PUT, DELETE, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization, X-Nex-Device-Token")
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		rev.ServeHTTP(w, r)
	})

	lp.httpSrv = &http.Server{
		Handler:           rootHandler,
		ReadHeaderTimeout: 30 * time.Second,
	}
	go func() {
		_ = lp.httpSrv.Serve(ln)
	}()
	return lp, nil
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

// LocalURL returns the loopback base URL (http://127.0.0.1:<port>) for WebView consumption.
func (lp *LocalProxy) LocalURL() string { return lp.localURL }

// Status returns the current active transport status snapshot.
func (lp *LocalProxy) Status() TransportStatus {
	lp.mu.RLock()
	defer lp.mu.RUnlock()
	return lp.status
}

func (lp *LocalProxy) dialYamuxStream(ctx context.Context, _, _ string) (net.Conn, error) {
	lp.mu.RLock()
	sess := lp.yamuxSess
	lp.mu.RUnlock()
	if sess == nil || sess.IsClosed() {
		if _, err := lp.Reconnect(ctx); err != nil {
			return nil, err
		}
		lp.mu.RLock()
		sess = lp.yamuxSess
		lp.mu.RUnlock()
	}
	if sess == nil {
		return nil, errors.New("remote: relay tunnel disconnected")
	}
	return sess.Open()
}

// Connect probes paths in priority order (LAN -> Direct -> TSNet -> Relay) and activates the best route.
func (lp *LocalProxy) Connect(ctx context.Context) (TransportStatus, error) {
	return lp.Reconnect(ctx)
}

// Reconnect re-evaluates path priority and establishes a fresh active transport.
func (lp *LocalProxy) Reconnect(ctx context.Context) (TransportStatus, error) {
	lp.connectMu.Lock()
	defer lp.connectMu.Unlock()

	lp.mu.RLock()
	if lp.closed {
		st := lp.status
		lp.mu.RUnlock()
		return st, errors.New("remote: local proxy closed")
	}
	tsnetURL := lp.opts.TSNetProxyURL
	lp.mu.RUnlock()

	probeHTTP := func(rawURL, transport string, timeout time.Duration) (TransportStatus, bool) {
		parsed, err := url.Parse(strings.TrimRight(rawURL, "/"))
		if err != nil {
			return TransportStatus{}, false
		}
		pCtx, cancel := context.WithTimeout(ctx, timeout)
		defer cancel()
		start := time.Now()
		req, _ := http.NewRequestWithContext(pCtx, http.MethodGet, strings.TrimRight(rawURL, "/")+"/api/v1/auth/status", nil)
		if lp.deviceToken != "" {
			req.Header.Set("X-Nex-Device-Token", lp.deviceToken)
		}
		hc := &http.Client{Transport: &http.Transport{ForceAttemptHTTP2: false}}
		resp, err := hc.Do(req)
		if err != nil {
			return TransportStatus{}, false
		}
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			return TransportStatus{}, false
		}
		latency := time.Since(start).Milliseconds()

		lp.mu.Lock()
		oldSess := lp.yamuxSess
		lp.yamuxSess = nil
		lp.mode = "http"
		lp.targetURL = parsed
		st := TransportStatus{
			NodeID:        lp.profile.NodeID,
			HostName:      lp.profile.HostName,
			Connected:     true,
			Transport:     transport,
			Target:        rawURL,
			LocalProxyURL: lp.localURL,
			LatencyMS:     latency,
		}
		lp.status = st
		lp.mu.Unlock()

		if oldSess != nil {
			_ = oldSess.Close()
		}
		if lp.opts.OnStatusChanged != nil {
			lp.opts.OnStatusChanged(st)
		}
		return st, true
	}

	// 1. LAN direct
	for _, u := range lp.profile.LANURLs {
		if st, ok := probeHTTP(u, "lan", 350*time.Millisecond); ok {
			return st, nil
		}
	}
	// 2. Direct network (Tailscale / Cloudflare / Public URL)
	for _, u := range lp.profile.DirectURLs {
		if st, ok := probeHTTP(u, "direct", 1200*time.Millisecond); ok {
			return st, nil
		}
	}
	// 3. Desktop nex-tsnet sidecar proxy (when installed)
	if strings.TrimSpace(tsnetURL) != "" {
		if st, ok := probeHTTP(tsnetURL, "tsnet", 2500*time.Millisecond); ok {
			return st, nil
		}
	}

	// 4. Zero-trust Relay mTLS + yamux fallback
	if strings.TrimSpace(lp.profile.RelayURL) != "" {
		start := time.Now()
		dCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
		rawConn, err := relay.DialClientTunnel(dCtx, lp.profile.RelayURL, lp.profile.NodeID, lp.profile.RelayToken)
		if err == nil {
			sess, tErr := DialTunnelSession(dCtx, rawConn, lp.clientID, lp.profile.Fingerprint)
			cancel()
			if tErr != nil {
				_ = rawConn.Close()
				err = tErr
			} else {
				hc := NewTunnelHTTPClient(sess)
				vCtx, vCancel := context.WithTimeout(ctx, 4*time.Second)
				req, _ := http.NewRequestWithContext(vCtx, http.MethodGet, "http://nex.tunnel/api/v1/health", nil)
				resp, vErr := hc.Do(req)
				vCancel()
				if vErr == nil && resp.StatusCode == http.StatusOK {
					_ = resp.Body.Close()
					latency := time.Since(start).Milliseconds()

					lp.mu.Lock()
					oldSess := lp.yamuxSess
					lp.yamuxSess = sess
					lp.mode = "yamux"
					lp.targetURL = nil
					st := TransportStatus{
						NodeID:        lp.profile.NodeID,
						HostName:      lp.profile.HostName,
						Connected:     true,
						Transport:     "relay",
						Target:        lp.profile.RelayURL,
						LocalProxyURL: lp.localURL,
						LatencyMS:     latency,
					}
					lp.status = st
					lp.mu.Unlock()

					if oldSess != nil && oldSess != sess {
						_ = oldSess.Close()
					}
					if lp.opts.OnStatusChanged != nil {
						lp.opts.OnStatusChanged(st)
					}
					return st, nil
				}
				if resp != nil {
					_ = resp.Body.Close()
				}
				_ = sess.Close()
				err = vErr
			}
		} else {
			cancel()
		}

		st := TransportStatus{
			NodeID:        lp.profile.NodeID,
			HostName:      lp.profile.HostName,
			Connected:     false,
			Transport:     "disconnected",
			LocalProxyURL: lp.localURL,
			Error:         errString(err),
		}
		lp.mu.Lock()
		lp.status = st
		lp.mu.Unlock()
		if lp.opts.OnStatusChanged != nil {
			lp.opts.OnStatusChanged(st)
		}
		if err != nil {
			return st, err
		}
	}

	st := TransportStatus{
		NodeID:        lp.profile.NodeID,
		HostName:      lp.profile.HostName,
		Connected:     false,
		Transport:     "disconnected",
		LocalProxyURL: lp.localURL,
		Error:         "no reachable host route",
	}
	lp.mu.Lock()
	lp.status = st
	lp.mu.Unlock()
	if lp.opts.OnStatusChanged != nil {
		lp.opts.OnStatusChanged(st)
	}
	return st, errors.New(st.Error)
}

// Close shuts down the local HTTP server, underlying yamux session, and transports.
func (lp *LocalProxy) Close() error {
	lp.mu.Lock()
	if lp.closed {
		lp.mu.Unlock()
		return nil
	}
	lp.closed = true
	sess := lp.yamuxSess
	lp.yamuxSess = nil
	lp.mu.Unlock()

	if sess != nil {
		_ = sess.Close()
	}
	lp.httpTransport.CloseIdleConnections()
	lp.yamuxRound.CloseIdleConnections()
	return lp.httpSrv.Close()
}

func errString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}
