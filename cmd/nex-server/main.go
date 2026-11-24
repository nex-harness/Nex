package main

import (
	"flag"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"

	"github.com/nex-harness/nex/pkg/server"
	"github.com/nex-harness/nex/pkg/store"
)

var (
	version  = "dev"
	buildSHA = ""
)

func main() {
	defaultDir := filepath.Join(userHomeOrDot(), ".nex-server")
	showVersion := flag.Bool("version", false, "Print version and exit")
	addr := flag.String("addr", ":8080", "HTTP/WebSocket listen address")
	dataDir := flag.String("data-dir", defaultDir, "Server data directory (main.db, archive.db, blobs)")
	pairingToken := flag.String("pairing-token", os.Getenv("NEX_PAIRING_TOKEN"), "Optional shared pairing token for Runners")
	publicURL := flag.String("public-url", os.Getenv("NEX_PUBLIC_URL"), "Public base URL advertised for /llm-proxy")
	runnersDir := flag.String("runners-dir", os.Getenv("NEX_RUNNERS_DIR"), "Optional directory containing prebuilt multi-platform nex-runner binaries")
	releaseManifestURL := flag.String("release-manifest-url", os.Getenv("NEX_RELEASE_MANIFEST_URL"), "Optional URL to release runner manifest.json for release-first updates")
	enableRelayDefault := os.Getenv("NEX_ENABLE_RELAY") == "true" || os.Getenv("NEX_ENABLE_RELAY") == "1"
	enableRelay := flag.Bool("enable-relay", enableRelayDefault, "Mount stateless zero-trust /relay/v1/* endpoints onto this server")
	relayToken := flag.String("relay-token", os.Getenv("NEX_RELAY_TOKEN"), "Optional shared secret token for embedded relay connections")
	flag.Parse()

	if *showVersion {
		fmt.Println(version)
		return
	}

	st, err := store.Open(*dataDir)
	if err != nil {
		log.Fatalf("failed to open store at %s: %v", *dataDir, err)
	}
	defer st.Close()

	listenPort := 8080
	if _, portStr, err := net.SplitHostPort(*addr); err == nil {
		if p, pErr := strconv.Atoi(portStr); pErr == nil && p > 0 {
			listenPort = p
		}
	}

	srv, err := server.New(server.Config{
		Store:              st,
		Version:            version,
		BuildSHA:           buildSHA,
		PairingSecret:      *pairingToken,
		PublicBaseURL:      *publicURL,
		ListenPort:         listenPort,
		RunnersDir:         *runnersDir,
		ReleaseManifestURL: *releaseManifestURL,
		StaticFS:           embeddedWebFS(),
		EnableRelay:        *enableRelay,
		RelayToken:         *relayToken,
	})
	if err != nil {
		log.Fatalf("failed to init nex-server: %v", err)
	}
	defer srv.Close()

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
