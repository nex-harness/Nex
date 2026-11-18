package app

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"sync"

	"github.com/nex-harness/nex/pkg/runner"
	"github.com/nex-harness/nex/pkg/server"
	"github.com/nex-harness/nex/pkg/store"
	"github.com/nex-harness/nex/pkg/webui"
)

// NativeBridge exposes typed IPC methods bindable via egoist/mygo (`mygo.Bind(&NativeBridge{...})`)
// for Desktop (macOS/Windows/Linux) and Mobile (iOS/Android) Web Mode containers.
type NativeBridge struct {
	mu          sync.Mutex
	configDir   string
	pushToken   string
	onNotify    func(title, body string)
	onHaptic    func(style string)
}

type storedCredentials struct {
	AuthToken string `json:"auth_token"`
}

// NewNativeBridge initializes the Go-side HostBridge implementation.
func NewNativeBridge(configDir string, onNotify func(title, body string), onHaptic func(style string)) *NativeBridge {
	_ = os.MkdirAll(configDir, 0o700)
	return &NativeBridge{
		configDir: configDir,
		onNotify:  onNotify,
		onHaptic:  onHaptic,
	}
}

func (b *NativeBridge) credFile() string {
	return filepath.Join(b.configDir, "credentials.json")
}

// GetAuthToken retrieves the stored Server auth token (bound to mygo Typed IPC).
func (b *NativeBridge) GetAuthToken() (string, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	raw, err := os.ReadFile(b.credFile())
	if os.IsNotExist(err) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	var c storedCredentials
	if err := json.Unmarshal(raw, &c); err != nil {
		return "", err
	}
	return c.AuthToken, nil
}

// SetAuthToken persists the Server auth token with 0600 file permissions.
func (b *NativeBridge) SetAuthToken(token string) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	raw, err := json.Marshal(storedCredentials{AuthToken: token})
	if err != nil {
		return err
	}
	return os.WriteFile(b.credFile(), raw, 0o600)
}

// Notify triggers a native desktop/mobile notification when an approval request arrives.
func (b *NativeBridge) Notify(title, body string) {
	if b.onNotify != nil {
		b.onNotify(title, body)
	}
}

// Haptic triggers device tactile feedback on mobile hosts.
func (b *NativeBridge) Haptic(style string) {
	if b.onHaptic != nil {
		b.onHaptic(style)
	}
}

// SetMobilePushToken records the APNs/FCM token from the mygo mobile container.
func (b *NativeBridge) SetMobilePushToken(token string) {
	b.mu.Lock()
	b.pushToken = token
	b.mu.Unlock()
}

// GetMobilePushToken returns the registered APNs/FCM token.
func (b *NativeBridge) GetMobilePushToken() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.pushToken
}

// AllInOneInstance manages an optional embedded Control Plane Server + local embedded Runner
// inside the Desktop host process ("Single-Machine Out-of-the-Box Mode" from Spec 02 Section 5).
type AllInOneInstance struct {
	URL        string
	httpServer *http.Server
	serverDB   *store.Store
	node       *runner.Node
	wiring     *runner.DefaultRuntimeWiring
	cancel     context.CancelFunc
}

// StartAllInOne boots an embedded nex-server (serving embedded Web UI) and auto-connects an in-process local nex-runner.
func StartAllInOne(ctx context.Context, baseDataDir, workspaceDir, listenAddr string) (*AllInOneInstance, error) {
	if listenAddr == "" {
		listenAddr = "127.0.0.1:0"
	}
	ln, err := net.Listen("tcp", listenAddr)
	if err != nil {
		return nil, err
	}
	actualAddr := ln.Addr().String()

	srvStore, err := store.Open(filepath.Join(baseDataDir, "server"))
	if err != nil {
		_ = ln.Close()
		return nil, err
	}

	cpSrv, err := server.New(server.Config{
		Store:    srvStore,
		StaticFS: webui.DistFS(),
	})
	if err != nil {
		_ = srvStore.Close()
		_ = ln.Close()
		return nil, err
	}

	httpSrv := &http.Server{Handler: cpSrv.Handler()}
	go func() {
		_ = httpSrv.Serve(ln)
	}()

	node, err := runner.NewNode(runner.Config{
		RunnerID:         "run_desktop_embedded",
		Name:             "Local Embedded Runner",
		ServerWSURL:      "ws://" + actualAddr + "/ws/runner",
		DataDir:          filepath.Join(baseDataDir, "runner"),
		WorkspaceDefault: workspaceDir,
	})
	if err != nil {
		_ = httpSrv.Close()
		_ = srvStore.Close()
		return nil, err
	}

	wiring := runner.AttachDefaultSubsystems(node)
	runCtx, cancel := context.WithCancel(ctx)
	go node.StartConnectLoop(runCtx)

	return &AllInOneInstance{
		URL:        "http://" + actualAddr,
		httpServer: httpSrv,
		serverDB:   srvStore,
		node:       node,
		wiring:     wiring,
		cancel:     cancel,
	}, nil
}

// Close cleanly shuts down the embedded Runner and embedded Server.
func (a *AllInOneInstance) Close() error {
	a.cancel()
	a.wiring.Close()
	_ = a.node.Close()
	_ = a.httpServer.Close()
	return a.serverDB.Close()
}
