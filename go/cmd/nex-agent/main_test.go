package main

import (
	"bufio"
	"bytes"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/tylinux/nex/go/internal/protocol"
)

var agentBinary string

func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "nex-agent-test")
	if err != nil {
		panic(err)
	}
	name := "nex-agent"
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	agentBinary = filepath.Join(dir, name)
	if out, err := exec.Command("go", "build", "-buildvcs=false", "-trimpath", "-o", agentBinary, ".").CombinedOutput(); err != nil {
		panic("build failed: " + string(out))
	}
	code := m.Run()
	os.RemoveAll(dir)
	os.Exit(code)
}

type agentProc struct {
	cmd    *exec.Cmd
	stdin  io.WriteCloser
	stdout *bufio.Reader
	stderr *bytes.Buffer
}

func startAgent(t *testing.T, args ...string) *agentProc {
	t.Helper()
	cmd := exec.Command(agentBinary, args...)
	// The agent creates its database under $HOME on startup; never touch the real one.
	home := t.TempDir()
	cmd.Env = append(os.Environ(), "HOME="+home, "USERPROFILE="+home)
	in, _ := cmd.StdinPipe()
	out, _ := cmd.StdoutPipe()
	stderr := &bytes.Buffer{}
	cmd.Stderr = stderr
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cmd.Process.Kill(); _ = cmd.Wait() })
	return &agentProc{cmd: cmd, stdin: in, stdout: bufio.NewReader(out), stderr: stderr}
}

func (a *agentProc) send(t *testing.T, line string) {
	t.Helper()
	if _, err := io.WriteString(a.stdin, line+"\n"); err != nil {
		t.Fatal(err)
	}
}

// recvReply returns the next frame that is not a startup/storageState notification.
func (a *agentProc) recvReply(t *testing.T) string {
	t.Helper()
	for {
		line := a.recv(t)
		if !strings.Contains(line, `"method":"startup/storageState"`) {
			return line
		}
	}
}

func (a *agentProc) recv(t *testing.T) string {
	t.Helper()
	done := make(chan string, 1)
	go func() { l, _ := a.stdout.ReadString('\n'); done <- l }()
	select {
	case l := <-done:
		return strings.TrimRight(l, "\n")
	case <-time.After(10 * time.Second):
		t.Fatal("timed out waiting for a frame")
		return ""
	}
}

func TestUnknownMethodGetsMethodNotFound(t *testing.T) {
	a := startAgent(t, "app-server", "--stdio")
	a.send(t, `{"id":41,"method":"session/create","params":{}}`)
	msg, err := protocol.ParseMessage([]byte(a.recvReply(t)))
	if err != nil {
		t.Fatal(err)
	}
	if msg.Kind != protocol.KindError || msg.ID.String() != "41" || msg.Error.Code != protocol.CodeMethodNotFound {
		t.Fatalf("unexpected reply: %+v", msg)
	}
}

func TestAgentServerAliasAndStringIDs(t *testing.T) {
	a := startAgent(t, "agent-server", "--stdio")
	a.send(t, `{"id":"req-é","method":"x"}`)
	msg, err := protocol.ParseMessage([]byte(a.recvReply(t)))
	if err != nil || msg.ID.String() != "req-é" || !msg.ID.IsString() {
		t.Fatalf("string id not preserved: %+v %v", msg, err)
	}
}

// Every byte on stdout must be a valid frame even while the process logs and
// is handed hostile input; all logging belongs on stderr.
func TestStdoutCarriesOnlyFrames(t *testing.T) {
	a := startAgent(t, "app-server", "--stdio")
	a.send(t, `{"method":"startup/ping"}`)                       // notification: no reply
	a.send(t, `{"id":1,"method":"a","params":{"t":"\u2028\n"}}`) // escaped LF stays inside the string
	a.send(t, `{"id":2,"result":null}`)                          // a response we never asked for: dropped
	a.send(t, `{"id":3,"method":"b"}`)
	// 每个请求在独立 goroutine 中处理（见 protocol.Serve），宿主按 id 关联回复，
	// 所以两个回复的先后顺序不是协议承诺；只要求 1 和 3 各恰好回复一次。
	got := map[string]int{}
	for range 2 {
		line := a.recvReply(t)
		msg, err := protocol.ParseMessage([]byte(line))
		if err != nil {
			t.Fatalf("unparsable reply %q: %v", line, err)
		}
		got[msg.ID.String()]++
	}
	if got["1"] != 1 || got["3"] != 1 || len(got) != 2 {
		t.Fatalf("expected exactly one reply each for ids 1 and 3, got %v", got)
	}
	_ = a.stdin.Close()
	rest, _ := io.ReadAll(a.stdout)
	if len(bytes.TrimSpace(rest)) != 0 {
		t.Fatalf("unexpected extra stdout bytes: %q", rest)
	}
	if err := a.cmd.Wait(); err != nil {
		t.Fatalf("exit after EOF: %v", err)
	}
	if !strings.Contains(a.stderr.String(), "agent starting") {
		t.Errorf("startup log missing from stderr: %q", a.stderr.String())
	}
	if strings.Contains(a.stderr.String(), `"id"`) {
		t.Errorf("protocol data leaked to stderr: %q", a.stderr.String())
	}
}

func TestInvalidFrameEndsSessionWithErrorExit(t *testing.T) {
	a := startAgent(t, "app-server", "--stdio")
	a.send(t, `{"id":1,"method":"a","extra":true}`) // strict schema: host would reject it too
	done := make(chan error, 1)
	go func() { done <- a.cmd.Wait() }()
	select {
	case err := <-done:
		var ee *exec.ExitError
		if err == nil || !asExit(err, &ee) || ee.ExitCode() != 1 {
			t.Fatalf("want exit code 1, got %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("agent did not exit on an invalid frame")
	}
	if !strings.Contains(a.stderr.String(), "parse frame") {
		t.Errorf("reason missing from stderr: %q", a.stderr.String())
	}
}

func asExit(err error, target **exec.ExitError) bool {
	ee, ok := err.(*exec.ExitError)
	if ok {
		*target = ee
	}
	return ok
}

func TestStdinEOFExitsPromptly(t *testing.T) {
	a := startAgent(t, "app-server", "--stdio")
	start := time.Now()
	_ = a.stdin.Close()
	if err := a.cmd.Wait(); err != nil {
		t.Fatalf("exit: %v", err)
	}
	// The host waits ~1.8 s for exit after EOF before killing the process tree
	// (STDIO_EOF_EXIT_WAIT_MS in nexStdioTransport.ts).
	if d := time.Since(start); d > 1500*time.Millisecond {
		t.Fatalf("slow exit after EOF: %v", d)
	}
}

func TestUsageErrors(t *testing.T) {
	for _, args := range [][]string{{}, {"app-server"}, {"app-server", "--tcp"}, {"bogus", "--stdio"}} {
		cmd := exec.Command(agentBinary, args...)
		out, err := cmd.Output()
		var ee *exec.ExitError
		if err == nil || !asExit(err, &ee) || ee.ExitCode() != 2 {
			t.Errorf("args %v: want exit 2, got %v", args, err)
		}
		if len(out) != 0 {
			t.Errorf("args %v: usage text leaked to stdout: %q", args, out)
		}
	}
}

func TestVersion(t *testing.T) {
	out, err := exec.Command(agentBinary, "--version").Output()
	if err != nil || strings.TrimSpace(string(out)) == "" {
		t.Fatalf("--version: %q %v", out, err)
	}
}

func TestClaimStdoutRedirectsStrayPrints(t *testing.T) {
	orig := os.Stdout
	defer func() { os.Stdout = orig }()
	frames := claimStdout()
	if frames != orig {
		t.Fatal("frames must be the original stdout")
	}
	if os.Stdout != os.Stderr {
		t.Fatal("os.Stdout must be redirected to stderr after the claim")
	}
}
