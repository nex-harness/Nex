package remote

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"sync"
	"time"

	"github.com/hashicorp/yamux"
)

type peerContextKey struct{}

// PeerInfo identifies an authenticated mTLS peer inside HTTP/WebSocket handlers served over a tunnel.
type PeerInfo struct {
	PubKeyFP    string
	DeviceID    string
	PairingOnly bool
	Transport   string
}

// WithPeerInfo attaches PeerInfo to ctx for downstream auth middleware.
func WithPeerInfo(ctx context.Context, info PeerInfo) context.Context {
	return context.WithValue(ctx, peerContextKey{}, info)
}

// PeerFromContext extracts PeerInfo when the request arrived over a pinned mTLS tunnel.
func PeerFromContext(ctx context.Context) (PeerInfo, bool) {
	info, ok := ctx.Value(peerContextKey{}).(PeerInfo)
	return info, ok
}

func yamuxConfig() *yamux.Config {
	cfg := yamux.DefaultConfig()
	cfg.LogOutput = io.Discard
	cfg.EnableKeepAlive = true
	cfg.KeepAliveInterval = 20 * time.Second
	cfg.ConnectionWriteTimeout = 30 * time.Second
	return cfg
}

// ServeTunnelConn upgrades rawConn with pinned mTLS + yamux and serves HTTP/WebSocket traffic through handler.
func ServeTunnelConn(
	ctx context.Context,
	rawConn net.Conn,
	id *Identity,
	handler http.Handler,
	transportName string,
	authorizePeer func(pubkeyFP string) (deviceID string, pairingOnly bool, allowed bool),
	trackSession func(pubkeyFP string, sess *yamux.Session) (cleanup func()),
) error {
	if rawConn == nil || id == nil || handler == nil || authorizePeer == nil {
		if rawConn != nil {
			_ = rawConn.Close()
		}
		return errors.New("remote: invalid ServeTunnelConn arguments")
	}

	tlsCfg := ServerTLSConfig(id, func(fp string) (bool, bool) {
		_, pairingOnly, allowed := authorizePeer(fp)
		return allowed, pairingOnly
	})
	tlsConn := tls.Server(rawConn, tlsCfg)
	hsCtx, hsCancel := context.WithTimeout(ctx, 10*time.Second)
	err := tlsConn.HandshakeContext(hsCtx)
	hsCancel()
	if err != nil {
		_ = tlsConn.Close()
		return err
	}

	state := tlsConn.ConnectionState()
	if len(state.PeerCertificates) == 0 {
		_ = tlsConn.Close()
		return errors.New("remote: missing peer certificate after handshake")
	}
	peerFP, err := FingerprintFromCert(state.PeerCertificates[0].Raw)
	if err != nil {
		_ = tlsConn.Close()
		return err
	}
	deviceID, pairingOnly, allowed := authorizePeer(peerFP)
	if !allowed {
		_ = tlsConn.Close()
		return errors.New("remote: peer unauthorized")
	}

	sess, err := yamux.Server(tlsConn, yamuxConfig())
	if err != nil {
		_ = tlsConn.Close()
		return err
	}

	if trackSession != nil {
		cleanup := trackSession(peerFP, sess)
		defer cleanup()
	}

	servedHandler := handler
	if pairingOnly {
		servedHandler = pairingOnlyMiddleware(handler)
	}

	peerInfo := PeerInfo{
		PubKeyFP:    peerFP,
		DeviceID:    deviceID,
		PairingOnly: pairingOnly,
		Transport:   transportName,
	}

	httpSrv := &http.Server{
		Handler:           servedHandler,
		ReadHeaderTimeout: 30 * time.Second,
		ConnContext: func(baseCtx context.Context, _ net.Conn) context.Context {
			return WithPeerInfo(baseCtx, peerInfo)
		},
	}

	done := make(chan struct{})
	var closeOnce sync.Once
	closeAll := func() {
		closeOnce.Do(func() {
			_ = httpSrv.Close()
			_ = sess.Close()
		})
	}
	go func() {
		select {
		case <-ctx.Done():
			closeAll()
		case <-done:
		}
	}()

	err = httpSrv.Serve(sess)
	close(done)
	closeAll()
	if errors.Is(err, http.ErrServerClosed) || errors.Is(err, yamux.ErrSessionShutdown) || errors.Is(err, io.EOF) {
		return nil
	}
	return err
}

func pairingOnlyMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if (r.Method == http.MethodPost && r.URL.Path == "/api/v1/remote/pair/complete") ||
			(r.Method == http.MethodGet && r.URL.Path == "/api/v1/health") {
			next.ServeHTTP(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusForbidden)
		_ = json.NewEncoder(w).Encode(map[string]string{
			"error": "device not paired; only pairing completion is permitted on this tunnel",
		})
	})
}

// DialTunnelSession wraps rawConn in a pinned TLS 1.3 client handshake and opens a yamux multiplex session.
func DialTunnelSession(ctx context.Context, rawConn net.Conn, clientID *Identity, expectedServerFP string) (*yamux.Session, error) {
	if rawConn == nil || clientID == nil {
		return nil, errors.New("remote: invalid DialTunnelSession arguments")
	}
	tlsConn := tls.Client(rawConn, ClientTLSConfig(clientID, expectedServerFP))
	if err := tlsConn.HandshakeContext(ctx); err != nil {
		_ = tlsConn.Close()
		return nil, err
	}
	sess, err := yamux.Client(tlsConn, yamuxConfig())
	if err != nil {
		_ = tlsConn.Close()
		return nil, err
	}
	return sess, nil
}

// NewTunnelHTTPClient creates an HTTP client that routes requests cleanly through sess streams.
func NewTunnelHTTPClient(sess *yamux.Session) *http.Client {
	return &http.Client{
		Transport: &http.Transport{
			DialContext: func(_ context.Context, _, _ string) (net.Conn, error) {
				return sess.Open()
			},
			DisableKeepAlives: false,
			MaxIdleConns:      16,
			IdleConnTimeout:   90 * time.Second,
		},
		Timeout: 60 * time.Second,
	}
}
