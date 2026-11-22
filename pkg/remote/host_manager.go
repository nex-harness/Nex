package remote

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/hashicorp/yamux"
	"github.com/nex-harness/nex/pkg/relay"
)

// HostConfig stores persistent Remote Access settings for an intranet Nex Server.
type HostConfig struct {
	Enabled             bool     `json:"enabled"`
	HostName            string   `json:"host_name"`
	RelayURL            string   `json:"relay_url"`
	RelayToken          string   `json:"relay_token,omitempty"`
	DirectURLs          []string `json:"direct_urls,omitempty"`
	PreferLAN           bool     `json:"prefer_lan"`
	TSNetEnabled        bool     `json:"tsnet_enabled"`
	TSNetControlURL     string   `json:"tsnet_control_url,omitempty"`
	TSNetHostname       string   `json:"tsnet_hostname,omitempty"`
	TSNetDownloadMirror string   `json:"tsnet_download_mirror,omitempty"`
}

// DefaultHostConfig returns sensible defaults for Remote Access.
func DefaultHostConfig() HostConfig {
	hostName, _ := os.Hostname()
	if strings.TrimSpace(hostName) == "" {
		hostName = "Nex Host"
	}
	return HostConfig{
		Enabled:   false,
		HostName:  hostName,
		PreferLAN: true,
	}
}

// HostStatus summarizes live remote-access identity, connectivity, and reachability.
type HostStatus struct {
	NodeID           string   `json:"node_id"`
	Fingerprint      string   `json:"fingerprint"`
	ShortFingerprint string   `json:"short_fingerprint"`
	Enabled          bool     `json:"enabled"`
	HostName         string   `json:"host_name"`
	RelayURL         string   `json:"relay_url"`
	RelayConnected   bool     `json:"relay_connected"`
	LastRelayError   string   `json:"last_relay_error,omitempty"`
	DirectURLs       []string `json:"direct_urls"`
	LANURLs          []string `json:"lan_urls"`
	ActiveTunnels    int      `json:"active_tunnels"`
	Config           HostConfig `json:"config"`
}

// PairingSession is returned by POST /api/v1/remote/pair/start to display QR codes / copyable URIs.
type PairingSession struct {
	PairURI          string   `json:"pair_uri"`
	Token            string   `json:"token"`
	ExpiresAt        int64    `json:"expires_at"`
	NodeID           string   `json:"node_id"`
	Fingerprint      string   `json:"fingerprint"`
	ShortFingerprint string   `json:"short_fingerprint"`
	HostName         string   `json:"host_name"`
	RelayURL         string   `json:"relay_url,omitempty"`
	DirectURLs       []string `json:"direct_urls,omitempty"`
	LANURLs          []string `json:"lan_urls,omitempty"`
}

// HostManagerOptions wires HostManager to Nex Server's handler and device authorization callback.
type HostManagerOptions struct {
	DataDir          string
	ListenPort       int
	Handler          http.Handler
	DeviceAuthorizer func(pubkeyFP string) (deviceID string, ok bool)
}

// HostManager owns the host's Ed25519 identity, pairing tokens, outbound relay link, and active tunnels.
type HostManager struct {
	identity   *Identity
	listenPort int
	handler    http.Handler
	authorizer func(pubkeyFP string) (deviceID string, ok bool)

	mu             sync.Mutex
	cfg            HostConfig
	relayCancel    context.CancelFunc
	relayConnected bool
	lastRelayErr   string
	pairingTokens  map[string]time.Time
	activeSessions map[string]map[*yamux.Session]struct{}
	closed         bool
}

// NewHostManager initializes HostManager and loads or creates the persistent identity keypair.
func NewHostManager(opts HostManagerOptions) (*HostManager, error) {
	if strings.TrimSpace(opts.DataDir) == "" {
		return nil, errors.New("remote: DataDir is required")
	}
	idPath := filepath.Join(opts.DataDir, "remote_identity.json")
	id, err := LoadOrCreateIdentity(idPath, "")
	if err != nil {
		return nil, err
	}
	return &HostManager{
		identity:       id,
		listenPort:     opts.ListenPort,
		handler:        opts.Handler,
		authorizer:     opts.DeviceAuthorizer,
		cfg:            DefaultHostConfig(),
		pairingTokens:  make(map[string]time.Time),
		activeSessions: make(map[string]map[*yamux.Session]struct{}),
	}, nil
}

// Identity returns the host's cryptographic identity.
func (m *HostManager) Identity() *Identity { return m.identity }

// ApplyConfig updates Remote Access settings and restarts or stops the outbound relay connection accordingly.
func (m *HostManager) ApplyConfig(cfg HostConfig) {
	if strings.TrimSpace(cfg.HostName) == "" {
		if hn, err := os.Hostname(); err == nil && strings.TrimSpace(hn) != "" {
			cfg.HostName = hn
		} else {
			cfg.HostName = "Nex Host"
		}
	}
	cfg.RelayURL = strings.TrimRight(strings.TrimSpace(cfg.RelayURL), "/")
	cfg.DirectURLs = cleanURLList(cfg.DirectURLs)

	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return
	}
	if m.relayCancel != nil {
		m.relayCancel()
		m.relayCancel = nil
	}
	m.relayConnected = false
	m.lastRelayErr = ""
	m.cfg = cfg

	shouldRunRelay := cfg.Enabled && cfg.RelayURL != ""
	var relayCtx context.Context
	if shouldRunRelay {
		relayCtx, m.relayCancel = context.WithCancel(context.Background())
	}
	m.mu.Unlock()

	if shouldRunRelay {
		go m.runRelayLoop(relayCtx, cfg.RelayURL, cfg.RelayToken)
	}
}

func (m *HostManager) runRelayLoop(ctx context.Context, relayURL, relayToken string) {
	backoff := 500 * time.Millisecond
	for {
		if ctx.Err() != nil {
			return
		}
		m.mu.Lock()
		m.relayConnected = true
		m.lastRelayErr = ""
		m.mu.Unlock()

		err := relay.DialHostControl(ctx, relayURL, m.identity.NodeID, relayToken, func(connCtx context.Context, _ string, conn net.Conn) {
			_ = ServeTunnelConn(connCtx, conn, m.identity, m.handler, "relay", m.authorizePeer, m.trackSession)
		})

		if ctx.Err() != nil {
			m.mu.Lock()
			m.relayConnected = false
			m.mu.Unlock()
			return
		}

		m.mu.Lock()
		m.relayConnected = false
		if err != nil {
			m.lastRelayErr = err.Error()
		}
		m.mu.Unlock()

		timer := time.NewTimer(backoff)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
		if backoff < 10*time.Second {
			backoff *= 2
			if backoff > 10*time.Second {
				backoff = 10 * time.Second
			}
		}
	}
}

func (m *HostManager) authorizePeer(pubkeyFP string) (deviceID string, pairingOnly bool, allowed bool) {
	if m.authorizer != nil {
		if devID, ok := m.authorizer(pubkeyFP); ok {
			return devID, false, true
		}
	}
	if m.HasActivePairingToken() {
		return "", true, true
	}
	return "", false, false
}

func (m *HostManager) trackSession(pubkeyFP string, sess *yamux.Session) func() {
	m.mu.Lock()
	set := m.activeSessions[pubkeyFP]
	if set == nil {
		set = make(map[*yamux.Session]struct{})
		m.activeSessions[pubkeyFP] = set
	}
	set[sess] = struct{}{}
	m.mu.Unlock()

	return func() {
		m.mu.Lock()
		if cur := m.activeSessions[pubkeyFP]; cur != nil {
			delete(cur, sess)
			if len(cur) == 0 {
				delete(m.activeSessions, pubkeyFP)
			}
		}
		m.mu.Unlock()
	}
}

// StartPairing mints a single-use pairing token (default 5 min TTL) and returns a QR-ready PairingSession.
func (m *HostManager) StartPairing(ttl time.Duration) (PairingSession, error) {
	if ttl <= 0 {
		ttl = 5 * time.Minute
	}
	rawTok := make([]byte, 24)
	if _, err := rand.Read(rawTok); err != nil {
		return PairingSession{}, err
	}
	token := base64.RawURLEncoding.EncodeToString(rawTok)
	exp := time.Now().Add(ttl)

	m.mu.Lock()
	m.pruneExpiredTokensLocked(time.Now())
	m.pairingTokens[token] = exp
	cfg := m.cfg
	listenPort := m.listenPort
	m.mu.Unlock()

	var lanURLs []string
	if cfg.PreferLAN && listenPort > 0 {
		lanURLs = DiscoverLANURLs(listenPort)
	}
	inv := PairingInvite{
		Version:     1,
		NodeID:      m.identity.NodeID,
		Fingerprint: m.identity.Fingerprint,
		HostName:    cfg.HostName,
		Token:       token,
		RelayURL:    cfg.RelayURL,
		RelayToken:  cfg.RelayToken,
		DirectURLs:  cfg.DirectURLs,
		LANURLs:     lanURLs,
	}
	return PairingSession{
		PairURI:          EncodePairingURI(inv),
		Token:            token,
		ExpiresAt:        exp.UnixMilli(),
		NodeID:           m.identity.NodeID,
		Fingerprint:      m.identity.Fingerprint,
		ShortFingerprint: ShortFingerprint(m.identity.Fingerprint),
		HostName:         cfg.HostName,
		RelayURL:         cfg.RelayURL,
		DirectURLs:       cfg.DirectURLs,
		LANURLs:          lanURLs,
	}, nil
}

// HasActivePairingToken reports whether an unexpired pairing window is currently open.
func (m *HostManager) HasActivePairingToken() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.pruneExpiredTokensLocked(time.Now())
	return len(m.pairingTokens) > 0
}

// ConsumePairingToken validates and atomically consumes a single-use pairing token.
func (m *HostManager) ConsumePairingToken(token string) bool {
	trimmed := strings.TrimSpace(token)
	if trimmed == "" {
		return false
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.pruneExpiredTokensLocked(time.Now())
	for k := range m.pairingTokens {
		if len(k) == len(trimmed) && subtle.ConstantTimeCompare([]byte(k), []byte(trimmed)) == 1 {
			delete(m.pairingTokens, k)
			return true
		}
	}
	return false
}

func (m *HostManager) pruneExpiredTokensLocked(now time.Time) {
	for k, exp := range m.pairingTokens {
		if now.After(exp) {
			delete(m.pairingTokens, k)
		}
	}
}

// DisconnectDevice immediately tears down all active yamux tunnels belonging to pubkeyFP.
func (m *HostManager) DisconnectDevice(pubkeyFP string) {
	if strings.TrimSpace(pubkeyFP) == "" {
		return
	}
	m.mu.Lock()
	var toClose []*yamux.Session
	if set := m.activeSessions[pubkeyFP]; set != nil {
		for s := range set {
			toClose = append(toClose, s)
		}
	}
	m.mu.Unlock()

	for _, s := range toClose {
		_ = s.Close()
	}
}

// Status returns a snapshot of Remote Access configuration, identity, and connectivity.
func (m *HostManager) Status() HostStatus {
	m.mu.Lock()
	cfg := m.cfg
	connected := m.relayConnected
	lastErr := m.lastRelayErr
	listenPort := m.listenPort
	active := 0
	for _, set := range m.activeSessions {
		active += len(set)
	}
	m.mu.Unlock()

	var lanURLs []string
	if cfg.PreferLAN && listenPort > 0 {
		lanURLs = DiscoverLANURLs(listenPort)
	}
	direct := append([]string(nil), cfg.DirectURLs...)
	if direct == nil {
		direct = []string{}
	}
	if lanURLs == nil {
		lanURLs = []string{}
	}
	return HostStatus{
		NodeID:           m.identity.NodeID,
		Fingerprint:      m.identity.Fingerprint,
		ShortFingerprint: ShortFingerprint(m.identity.Fingerprint),
		Enabled:          cfg.Enabled,
		HostName:         cfg.HostName,
		RelayURL:         cfg.RelayURL,
		RelayConnected:   connected,
		LastRelayError:   lastErr,
		DirectURLs:       direct,
		LANURLs:          lanURLs,
		ActiveTunnels:    active,
		Config:           cfg,
	}
}

// Close shuts down the outbound relay loop and terminates all active remote tunnels.
func (m *HostManager) Close() error {
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return nil
	}
	m.closed = true
	if m.relayCancel != nil {
		m.relayCancel()
		m.relayCancel = nil
	}
	var toClose []*yamux.Session
	for _, set := range m.activeSessions {
		for s := range set {
			toClose = append(toClose, s)
		}
	}
	m.mu.Unlock()

	for _, s := range toClose {
		_ = s.Close()
	}
	return nil
}
