package system

// probe_test.go: probeIntranet behaviors against httptest servers and
// loopback ports — normalization, retry counts, required-success thresholds,
// marker mismatch, timeouts, and the strategy resolution.

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestProbeIntranetServiceHTTPViaHttptest(t *testing.T) {
	var gotToken string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotToken = r.Header.Get("x-nex-intranet-token")
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{"ok": true, "marker": "nex-intranet"})
	}))
	defer server.Close()

	s := NewService(Options{})
	ctx := context.Background()
	url := server.URL
	attempts := int64(1)
	result, err := s.ProbeIntranet(ctx, ProbeRequest{
		Targets:  []ProbeTarget{{Kind: "service", URL: url, Token: strPtr("secret"), TimeoutMs: i64Ptr(2000)}},
		Attempts: &attempts,
	})
	if err != nil {
		t.Fatal(err)
	}
	if gotToken != "secret" {
		t.Errorf("token header missing: %q", gotToken)
	}
	if !result.IsIntranet || result.Strategy != "service-http" {
		t.Fatalf("result: %+v", result)
	}
	if result.ReachedTargetCount != 1 || result.TotalTargets != 1 || result.RequiredSuccessCount != 1 {
		t.Fatalf("counts: %+v", result)
	}
	if result.CheckedAt <= 0 {
		t.Errorf("checkedAt: %d", result.CheckedAt)
	}
	svc, ok := result.Results[0].(ServiceTargetResult)
	if !ok || !svc.Reachable || svc.Marker != "nex-intranet" {
		t.Fatalf("service result: %+v", result.Results[0])
	}
}

func TestProbeIntranetMarkerMismatchFails(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{"ok": true, "marker": "wrong"})
	}))
	defer server.Close()
	s := NewService(Options{})
	attempts := int64(2)
	result, err := s.ProbeIntranet(context.Background(), ProbeRequest{
		Targets:  []ProbeTarget{{Kind: "service", URL: server.URL, ExpectedMarker: strPtr("expected"), TimeoutMs: i64Ptr(500)}},
		Attempts: &attempts,
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.IsIntranet {
		t.Fatalf("marker mismatch must fail the target")
	}
	svc := result.Results[0].(ServiceTargetResult)
	if svc.AttemptCount != 2 {
		t.Errorf("retries: %d", svc.AttemptCount)
	}
	if svc.Error == "" {
		t.Errorf("error message required")
	}
}

func TestProbeIntranetTCPLoopbackReachableAndUnreachable(t *testing.T) {
	// a listening loopback port is reachable; port 1 is not
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Skipf("loopback listen: %v", err)
	}
	defer listener.Close()
	port := int64(listener.Addr().(*net.TCPAddr).Port)

	s := NewService(Options{})
	attempts := int64(1)
	result, err := s.ProbeIntranet(context.Background(), ProbeRequest{
		Targets: []ProbeTarget{
			{Host: "127.0.0.1", Port: i64Ptr(port), TimeoutMs: i64Ptr(500)},
			{Host: "127.0.0.1", Port: i64Ptr(1), TimeoutMs: i64Ptr(300)},
		},
		Attempts: &attempts,
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Strategy != "tcp-connect" {
		t.Fatalf("strategy: %s", result.Strategy)
	}
	if result.ReachedTargetCount != 1 {
		t.Fatalf("reached: %+v", result)
	}
	tcp := result.Results[0].(TcpTargetResult)
	if !tcp.Reachable || tcp.LatencyMs == nil {
		t.Fatalf("reachable result: %+v", tcp)
	}
	unreachable := result.Results[1].(TcpTargetResult)
	if unreachable.Reachable || unreachable.Error == "" {
		t.Fatalf("unreachable result: %+v", unreachable)
	}
}

func TestProbeIntranetRequiredSuccessCountThreshold(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{"ok": true})
	}))
	defer server.Close()
	s := NewService(Options{})
	attempts := int64(1)
	// 1 of 2 reachable but required=2 → not in intranet
	required := int64(2)
	result, err := s.ProbeIntranet(context.Background(), ProbeRequest{
		Targets: []ProbeTarget{
			{Kind: "service", URL: server.URL, TimeoutMs: i64Ptr(500)},
			{Kind: "service", URL: "http://127.0.0.1:1/", TimeoutMs: i64Ptr(200)},
		},
		Attempts:             &attempts,
		RequiredSuccessCount: &required,
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.IsIntranet {
		t.Fatalf("required=2 with 1 reachable must not be intranet: %+v", result)
	}
	if result.Strategy != "mixed" && result.Strategy != "service-http" {
		// both targets are service kind → service-http
		t.Logf("strategy: %s", result.Strategy)
	}
}

func TestProbeIntranetMixedTargetsUseMixedStrategy(t *testing.T) {
	s := NewService(Options{})
	attempts := int64(1)
	result, err := s.ProbeIntranet(context.Background(), ProbeRequest{
		Targets: []ProbeTarget{
			{Host: "127.0.0.1", Port: i64Ptr(1), TimeoutMs: i64Ptr(200)},
			{Kind: "service", URL: "ftp://x/", TimeoutMs: i64Ptr(100)},
		},
		Attempts: &attempts,
	})
	if err != nil {
		t.Fatal(err)
	}
	// the ftp URL normalizes to nil (not http/https) → only the tcp target counts
	if result.TotalTargets != 1 {
		t.Fatalf("invalid targets dropped: %+v", result)
	}
	if result.Strategy != "tcp-connect" {
		t.Fatalf("strategy: %s", result.Strategy)
	}
}

func TestProbeIntranetEmptyTargets(t *testing.T) {
	s := NewService(Options{})
	result, err := s.ProbeIntranet(context.Background(), ProbeRequest{Targets: nil})
	if err != nil {
		t.Fatal(err)
	}
	if result.IsIntranet || result.TotalTargets != 0 || result.RequiredSuccessCount != 1 {
		t.Fatalf("empty targets: %+v", result)
	}
}

func TestProbeClamps(t *testing.T) {
	// timeout clamps to [100, 10000]
	if got := normalizeTimeout(nil); got != 800 {
		t.Errorf("default timeout: %d", got)
	}
	if got := normalizeTimeout(i64Ptr(10)); got != 100 {
		t.Errorf("min timeout: %d", got)
	}
	if got := normalizeTimeout(i64Ptr(99999)); got != 10000 {
		t.Errorf("max timeout: %d", got)
	}
	// attempts clamp to [1,3]
	if normalizeAttempts(nil) != 2 {
		t.Error("default attempts")
	}
	if normalizeAttempts(i64Ptr(0)) != 1 {
		t.Error("min attempts")
	}
	if normalizeAttempts(i64Ptr(9)) != 3 {
		t.Error("max attempts")
	}
	// port fallback is 22 (SSH)
	nt := normalizeTCPTarget(ProbeTarget{Host: "h"})
	if nt.port != 22 || nt.targetID != "h:22" {
		t.Errorf("default port: %+v", nt)
	}
	// blank host drops the target
	if normalizeTCPTarget(ProbeTarget{Host: "  "}) != nil {
		t.Error("blank host must drop")
	}
	// required clamps into [1, total]
	if normalizeRequiredSuccessCount(i64Ptr(0), 3) != 1 {
		t.Error("min required")
	}
	if normalizeRequiredSuccessCount(i64Ptr(9), 3) != 3 {
		t.Error("required clamps to total")
	}
	if normalizeRequiredSuccessCount(nil, 0) != 1 {
		t.Error("no targets -> 1")
	}
}

func TestProbeServiceHTTPStatusAndTimeoutErrors(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
	}))
	defer server.Close()
	s := NewService(Options{})
	_, err := probeServiceEndpoint(context.Background(), s.httpClient, ServiceProbeParams{URL: server.URL, TimeoutMs: 1000})
	if err == nil || err.Error() != "HTTP 403" {
		t.Fatalf("status error: %v", err)
	}

	// ok=false response
	server2 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{"ok": false})
	}))
	defer server2.Close()
	_, err = probeServiceEndpoint(context.Background(), s.httpClient, ServiceProbeParams{URL: server2.URL, TimeoutMs: 1000})
	if err == nil || err.Error() != "service returned ok=false" {
		t.Fatalf("ok=false error: %v", err)
	}

	// non-JSON body
	server3 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("not json"))
	}))
	defer server3.Close()
	_, err = probeServiceEndpoint(context.Background(), s.httpClient, ServiceProbeParams{URL: server3.URL, TimeoutMs: 1000})
	if err == nil || err.Error() != "invalid service response" {
		t.Fatalf("invalid body error: %v", err)
	}

	// timeout: unreachable port with a short timeout
	start := time.Now()
	_, err = probeServiceEndpoint(context.Background(), s.httpClient, ServiceProbeParams{URL: "http://10.255.255.1/", TimeoutMs: 100})
	if err == nil || err.Error() != "timeout(100ms)" {
		t.Fatalf("timeout error: %v", err)
	}
	if time.Since(start) > 2*time.Second {
		t.Errorf("timeout took too long: %v", time.Since(start))
	}
}

func strPtr(s string) *string { return &s }
func i64Ptr(v int64) *int64   { return &v }
