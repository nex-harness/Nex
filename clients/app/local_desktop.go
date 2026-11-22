//go:build !ios && !android

package app

import (
	"context"
	"net"
	"net/http"
	"path/filepath"

	"github.com/nex-harness/nex/pkg/runner"
	"github.com/nex-harness/nex/pkg/server"
	"github.com/nex-harness/nex/pkg/store"
	"github.com/nex-harness/nex/pkg/webui"
)

const supportsLocalMode = true

// AllInOneInstance manages an optional embedded Control Plane Server + local embedded Runner
// inside the Desktop host process ("Single-Machine Out-of-the-Box Mode" from Spec 02 Section 5).
type AllInOneInstance struct {
	URL        string
	httpServer *http.Server
	cpSrv      *server.Server
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
	listenPort := 0
	if tcpAddr, ok := ln.Addr().(*net.TCPAddr); ok {
		listenPort = tcpAddr.Port
	}

	srvStore, err := store.Open(filepath.Join(baseDataDir, "server"))
	if err != nil {
		_ = ln.Close()
		return nil, err
	}

	cpSrv, err := server.New(server.Config{
		Store:      srvStore,
		ListenPort: listenPort,
		StaticFS:   webui.DistFS(),
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
		cpSrv:      cpSrv,
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
	if a.cpSrv != nil {
		_ = a.cpSrv.Close()
	}
	_ = a.httpServer.Close()
	return a.serverDB.Close()
}

func (h *HostService) startLocalBackendLocked(ctx context.Context) (string, error) {
	if cur, ok := h.localInst.(*AllInOneInstance); ok && cur != nil {
		return cur.URL, nil
	}
	inst, err := StartAllInOne(ctx, filepath.Join(h.configDir, "local-runtime"), h.workspaceDir, h.localAddr)
	if err != nil {
		return "", err
	}
	h.localInst = inst
	h.localURL = inst.URL
	return inst.URL, nil
}

func (h *HostService) stopLocalBackendLocked() {
	if h.localInst != nil {
		_ = h.localInst.Close()
		h.localInst = nil
	}
	h.localURL = ""
}
