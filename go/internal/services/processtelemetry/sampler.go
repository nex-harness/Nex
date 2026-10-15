// sampler.go ports createProcessResourceSampler: the whole-machine table read
// plus the CPU differential.
//
// The baseline map is the PID-reuse guard. A baseline is only reused when the
// command is unchanged, the cputime has not gone backwards, and the clock has
// moved forward; any of those failing re-establishes the baseline and reports
// 0% for that sample (there is no valid delta to report).
package processtelemetry

import (
	"context"
	"math"
	"sync"
	"time"
)

// TableReader reads the whole-machine process table once. Returning ok=false
// skips the round instead of erroring: a table read failure must not break the
// resource manager.
type TableReader func(ctx context.Context) (rows []ResourceRow, ok bool)

// LogicalCPUCount reports the machine's logical core count (>= 1).
type LogicalCPUCount func() int

// Baseline is one process's previous CPU reading.
type Baseline struct {
	CPUTimeMs int64
	AtMs      int64
	Command   string
}

// Sampler ports ProcessResourceSampler.
type Sampler struct {
	readTable TableReader
	now       func() int64
	cpuCount  LogicalCPUCount

	mu        sync.Mutex
	baselines map[int]Baseline
}

// SamplerOptions ports CreateProcessResourceSamplerOptions.
type SamplerOptions struct {
	ReadTable       TableReader
	Now             func() int64
	LogicalCPUCount LogicalCPUCount
}

// NewSampler builds the sampler. A nil clock reads time.Now's Unix millis; a
// nil core count falls back to runtime.NumCPU.
func NewSampler(options SamplerOptions) *Sampler {
	now := options.Now
	if now == nil {
		now = func() int64 { return time.Now().UnixMilli() }
	}
	cpuCount := options.LogicalCPUCount
	if cpuCount == nil {
		cpuCount = defaultLogicalCPUCount
	}
	return &Sampler{
		readTable: options.ReadTable, now: now, cpuCount: cpuCount,
		baselines: map[int]Baseline{},
	}
}

// Sample reads the table once and returns the machine-normalized CPU percent
// per PID. ok=false means the round was skipped (table read failed or the
// context ended), which the caller must not turn into a stale-but-present
// snapshot.
func (s *Sampler) Sample(ctx context.Context) (samples map[int]Sample, ok bool) {
	if s.readTable == nil {
		return nil, false
	}
	rows, readOK := s.readTable(ctx)
	// A late read after the window closed must not shift the next window's
	// baseline (the TS AbortSignal check).
	if ctx.Err() != nil {
		return nil, false
	}
	if !readOK {
		return nil, false
	}
	at := s.now()
	s.mu.Lock()
	defer s.mu.Unlock()
	samples = make(map[int]Sample, len(rows))
	for _, row := range rows {
		baseline, hasBaseline := s.baselines[row.PID]
		cpuPercent := 0.0
		if hasBaseline && baseline.Command == row.Command &&
			row.CPUTimeMs >= baseline.CPUTimeMs && at > baseline.AtMs {
			elapsedMs := at - baseline.AtMs
			cpuPercent = ((float64(row.CPUTimeMs-baseline.CPUTimeMs) / float64(elapsedMs)) * 100) /
				float64(max(1, s.cpuCount()))
		}
		s.baselines[row.PID] = Baseline{CPUTimeMs: row.CPUTimeMs, AtMs: at, Command: row.Command}
		samples[row.PID] = Sample{
			PID: row.PID, PPID: row.PPID, RSSKb: row.RSSKb,
			CPUPercent: clampPercent(cpuPercent), Command: row.Command,
		}
	}
	// Baselines for processes no longer in the table expire; until then they
	// must survive so a briefly unreadable /proc entry does not reset the
	// delta to 0.
	for pid, baseline := range s.baselines {
		if _, stillPresent := samples[pid]; !stillPresent && at-baseline.AtMs > CPUBaselineTTLMs {
			delete(s.baselines, pid)
		}
	}
	return samples, true
}

// BaselineCount reports how many baselines are held (leak assertion in tests).
func (s *Sampler) BaselineCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.baselines)
}

// SeedBaseline installs a baseline directly (tests only).
func (s *Sampler) SeedBaseline(pid int, baseline Baseline) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.baselines[pid] = baseline
}

// clampPercent ports Math.max(0, Math.min(100, roundPercent(v))).
func clampPercent(value float64) float64 {
	return math.Max(0, math.Min(100, roundPercent(value)))
}

// roundPercentTenths rounds to one decimal place, half away from zero the same
// way JS's Math.round(x*10)/10 does for the positive values this path produces.
func roundPercentTenths(value float64) float64 {
	scaled := value * 10
	if scaled >= 0 {
		return math.Floor(scaled+0.5) / 10
	}
	return math.Ceil(scaled-0.5) / 10
}
