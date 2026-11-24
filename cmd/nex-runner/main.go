package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"syscall"

	"github.com/kardianos/service"
	"github.com/nex-harness/nex/pkg/harness/scheduler"
	"github.com/nex-harness/nex/pkg/protocol"
	"github.com/nex-harness/nex/pkg/runner"
)

var (
	version  = "dev"
	buildSHA = ""
)

func main() {
	if len(os.Args) >= 2 && (os.Args[1] == "-version" || os.Args[1] == "--version" || os.Args[1] == "version") {
		fmt.Println(version)
		return
	}
	// Support `nex-runner service install|uninstall|start|stop|restart` via kardianos/service
	if len(os.Args) >= 3 && os.Args[1] == "service" {
		action := os.Args[2]
		svcArgs := os.Args[3:]
		svc, err := scheduler.NewOSService(svcArgs, func(ctx context.Context) {
			runDaemon(ctx, svcArgs)
		})
		if err != nil {
			log.Fatalf("init OS service failed: %v", err)
		}
		if err := service.Control(svc, action); err != nil {
			log.Fatalf("service %s failed: %v", action, err)
		}
		fmt.Printf("nex-runner service %s succeeded\n", action)
		return
	}
	// Support `nex-runner autostart install|uninstall|status` (Windows elevated Task Scheduler logon task)
	if len(os.Args) >= 3 && os.Args[1] == "autostart" {
		if err := handleAutostartSubcommand(os.Args[2], os.Args[3:]); err != nil {
			log.Fatalf("autostart %s failed: %v", os.Args[2], err)
		}
		return
	}
	// Support `nex-runner pair <nex://pair?...> [-data-dir ...] [-name ...]` for intranet/relay pairing
	if len(os.Args) >= 3 && os.Args[1] == "pair" {
		pfs := flag.NewFlagSet("pair", flag.ExitOnError)
		pDataDir := pfs.String("data-dir", filepath.Join(userHomeOrDot(), ".nex-runner"), "Local autonomous state directory")
		pName := pfs.String("name", os.Getenv("NEX_RUNNER_NAME"), "Human-readable runner name")
		pairURI := os.Args[2]
		_ = pfs.Parse(os.Args[3:])
		st, transport, err := pairRunnerWithURI(context.Background(), *pDataDir, pairURI, *pName)
		if err != nil {
			log.Fatalf("pair failed: %v", err)
		}
		fmt.Printf("Paired with %s (node=%s) via %s. Saved to %s\n", st.Profile.HostName, st.Profile.NodeID, transport, filepath.Join(*pDataDir, pairedServerFile))
		return
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	runDaemonWithCancel(ctx, stop, os.Args[1:])
}

func runDaemon(ctx context.Context, rawArgs []string) {
	runDaemonWithCancel(ctx, nil, rawArgs)
}

func runDaemonWithCancel(ctx context.Context, cancel context.CancelFunc, rawArgs []string) {
	fs := flag.NewFlagSet("nex-runner", flag.ExitOnError)
	defaultDir := filepath.Join(userHomeOrDot(), ".nex-runner")
	cwd, _ := os.Getwd()

	runnerID := fs.String("id", os.Getenv("NEX_RUNNER_ID"), "Unique runner node ID")
	name := fs.String("name", os.Getenv("NEX_RUNNER_NAME"), "Human-readable runner name")
	serverWS := fs.String("server", os.Getenv("NEX_SERVER_WS"), "Central server WebSocket URL (e.g. ws://127.0.0.1:8080/ws/runner)")
	pairURI := fs.String("pair", os.Getenv("NEX_PAIR_URI"), "Optional nex://pair?... URI for intranet/nex-relay E2EE tunnel pairing")
	token := fs.String("token", os.Getenv("NEX_PAIRING_TOKEN"), "Runner pairing/auth token")
	dataDir := fs.String("data-dir", defaultDir, "Local autonomous state & snapshot directory")
	workspace := fs.String("workspace", cwd, "Default workspace directory")
	oncePrompt := fs.String("prompt", "", "Optional standalone headless one-shot prompt (offline autonomous execution)")
	autoUpdateDefault := os.Getenv("NEX_AUTO_UPDATE") != "false" && os.Getenv("NEX_AUTO_UPDATE") != "0"
	autoUpdate := fs.Bool("auto-update", autoUpdateDefault, "Allow automatic self-updates for non-dev builds")
	noUACDefault := os.Getenv("NEX_NO_UAC") == "1" || os.Getenv("NEX_NO_UAC") == "true"
	noUAC := fs.Bool("no-uac", noUACDefault, "Skip automatic Windows UAC elevation prompt on startup")
	uacAttempted := fs.Bool("uac-attempted", false, "Internal flag preventing infinite UAC elevation loops")
	trayDefault := runtime.GOOS == "windows" && service.Interactive()
	trayEnabled := fs.Bool("tray", trayDefault, "Show Windows system tray icon and control menu")
	_ = fs.Parse(rawArgs)

	interactive := service.Interactive() && *oncePrompt == ""
	if relaunched, err := maybeElevateWindows(rawArgs, *noUAC, *uacAttempted, interactive); relaunched {
		return
	} else if err != nil {
		log.Printf("nex-runner: Windows UAC elevation skipped/cancelled (%v); continuing with standard privileges", err)
	}

	effectiveServerWS := *serverWS
	effectiveToken := *token
	var tunnelCleanup func()
	if *pairURI != "" {
		st, transport, err := pairRunnerWithURI(ctx, *dataDir, *pairURI, *name)
		if err != nil {
			log.Fatalf("failed to pair with server: %v", err)
		}
		log.Printf("nex-runner: paired with %s (node=%s) via %s", st.Profile.HostName, st.Profile.NodeID, transport)
		wsURL, devTok, cleanup, err := startPairedServerTunnel(ctx, *dataDir, st)
		if err != nil {
			log.Fatalf("failed to start paired server tunnel: %v", err)
		}
		effectiveServerWS = wsURL
		effectiveToken = devTok
		tunnelCleanup = cleanup
		defer tunnelCleanup()
	} else if effectiveServerWS == "" && *oncePrompt == "" {
		if st, err := loadPairedServerState(*dataDir); err == nil && st != nil {
			wsURL, devTok, cleanup, err := startPairedServerTunnel(ctx, *dataDir, st)
			if err != nil {
				log.Printf("nex-runner: failed to start saved paired server tunnel: %v", err)
			} else {
				effectiveServerWS = wsURL
				if effectiveToken == "" {
					effectiveToken = devTok
				}
				tunnelCleanup = cleanup
				defer tunnelCleanup()
			}
		}
	}

	var trayCleanup func()
	node, err := runner.NewNode(runner.Config{
		RunnerID:         *runnerID,
		Name:             *name,
		AuthToken:        effectiveToken,
		ServerWSURL:      effectiveServerWS,
		DataDir:          *dataDir,
		WorkspaceDefault: *workspace,
		Version:          version,
		BuildSHA:         buildSHA,
		Elevated:         isProcessElevated(),
		AutoUpdate:       autoUpdate,
		OnBeforeRestart: func() {
			if trayCleanup != nil {
				trayCleanup()
			}
		},
	})
	if err != nil {
		log.Fatalf("failed to initialize nex-runner: %v", err)
	}
	defer node.Close()

	if *oncePrompt == "" {
		trayCleanup = startWindowsTray(ctx, cancel, node, rawArgs, *trayEnabled)
		defer trayCleanup()
	}

	wiring := runner.AttachDefaultSubsystems(node)
	defer wiring.Close()

	if *oncePrompt != "" {
		msg, err := node.ExecutePrompt(ctx, protocol.PromptPayload{
			Text:          *oncePrompt,
			WorkspacePath: *workspace,
		}, "chat", false)
		if err != nil {
			log.Fatalf("standalone execution failed: %v", err)
		}
		fmt.Println(msg.PlainText())
		return
	}

	fmt.Printf("nex-runner (%s) started; server=%q data=%s\n", node.RunnerID(), effectiveServerWS, *dataDir)
	if effectiveServerWS != "" {
		node.StartConnectLoop(ctx)
	} else {
		// Standalone autonomous cron daemon mode (disconnected from any server)
		<-ctx.Done()
	}
}

func userHomeOrDot() string {
	if h, err := os.UserHomeDir(); err == nil && h != "" {
		return h
	}
	return "."
}
