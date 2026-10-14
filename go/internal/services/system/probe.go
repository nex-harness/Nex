package system

// probe.go holds the probeIntranet port of systemService.ts: target
// normalization with the attempt/timeout/required-count clamps, retry loops,
// strategy resolution, and the default tcp/service probe implementations.

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"time"

	"github.com/tylinux/nex/go/internal/adapters/probeio"
)

// ---------------------------------------------------------------------------
// probeIntranet
// ---------------------------------------------------------------------------

// normalizedTarget is NormalizedTarget.
type normalizedTarget struct {
	kind      string // "tcp" | "service"
	targetID  string
	host      string
	port      int
	url       string
	marker    string
	hasMarker bool
	token     string
	timeoutMs int64
}

// ProbeTarget is the JSON shape of IntranetProbeTarget (from the RPC params).
type ProbeTarget struct {
	ID             *string `json:"id"`
	Kind           string  `json:"kind"`
	Host           string  `json:"host"`
	Port           *int64  `json:"port"`
	URL            string  `json:"url"`
	ExpectedMarker *string `json:"expectedMarker"`
	Token          *string `json:"token"`
	TimeoutMs      *int64  `json:"timeoutMs"`
}

// ProbeRequest is IntranetProbeRequest.
type ProbeRequest struct {
	Targets              []ProbeTarget `json:"targets"`
	Attempts             *int64        `json:"attempts"`
	RequiredSuccessCount *int64        `json:"requiredSuccessCount"`
}

// TcpTargetResult is IntranetProbeTcpTargetResult.
type TcpTargetResult struct {
	TargetID     string `json:"targetId"`
	Kind         string `json:"kind"`
	Host         string `json:"host"`
	Port         int    `json:"port"`
	Reachable    bool   `json:"reachable"`
	AttemptCount int    `json:"attemptCount"`
	LatencyMs    *int64 `json:"latencyMs"`
	Error        string `json:"error,omitempty"`
}

// ServiceTargetResult is IntranetProbeServiceTargetResult.
type ServiceTargetResult struct {
	TargetID     string `json:"targetId"`
	Kind         string `json:"kind"`
	URL          string `json:"url"`
	Reachable    bool   `json:"reachable"`
	AttemptCount int    `json:"attemptCount"`
	LatencyMs    *int64 `json:"latencyMs"`
	Marker       string `json:"marker,omitempty"`
	Error        string `json:"error,omitempty"`
}

// ProbeResult is IntranetProbeResult.
type ProbeResult struct {
	IsIntranet           bool   `json:"isIntranet"`
	ReachedTargetCount   int    `json:"reachedTargetCount"`
	RequiredSuccessCount int    `json:"requiredSuccessCount"`
	TotalTargets         int    `json:"totalTargets"`
	CheckedAt            int64  `json:"checkedAt"`
	Strategy             string `json:"strategy"`
	Results              []any  `json:"results"`
}

func normalizeAttempts(attempts *int64) int {
	if attempts == nil {
		return defaultProbeAttempts
	}
	a := int(*attempts)
	if a < 1 {
		a = 1
	}
	if a > maxProbeAttempts {
		a = maxProbeAttempts
	}
	return a
}

func normalizeRequiredSuccessCount(required *int64, totalTargets int) int {
	if totalTargets <= 0 {
		return 1
	}
	if required == nil {
		return 1
	}
	r := int(*required)
	if r < 1 {
		r = 1
	}
	if r > totalTargets {
		r = totalTargets
	}
	return r
}

func normalizeTimeout(timeoutMs *int64) int64 {
	if timeoutMs == nil {
		return defaultProbeTimeoutMs
	}
	t := *timeoutMs
	if t < 100 {
		t = 100
	}
	if t > 10_000 {
		t = 10_000
	}
	return t
}

// normalizeTCPTarget ports normalizeTcpTarget; nil when the host is blank.
func normalizeTCPTarget(t ProbeTarget) *normalizedTarget {
	host := trimSpace(t.Host)
	if host == "" {
		return nil
	}
	port := defaultProbePort
	if t.Port != nil && *t.Port >= 1 && *t.Port <= 65535 {
		port = int(*t.Port)
	}
	id := fmt.Sprintf("%s:%d", host, port)
	if t.ID != nil {
		if trimmed := trimSpace(*t.ID); trimmed != "" {
			id = trimmed
		}
	}
	return &normalizedTarget{kind: "tcp", targetID: id, host: host, port: port, timeoutMs: normalizeTimeout(t.TimeoutMs)}
}

// normalizeServiceTarget ports normalizeServiceTarget; nil when the URL is
// blank or not http(s).
func normalizeServiceTarget(t ProbeTarget) *normalizedTarget {
	urlText := trimSpace(t.URL)
	if urlText == "" {
		return nil
	}
	parsed, err := parseURL(urlText)
	if err != nil {
		return nil
	}
	if parsed.scheme != "http" && parsed.scheme != "https" {
		return nil
	}
	target := &normalizedTarget{
		kind:      "service",
		targetID:  parsed.normalized,
		url:       parsed.normalized,
		timeoutMs: normalizeTimeout(t.TimeoutMs),
	}
	if t.ID != nil {
		if trimmed := trimSpace(*t.ID); trimmed != "" {
			target.targetID = trimmed
		}
	}
	if t.ExpectedMarker != nil {
		if trimmed := trimSpace(*t.ExpectedMarker); trimmed != "" {
			target.marker = trimmed
			target.hasMarker = true
		}
	}
	if t.Token != nil {
		if trimmed := trimSpace(*t.Token); trimmed != "" {
			target.token = trimmed
		}
	}
	return target
}

func normalizeTarget(t ProbeTarget) *normalizedTarget {
	if t.Kind == "service" {
		return normalizeServiceTarget(t)
	}
	return normalizeTCPTarget(t)
}

// ProbeIntranet ports probeIntranet: all targets probed concurrently (here:
// in parallel goroutines joined before returning, preserving input order).
func (s *Service) ProbeIntranet(ctx context.Context, request ProbeRequest) (ProbeResult, error) {
	targets := make([]*normalizedTarget, 0, len(request.Targets))
	for _, t := range request.Targets {
		if nt := normalizeTarget(t); nt != nil {
			targets = append(targets, nt)
		}
	}
	attempts := normalizeAttempts(request.Attempts)
	required := normalizeRequiredSuccessCount(request.RequiredSuccessCount, len(targets))

	results := make([]any, len(targets))
	var wg sync.WaitGroup
	for i, t := range targets {
		wg.Add(1)
		go func(i int, t *normalizedTarget) {
			defer wg.Done()
			if t.kind == "service" {
				results[i] = s.runServiceProbeWithRetry(ctx, t, attempts)
			} else {
				results[i] = s.runTCPProbeWithRetry(ctx, t, attempts)
			}
		}(i, t)
	}
	wg.Wait()

	reached := 0
	for _, r := range results {
		switch v := r.(type) {
		case TcpTargetResult:
			if v.Reachable {
				reached++
			}
		case ServiceTargetResult:
			if v.Reachable {
				reached++
			}
		}
	}
	return ProbeResult{
		IsIntranet:           len(targets) > 0 && reached >= required,
		ReachedTargetCount:   reached,
		RequiredSuccessCount: required,
		TotalTargets:         len(targets),
		CheckedAt:            s.now(),
		Strategy:             resolveProbeStrategy(targets),
		Results:              results,
	}, nil
}

func (s *Service) runTCPProbeWithRetry(ctx context.Context, t *normalizedTarget, attempts int) TcpTargetResult {
	lastErr := ""
	for attempt := 1; attempt <= attempts; attempt++ {
		latency, err := s.tcpProbe(ctx, TcpProbeParams{Host: t.host, Port: t.port, TimeoutMs: t.timeoutMs})
		if err == nil {
			return TcpTargetResult{
				TargetID: t.targetID, Kind: "tcp", Host: t.host, Port: t.port,
				Reachable: true, AttemptCount: attempt, LatencyMs: &latency,
			}
		}
		lastErr = err.Error()
	}
	return TcpTargetResult{
		TargetID: t.targetID, Kind: "tcp", Host: t.host, Port: t.port,
		Reachable: false, AttemptCount: attempts, LatencyMs: nil,
		Error: orDefault(lastErr, "probe failed"),
	}
}

func (s *Service) runServiceProbeWithRetry(ctx context.Context, t *normalizedTarget, attempts int) ServiceTargetResult {
	lastErr := ""
	for attempt := 1; attempt <= attempts; attempt++ {
		result, err := s.serviceProbe(ctx, ServiceProbeParams{URL: t.url, TimeoutMs: t.timeoutMs, Token: t.token})
		if err == nil {
			if t.hasMarker && result.Marker != t.marker {
				lastErr = fmt.Sprintf("marker mismatch(expected=%s, actual=%s)", t.marker, orDefault(result.Marker, "<empty>"))
				continue
			}
			return ServiceTargetResult{
				TargetID: t.targetID, Kind: "service", URL: t.url,
				Reachable: true, AttemptCount: attempt, LatencyMs: &result.LatencyMs,
				Marker: result.Marker,
			}
		}
		lastErr = err.Error()
	}
	return ServiceTargetResult{
		TargetID: t.targetID, Kind: "service", URL: t.url,
		Reachable: false, AttemptCount: attempts, LatencyMs: nil,
		Error: orDefault(lastErr, "probe failed"),
	}
}

func resolveProbeStrategy(targets []*normalizedTarget) string {
	// Array.prototype.every on an empty array is true: empty targets resolve
	// to "tcp-connect" (the first every() check wins).
	if len(targets) == 0 {
		return "tcp-connect"
	}
	allTCP, allService := true, true
	for _, t := range targets {
		if t.kind != "tcp" {
			allTCP = false
		}
		if t.kind != "service" {
			allService = false
		}
	}
	switch {
	case allTCP:
		return "tcp-connect"
	case allService:
		return "service-http"
	default:
		return "mixed"
	}
}

// probeTCPPort ports probeTcpPort — the dial itself lives in the probeio
// adapter (net is not importable from this package).
func probeTCPPort(ctx context.Context, p TcpProbeParams) (int64, error) {
	return probeio.DialTCP(ctx, p.Host, int64(p.Port), p.TimeoutMs)
}

// probeServiceEndpoint ports probeServiceEndpoint: GET with the
// x-nex-intranet-token header, JSON body {ok, marker?}; timeout(…) errors.
// The client is the injected HTTPDoer (internal/adapters/probehttp by default).
func probeServiceEndpoint(ctx context.Context, client probeio.HTTPDoer, p ServiceProbeParams) (ServiceProbeResult, error) {
	started := time.Now()
	ctx, cancel := context.WithTimeout(ctx, time.Duration(p.TimeoutMs)*time.Millisecond)
	defer cancel()
	header := map[string]string{}
	if p.Token != "" {
		header["x-nex-intranet-token"] = p.Token
	}
	resp, err := client.Do(ctx, "GET", p.URL, header)
	if err != nil {
		if ctx.Err() != nil {
			return ServiceProbeResult{}, fmt.Errorf("timeout(%dms)", p.TimeoutMs)
		}
		return ServiceProbeResult{}, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return ServiceProbeResult{}, fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	var body struct {
		OK     bool   `json:"ok"`
		Marker string `json:"marker"`
	}
	if err := json.Unmarshal(resp.Body, &body); err != nil {
		return ServiceProbeResult{}, fmt.Errorf("invalid service response")
	}
	if !body.OK {
		return ServiceProbeResult{}, fmt.Errorf("service returned ok=false")
	}
	latency := time.Since(started).Milliseconds()
	if latency < 0 {
		latency = 0
	}
	return ServiceProbeResult{LatencyMs: latency, Marker: body.Marker, HasMarker: body.Marker != ""}, nil
}

func orDefault(s, def string) string {
	if s == "" {
		return def
	}
	return s
}

func trimSpace(s string) string {
	start, end := 0, len(s)
	for start < end && isSpace(s[start]) {
		start++
	}
	for end > start && isSpace(s[end-1]) {
		end--
	}
	return s[start:end]
}

func isSpace(b byte) bool {
	return b == ' ' || b == '\t' || b == '\n' || b == '\r' || b == '\v' || b == '\f'
}
