package node

// Crash budget tests. The constants are the contract, so the first test asserts them
// against the values in packages/nex-server-cli/src/contracts.ts verbatim — if the
// TypeScript changes, this test is what makes the Go copy a deliberate decision
// rather than a silent divergence.

import (
	"sync"
	"testing"
	"time"
)

// fakeClock is the injectable clock crashBudget.ts takes through its `now` option.
type fakeClock struct {
	mu  sync.Mutex
	now time.Time
}

func newFakeClock() *fakeClock {
	return &fakeClock{now: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)}
}

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *fakeClock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
}

// TestCrashConstantsMatchTypeScript pins CRASH_WINDOW_MS and CRASH_BACKOFF_MS from
// packages/nex-server-cli/src/contracts.ts:
//
//	export const CRASH_WINDOW_MS = 5 * 60_000;
//	export const CRASH_BACKOFF_MS = [1_000, 2_000, 4_000, 8_000, 16_000] as const;
func TestCrashConstantsMatchTypeScript(t *testing.T) {
	if CrashWindowMs != 5*60_000 {
		t.Errorf("CrashWindowMs = %d, want 300000 (5 * 60_000)", CrashWindowMs)
	}
	want := [5]int{1_000, 2_000, 4_000, 8_000, 16_000}
	for i, expected := range want {
		if CrashBackoffMs[i] != expected {
			t.Errorf("CrashBackoffMs[%d] = %d, want %d", i, CrashBackoffMs[i], expected)
		}
	}
}

// TestRecordCrashSequence is recordCrash() walked to exhaustion, asserting the exact
// (shouldRestart, delayMs, exhausted) triple per crash.
//
// The subtlety this pins: exhausted is `index >= CRASH_BACKOFF_MS.length`, so the
// first FIVE crashes restart with 1s/2s/4s/8s/16s and the SIXTH stops. An
// implementation that used `>` instead of `>=`, or that counted the first restart as
// crash 0 with a shorter table, would look right and stop one restart late.
func TestRecordCrashSequence(t *testing.T) {
	clock := newFakeClock()
	budget := NewCrashBudget(clock.Now)

	want := []struct {
		shouldRestart bool
		delayMs       int
		exhausted     bool
	}{
		{true, 1_000, false},
		{true, 2_000, false},
		{true, 4_000, false},
		{true, 8_000, false},
		{true, 16_000, false},
		{false, 16_000, true}, // index clamped to the last entry, but no restart
		{false, 16_000, true},
	}
	for i, expected := range want {
		got := budget.RecordCrash()
		if got != (Decision{ShouldRestart: expected.shouldRestart, DelayMs: expected.delayMs, Exhausted: expected.exhausted}) {
			t.Errorf("crash %d = %+v, want {shouldRestart:%v delayMs:%d exhausted:%v}",
				i+1, got, expected.shouldRestart, expected.delayMs, expected.exhausted)
		}
	}
}

// TestWindowResetsAfterFiveMinutes is the window half of the policy: crashes more
// than CRASH_WINDOW_MS apart start a fresh budget, so an upstream that crashed once
// this morning and once this afternoon is not treated as a loop.
func TestWindowResetsAfterFiveMinutes(t *testing.T) {
	clock := newFakeClock()
	budget := NewCrashBudget(clock.Now)

	for i := 0; i < 5; i++ {
		budget.RecordCrash()
	}
	// One millisecond short of the window: still the same budget, still exhausted.
	clock.Advance(time.Duration(CrashWindowMs)*time.Millisecond - time.Millisecond)
	if decision := budget.RecordCrash(); !decision.Exhausted {
		t.Fatalf("crash just inside the window restarted: %+v", decision)
	}
	// Crossing the window resets both the counter and the exhaustion.
	clock.Advance(2 * time.Millisecond)
	if decision := budget.RecordCrash(); !decision.ShouldRestart || decision.DelayMs != 1_000 {
		t.Fatalf("first crash after the window = %+v, want a fresh 1000ms restart", decision)
	}
}

// TestSnapshotMatchesSnapshotSchema is CrashBudgetSnapshot from contracts.ts:
// {windowStartedAt?, crashCount, nextRestartDelayMs, exhausted}.
//
// The two exhaustion predicates are deliberately different in the TypeScript
// (recordCrash uses index >= length, snapshot uses crashCount > length), and this
// test pins the observable consequence: after the fifth crash the snapshot still says
// not exhausted, after the sixth it does.
func TestSnapshotMatchesSnapshotSchema(t *testing.T) {
	clock := newFakeClock()
	budget := NewCrashBudget(clock.Now)

	empty := budget.Snapshot()
	if empty.HasWindowStartedAt || empty.CrashCount != 0 {
		t.Fatalf("fresh snapshot = %+v, want no window and crashCount 0", empty)
	}
	if empty.NextRestartDelayMs != 1_000 {
		t.Errorf("nextRestartDelayMs on a fresh budget = %d, want %d", empty.NextRestartDelayMs, 1_000)
	}

	for i := 0; i < 5; i++ {
		budget.RecordCrash()
	}
	afterFive := budget.Snapshot()
	if afterFive.CrashCount != 5 || afterFive.Exhausted {
		t.Errorf("snapshot after five crashes = %+v, want crashCount 5 and exhausted false", afterFive)
	}
	// nextRestartDelayMs is CRASH_BACKOFF_MS[crashCount], and 0 once crashCount has
	// reached the table length — including at exactly 5, which is where snapshot()
	// and recordCrash() disagree about exhaustion (see below).
	if afterFive.NextRestartDelayMs != 0 {
		t.Errorf("nextRestartDelayMs after five crashes = %d, want 0", afterFive.NextRestartDelayMs)
	}
	if !afterFive.HasWindowStartedAt || afterFive.WindowStartedAt != clock.Now().UnixMilli() {
		t.Errorf("windowStartedAt = %d, want %d", afterFive.WindowStartedAt, clock.Now().UnixMilli())
	}

	budget.RecordCrash()
	if afterSix := budget.Snapshot(); !afterSix.Exhausted {
		t.Errorf("snapshot after six crashes = %+v, want exhausted true", afterSix)
	}
}

// TestSnapshotResetsAnElapsedWindow is snapshot()'s own reset, which is independent of
// recordCrash: a caller polling status long after the last crash must see a fresh
// budget, not a stale exhausted one.
func TestSnapshotResetsAnElapsedWindow(t *testing.T) {
	clock := newFakeClock()
	budget := NewCrashBudget(clock.Now)
	for i := 0; i < 6; i++ {
		budget.RecordCrash()
	}
	if !budget.Snapshot().Exhausted {
		t.Fatal("budget should be exhausted before the clock advances")
	}
	clock.Advance(time.Duration(CrashWindowMs) * time.Millisecond)
	reset := budget.Snapshot()
	if reset.CrashCount != 0 || reset.Exhausted || reset.HasWindowStartedAt {
		t.Errorf("snapshot after the window elapsed = %+v, want a reset budget", reset)
	}
}

// TestResetClearsEverything covers the public reset().
func TestResetClearsEverything(t *testing.T) {
	budget := NewCrashBudget(newFakeClock().Now)
	for i := 0; i < 4; i++ {
		budget.RecordCrash()
	}
	budget.Reset()
	snapshot := budget.Snapshot()
	if snapshot.CrashCount != 0 || snapshot.HasWindowStartedAt || snapshot.NextRestartDelayMs != 1_000 {
		t.Fatalf("snapshot after Reset = %+v, want a fresh budget", snapshot)
	}
}

// TestConcurrentRecordCrash is the race-detector case: the supervisor records crashes
// from its run goroutine while the status path snapshots them, so the budget must be
// usable from both.
func TestConcurrentRecordCrash(t *testing.T) {
	budget := NewCrashBudget(newFakeClock().Now)
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(2)
		go func() { defer wg.Done(); budget.RecordCrash() }()
		go func() { defer wg.Done(); budget.Snapshot() }()
	}
	wg.Wait()
}
