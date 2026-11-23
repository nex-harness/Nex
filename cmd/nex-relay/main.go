package main

import (
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"time"

	"github.com/nex-harness/nex/pkg/relay"
)

var version = "dev"

func main() {
	showVersion := flag.Bool("version", false, "Print version and exit")
	defaultAddr := os.Getenv("NEX_RELAY_ADDR")
	if defaultAddr == "" {
		defaultAddr = ":8443"
	}
	addr := flag.String("addr", defaultAddr, "HTTP/WebSocket listen address")
	token := flag.String("token", os.Getenv("NEX_RELAY_TOKEN"), "Optional shared secret token required for relay connections")
	acceptTimeout := flag.Duration("accept-timeout", 15*time.Second, "Timeout waiting for intranet host to accept a relayed tunnel")
	flag.Parse()

	if *showVersion {
		fmt.Println(version)
		return
	}

	srv := relay.New(relay.Config{
		AuthToken:     *token,
		AcceptTimeout: *acceptTimeout,
	})

	fmt.Printf("nex-relay listening on %s (auth_required=%v)\n", *addr, *token != "")
	if err := http.ListenAndServe(*addr, srv.Handler()); err != nil {
		log.Fatalf("nex-relay exited: %v", err)
	}
}
