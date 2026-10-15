// Command fakeagent stands in for the Go agent in the process-manager tests.
//
// It is a real child process (a real spawn, real pipes, a real kill) that can be
// scripted into every failure mode the manager has to survive, without a built
// agent binary and without the storage/model setup a real `app-server` needs.
// Everything it does is off by default, so the default child is a well-behaved
// agent that waits for stdin EOF and exits 0 — exactly the contract
// `app-server --stdio` implements.
//
// Scripted behaviors, all selected by environment variables:
//
//	FAKE_MODE=hang              keep running, ignore stdin EOF entirely
//	FAKE_MODE=ignore-eof        keep running after stdin EOF (tests the EOF wait
//	                            expiring and the tree kill taking over)
//	FAKE_MODE=crash             write a diagnostic to stderr, then exit non-zero
//	FAKE_MODE=crash-plain       write plain stderr lines, then exit non-zero
//	FAKE_MODE=slow-start        sleep FAKE_SLOW_START_MS before the first frame
//	FAKE_MODE=stdout-eof        close stdout while staying alive (the host must
//	                            see the protocol leg end before the process does)
//	FAKE_MODE=exit-immediately  exit 0 without waiting for anything
//
// Supporting variables:
//
//	FAKE_EXIT_CODE         the exit code for crash modes (default 1)
//	FAKE_SLOW_START_MS     delay before the first stdout frame
//	FAKE_STDERR_LINES      how many plain stderr lines to write
//	FAKE_STDERR_TEXT       text of those lines (a redaction test can put a
//	                        secret-shaped value here)
//	FAKE_CHILD             when set, spawn a grandchild that outlives the parent,
//	                        which is how the tree kill is proven
//	FAKE_READY_FILE        touch this path once the agent is "ready", so a test
//	                        can wait for readiness without polling the protocol
//	FAKE_ECHO_STDIN        echo every stdin line back on stdout
package main

import (
	"bufio"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"
)

func main() {
	mode := os.Getenv("FAKE_MODE")

	// A grandchild in the same process group is the point of a tree kill: killing
	// only the root leaves it running, and a test that checks the process table
	// would catch exactly that.
	if os.Getenv("FAKE_CHILD") != "" {
		startGrandchild()
	}

	switch mode {
	case "hang":
		hangForever()
	case "ignore-eof":
		// Read stdin until EOF, then keep running: the manager's bounded EOF wait
		// must expire and escalate to the tree kill.
		drainStdin()
		fmt.Fprintln(os.Stderr, "fakeagent: ignoring stdin EOF")
		hangForever()
	case "crash":
		writeDiagnostic()
		os.Exit(exitCode())
	case "crash-plain":
		writePlainStderr()
		os.Exit(exitCode())
	case "slow-start":
		delay()
		announceReady()
		serveUntilEOF()
	case "stdout-eof":
		announceReady()
		// Close stdout and keep the process (and its stderr) alive. The host must
		// treat the protocol leg as closed without treating the process as gone.
		os.Stdout.Close()
		hangForever()
	case "exit-immediately":
		os.Exit(0)
	default:
		announceReady()
		serveUntilEOF()
	}
}

// startGrandchild forks a child that sleeps for a long time, so it outlives the
// parent's own exit. It stays in the parent's process group (the exec adapter put
// the parent in a fresh one), which is what makes a group kill reach it.
//
// The grandchild's pid is written to $FAKE_CHILD_PID_FILE when set: this process
// exits almost immediately and the grandchild is re-parented to init, so a test
// cannot find it by PPID and needs the pid handed to it directly.
func startGrandchild() {
	cmd := exec.Command(sleepCommand(), "120")
	if err := cmd.Start(); err != nil {
		fmt.Fprintln(os.Stderr, "fakeagent: grandchild failed to start:", err)
		return
	}
	if path := os.Getenv("FAKE_CHILD_PID_FILE"); path != "" {
		_ = os.WriteFile(path, []byte(strconv.Itoa(cmd.Process.Pid)), 0o600)
	}
	// Reap the grandchild when it eventually ends, so this process does not leave
	// a zombie behind if it outlives the parent.
	go func() { _ = cmd.Wait() }()
}

// sleepCommand is portable enough for the test hosts: `sleep` on Unix, and the
// Windows build of this helper is never run (the manager's Windows path is
// cross-compiled, not executed here).
func sleepCommand() string {
	if _, err := exec.LookPath("sleep"); err == nil {
		return "sleep"
	}
	return "timeout"
}

// hangForever blocks until a signal arrives. A bare `select {}` would be reported
// by the Go runtime as "all goroutines are asleep - deadlock!" and exit(2), which
// is NOT the hang a test needs: the test needs a process that stays alive until it
// is signalled.
func hangForever() {
	signals := make(chan os.Signal, 1)
	signal.Notify(signals, os.Interrupt, syscall.SIGTERM)
	<-signals
}

func drainStdin() {
	scanner := bufio.NewScanner(os.Stdin)
	for scanner.Scan() {
		if os.Getenv("FAKE_ECHO_STDIN") != "" {
			fmt.Fprintln(os.Stdout, scanner.Text())
		}
	}
}

// serveUntilEOF is the real agent contract: read protocol frames from stdin until
// the host closes it, then exit 0.
func serveUntilEOF() {
	drainStdin()
	os.Exit(0)
}

func announceReady() {
	if path := os.Getenv("FAKE_READY_FILE"); path != "" {
		_ = os.WriteFile(path, []byte(strconv.Itoa(os.Getpid())), 0o600)
	}
	fmt.Fprintln(os.Stdout, `{"type":"ready"}`)
}

func delay() {
	ms, err := strconv.Atoi(os.Getenv("FAKE_SLOW_START_MS"))
	if err != nil || ms <= 0 {
		ms = 500
	}
	time.Sleep(time.Duration(ms) * time.Millisecond)
}

func exitCode() int {
	code, err := strconv.Atoi(os.Getenv("FAKE_EXIT_CODE"))
	if err != nil || code == 0 {
		return 1
	}
	return code
}

// writeDiagnostic emits a well-formed `[nex-process-exception]` line, which the
// manager must parse and report as a structured exception rather than as log
// noise. The secret-shaped value in the message is deliberate: the redaction
// happens before the record leaves the host process.
func writeDiagnostic() {
	fmt.Fprintf(os.Stderr,
		"%s{\"version\":1,\"errorId\":\"11111111-2222-4333-8444-555555555555\",\"kind\":\"uncaughtException\",\"origin\":\"uncaughtException\",\"name\":\"FakeAgentCrash\",\"message\":\"boom token=sk-abcdefghijklmnopqrstuvwx\",\"stack\":\"at fakeagent.main\",\"occurredAt\":1700000000000}\n",
		diagnosticPrefix())
	fmt.Fprintln(os.Stderr, "fakeagent: crashing on purpose")
}

func writePlainStderr() {
	count, err := strconv.Atoi(os.Getenv("FAKE_STDERR_LINES"))
	if err != nil || count <= 0 {
		count = 3
	}
	text := os.Getenv("FAKE_STDERR_TEXT")
	if text == "" {
		text = "fakeagent: line %d"
	}
	for i := 0; i < count; i++ {
		line := text
		if strings.Contains(text, "%d") {
			line = strings.Replace(text, "%d", strconv.Itoa(i), 1)
		}
		fmt.Fprintln(os.Stderr, line)
	}
}

// diagnosticPrefix must stay byte-identical to the host's constant; the host
// parses on it, so a mismatch here is exactly the bug the test would report.
func diagnosticPrefix() string { return "[nex-process-exception] " }
