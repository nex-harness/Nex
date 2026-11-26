//go:build !ios && !android

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

	"github.com/egoist/mygo"
	"github.com/nex-harness/nex/clients/app"
	"github.com/nex-harness/nex/pkg/webui"
)

func main() {
	home, _ := os.UserHomeDir()
	defaultWorkspace := home
	if defaultWorkspace == "" {
		defaultWorkspace, _ = os.Getwd()
	}

	defaultDataDir := filepath.Join(home, ".nex-desktop")
	dataDir := flag.String("data-dir", "", "Desktop app local data directory")
	workspace := flag.String("workspace", defaultWorkspace, "Default workspace directory for embedded Runner")
	addr := flag.String("addr", "127.0.0.1:18080", "Headless workbench listen address (with -open=false)")
	openWindow := flag.Bool("open", true, "Launch native mygo desktop window (false runs headless all-in-one daemon)")
	serverURL := flag.String("server", "", "Connect to an explicit remote nex-server URL (http:// or https://)")
	forceLocal := flag.Bool("local", false, "Start in Local All-in-One mode")
	flag.Parse()

	if !*openWindow && os.Getenv("MYGO_GENERATE") == "" {
		dir := *dataDir
		if dir == "" {
			dir = defaultDataDir
		}
		ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
		defer stop()

		inst, err := app.StartAllInOne(ctx, dir, *workspace, *addr)
		if err != nil {
			log.Fatalf("nex-app startup failed: %v", err)
		}
		defer inst.Close()

		fmt.Printf("Nex Desktop All-in-One Workbench ready at %s\n", inst.URL)
		<-ctx.Done()
		return
	}

	mygo.SetFrontend(webui.DistFS())
	if err := app.RunDesktop(app.DesktopRunOptions{
		DataDir:      *dataDir,
		WorkspaceDir: *workspace,
		LocalAddr:    "",
		ServerURL:    *serverURL,
		ForceLocal:   *forceLocal,
	}); err != nil {
		log.Fatalf("nex-app exited with error: %v", err)
	}
}
