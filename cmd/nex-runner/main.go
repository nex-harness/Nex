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

	"github.com/nex-harness/nex/pkg/protocol"
	"github.com/nex-harness/nex/pkg/runner"
)

func main() {
	defaultDir := filepath.Join(userHomeOrDot(), ".nex-runner")
	cwd, _ := os.Getwd()

	runnerID := flag.String("id", os.Getenv("NEX_RUNNER_ID"), "Unique runner node ID")
	name := flag.String("name", os.Getenv("NEX_RUNNER_NAME"), "Human-readable runner name")
	serverWS := flag.String("server", os.Getenv("NEX_SERVER_WS"), "Central server WebSocket URL (e.g. ws://127.0.0.1:8080/ws/runner)")
	token := flag.String("token", os.Getenv("NEX_PAIRING_TOKEN"), "Runner pairing/auth token")
	dataDir := flag.String("data-dir", defaultDir, "Local autonomous state & snapshot directory")
	workspace := flag.String("workspace", cwd, "Default workspace directory")
	oncePrompt := flag.String("prompt", "", "Optional standalone headless one-shot prompt (offline autonomous execution)")
	flag.Parse()

	node, err := runner.NewNode(runner.Config{
		RunnerID:         *runnerID,
		Name:             *name,
		AuthToken:        *token,
		ServerWSURL:      *serverWS,
		DataDir:          *dataDir,
		WorkspaceDefault: *workspace,
	})
	if err != nil {
		log.Fatalf("failed to initialize nex-runner: %v", err)
	}
	defer node.Close()

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

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

	fmt.Printf("nex-runner (%s) started; connecting to %s\n", node.RunnerID(), *serverWS)
	node.StartConnectLoop(ctx)
}

func userHomeOrDot() string {
	if h, err := os.UserHomeDir(); err == nil && h != "" {
		return h
	}
	return "."
}
