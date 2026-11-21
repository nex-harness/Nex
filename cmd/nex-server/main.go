package main

import (
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"

	"github.com/nex-harness/nex/pkg/server"
	"github.com/nex-harness/nex/pkg/store"
)

func main() {
	defaultDir := filepath.Join(userHomeOrDot(), ".nex-server")
	addr := flag.String("addr", ":8080", "HTTP/WebSocket listen address")
	dataDir := flag.String("data-dir", defaultDir, "Server data directory (main.db, archive.db, blobs)")
	pairingToken := flag.String("pairing-token", os.Getenv("NEX_PAIRING_TOKEN"), "Optional shared pairing token for Runners")
	publicURL := flag.String("public-url", os.Getenv("NEX_PUBLIC_URL"), "Public base URL advertised for /llm-proxy")
	runnersDir := flag.String("runners-dir", os.Getenv("NEX_RUNNERS_DIR"), "Optional directory containing prebuilt multi-platform nex-runner binaries")
	flag.Parse()

	st, err := store.Open(*dataDir)
	if err != nil {
		log.Fatalf("failed to open store at %s: %v", *dataDir, err)
	}
	defer st.Close()

	srv, err := server.New(server.Config{
		Store:         st,
		PairingSecret: *pairingToken,
		PublicBaseURL: *publicURL,
		RunnersDir:    *runnersDir,
		StaticFS:      embeddedWebFS(),
	})
	if err != nil {
		log.Fatalf("failed to init nex-server: %v", err)
	}

	fmt.Printf("nex-server listening on %s (data: %s)\n", *addr, *dataDir)
	if err := http.ListenAndServe(*addr, srv.Handler()); err != nil {
		log.Fatalf("server exited: %v", err)
	}
}

func userHomeOrDot() string {
	if h, err := os.UserHomeDir(); err == nil && h != "" {
		return h
	}
	return "."
}
