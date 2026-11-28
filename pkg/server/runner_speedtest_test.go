package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/nex-harness/nex/pkg/protocol"
)

func TestRunnerLatencyAndSpeedtestEndpoint(t *testing.T) {
	srv, _, _ := newTestServer(t, nil)
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	wsURL := "ws" + strings.TrimPrefix(ts.URL, "http") + "/ws/runner"
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	ws, _, err := websocket.Dial(ctx, wsURL, nil)
	if err != nil {
		t.Fatalf("dial runner ws: %v", err)
	}
	defer ws.CloseNow()
	ws.SetReadLimit(16 * 1024 * 1024)

	hello, _ := protocol.NewEnvelope(protocol.TypeRunnerHello, protocol.RunnerHelloPayload{
		RunnerID: "run_speed_test",
		Name:     "SpeedNode",
		OS:       "linux",
		Arch:     "amd64",
		Version:  "v1.0.0",
	})
	b, _ := json.Marshal(hello)
	if err := ws.Write(ctx, websocket.MessageText, b); err != nil {
		t.Fatalf("write hello: %v", err)
	}

	// Background mock runner loop: respond to runner.speedtest frames.
	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			_, data, err := ws.Read(ctx)
			if err != nil {
				return
			}
			var env protocol.Envelope
			if json.Unmarshal(data, &env) != nil {
				continue
			}
			if env.Type == protocol.TypeRunnerSpeedtest {
				var req protocol.RunnerSpeedtestPayload
				_ = json.Unmarshal(env.Payload, &req)
				res := protocol.RunnerSpeedtestResultPayload{
					Direction:     req.Direction,
					ReceivedBytes: len(req.Payload),
				}
				if req.Direction == "upload" && req.ResponseBytes > 0 {
					res.Payload = strings.Repeat("N", req.ResponseBytes)
				}
				reply, _ := protocol.NewEnvelope(protocol.TypeRunnerSpeedtestResult, res)
				reply.ReplyTo = env.ID
				rb, _ := json.Marshal(reply)
				_ = ws.Write(ctx, websocket.MessageText, rb)
			}
		}
	}()

	// Wait briefly for server to register runner.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		srv.mu.RLock()
		_, ok := srv.runners["run_speed_test"]
		srv.mu.RUnlock()
		if ok {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}

	// Trigger POST /api/v1/runners/run_speed_test/speedtest.
	resp, err := http.Post(ts.URL+"/api/v1/runners/run_speed_test/speedtest", "application/json", nil)
	if err != nil {
		t.Fatalf("speedtest request: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	var out runnerSpeedtestResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatalf("decode speedtest response: %v", err)
	}
	if out.RunnerID != "run_speed_test" {
		t.Fatalf("unexpected runner_id: %s", out.RunnerID)
	}
	if out.DownloadMbps == nil || *out.DownloadMbps <= 0 {
		t.Fatalf("expected positive download_mbps, got %v", out.DownloadMbps)
	}
	if out.UploadMbps == nil || *out.UploadMbps <= 0 {
		t.Fatalf("expected positive upload_mbps, got %v", out.UploadMbps)
	}

	// Verify GET /api/v1/runners includes latency_ms now.
	listResp, err := http.Get(ts.URL + "/api/v1/runners")
	if err != nil {
		t.Fatalf("list runners: %v", err)
	}
	defer listResp.Body.Close()
	var list []struct {
		ID        string `json:"id"`
		LatencyMS *int64 `json:"latency_ms"`
	}
	if err := json.NewDecoder(listResp.Body).Decode(&list); err != nil {
		t.Fatalf("decode runners: %v", err)
	}
	found := false
	for _, r := range list {
		if r.ID == "run_speed_test" {
			found = true
			if r.LatencyMS == nil {
				t.Fatalf("expected latency_ms to be populated on online runner")
			}
		}
	}
	if !found {
		t.Fatalf("runner run_speed_test not found in list")
	}

	cancel()
	<-done
}
