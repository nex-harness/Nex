// Command fakeupstream stands in for the Node @nex/server in the supervisor's unit
// tests. It exists so the crash/restart/backoff tests exercise the REAL supervision
// path — a real child process, a real port, a real kill — without requiring a built
// Node bundle, and without the several seconds a Node boot would cost per iteration.
//
// It reproduces the four observable behaviours the supervisor depends on, and nothing
// else:
//
//  1. binds 127.0.0.1 on the port from $PORT (0 = ephemeral) and prints
//     `http://127.0.0.1:<port>` on stdout — the exact line createHttpServer's listen
//     callback logs and the supervisor parses;
//  2. answers GET /api/auth/session with {"authRequired":true,...}, 200 for the right
//     ?token= and 401 for a wrong or missing one — the middleware contract webAuth.ts
//     implements, because "is it ready" is exactly this answer;
//  3. exits after $FAKE_CRASH_AFTER_MS, or as soon as $FAKE_CONTROL_FILE exists —
//     the crash the supervisor must notice and restart. The trigger file is REMOVED
//     before exiting, so crashing generation N cannot instantly crash generation N+1,
//     which is spawned with the same trigger path;
//  4. stays alive until signalled, which is what a stop test needs: the supervisor has
//     to prove the tree is gone rather than observe it exiting on its own.
//
// Every behaviour is off by default, so the default child is a well-behaved server.
package main

import (
	"fmt"
	"net"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"
)

func main() {
	port := 0
	if raw := strings.TrimSpace(os.Getenv("PORT")); raw != "" {
		if parsed, err := strconv.Atoi(raw); err == nil {
			port = parsed
		}
	}
	token := strings.TrimSpace(os.Getenv("NEX_SERVER_AUTH_TOKEN"))
	controlFile := strings.TrimSpace(os.Getenv("FAKE_CONTROL_FILE"))

	listener, err := net.Listen("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(port)))
	if err != nil {
		fmt.Fprintf(os.Stderr, "fakeupstream: listen: %v\n", err)
		os.Exit(1)
	}
	actual := listener.Addr().(*net.TCPAddr).Port

	// Generation marker: lets a test tell a restarted child from the first one.
	generation := os.Getenv("FAKE_GENERATION")

	mux := http.NewServeMux()
	mux.HandleFunc("/api/auth/session", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		if token != "" && r.URL.Query().Get("token") != token {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(`{"error":"Unauthorized"}`))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"authRequired":true,"authenticated":true,"generation":"` + generation + `"}`))
	})
	// A generation marker the test can read back over HTTP.
	mux.HandleFunc("/fake/generation", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(generation))
	})

	server := &http.Server{Handler: mux}
	go func() {
		if err := server.Serve(listener); err != nil && err != http.ErrServerClosed {
			fmt.Fprintf(os.Stderr, "fakeupstream: serve: %v\n", err)
			os.Exit(1)
		}
	}()

	// The line the supervisor parses. Same shape as http.ts's listen log.
	fmt.Printf("[nex-server:http][%d] http://127.0.0.1:%d\n", os.Getpid(), actual)
	_ = os.Stdout.Sync()

	go watchForCrash(controlFile, durationEnv("FAKE_CRASH_AFTER_MS"))

	// Keep serving until the supervisor signals the tree down. Exiting by itself would
	// make the stop tests vacuous: they assert the tree is gone BECAUSE of the stop.
	select {}
}

// watchForCrash exits the process when the trigger file appears or the timer fires,
// whichever comes first.
//
// The trigger file is removed before exiting: the next generation is spawned with the
// same path, and a leftover file would kill it before its first health probe.
func watchForCrash(controlFile string, after time.Duration) {
	var deadline <-chan time.Time
	if after > 0 {
		timer := time.NewTimer(after)
		defer timer.Stop()
		deadline = timer.C
	}
	for {
		if controlFile != "" {
			if _, err := os.Stat(controlFile); err == nil {
				_ = os.Remove(controlFile)
				os.Exit(crashExitCode)
			}
		}
		select {
		case <-deadline:
			os.Exit(crashExitCode)
		case <-time.After(pollInterval):
		}
	}
}

// crashExitCode is a non-zero code a test can distinguish from a signal.
const crashExitCode = 7

// pollInterval is how often the trigger file is checked. Short enough that a test does
// not have to guess a sleep, long enough not to spin.
const pollInterval = 10 * time.Millisecond

func durationEnv(name string) time.Duration {
	raw := strings.TrimSpace(os.Getenv(name))
	if raw == "" {
		return 0
	}
	ms, err := strconv.Atoi(raw)
	if err != nil || ms <= 0 {
		return 0
	}
	return time.Duration(ms) * time.Millisecond
}
