package modelclient

import (
	"path/filepath"
	"testing"
)

type retryVectors struct {
	Delays []struct {
		Attempt       int    `json:"attempt"`
		BaseDelayMs   int    `json:"baseDelayMs"`
		BackoffFactor int    `json:"backoffFactor"`
		MaxDelayMs    int    `json:"maxDelayMs"`
		Jitter        bool   `json:"jitter"`
		RetryAfterMs  *int64 `json:"retryAfterMs"`
		DelayMs       int64  `json:"delayMs"`
	} `json:"delays"`
	Options []struct {
		Options  *RetryOptions     `json:"options"`
		Env      map[string]string `json:"env"`
		Resolved struct {
			MaxAttempts   int     `json:"maxAttempts"`
			BaseDelayMs   int     `json:"baseDelayMs"`
			BackoffFactor float64 `json:"backoffFactor"`
			MaxDelayMs    int     `json:"maxDelayMs"`
			Jitter        bool    `json:"jitter"`
		} `json:"resolved"`
	} `json:"options"`
	Idle []struct {
		BaseTimeoutMs *int `json:"baseTimeoutMs"`
		RetryNumber   *int `json:"retryNumber"`
		Resolved      int  `json:"resolved"`
	} `json:"idle"`
}

func TestRetryDelayMatchesVectors(t *testing.T) {
	var v retryVectors
	readJSONFile(t, filepath.Join(vectorDir(t), "vectors", "model-retry.json"), &v)
	if len(v.Delays) == 0 {
		t.Fatal("no delay vectors")
	}
	for _, c := range v.Delays {
		got := CalculateRetryDelay(ResolvedRetryOptions{
			MaxAttempts:   11,
			BaseDelayMs:   c.BaseDelayMs,
			BackoffFactor: float64(c.BackoffFactor),
			MaxDelayMs:    c.MaxDelayMs,
			Jitter:        c.Jitter,
		}, c.Attempt, c.RetryAfterMs, nil)
		if got != c.DelayMs {
			t.Errorf("attempt %d base %d retryAfter %v: got %d want %d", c.Attempt, c.BaseDelayMs, derefInt64(c.RetryAfterMs), got, c.DelayMs)
		}
	}
}

func TestRetryOptionsMatchVectors(t *testing.T) {
	var v retryVectors
	readJSONFile(t, filepath.Join(vectorDir(t), "vectors", "model-retry.json"), &v)
	if len(v.Options) == 0 {
		t.Fatal("no option vectors")
	}
	for i, c := range v.Options {
		got := ResolveRetryOptions(c.Options, c.Env)
		if got.MaxAttempts != c.Resolved.MaxAttempts ||
			got.BaseDelayMs != c.Resolved.BaseDelayMs ||
			got.BackoffFactor != c.Resolved.BackoffFactor ||
			got.MaxDelayMs != c.Resolved.MaxDelayMs ||
			got.Jitter != c.Resolved.Jitter {
			t.Errorf("option vector %d: got %+v want %+v", i, got, c.Resolved)
		}
	}
}

func TestIdleTimeoutMatchesVectors(t *testing.T) {
	var v retryVectors
	readJSONFile(t, filepath.Join(vectorDir(t), "vectors", "model-retry.json"), &v)
	if len(v.Idle) == 0 {
		t.Fatal("no idle vectors")
	}
	for i, c := range v.Idle {
		got := ResolveModelStreamIdleTimeoutMs(c.BaseTimeoutMs, c.RetryNumber)
		if got != c.Resolved {
			t.Errorf("idle vector %d: got %d want %d", i, got, c.Resolved)
		}
	}
}

// TestRetryBudgetBoundaries pins the boundary behavior of the retry gates that
// the delay/option vectors do not reach.
func TestRetryBudgetBoundaries(t *testing.T) {
	// Default budget: allowed while retryBudgetAttempt < maxAttempts.
	if !RetryBudgetAllows(RetryBudgetDefault, 10, 11) {
		t.Fatal("attempt 10 of 11 should be allowed")
	}
	if RetryBudgetAllows(RetryBudgetDefault, 11, 11) {
		t.Fatal("attempt 11 of 11 should NOT be allowed")
	}
	// Unbounded is always allowed.
	if !RetryBudgetAllows(RetryBudgetUnbounded, 1000, 11) {
		t.Fatal("unbounded should always be allowed")
	}
	// Loop continues while attempt <= loopMaxAttempts.
	if !RetryAttemptLoopContinues(RetryBudgetDefault, 11, 11) {
		t.Fatal("loop should run for attempt == max")
	}
	if RetryAttemptLoopContinues(RetryBudgetDefault, 12, 11) {
		t.Fatal("loop should stop past max")
	}
	// The maxAttempts written to status events is the unbounded sentinel 0.
	if RetryBudgetMaxAttempts(RetryBudgetUnbounded, 11) != UnboundedRetryMaxAttempts {
		t.Fatal("unbounded should report sentinel 0")
	}
	if RetryBudgetMaxAttempts(RetryBudgetDefault, 11) != 11 {
		t.Fatal("default should report the real max")
	}
}

// TestCalculateRetryDelayJitterRange checks the jitter branch stays within
// [0.5, 1.0] * capped.
func TestCalculateRetryDelayJitterRange(t *testing.T) {
	opts := ResolvedRetryOptions{MaxAttempts: 11, BaseDelayMs: 2000, BackoffFactor: 2, MaxDelayMs: 60000, Jitter: true}
	for _, r := range []float64{0, 0.25, 0.5, 0.999} {
		got := CalculateRetryDelay(opts, 3, nil, func() float64 { return r })
		// attempt 3 -> uncapped 8000
		lo := int64(4000)
		hi := int64(8000)
		if got < lo || got > hi {
			t.Fatalf("rand %v: delay %d outside [%d,%d]", r, got, lo, hi)
		}
	}
}
