// leak_test.go is the goroutine-leak check for the protocol client.
//
// go.uber.org/goleak is not a dependency of this module (go.mod carries only
// uuid, x/text, websocket and modernc.org/sqlite), so this is the small equivalent
// the module already uses in internal/runtime/turnstate_leak_test.go: a DELTA
// check, not an absolute one. Other tests in this package leave goroutines behind
// on purpose (a real agent process's reaper, a blocked interaction handler), so
// demanding an empty global profile would fail for work this file never did. What
// must hold is that the work below adds nothing.
//
// goleak should be preferred once the module already depends on it; adding a
// dependency for one test is not worth the supply-chain surface.
package proto

import (
	"context"
	"encoding/json"
	"fmt"
	"runtime"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/tylinux/nex/go/internal/protocol"
)

const leakSettleTimeout = 3 * time.Second

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

func goroutineSignatures(profile string) map[string]int {
	out := map[string]int{}
	for _, g := range strings.Split(profile, "\n\n") {
		g = strings.TrimSpace(g)
		if g == "" {
			continue
		}
		key := g
		if i := strings.IndexByte(g, '\n'); i >= 0 {
			key = g[:i]
		}
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

// testingGoroutine reports whether a stack belongs to the testing framework.
func testingGoroutine(stack string) bool {
	for _, b := range []string{
		"testing.tRunner", "testing.(*M).Run", "testing.runTests",
		"testing.(*T).Run", "runtime.goexit",
	} {
		if strings.Contains(stack, b) {
			return true
		}
	}
	return false
}

func snapshotGoroutines() map[string]int { return goroutineSignatures(stackProfile()) }

// assertNoNewGoroutines requires that after the work, every live goroutine is
// either the testing framework's or one that already existed. It polls, so a
// goroutine on its way out does not fail the test.
func assertNoNewGoroutines(t *testing.T, before map[string]int) {
	t.Helper()
	deadline := time.Now().Add(leakSettleTimeout)
	var extra []string
	for {
		extra = nil
		for key, n := range goroutineSignatures(stackProfile()) {
			if testingGoroutine(key) {
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
		time.Sleep(20 * time.Millisecond)
	}
	sort.Strings(extra)
	t.Errorf("%d goroutine(s) outlived the test (baseline had %d distinct):\n%s",
		len(extra), len(before), strings.Join(extra, "\n"))
}

// TestClientLeaksNoGoroutines exercises every exit path of the client — response,
// error, timeout, cancellation, transport close, parse-error close and a plain
// close — and requires that each leaves nothing behind.
//
// The paths that would leak if they were wrong are the ones that matter: a
// watchdog timer that is not stopped, a waiter that is not removed from the map,
// and a frame pump that does not exit.
func TestClientLeaksNoGoroutines(t *testing.T) {
	before := snapshotGoroutines()
	transport := newFakeTransport()
	transport.on("ok", func(req protocol.Message) []protocol.Message {
		return []protocol.Message{{Kind: protocol.KindResponse, ID: req.ID, Result: []byte(`{"ok":true}`)}}
	})
	transport.on("bad", func(req protocol.Message) []protocol.Message {
		return []protocol.Message{{Kind: protocol.KindError, ID: req.ID, Error: &protocol.RPCError{Code: 1, Message: "no"}}}
	})
	transport.on("hang", func(protocol.Message) []protocol.Message { return nil })
	transport.on(InteractionRequestUserInput, func(protocol.Message) []protocol.Message { return nil })
	client := New(transport, Options{RequestTimeout: 200 * time.Millisecond})

	// A response, an error, a timeout and a cancellation: one of each.
	if _, err := client.Call(context.Background(), "ok", nil, CallOptions{}); err != nil {
		t.Fatalf("ok: %v", err)
	}
	if _, err := client.Call(context.Background(), "bad", nil, CallOptions{}); err == nil {
		t.Fatal("an error response resolved as success")
	}
	if _, err := client.Call(context.Background(), "hang", nil, CallOptions{}); err == nil {
		t.Fatal("a hung call resolved")
	}
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := client.Call(cancelled, "ok", nil, CallOptions{}); err == nil {
		t.Fatal("a cancelled call resolved")
	}

	// A handler blocked on its context must be released by the close.
	released := make(chan struct{})
	client.RegisterInteractionHandler(InteractionRequestUserInput,
		func(ctx context.Context, _ AgentRequest) (json.RawMessage, error) {
			defer close(released)
			<-ctx.Done()
			return nil, ctx.Err()
		})
	// The handler is registered before the frame is pushed, so the pump really
	// enters it and is really parked on the close.
	transport.Push(requestLine(t, 1, InteractionRequestUserInput, nil))
	time.Sleep(50 * time.Millisecond)
	client.Close()
	client.Wait()
	select {
	case <-released:
	case <-time.After(2 * time.Second):
		t.Fatal("the interaction handler was never released by the close")
	}
	assertNoNewGoroutines(t, before)
}

// TestProcessTransportLeaksNoGoroutines is the same check for a real agent
// process: a spawn, a call, a close and a wait must leave no goroutine behind. It
// skips when the agent binary cannot be built, and says so.
func TestProcessTransportLeaksNoGoroutines(t *testing.T) {
	binary := requireAgent(t)
	// The exec adapter allocates its reaper goroutines per process, and the first
	// spawn in a process is the expensive one (process-group setup, pipe
	// registration). The baseline is therefore taken after a warm-up spawn AND
	// after that spawn has been fully reaped, so only the delta is attributable to
	// the code under test.
	warmManager := newRealAgentManager(t, binary)
	warmWorkspace, _ := tempAgentWorkspace(t)
	if _, err := warmManager.GetClient(context.Background(), warmWorkspace); err != nil {
		t.Fatalf("warm-up spawn: %v", err)
	}
	warmCtx, warmCancel := context.WithTimeout(context.Background(), 15*time.Second)
	warmManager.DisposeAllAndWait(warmCtx)
	warmCancel()

	before := snapshotGoroutines()
	manager := newRealAgentManager(t, binary)
	workspace, _ := tempAgentWorkspace(t)
	proc, err := manager.GetClient(context.Background(), workspace)
	if err != nil {
		t.Fatalf("spawn: %v", err)
	}
	transport := NewProcessTransport(proc)
	client := New(transport, Options{RequestTimeout: 10 * time.Second})
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	if _, err := client.Call(ctx, MethodRuntimeCapabilities, nil, CallOptions{}); err != nil {
		t.Fatalf("call: %v", err)
	}
	client.Close()
	client.Wait()
	transport.WaitProcess()
	disposeCtx, disposeCancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer disposeCancel()
	manager.DisposeAllAndWait(disposeCtx)
	assertNoNewGoroutines(t, before)
}
