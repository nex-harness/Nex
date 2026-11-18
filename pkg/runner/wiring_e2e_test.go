package runner

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/nex-harness/nex/pkg/protocol"
	"github.com/nex-harness/nex/pkg/server"
	"github.com/nex-harness/nex/pkg/store"
	"net/http/httptest"
)

type stubServerMCP struct{}

func (s *stubServerMCP) CallTool(_ context.Context, ns, tool string, _ json.RawMessage) (json.RawMessage, error) {
	return json.RawMessage(`{"mcp":"` + ns + "." + tool + `_ok"}`), nil
}

func TestFullSubsystemWiringWorkspacePTYAndExtensionsE2E(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	srvStore, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer srvStore.Close()

	cpServer, err := server.New(server.Config{
		Store:     srvStore,
		RemoteMCP: &stubServerMCP{},
	})
	if err != nil {
		t.Fatal(err)
	}
	httpSrv := httptest.NewServer(cpServer.Handler())
	defer httpSrv.Close()
	wsBase := "ws" + strings.TrimPrefix(httpSrv.URL, "http")

	workDir := t.TempDir()
	_ = os.WriteFile(filepath.Join(workDir, "hello.go"), []byte("package main\nfunc main() {}\n"), 0o644)

	node, err := NewNode(Config{
		RunnerID:         "run_wired_1",
		Name:             "Wired Runner",
		ServerWSURL:      wsBase + "/ws/runner",
		DataDir:          t.TempDir(),
		WorkspaceDefault: workDir,
		ProviderOverride: &mockE2EProvider{},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer node.Close()
	wiring := AttachDefaultSubsystems(node)
	defer wiring.Close()

	go node.StartConnectLoop(ctx)

	// Wait until runner registers online
	for i := 0; i < 50; i++ {
		if node.IsOnline() {
			break
		}
		time.Sleep(30 * time.Millisecond)
	}
	if !node.IsOnline() {
		t.Fatalf("runner did not come online")
	}

	// Connect UI client over /ws/client
	cliWS, _, err := websocket.Dial(ctx, wsBase+"/ws/client", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer cliWS.Close(websocket.StatusNormalClosure, "")

	// 1. Test Remote Workspace FS Preview RPC over Client -> Server -> Runner -> Server -> Client!
	wsReqEnv, _ := protocol.NewEnvelope(protocol.TypeWorkspaceReq, map[string]any{
		"action":   "fs_read_preview",
		"rel_path": "hello.go",
	})
	wsReqEnv.RunnerID = "run_wired_1"
	reqBytes, _ := json.Marshal(wsReqEnv)
	_ = cliWS.Write(ctx, websocket.MessageText, reqBytes)

	var gotPreview bool
	for !gotPreview {
		_, raw, err := cliWS.Read(ctx)
		if err != nil {
			t.Fatal(err)
		}
		var env protocol.Envelope
		_ = json.Unmarshal(raw, &env)
		if env.ReplyTo == wsReqEnv.ID && env.Type == protocol.TypeWorkspaceResp {
			if !strings.Contains(string(env.Payload), `"language":"go"`) {
				t.Fatalf("unexpected workspace preview payload: %s", string(env.Payload))
			}
			gotPreview = true
		}
	}

	// 2. Test Remote Binary PTY fast-path (Client <-> Server <-> Runner PTY)
	ptyOpenEnv, _ := protocol.NewEnvelope(protocol.TypePTYOpen, map[string]any{
		"shell": "/bin/sh", "cols": 80, "rows": 24,
	})
	ptyOpenEnv.RunnerID = "run_wired_1"
	poBytes, _ := json.Marshal(ptyOpenEnv)
	_ = cliWS.Write(ctx, websocket.MessageText, poBytes)

	var ptyID string
	for ptyID == "" {
		msgType, raw, err := cliWS.Read(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if msgType != websocket.MessageText {
			continue
		}
		var env protocol.Envelope
		_ = json.Unmarshal(raw, &env)
		if env.Type == protocol.TypePTYOpened {
			var p struct {
				PTYID string `json:"pty_id"`
			}
			_ = json.Unmarshal(env.Payload, &p)
			ptyID = p.PTYID
		}
	}

	// Send binary Stdin frame -> receive binary Stdout frame!
	stdinFrame := protocol.EncodePTYBinaryFrame(protocol.PTYBinaryStdin, ptyID, []byte("echo BINARY_E2E_OK\n"))
	if err := cliWS.Write(ctx, websocket.MessageBinary, stdinFrame); err != nil {
		t.Fatal(err)
	}

	var ptyOut bytes.Buffer
	deadline := time.Now().Add(4 * time.Second)
	for time.Now().Before(deadline) && !strings.Contains(ptyOut.String(), "BINARY_E2E_OK") {
		msgType, raw, err := cliWS.Read(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if msgType == websocket.MessageBinary {
			op, id, chunk, decErr := protocol.DecodePTYBinaryFrame(raw)
			if decErr == nil && op == protocol.PTYBinaryStdout && id == ptyID {
				ptyOut.Write(chunk)
			}
		}
	}
	if !strings.Contains(ptyOut.String(), "BINARY_E2E_OK") {
		t.Fatalf("did not receive binary PTY stdout over E2E bridge: %q", ptyOut.String())
	}

	// 3. Test Server-hosted Remote MCP call from Runner wiring
	mcpRes, err := wiring.CallRemoteMCP(ctx, "github", "ping", json.RawMessage(`{}`))
	if err != nil || !strings.Contains(string(mcpRes), "github.ping_ok") {
		t.Fatalf("CallRemoteMCP failed: %s err=%v", string(mcpRes), err)
	}
}
