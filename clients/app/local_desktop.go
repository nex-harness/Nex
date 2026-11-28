//go:build !ios && !android

package app

import (
	"context"
	"log"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/egoist/mygo"
	"github.com/nex-harness/nex/pkg/runner"
	"github.com/nex-harness/nex/pkg/server"
	"github.com/nex-harness/nex/pkg/store"
	"github.com/nex-harness/nex/pkg/webui"
)

const supportsLocalMode = true

func embeddedAppVersion() string {
	v := strings.TrimSpace(mygo.App.Version())
	if v == "" {
		return "dev"
	}
	return v
}

func resolveDefaultWorkspace(workspaceDir string) string {
	ws := strings.TrimSpace(workspaceDir)
	if ws != "" && ws != "/" {
		return ws
	}
	if home, err := os.UserHomeDir(); err == nil && strings.TrimSpace(home) != "" {
		return home
	}
	if ws != "" {
		return ws
	}
	return "."
}

// AllInOneInstance manages an optional embedded Control Plane Server + local embedded Runner
// inside the Desktop host process ("Single-Machine Out-of-the-Box Mode" from Spec 02 Section 5).
type AllInOneInstance struct {
	URL            string
	httpServer     *http.Server
	cpSrv          *server.Server
	serverDB       *store.Store
	sleepInhibitor *SleepInhibitor

	rootCtx     context.Context
	rootCancel  context.CancelFunc
	runnerCfg   runner.Config
	runnerMu    sync.Mutex
	node        *runner.Node
	wiring      *runner.DefaultRuntimeWiring
	runnerStop  context.CancelFunc
}

func (a *AllInOneInstance) startEmbeddedRunnerLocked() error {
	node, err := runner.NewNode(a.runnerCfg)
	if err != nil {
		return err
	}
	wiring := runner.AttachDefaultSubsystems(node)
	runCtx, stop := context.WithCancel(a.rootCtx)
	a.node = node
	a.wiring = wiring
	a.runnerStop = stop

	go func(n *runner.Node, rCtx context.Context) {
		for {
			panicked := false
			func() {
				defer func() {
					if r := recover(); r != nil {
						panicked = true
						log.Printf("nex-desktop: embedded runner panic recovered: %v", r)
					}
				}()
				n.StartConnectLoop(rCtx)
			}()
			if !panicked || rCtx.Err() != nil {
				return
			}
			select {
			case <-rCtx.Done():
				return
			case <-time.After(2 * time.Second):
			}
		}
	}(node, runCtx)
	return nil
}

// RestartEmbeddedRunner cleanly tears down the in-process embedded runner (MCP/Browser/PTY/WS)
// and boots a fresh instance without restarting the desktop app.
func (a *AllInOneInstance) RestartEmbeddedRunner(_ context.Context) error {
	a.runnerMu.Lock()
	defer a.runnerMu.Unlock()

	if a.runnerStop != nil {
		a.runnerStop()
		a.runnerStop = nil
	}
	if a.wiring != nil {
		a.wiring.Close()
		a.wiring = nil
	}
	if a.node != nil {
		_ = a.node.Close()
		a.node = nil
	}
	return a.startEmbeddedRunnerLocked()
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

	appVer := embeddedAppVersion()
	effectiveWorkspace := resolveDefaultWorkspace(workspaceDir)

	rootCtx, rootCancel := context.WithCancel(ctx)
	inst := &AllInOneInstance{
		URL:        "http://" + actualAddr,
		serverDB:   srvStore,
		rootCtx:    rootCtx,
		rootCancel: rootCancel,
		runnerCfg: runner.Config{
			RunnerID:         "run_desktop_embedded",
			Name:             "Local Embedded Runner",
			Version:          appVer,
			ServerWSURL:      "ws://" + actualAddr + "/ws/runner",
			DataDir:          filepath.Join(baseDataDir, "runner"),
			WorkspaceDefault: effectiveWorkspace,
		},
	}

	cpSrv, err := server.New(server.Config{
		Store:                srvStore,
		ListenPort:           listenPort,
		StaticFS:             webui.DistFS(),
		Version:              appVer,
		OnRestartLocalRunner: inst.RestartEmbeddedRunner,
	})
	if err != nil {
		rootCancel()
		_ = srvStore.Close()
		_ = ln.Close()
		return nil, err
	}
	inst.cpSrv = cpSrv

	httpSrv := &http.Server{Handler: cpSrv.Handler()}
	inst.httpServer = httpSrv
	go func() {
		_ = httpSrv.Serve(ln)
	}()

	inst.runnerMu.Lock()
	err = inst.startEmbeddedRunnerLocked()
	inst.runnerMu.Unlock()
	if err != nil {
		rootCancel()
		_ = httpSrv.Close()
		_ = srvStore.Close()
		return nil, err
	}

	inhibitor := &SleepInhibitor{}
	inhibitor.Start()
	inst.sleepInhibitor = inhibitor

	return inst, nil
}

// Close cleanly shuts down the embedded Runner and embedded Server.
func (a *AllInOneInstance) Close() error {
	a.rootCancel()
	if a.sleepInhibitor != nil {
		a.sleepInhibitor.Stop()
	}
	a.runnerMu.Lock()
	if a.runnerStop != nil {
		a.runnerStop()
		a.runnerStop = nil
	}
	if a.wiring != nil {
		a.wiring.Close()
		a.wiring = nil
	}
	if a.node != nil {
		_ = a.node.Close()
		a.node = nil
	}
	a.runnerMu.Unlock()
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
