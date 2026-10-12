package openai_test

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/tylinux/nex/go/internal/adapters/model/openai"
	"github.com/tylinux/nex/go/internal/modelclient"
)

// TestAgainstNodeMockModel drives the real scripts/go-rewrite/record/mock-model.mjs
// from a node subprocess and streams its output through the Go client. This is the
// strongest cross-language check available without the full recorder: the bytes
// are produced by the same mock the TypeScript recordings use.
//
// It skips when node is not on PATH so it never blocks a machine without Node.
func TestAgainstNodeMockModel(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node not on PATH")
	}
	repo := repoRoot(t)
	mockScript := filepath.Join(repo, "scripts", "go-rewrite", "record", "mock-model.mjs")
	if _, err := os.Stat(mockScript); err != nil {
		t.Skipf("mock-model.mjs not found: %v", err)
	}

	// The driver starts the mock and prints its port, then stays alive until
	// killed. Steps: 429 then a streamed text answer (S13).
	driver := `
	import { startMockModel } from %s;
	const mock = await startMockModel({ steps: [
	  { error: { status: 429, headers: { "retry-after": "0" } } },
	  { text: "Recovered after a retry." },
	]});
	process.stdout.write(JSON.stringify({ port: mock.port }) + "\n");
	setInterval(() => {}, 1000);
	`
	driverPath := filepath.Join(t.TempDir(), "driver.mjs")
	if err := os.WriteFile(driverPath, []byte(fmt.Sprintf(driver, jsQuote(mockScript))), 0o600); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(node, driverPath)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = cmd.Process.Kill() }()

	sc := bufio.NewScanner(stdout)
	if !sc.Scan() {
		t.Fatalf("mock did not report a port: %v", sc.Err())
	}
	var info struct {
		Port int `json:"port"`
	}
	if err := json.Unmarshal(sc.Bytes(), &info); err != nil {
		t.Fatalf("bad mock banner %q: %v", sc.Text(), err)
	}
	waitForPort(t, info.Port)

	sink := &recordingSink{}
	tr := openai.New(openai.Config{
		URL:     fmt.Sprintf("http://127.0.0.1:%d/v1/chat/completions", info.Port),
		Timeout: 10 * time.Second,
	})
	client := modelclient.NewClient(modelclient.Config{
		ProviderID:   "mock",
		ModelID:      "mock-model",
		ProviderKind: "openai-compatible",
		Transport:    "sse",
		Retry:        modelclient.DefaultRetryOptions(),
		Clock:        newFreeClock(),
	}, tr, sink)

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	h := client.Stream(ctx, modelclient.Request{
		URL:     fmt.Sprintf("http://127.0.0.1:%d/v1/chat/completions", info.Port),
		Headers: map[string]string{"x-request-id": "req-1"},
		Body:    []byte(`{"model":"mock-model","messages":[],"stream":true}`),
	})
	var texts []string
	for ev := range h.Events() {
		if ev.Kind == modelclient.EventTextDelta {
			texts = append(texts, ev.Text)
		}
	}
	if err := h.Wait(); err != nil {
		t.Fatalf("stream failed: %v", err)
	}
	if strings.Join(texts, "") != "Recovered after a retry." {
		t.Fatalf("text: %q", strings.Join(texts, ""))
	}
	got := sink.statuses()
	want := []string{
		"model_request_started", "model_request_failed", "model_retry_scheduled",
		"model_request_started", "model_request_completed",
	}
	if !equalStrings(got, want) {
		t.Fatalf("status sequence: got %v want %v", got, want)
	}
}

func repoRoot(t *testing.T) string {
	t.Helper()
	dir, _ := os.Getwd()
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return filepath.Dir(dir)
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("repo root not found")
		}
		dir = parent
	}
}

func waitForPort(t *testing.T, port int) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		conn, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", port), 200*time.Millisecond)
		if err == nil {
			_ = conn.Close()
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("mock port %d never opened", port)
}

func jsQuote(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}
