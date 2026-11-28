package server

import (
	"context"
	"fmt"
	"math"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/nex-harness/nex/pkg/protocol"
)

const (
	runnerLatencyProbeInterval = 25 * time.Second
	runnerLatencyProbeTimeout  = 5 * time.Second
	runnerSpeedtestBytes       = 256 * 1024 // 256 KiB per direction
)

// runRunnerLatencyProbe periodically sends WebSocket Ping frames to a connected runner
// and records the round-trip time (RTT) in runnerRuntimeMeta.
func (s *Server) runRunnerLatencyProbe(ctx context.Context, runnerID string, rc *runnerConn) {
	if rc == nil || rc.ws == nil {
		return
	}
	probe := func() {
		pCtx, cancel := context.WithTimeout(ctx, runnerLatencyProbeTimeout)
		defer cancel()
		start := time.Now()
		if err := rc.ws.Ping(pCtx); err != nil {
			return
		}
		rttMS := time.Since(start).Milliseconds()
		if rttMS < 0 {
			rttMS = 0
		}
		now := time.Now().UnixMilli()
		shouldBroadcast := false
		s.mu.Lock()
		if s.runners[runnerID] == rc {
			cur := s.runnerMeta[runnerID]
			if cur != nil {
				if cur.LatencyMS == nil {
					shouldBroadcast = true
				} else {
					prev := *cur.LatencyMS
					diff := rttMS - prev
					if diff < 0 {
						diff = -diff
					}
					if diff >= 15 || (prev > 0 && float64(diff)/float64(prev) >= 0.25) {
						shouldBroadcast = true
					}
				}
				v := rttMS
				cur.LatencyMS = &v
				cur.LatencyUpdatedAt = now
			}
		}
		s.mu.Unlock()
		if shouldBroadcast {
			s.broadcastRunnerStatus(context.Background(), runnerID, "online")
		}
	}

	// Quick initial probe shortly after connect so the UI shows RTT immediately.
	select {
	case <-ctx.Done():
		return
	case <-time.After(800 * time.Millisecond):
		probe()
	}

	ticker := time.NewTicker(runnerLatencyProbeInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			probe()
		}
	}
}

type runnerSpeedtestResponse struct {
	RunnerID              string   `json:"runner_id"`
	RTTMS                 int64    `json:"rtt_ms"`
	JitterMS              int64    `json:"jitter_ms"`
	DownloadMbps          *float64 `json:"download_mbps,omitempty"`
	UploadMbps            *float64 `json:"upload_mbps,omitempty"`
	PayloadBytes          int      `json:"payload_bytes"`
	ThroughputUnsupported bool     `json:"throughput_unsupported,omitempty"`
	MeasuredAt            int64    `json:"measured_at"`
}

func roundMbps(bytes int, d time.Duration) float64 {
	if d <= 0 || bytes <= 0 {
		return 0
	}
	mbps := (float64(bytes) * 8.0) / (d.Seconds() * 1_000_000.0)
	return math.Round(mbps*100) / 100
}

// handleRunnerSpeedtest handles POST /api/v1/runners/{id}/speedtest.
// It measures live WebSocket RTT/jitter plus on-demand download (server->runner)
// and upload (runner->server) throughput over the runner's active WebSocket link.
func (s *Server) handleRunnerSpeedtest(w http.ResponseWriter, r *http.Request, runnerID string) {
	if r.Method != http.MethodPost {
		writeErr(w, http.StatusMethodNotAllowed, fmt.Errorf("method not allowed"))
		return
	}
	s.mu.RLock()
	rc := s.runners[runnerID]
	s.mu.RUnlock()
	if rc == nil || rc.ws == nil {
		writeErr(w, http.StatusConflict, fmt.Errorf("runner %s is offline", runnerID))
		return
	}

	// 1. Measure 3 WebSocket Ping samples for RTT & jitter.
	samples := make([]int64, 0, 3)
	for i := 0; i < 3; i++ {
		pCtx, cancel := context.WithTimeout(r.Context(), runnerLatencyProbeTimeout)
		start := time.Now()
		err := rc.ws.Ping(pCtx)
		elapsed := time.Since(start).Milliseconds()
		cancel()
		if err != nil {
			writeErr(w, http.StatusBadGateway, fmt.Errorf("ping runner failed: %w", err))
			return
		}
		if elapsed < 0 {
			elapsed = 0
		}
		samples = append(samples, elapsed)
	}
	sort.Slice(samples, func(i, j int) bool { return samples[i] < samples[j] })
	medianRTT := samples[len(samples)/2]
	jitter := samples[len(samples)-1] - samples[0]

	now := time.Now().UnixMilli()
	s.mu.Lock()
	if cur := s.runnerMeta[runnerID]; cur != nil {
		v := medianRTT
		cur.LatencyMS = &v
		cur.LatencyUpdatedAt = now
	}
	s.mu.Unlock()
	s.broadcastRunnerStatus(r.Context(), runnerID, "online")

	resp := runnerSpeedtestResponse{
		RunnerID:     runnerID,
		RTTMS:        medianRTT,
		JitterMS:     jitter,
		PayloadBytes: runnerSpeedtestBytes,
		MeasuredAt:   now,
	}

	// 2. Measure download throughput (server -> runner).
	payload := strings.Repeat("N", runnerSpeedtestBytes)
	dlEnv, err := protocol.NewEnvelope(protocol.TypeRunnerSpeedtest, protocol.RunnerSpeedtestPayload{
		Direction: "download",
		Payload:   payload,
	})
	if err == nil {
		dlStart := time.Now()
		if _, rpcErr := s.callRunnerRPC(r.Context(), runnerID, dlEnv, 8*time.Second); rpcErr == nil {
			dlElapsed := time.Since(dlStart)
			mbps := roundMbps(runnerSpeedtestBytes, dlElapsed)
			resp.DownloadMbps = &mbps
		} else {
			// Older runners that do not implement runner.speedtest will time out.
			resp.ThroughputUnsupported = true
			writeJSON(w, http.StatusOK, resp)
			return
		}
	}

	// 3. Measure upload throughput (runner -> server).
	ulEnv, err := protocol.NewEnvelope(protocol.TypeRunnerSpeedtest, protocol.RunnerSpeedtestPayload{
		Direction:     "upload",
		ResponseBytes: runnerSpeedtestBytes,
	})
	if err == nil {
		ulStart := time.Now()
		if reply, rpcErr := s.callRunnerRPC(r.Context(), runnerID, ulEnv, 8*time.Second); rpcErr == nil {
			ulElapsed := time.Since(ulStart)
			size := len(reply.Payload)
			if size < runnerSpeedtestBytes {
				size = runnerSpeedtestBytes
			}
			mbps := roundMbps(size, ulElapsed)
			resp.UploadMbps = &mbps
		}
	}

	writeJSON(w, http.StatusOK, resp)
}
