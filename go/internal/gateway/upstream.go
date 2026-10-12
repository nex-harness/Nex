package gateway

// upstream.go is the Node leg of the gateway: ONE WebSocket per client connection, opened in the
// same client mode, with the same bytes forwarded in both directions.
//
// Why 1:1 and never pooled: Node builds per-connection state in setupChannelServer
// (packages/server/src/http.ts) — the INexAgentService connection scope with its clientMode and
// role, the per-connection IWindowControllerService attachment, the provisioning override. Two
// client connections sharing one upstream would share that state, and the observable symptom is a
// client seeing another client's workspace projection. So the upstream is a socket the client owns,
// not a pool it borrows.
//
// Why bytes are forwarded rather than decoded: a forwarded request must reach Node exactly as the
// client wrote it. Re-encoding it would mean the gateway, not the client's codec, decides what a
// payload looks like — and any divergence (a float that should have taken the JSON tag, an object
// whose key order changed) would stay invisible until something broke in production. So the gateway
// parses only what it must route on — the first values of the request header array — and writes
// the ORIGINAL frame bytes onwards.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/tylinux/nex/go/internal/adapters/ws"
)

// Client modes, verbatim from setupChannelServer's `clientMode` argument. Go mirrors the value
// rather than inventing its own: the Node scope's behaviour branches on exactly these two strings
// (createNexAgentConnectionScope picks terminal-client vs trusted-host-relay, and deliveryProfile
// picks replayable vs continuous), so a renamed constant would silently change the handshake.
const (
	// ModeWebRemoteReplayable is /ws: always terminal-client.
	ModeWebRemoteReplayable = "web-remote-replayable"
	// ModeDesktopContinuous is /ws/host: the trusted host relay.
	ModeDesktopContinuous = "desktop-continuous"
)

// DefaultHostCapabilityHeader is NEX_RPC_HOST_CAPABILITY_HEADER from packages/shared/src/channels.ts.
const DefaultHostCapabilityHeader = "x-nex-rpc-host-capability"

// capabilityPath is Node's own capability route. Go calls it for EVERY /ws/host upstream because
// the ticket is single-use with a 30 s TTL — there is no ticket to reuse across the several client
// connections one desktop window opens.
const capabilityPath = "/api/rpc-host-capability"

// maxCapabilityBody bounds the capability response. A larger one means this is not the server we
// think it is, and reading it into memory would be the wrong way to find out.
const maxCapabilityBody = 1 << 16

// UpstreamConfig addresses the Node process.
//
// Node is reached on loopback only, with a shared secret Go generated at start and passed through
// the EXISTING NEX_SERVER_AUTH_TOKEN mechanism (a child process environment is not a user-facing
// environment variable, so D7 holds). The secret reaches the upstream leg as `?token=`, exactly the
// path webAuth already accepts for a programmatic client. Nothing about it reaches a browser: Go
// terminates the client's own authentication and never forwards the client's credentials.
type UpstreamConfig struct {
	// BaseURL is Node's http base, e.g. "http://127.0.0.1:3030".
	BaseURL string
	// AuthToken is the shared secret Go generated for this Node process. Empty means Node runs
	// without auth, which is only safe because Node is bound to loopback.
	AuthToken string
	// HTTPClient issues the loopback HTTP requests (capability issue). Nil uses http.DefaultClient.
	HTTPClient *http.Client
	// WebSocketOptions configures the upstream socket. Zero uses the adapter's defaults.
	WebSocketOptions ws.Options
}

// validate rejects a config that cannot produce a working leg.
func (c UpstreamConfig) validate() error {
	if strings.TrimSpace(c.BaseURL) == "" {
		return ErrNoUpstream
	}
	parsed, err := url.Parse(c.BaseURL)
	if err != nil {
		return fmt.Errorf("gateway: upstream base URL %q: %w", c.BaseURL, err)
	}
	switch parsed.Scheme {
	case "http", "https", "ws", "wss":
		return nil
	default:
		return fmt.Errorf("gateway: upstream base URL %q must be http, https, ws or wss", c.BaseURL)
	}
}

// dialer opens one upstream leg per client connection.
type dialer struct {
	cfg         UpstreamConfig
	log         Logger
	dialTimeout time.Duration
	header      string
}

// newDialer validates cfg and builds the dialer.
func newDialer(cfg UpstreamConfig, log Logger, dialTimeout time.Duration, header string) (*dialer, error) {
	if err := cfg.validate(); err != nil {
		return nil, err
	}
	if dialTimeout <= 0 {
		dialTimeout = DefaultDialTimeout
	}
	if header == "" {
		header = DefaultHostCapabilityHeader
	}
	if cfg.HTTPClient == nil {
		cfg.HTTPClient = http.DefaultClient
	}
	return &dialer{cfg: cfg, log: log, dialTimeout: dialTimeout, header: header}, nil
}

// Open establishes the upstream leg for one client connection.
//
// mode selects the Node endpoint and, with it, the per-connection scope Node builds: /ws is always
// terminal-client, /ws/host is the trusted host relay. The mode is the CLIENT's mode, passed through
// unchanged — that is the whole point of a 1:1 leg, and it is why both modes are observable on the
// Node side.
func (d *dialer) Open(ctx context.Context, mode string) (*Upstream, error) {
	target, err := d.upstreamURL(mode)
	if err != nil {
		return nil, err
	}

	dialCtx, cancel := context.WithTimeout(ctx, d.dialTimeout)
	defer cancel()

	header := http.Header{}
	if mode == ModeDesktopContinuous {
		// Minted per leg and spent immediately on the upgrade below: Node's store is single-use.
		capability, err := d.issueCapability(dialCtx)
		if err != nil {
			return nil, fmt.Errorf("gateway: mint host capability: %w", err)
		}
		header.Set(d.header, capability)
	}

	socket, err := ws.Dial(dialCtx, target, header, d.cfg.WebSocketOptions)
	if err != nil {
		return nil, fmt.Errorf("gateway: upstream %s: %w", target, err)
	}
	return &Upstream{
		socket: socket,
		log:    d.log,
		done:   make(chan struct{}),
		end:    make(chan struct{}),
	}, nil
}

// upstreamURL builds the Node WebSocket URL for a client mode, carrying the shared secret.
//
// The secret goes in the query string because that is the only credential channel webAuth accepts
// on a WebSocket upgrade — a browser cannot set a header, and Node guards /ws and /ws/* as
// protected paths, so without a credential the upstream upgrade is a 401.
func (d *dialer) upstreamURL(mode string) (string, error) {
	parsed, err := url.Parse(d.cfg.BaseURL)
	if err != nil {
		return "", fmt.Errorf("gateway: upstream base URL %q: %w", d.cfg.BaseURL, err)
	}
	switch parsed.Scheme {
	case "http", "ws":
		parsed.Scheme = "ws"
	case "https", "wss":
		parsed.Scheme = "wss"
	}
	switch mode {
	case ModeDesktopContinuous:
		parsed.Path = strings.TrimSuffix(parsed.Path, "/") + "/ws/host"
	case ModeWebRemoteReplayable:
		parsed.Path = strings.TrimSuffix(parsed.Path, "/") + "/ws"
	default:
		return "", fmt.Errorf("gateway: unknown client mode %q", mode)
	}
	if d.cfg.AuthToken != "" {
		query := parsed.Query()
		query.Set("token", d.cfg.AuthToken)
		parsed.RawQuery = query.Encode()
	}
	return parsed.String(), nil
}

// issuedCapability is the body of POST /api/rpc-host-capability.
type issuedCapability struct {
	Capability string `json:"capability"`
	ExpiresAt  int64  `json:"expiresAt"`
}

// issueCapability calls Node's capability route over loopback.
//
// The request carries the shared secret, because the route is an /api/* path and therefore
// protected — the same guard a browser's fetch hits. Minting it here rather than in Go is
// deliberate: Node's /ws/host consumes from Node's OWN store, so a Go-minted ticket would be
// rejected, and a Go-side store would be a second implementation of a security primitive that
// already exists and is reviewed.
func (d *dialer) issueCapability(ctx context.Context) (string, error) {
	target, err := url.Parse(strings.TrimSuffix(d.cfg.BaseURL, "/") + capabilityPath)
	if err != nil {
		return "", err
	}
	if d.cfg.AuthToken != "" {
		query := target.Query()
		query.Set("token", d.cfg.AuthToken)
		target.RawQuery = query.Encode()
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, target.String(), nil)
	if err != nil {
		return "", err
	}
	response, err := d.cfg.HTTPClient.Do(request)
	if err != nil {
		return "", err
	}
	defer response.Body.Close()
	body, err := io.ReadAll(io.LimitReader(response.Body, maxCapabilityBody))
	if err != nil {
		return "", err
	}
	if response.StatusCode != http.StatusOK {
		return "", fmt.Errorf("gateway: capability route answered %d: %s", response.StatusCode, strings.TrimSpace(string(body)))
	}
	var issued issuedCapability
	if err := json.Unmarshal(body, &issued); err != nil {
		return "", fmt.Errorf("gateway: capability route body: %w", err)
	}
	if issued.Capability == "" {
		return "", errors.New("gateway: capability route returned an empty capability")
	}
	return issued.Capability, nil
}
