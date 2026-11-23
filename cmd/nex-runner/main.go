package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"path/filepath"
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

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	runDaemon(ctx, os.Args[1:])
}

func runDaemon(ctx context.Context, rawArgs []string) {
	fs := flag.NewFlagSet("nex-runner", flag.ExitOnError)
	defaultDir := filepath.Join(userHomeOrDot(), ".nex-runner")
	cwd, _ := os.Getwd()

	runnerID := fs.String("id", os.Getenv("NEX_RUNNER_ID"), "Unique runner node ID")
	name := fs.String("name", os.Getenv("NEX_RUNNER_NAME"), "Human-readable runner name")
	serverWS := fs.String("server", os.Getenv("NEX_SERVER_WS"), "Central server WebSocket URL (e.g. ws://127.0.0.1:8080/ws/runner)")
	token := fs.String("token", os.Getenv("NEX_PAIRING_TOKEN"), "Runner pairing/auth token")
	dataDir := fs.String("data-dir", defaultDir, "Local autonomous state & snapshot directory")
	workspace := fs.String("workspace", cwd, "Default workspace directory")
	oncePrompt := fs.String("prompt", "", "Optional standalone headless one-shot prompt (offline autonomous execution)")
	autoUpdateDefault := os.Getenv("NEX_AUTO_UPDATE") != "false" && os.Getenv("NEX_AUTO_UPDATE") != "0"
	autoUpdate := fs.Bool("auto-update", autoUpdateDefault, "Allow automatic self-updates for non-dev builds")
	_ = fs.Parse(rawArgs)

	node, err := runner.NewNode(runner.Config{
		RunnerID:         *runnerID,
		Name:             *name,
		AuthToken:        *token,
		ServerWSURL:      *serverWS,
		DataDir:          *dataDir,
		WorkspaceDefault: *workspace,
		Version:          version,
		BuildSHA:         buildSHA,
		AutoUpdate:       autoUpdate,
	})
	if err != nil {
		log.Fatalf("failed to initialize nex-runner: %v", err)
	}
	defer node.Close()

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

	fmt.Printf("nex-runner (%s) started; server=%q data=%s\n", node.RunnerID(), *serverWS, *dataDir)
	if *serverWS != "" {
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
