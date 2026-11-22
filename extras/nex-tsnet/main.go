package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"os/signal"
	"strings"
	"sync"
	"syscall"
	"time"

	"tailscale.com/tsnet"
)

type statusFrame struct {
	State     string   `json:"state"`
	AuthURL   string   `json:"auth_url,omitempty"`
	DNSName   string   `json:"dns_name,omitempty"`
	IPs       []string `json:"ips,omitempty"`
	ListenURL string   `json:"listen_url,omitempty"`
	Error     string   `json:"error,omitempty"`
}

var outMu sync.Mutex

func emitStatus(st statusFrame) {
	outMu.Lock()
	defer outMu.Unlock()
	b, _ := json.Marshal(st)
	_, _ = fmt.Fprintln(os.Stdout, string(b))
}

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, "usage: nex-tsnet <host|dial> [flags]")
		os.Exit(2)
	}
	sub := os.Args[1]
	fs := flag.NewFlagSet(sub, flag.ExitOnError)
	stateDir := fs.String("state-dir", "", "Directory for persistent tsnet state")
	hostname := fs.String("hostname", "nex-host", "Tailnet node hostname")
	controlURL := fs.String("control-url", "", "Optional Headscale / custom coordination server URL")
	authKey := fs.String("auth-key", os.Getenv("TS_AUTHKEY"), "Optional ephemeral or reusable Tailscale auth key")
	upstream := fs.String("upstream", "http://127.0.0.1:8080", "Local Nex server HTTP URL (host mode)")
	target := fs.String("target", "", "Target Tailnet URL http(s)://... (dial mode)")
	listen := fs.String("listen", "127.0.0.1:0", "Local loopback listen addr (dial mode)")
	_ = fs.Parse(os.Args[2:])

	if strings.TrimSpace(*stateDir) == "" {
		emitStatus(statusFrame{State: "error", Error: "--state-dir is required"})
		os.Exit(1)
	}
	_ = os.MkdirAll(*stateDir, 0o700)

	srv := &tsnet.Server{
		Dir:        *stateDir,
		Hostname:   strings.TrimSpace(*hostname),
		ControlURL: strings.TrimSpace(*controlURL),
		AuthKey:    strings.TrimSpace(*authKey),
		Logf:       func(string, ...any) {},
	}
	defer srv.Close()

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	emitStatus(statusFrame{State: "starting"})

	// Poll LocalClient status in background so login QR / DNS name / IPs reach Nex Server cleanly.
	go monitorTailscaleStatus(ctx, srv)

	switch sub {
	case "host":
		runHostMode(ctx, srv, *upstream)
	case "dial":
		runDialMode(ctx, srv, *target, *listen)
	default:
		emitStatus(statusFrame{State: "error", Error: fmt.Sprintf("unknown subcommand %q", sub)})
		os.Exit(2)
	}
}

func monitorTailscaleStatus(ctx context.Context, srv *tsnet.Server) {
	lc, err := srv.LocalClient()
	if err != nil {
		return
	}
	lastKey := ""
	ticker := time.NewTicker(1 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			sCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
			st, err := lc.Status(sCtx)
			cancel()
			if err != nil || st == nil {
				continue
			}
			var ips []string
			for _, ip := range st.TailscaleIPs {
				ips = append(ips, ip.String())
			}
			dns := ""
			if st.Self != nil {
				dns = strings.TrimRight(st.Self.DNSName, ".")
			}
			frame := statusFrame{
				State:   strings.ToLower(st.BackendState),
				AuthURL: st.AuthURL,
				DNSName: dns,
				IPs:     ips,
			}
			if st.BackendState == "NeedsLogin" {
				frame.State = "needs_login"
			} else if st.BackendState == "Running" {
				frame.State = "running"
			}
			keyBytes, _ := json.Marshal(frame)
			if string(keyBytes) != lastKey {
				lastKey = string(keyBytes)
				emitStatus(frame)
			}
		}
	}
}

func runHostMode(ctx context.Context, srv *tsnet.Server, rawUpstream string) {
	upURL, err := url.Parse(strings.TrimRight(strings.TrimSpace(rawUpstream), "/"))
	if err != nil {
		emitStatus(statusFrame{State: "error", Error: err.Error()})
		os.Exit(1)
	}
	ln, err := srv.Listen("tcp", ":80")
	if err != nil {
		emitStatus(statusFrame{State: "error", Error: err.Error()})
		os.Exit(1)
	}
	defer ln.Close()

	rev := httputil.NewSingleHostReverseProxy(upURL)
	rev.Transport = &http.Transport{ForceAttemptHTTP2: false}
	httpSrv := &http.Server{Handler: rev, ReadHeaderTimeout: 30 * time.Second}
	go func() {
		<-ctx.Done()
		_ = httpSrv.Close()
	}()
	if err := httpSrv.Serve(ln); err != nil && ctx.Err() == nil {
		emitStatus(statusFrame{State: "error", Error: err.Error()})
	}
}

func runDialMode(ctx context.Context, srv *tsnet.Server, rawTarget, listenAddr string) {
	tgtURL, err := url.Parse(strings.TrimRight(strings.TrimSpace(rawTarget), "/"))
	if err != nil || tgtURL.Host == "" {
		emitStatus(statusFrame{State: "error", Error: "valid --target URL is required in dial mode"})
		os.Exit(1)
	}
	if _, err := srv.Up(ctx); err != nil {
		emitStatus(statusFrame{State: "error", Error: err.Error()})
		os.Exit(1)
	}
	ln, err := net.Listen("tcp", listenAddr)
	if err != nil {
		emitStatus(statusFrame{State: "error", Error: err.Error()})
		os.Exit(1)
	}
	defer ln.Close()

	listenURL := "http://" + ln.Addr().String()
	emitStatus(statusFrame{State: "running", ListenURL: listenURL})

	rev := httputil.NewSingleHostReverseProxy(tgtURL)
	rev.Transport = &http.Transport{
		ForceAttemptHTTP2: false,
		DialContext:       srv.Dial,
	}
	httpSrv := &http.Server{Handler: rev, ReadHeaderTimeout: 30 * time.Second}
	go func() {
		<-ctx.Done()
		_ = httpSrv.Close()
	}()
	if err := httpSrv.Serve(ln); err != nil && ctx.Err() == nil {
		emitStatus(statusFrame{State: "error", Error: err.Error()})
	}
}
