//go:build windows

// Windows half of the process-tree tests. The step-1.8 matrix runs
// `go test ./internal/adapters/exec/` on all three OSes, so this file has to
// exist and compile even where the Unix scenarios cannot.
package exec

import (
	"context"
	"os"
	osexec "os/exec"
	"strings"
	"testing"
	"time"
)

// TestKillProcessTreeRefusesPidOne is the one tree-kill invariant that holds on
// every platform: pid 1 is init, pid 0 addresses our own process group, and a
// signal there would take the agent down with it.
func TestKillProcessTreeRefusesPidOne(t *testing.T) {
	for _, pid := range []int{0, 1, -1} {
		err := KillProcessTree(context.Background(), pid)
		if err == nil {
			t.Errorf("KillProcessTree(ctx, %d) = nil, want a refusal", pid)
			continue
		}
		if !strings.Contains(err.Error(), "refusing") {
			t.Errorf("KillProcessTree(ctx, %d) = %v, want the refusal error", pid, err)
		}
	}
}

// TestCancellationObservedWithin500ms is the cancellation budget of step 1.5,
// on Windows: cancelling the Start context has to take the job down and let Wait
// return inside the same 500 ms.
func TestCancellationObservedWithin500ms(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	p, err := Start(ctx, Options{Args: []string{"cmd", "/c", "ping -n 60 127.0.0.1 > nul"}})
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan Result, 1)
	go func() { done <- p.Wait() }()
	cancel()
	select {
	case res := <-done:
		if res.WaitErr == nil {
			t.Errorf("a killed child must report a non-nil WaitErr, got %+v", res)
		}
	case <-time.After(500 * time.Millisecond):
		t.Fatalf("Wait did not return within 500ms of cancel (DefaultTermGrace=%v)", DefaultTermGrace)
	}
}

// TestOutputCollectorTruncation is the step-1.8 10 MiB truncation case, written
// with a shell that exists on every Windows host.
func TestOutputCollectorTruncation(t *testing.T) {
	const size = 10 << 20
	// A 1 MiB block written N times: the loop keeps the child's own memory flat,
	// which is the property the Unix awk version relies on too.
	cmd := osexec.Command("cmd", "/c", "for /L %i in (1,1,10) do @fsutil file createnew %TEMP%\\nex-exec-big.bin 1048576 > nul & type %TEMP%\\nex-exec-big.bin")
	cmd.Env = append(os.Environ(), "TEMP="+t.TempDir())
	collector := NewOutputCollector(4096, 1024)
	cmd.Stdout = collector
	if err := cmd.Run(); err != nil {
		t.Skipf("could not produce the 10 MiB fixture on this host: %v", err)
	}
	out := collector.Result()
	if out.Bytes < size {
		t.Errorf("Bytes = %d, want >= %d: dropped bytes must still be counted", out.Bytes, size)
	}
	if len(out.Text) != 4096 {
		t.Errorf("head = %d bytes, want exactly the 4096 cap", len(out.Text))
	}
	if len(out.Tail) != 1024 {
		t.Errorf("tail = %d bytes, want exactly the 1024 cap", len(out.Tail))
	}
	if !out.Truncated {
		t.Errorf("Truncated = false for %d bytes behind a 4096 cap", out.Bytes)
	}
}

// TestKillTreeIdempotent checks the contract KillTree documents: concurrent
// callers join the first kill, and a tree that is already gone is not an error.
func TestKillTreeIdempotent(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	p, err := Start(ctx, Options{Args: []string{"cmd", "/c", "ping -n 60 127.0.0.1 > nul"}})
	if err != nil {
		t.Fatal(err)
	}
	const callers = 8
	errs := make([]error, callers)
	done := make(chan struct{}, callers)
	for i := range callers {
		go func() {
			errs[i] = p.KillTree(context.Background())
			done <- struct{}{}
		}()
	}
	for range callers {
		<-done
	}
	for i, err := range errs {
		if err != nil {
			t.Errorf("KillTree caller %d: %v", i, err)
		}
	}
	if err := p.KillTree(context.Background()); err != nil {
		t.Errorf("KillTree after completion: %v", err)
	}
}

// TestExecReportsCleanExit is the baseline the kill tests sit on.
func TestExecReportsCleanExit(t *testing.T) {
	p, err := Start(context.Background(), Options{Args: []string{"cmd", "/c", "echo hello"}})
	if err != nil {
		t.Fatal(err)
	}
	res := p.Wait()
	if res.WaitErr != nil || res.ExitCode != 0 || res.Killed() {
		t.Errorf("clean exit reported as %+v", res)
	}
	// Windows has no signals, so Result.Signal stays empty even for a job the
	// agent terminated: the exit code carries the outcome instead.
	if res.Signal != "" {
		t.Errorf("Signal = %q, want empty on Windows", res.Signal)
	}
}
