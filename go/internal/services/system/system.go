// Package system ports the Tier-1 ISystemService surface
// (packages/services/src/system/*) of the Go rewrite, step 3.2:
//
//   - systemService.ts (info, listIntegratedTerminalShells, probeIntranet)
//   - integratedTerminalShells.ts (Windows shell discovery)
//   - sshConfigAlias.ts (~/.ssh/config alias parsing, in sshconfig.go)
//
// probeIntranet supports tcp-connect and service-http strategies with the
// TS's normalize/retry/attempt clamps; the probe functions are injectable so
// tests use httptest and loopback ports.
package system

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"runtime"
	"strings"
	"time"

	"github.com/tylinux/nex/go/internal/adapters/probeio"
)

// ChannelName is ServiceChannels.System.
const ChannelName = "system"

const (
	defaultProbeTimeoutMs = 800
	defaultProbeAttempts  = 2
	maxProbeAttempts      = 3
	defaultProbePort      = 22
)

// SystemInfo is SystemInfo from protocol.ts.
type SystemInfo struct {
	Homedir  string `json:"homedir"`
	Platform string `json:"platform"`
}

// TcpProbeParams is the injectable tcp probe's input.
type TcpProbeParams struct {
	Host      string
	Port      int
	TimeoutMs int64
}

// ServiceProbeParams is the injectable service probe's input.
type ServiceProbeParams struct {
	URL       string
	TimeoutMs int64
	Token     string
}

// ServiceProbeResult is the injectable service probe's output.
type ServiceProbeResult struct {
	LatencyMs int64
	Marker    string
	HasMarker bool
}

// Options mirrors CreateSystemServiceOptions.
type Options struct {
	Env          map[string]string
	IsExecutable func(path string) bool
	Platform     string
	TCPProbe     func(ctx context.Context, p TcpProbeParams) (int64, error)
	ServiceProbe func(ctx context.Context, p ServiceProbeParams) (ServiceProbeResult, error)
	Now          func() int64
	HTTPClient   probeio.HTTPDoer
}

// Service is the ISystemService implementation.
type Service struct {
	env          map[string]string
	isExecutable func(string) bool
	platform     string
	tcpProbe     func(context.Context, TcpProbeParams) (int64, error)
	serviceProbe func(context.Context, ServiceProbeParams) (ServiceProbeResult, error)
	now          func() int64
	httpClient   probeio.HTTPDoer
}

// NewService builds the system service with default probes.
func NewService(options Options) *Service {
	s := &Service{
		env:          options.Env,
		isExecutable: options.IsExecutable,
		platform:     options.Platform,
		now:          options.Now,
		httpClient:   options.HTTPClient,
	}
	if s.env == nil {
		s.env = envMap()
	}
	if s.platform == "" {
		s.platform = runtime.GOOS
	}
	if s.isExecutable == nil {
		s.isExecutable = defaultIsExecutable
	}
	if s.now == nil {
		s.now = func() int64 { return time.Now().UnixMilli() }
	}
	if s.httpClient == nil {
		// The TS fetch follows redirects with default TLS settings; the
		// concrete client lives in the adapter (internal/adapters/probeio)
		// so this package stays free of net/http.
		s.httpClient = probeio.DefaultDoer()
	}
	s.tcpProbe = options.TCPProbe
	if s.tcpProbe == nil {
		s.tcpProbe = probeTCPPort
	}
	s.serviceProbe = options.ServiceProbe
	if s.serviceProbe == nil {
		s.serviceProbe = func(ctx context.Context, p ServiceProbeParams) (ServiceProbeResult, error) {
			return probeServiceEndpoint(ctx, s.httpClient, p)
		}
	}
	return s
}

// parsedURL is the tiny URL normalization normalizeServiceTarget needs: the
// scheme plus URL.toString()'s trailing-slash canonicalization (what
// `new URL("http://x").toString()` gives: scheme://host/).
type parsedURL struct {
	scheme     string
	normalized string
}

func parseURL(text string) (parsedURL, error) {
	u, err := url.Parse(text)
	if err != nil {
		return parsedURL{}, err
	}
	if u.Scheme == "" || u.Host == "" {
		return parsedURL{}, fmt.Errorf("invalid URL: %s", text)
	}
	return parsedURL{scheme: strings.ToLower(u.Scheme), normalized: u.String()}, nil
}

func envMap() map[string]string {
	out := map[string]string{}
	for _, kv := range os.Environ() {
		if i := indexByte(kv, '='); i >= 0 {
			out[kv[:i]] = kv[i+1:]
		}
	}
	return out
}

func indexByte(s string, b byte) int {
	for i := 0; i < len(s); i++ {
		if s[i] == b {
			return i
		}
	}
	return -1
}

func defaultIsExecutable(path string) bool {
	info, err := os.Stat(path)
	return err == nil && !info.IsDir() && info.Mode()&0o111 != 0
}

// Info ports info(): { homedir, platform }.
func (s *Service) Info(ctx context.Context) (SystemInfo, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return SystemInfo{}, err
	}
	return SystemInfo{Homedir: home, Platform: runtime.GOOS}, nil
}

// ListIntegratedTerminalShells ports listIntegratedTerminalShells: Windows
// only (CMD + optional Git Bash); other platforms return [].
func (s *Service) ListIntegratedTerminalShells(ctx context.Context) ([]ShellOption, error) {
	return listIntegratedTerminalShellOptions(s.env, s.isExecutable, s.platform), nil
}
