package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"syscall"

	"github.com/nex-harness/nex/clients/app"
)

func main() {
	home, _ := os.UserHomeDir()
	cwd, _ := os.Getwd()

	dataDir := flag.String("data-dir", filepath.Join(home, ".nex-desktop"), "Desktop app local data directory")
	workspace := flag.String("workspace", cwd, "Default workspace directory for embedded Runner")
	addr := flag.String("addr", "127.0.0.1:18080", "Local workbench listen address")
	openWindow := flag.Bool("open", true, "Automatically launch native/system window")
	flag.Parse()

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	inst, err := app.StartAllInOne(ctx, *dataDir, *workspace, *addr)
	if err != nil {
		log.Fatalf("nex-app startup failed: %v", err)
	}
	defer inst.Close()

	fmt.Printf("Nex Desktop All-in-One Workbench ready at %s\n", inst.URL)
	if *openWindow {
		openSystemWindow(inst.URL)
	}
	<-ctx.Done()
}

func openSystemWindow(url string) {
	switch runtime.GOOS {
	case "darwin":
		_ = exec.Command("open", url).Start()
	case "windows":
		_ = exec.Command("rundll32", "url.dll,FileProtocolHandler", url).Start()
	default:
		_ = exec.Command("xdg-open", url).Start()
	}
}
