package pty

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nex-harness/nex/pkg/protocol"
)

func TestRingBufferWrapAround(t *testing.T) {
	rb := NewRingBuffer(8)
	_, _ = rb.Write([]byte("12345"))
	if string(rb.Snapshot()) != "12345" {
		t.Fatalf("expected 12345, got %q", string(rb.Snapshot()))
	}
	_, _ = rb.Write([]byte("67890"))
	if string(rb.Snapshot()) != "34567890" {
		t.Fatalf("expected 34567890 after wrap, got %q", string(rb.Snapshot()))
	}
}

func TestPTYManagerOpenIOAndReconnectReplay(t *testing.T) {
	ctx := context.Background()
	mgr := NewManager(t.TempDir())
	defer mgr.CloseAll()

	var mu sync.Mutex
	var outBuf bytes.Buffer

	openEnv, _ := protocol.NewEnvelope(protocol.TypePTYOpen, map[string]any{
		"shell": "/bin/sh",
		"cols":  80,
		"rows":  24,
	})
	resp, err := mgr.HandleControl(ctx, openEnv, func(frame []byte) {
		op, _, chunk, decErr := protocol.DecodePTYBinaryFrame(frame)
		if decErr == nil && op == protocol.PTYBinaryStdout {
			mu.Lock()
			outBuf.Write(chunk)
			mu.Unlock()
		}
	}, nil)
	if err != nil {
		t.Fatalf("PTY open failed: %v", err)
	}

	var opened struct {
		PTYID string `json:"pty_id"`
	}
	_ = json.Unmarshal(resp.Payload, &opened)
	if opened.PTYID == "" {
		t.Fatalf("empty pty_id")
	}

	if err := mgr.WriteStdin(opened.PTYID, []byte("echo GHOSTTY_PTY_OK\n")); err != nil {
		t.Fatalf("WriteStdin: %v", err)
	}

	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		mu.Lock()
		got := outBuf.String()
		mu.Unlock()
		if strings.Contains(got, "GHOSTTY_PTY_OK") {
			break
		}
		time.Sleep(25 * time.Millisecond)
	}

	mu.Lock()
	gotOut := outBuf.String()
	mu.Unlock()
	if !strings.Contains(gotOut, "GHOSTTY_PTY_OK") {
		t.Fatalf("expected GHOSTTY_PTY_OK in PTY binary output, got %q", gotOut)
	}

	// Resize & Re-attach (verifying RingBuffer history replay!)
	resizeEnv, _ := protocol.NewEnvelope(protocol.TypePTYResize, map[string]any{
		"pty_id": opened.PTYID, "cols": 120, "rows": 40,
	})
	if _, err := mgr.HandleControl(ctx, resizeEnv, nil, nil); err != nil {
		t.Fatalf("PTY resize failed: %v", err)
	}

	reattachEnv, _ := protocol.NewEnvelope(protocol.TypePTYOpen, map[string]any{
		"pty_id": opened.PTYID, "cols": 120, "rows": 40,
	})
	reResp, err := mgr.HandleControl(ctx, reattachEnv, nil, nil)
	if err != nil {
		t.Fatalf("PTY reattach failed: %v", err)
	}
	var reOpened struct {
		History []byte `json:"history"`
	}
	_ = json.Unmarshal(reResp.Payload, &reOpened)
	if !strings.Contains(string(reOpened.History), "GHOSTTY_PTY_OK") {
		t.Fatalf("expected RingBuffer replay on reattach, got %q", string(reOpened.History))
	}

	closeEnv, _ := protocol.NewEnvelope(protocol.TypePTYClose, map[string]any{"pty_id": opened.PTYID})
	_, _ = mgr.HandleControl(ctx, closeEnv, nil, nil)
}
