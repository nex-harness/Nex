package runtime

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"runtime"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/tylinux/nex/go/internal/modelclient"
)

// blockingTransport is a model transport whose request never finishes on its
// own: it signals that it was entered, then blocks until release. It is the
// "turn is running" state the cancel test needs.
type blockingTransport struct {
	entered chan struct{}
	release chan struct{}
	once    sync.Once
}

func (b *blockingTransport) Do(ctx context.Context, _ modelclient.Request) (*modelclient.Response, error) {
	b.once.Do(func() { close(b.entered) })
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-b.release:
		return &modelclient.Response{StatusCode: 200, Stream: io.NopCloser(strings.NewReader(""))}, nil
	}
}

// The goroutine-leak check. go.uber.org/goleak is not a dependency of this
// module (go.mod carries only uuid, x/text and modernc.org/sqlite), so this is
// the small equivalent the task allows.
//
// It is a delta check, not an absolute one: other tests in this package leave
// goroutines behind on purpose (a turn goroutine parked on firstSub, the
// permission-approving helper), so demanding a globally empty profile would
// fail for work this file never did. What must hold is that the work below
// adds nothing: every goroutine alive afterwards either was alive before or
// belongs to the testing framework.
//
// goleak should be preferred once the module already depends on it; adding a
// dependency for one test is not worth the supply-chain surface.

const leakSettleTimeout = 2 * time.Second

// goroutineCount is the number of live goroutines.
func goroutineCount() int { return runtime.NumGoroutine() }

// stackProfile returns the stacks of every live goroutine.
func stackProfile() string {
	buf := make([]byte, 1<<20)
	for {
		n := runtime.Stack(buf, true)
		if n < len(buf) {
			return string(buf[:n])
		}
		buf = make([]byte, 2*len(buf))
	}
}

// testingGoroutine reports whether a goroutine stack belongs to the testing
// framework itself.
func testingGoroutine(stack string) bool {
	for _, b := range []string{
		"testing.tRunner",
		"testing.(*M).Run",
		"testing.runTests",
		"testing.(*T).Run",
		"runtime.goexit",
	} {
		if strings.Contains(stack, b) {
			return true
		}
	}
	return false
}

// goroutineSignatures reduces the profile to one key per goroutine: its state
// plus the topmost frame in this package (or in the runtime). Two goroutines
// with the same key are interchangeable, which is what lets a delta check
// tolerate goroutines left by other tests while still catching a new one.
func goroutineSignatures(profile string) map[string]int {
	out := map[string]int{}
	for _, g := range strings.Split(profile, "\n\n") {
		g = strings.TrimSpace(g)
		if g == "" {
			continue
		}
		key := firstLine(g)
		for _, line := range strings.Split(g, "\n") {
			line = strings.TrimSpace(line)
			if strings.Contains(line, "github.com/tylinux/nex/go/") || strings.HasPrefix(line, "runtime.") {
				key += "\n\t" + line
				break
			}
		}
		out[key]++
	}
	return out
}

func firstLine(g string) string {
	if i := strings.IndexByte(g, '\n'); i >= 0 {
		return g[:i]
	}
	return g
}

// assertNoNewGoroutines requires that after the work, every live goroutine is
// either part of the testing framework or one that existed before it started.
// It polls, so a goroutine that is on its way out does not fail the test.
func assertNoNewGoroutines(t *testing.T, before map[string]int) {
	t.Helper()
	deadline := time.Now().Add(leakSettleTimeout)
	var extra []string
	for {
		extra = nil
		for key, n := range goroutineSignatures(stackProfile()) {
			if testingKey(key) {
				continue
			}
			if n > before[key] {
				extra = append(extra, fmt.Sprintf("x%d %s", n-before[key], key))
			}
		}
		if len(extra) == 0 {
			return
		}
		if time.Now().After(deadline) {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	sort.Strings(extra)
	t.Errorf("%d goroutine(s) outlived the test (baseline had %d distinct):\n%s",
		len(extra), len(before), strings.Join(extra, "\n"))
}

func testingKey(key string) bool { return testingGoroutine(key) }

// snapshotGoroutines is the "before" side of the delta check.
func snapshotGoroutines() map[string]int { return goroutineSignatures(stackProfile()) }

// TestTurnStateMachineLeaksNoGoroutines runs a long random-walk over the machine
// and requires that it leaves nothing behind. The state machine is pure, so the
// only way this can fail is if someone later starts a goroutine inside
// Transition; that is exactly the regression the check is for.
func TestTurnStateMachineLeaksNoGoroutines(t *testing.T) {
	before := snapshotGoroutines()
	startCount := goroutineCount()
	peak := startCount
	for round := 0; round < 5000; round++ {
		state := TurnIdle
		for step := 0; step < 32; step++ {
			// A deterministic sweep over the whole state/event space, offset by
			// the round, so every pair is exercised repeatedly without a
			// dependency on a random source.
			e := AllTurnEvents()[(round+step)%len(AllTurnEvents())]
			if next, _, err := Transition(state, e); err == nil {
				state = next
			}
		}
		// Sample the peak, not just the end state: a goroutine started and
		// already finished by the final check would otherwise be invisible, and
		// that is exactly what "started one and dropped it" looks like.
		if n := goroutineCount(); n > peak {
			peak = n
		}
	}
	// No test in this package calls t.Parallel, so the samples are not racing
	// other tests' goroutines and a rise above the baseline is this loop's.
	if peak > startCount {
		t.Errorf("the walks pushed the goroutine count from %d to %d; the machine must start none", startCount, peak)
	}
	// The delta check catches a goroutine that is still alive at the end, which
	// the peak cannot see if it started and stopped inside one sample window.
	assertNoNewGoroutines(t, before)
}

// TestTurnCancelLeavesNoGoroutines is the cancel half of step 1.5: a turn that
// is cancelled mid-flight must release everything it started, and the
// cancellation must be observable well inside the 500 ms the spec allows.
//
// The turn loop takes its context; cancelling it ends the model step and the
// tool batch. The test drives a real runtime with a model stream that never
// finishes, cancels, and requires the loop to return and leave no goroutine.
func TestTurnCancelLeavesNoGoroutines(t *testing.T) {
	before := snapshotGoroutines()
	ctx, cancel := context.WithCancel(context.Background())
	transport := &blockingTransport{entered: make(chan struct{}), release: make(chan struct{})}
	rt, _, _ := newTestRuntime(t, transport)
	// Without the production retry options the attempt loop declines to run at
	// all, so the turn would never open a request (the same trap runOneModelStep
	// documents).
	rt.deps.ModelConfig.Retry = modelclient.DefaultRetryOptions()
	rt.ctx = ctx
	s, err := rt.newSession("sess_cancel", "epoch1", Workspace{Path: t.TempDir(), Key: "/tmp/ws"})
	if err != nil {
		t.Fatal(err)
	}
	s.firstText = "cancel me"
	s.firstSub = closedChan()
	s.subscribed = true
	s.modelSelection = json.RawMessage(`{"providerId":"mock","modelId":"mock","options":{"reasoningLevel":"none"}}`)
	s.setMemoryEnabled(false)

	done := make(chan struct{})
	go func() {
		defer close(done)
		rt.runTurn(ctx, s, "queue_1")
	}()

	// Wait for the turn to actually be inside the model request before
	// cancelling; otherwise the test would pass without ever running anything.
	select {
	case <-transport.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("the turn never reached the model request")
	}

	start := time.Now()
	cancel()
	select {
	case <-done:
	case <-time.After(500 * time.Millisecond):
		t.Fatalf("the turn did not observe the cancel within 500 ms (took %s)", time.Since(start))
	}
	if elapsed := time.Since(start); elapsed > 500*time.Millisecond {
		t.Errorf("cancellation took %s, the spec allows 500 ms", elapsed)
	}
	// Let the transport's own goroutine unwind, then require nothing is left.
	close(transport.release)
	assertNoNewGoroutines(t, before)
}

// TestTransitionIsConcurrencySafe runs the machine from many goroutines at once.
// It is pure, so this must be race-free under -race; the test exists so a
// future change that adds shared state fails here rather than in production.
func TestTransitionIsConcurrencySafe(t *testing.T) {
	const goroutines, iterations = 8, 500
	done := make(chan struct{}, goroutines)
	for g := 0; g < goroutines; g++ {
		go func(g int) {
			defer func() { done <- struct{}{} }()
			states, events := AllTurnStates(), AllTurnEvents()
			state := states[g%len(states)]
			for i := 0; i < iterations; i++ {
				e := events[(g*i+i)%len(events)]
				if next, _, err := Transition(state, e); err == nil {
					state = next
				}
				// Every goroutine must agree on the table itself.
				if !CanTransitionTo(TurnStreaming, TurnSchedulingTools) {
					t.Error("CanTransitionTo is not constant across goroutines")
					return
				}
			}
		}(g)
	}
	for g := 0; g < goroutines; g++ {
		select {
		case <-done:
		case <-time.After(10 * time.Second):
			t.Fatal("a Transition goroutine did not finish")
		}
	}
}
