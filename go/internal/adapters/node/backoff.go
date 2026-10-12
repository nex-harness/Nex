package node

// backoff.go is the crash budget: CrashBudget in
// packages/nex-server-cli/src/supervisor/crashBudget.ts, with its two constants
// copied from packages/nex-server-cli/src/contracts.ts.
//
// The policy is small but the constants are not negotiable: they are the same
// numbers the TypeScript supervisor (and the service managers that wrap it) are
// timed against. A restart storm must look identical whether the Go front or the
// Node CLI owns the process, so CRASH_WINDOW_MS and CRASH_BACKOFF_MS are ported
// verbatim rather than tuned, and RecordCrash is a line-by-line translation of
// the TypeScript method including its off-by-one behaviour at the end of the
// table (see recordCrashLocked below).

import (
	"sync"
	"time"
)

const (
	// CrashWindowMs is CRASH_WINDOW_MS: crashes further apart than this start a new
	// window. 5 minutes is the operator-visible "is this flapping or was that a
	// one-off" boundary.
	CrashWindowMs = 5 * 60_000
)

// CrashBackoffMs is CRASH_BACKOFF_MS, in order. It is a var (not a const) because an
// array is not a constant expression; the values are fixed.
var CrashBackoffMs = [...]int{1_000, 2_000, 4_000, 8_000, 16_000}

// Decision is the recordCrash() result: whether to restart at all, how long to
// wait first, and whether the budget is exhausted.
type Decision struct {
	// ShouldRestart is false once the table has been walked off the end. The
	// supervisor then parks in crash-loop-stopped until a human intervenes.
	ShouldRestart bool
	// DelayMs is the backoff before the restart. It is meaningless when
	// ShouldRestart is false (the TypeScript returns the clamped last entry there).
	DelayMs int
	// Exhausted mirrors the TypeScript field of the same name.
	Exhausted bool
}

// BudgetSnapshot is CrashBudgetSnapshot (z.infer of crashBudgetSchema).
type BudgetSnapshot struct {
	// WindowStartedAt is when the current window opened, in Unix milliseconds.
	// HasWindowStartedAt is false before the first crash.
	WindowStartedAt    int64
	HasWindowStartedAt bool
	CrashCount         int
	NextRestartDelayMs int
	Exhausted          bool
}

// CrashBudget is the port of CrashBudget. It is safe for concurrent use: the
// supervisor records crashes from the process-exit goroutine while the status
// path snapshots them.
type CrashBudget struct {
	mu             sync.Mutex
	windowStarted  time.Time
	windowHasStart bool
	crashCount     int
	now            func() time.Time
}

// NewCrashBudget returns a budget reading the clock from now. A nil now uses
// time.Now, mirroring `this.options.now?.() ?? Date.now()`.
func NewCrashBudget(now func() time.Time) *CrashBudget {
	if now == nil {
		now = time.Now
	}
	return &CrashBudget{now: now}
}

// RecordCrash accounts for one crash and returns the restart decision.
//
// The translation of crashBudget.ts recordCrash:
//
//	if (windowStartedAt === undefined || now - windowStartedAt >= CRASH_WINDOW_MS) {
//	  windowStartedAt = now;
//	  crashCount = 0;
//	}
//	const index = crashCount;
//	crashCount += 1;
//	const delayMs = CRASH_BACKOFF_MS[Math.min(index, len - 1)] ?? 0;
//	const exhausted = index >= CRASH_BACKOFF_MS.length;
//	return { shouldRestart: !exhausted, delayMs, exhausted };
//
// Note the consequence of `exhausted = index >= length`: with a 5-entry table the
// first FIVE crashes restart (index 0..4, delays 1s..16s) and the SIXTH (index 5)
// stops. That is the semantics to keep, not "restart len-1 times".
func (b *CrashBudget) RecordCrash() Decision {
	b.mu.Lock()
	defer b.mu.Unlock()
	now := b.now()
	if !b.windowHasStart || now.Sub(b.windowStarted) >= time.Duration(CrashWindowMs)*time.Millisecond {
		b.windowStarted = now
		b.windowHasStart = true
		b.crashCount = 0
	}
	index := b.crashCount
	b.crashCount++
	last := len(CrashBackoffMs) - 1
	clamped := index
	if clamped > last {
		clamped = last
	}
	delay := 0
	if clamped >= 0 {
		delay = CrashBackoffMs[clamped]
	}
	exhausted := index >= len(CrashBackoffMs)
	return Decision{ShouldRestart: !exhausted, DelayMs: delay, Exhausted: exhausted}
}

// Reset clears the window and the count. supervisor.ts exposes it for tests and
// for a caller that decides the process is healthy again.
func (b *CrashBudget) Reset() {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.resetLocked()
}

func (b *CrashBudget) resetLocked() {
	b.windowHasStart = false
	b.windowStarted = time.Time{}
	b.crashCount = 0
}

// Snapshot is CrashBudgetSnapshot. It is the same computation as the TypeScript:
// an elapsed window resets the budget BEFORE the fields are read, so a caller
// polling the status after five idle minutes sees a fresh budget, not a stale one.
func (b *CrashBudget) Snapshot() BudgetSnapshot {
	b.mu.Lock()
	defer b.mu.Unlock()
	now := b.now()
	if b.windowHasStart && now.Sub(b.windowStarted) >= time.Duration(CrashWindowMs)*time.Millisecond {
		b.resetLocked()
	}
	next := 0
	if b.crashCount < len(CrashBackoffMs) {
		next = CrashBackoffMs[b.crashCount]
	}
	return BudgetSnapshot{
		WindowStartedAt:    unixMillis(b.windowStarted),
		HasWindowStartedAt: b.windowHasStart,
		CrashCount:         b.crashCount,
		NextRestartDelayMs: next,
		// The TypeScript uses `crashCount > CRASH_BACKOFF_MS.length` here while
		// recordCrash uses `index >= length`. Both are reproduced as written: at
		// crashCount == 5 the snapshot says not exhausted (the fifth crash still
		// restarts) and at 6 it does.
		Exhausted: b.crashCount > len(CrashBackoffMs),
	}
}

// Delay is the snapshot's next-restart delay as a duration; zero when exhausted.
func (b *CrashBudget) Delay() time.Duration {
	return time.Duration(b.Snapshot().NextRestartDelayMs) * time.Millisecond
}

func unixMillis(t time.Time) int64 {
	if t.IsZero() {
		return 0
	}
	return t.UnixMilli()
}
