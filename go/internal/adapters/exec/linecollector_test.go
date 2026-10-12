//go:build !windows

// Tests for the stdout line tap (OutputCollector.SetLineSink and
// exec.Options.StdoutLines), added in step 2.9 for the Node supervisor: it has to
// learn the port the child announces on its listen line BEFORE the process exits,
// which a retained buffer cannot provide.
//
// The tap is additive — the caps, the head, the tail and the byte accounting are
// unchanged — so most of these are about not breaking the existing guarantees while a
// sink is installed.
package exec

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"
)

// collectLines drives a collector with writes and returns every line the sink saw
// AFTER a Flush. A caller that must distinguish an unterminated line from a complete one
// uses the collector directly (see TestLineTapFlushDeliversTheUnterminatedLine).
func collectLines(t *testing.T, collector *OutputCollector, writes ...string) []string {
	t.Helper()
	sink := make(chan string, 64)
	collector.SetLineSink(sink)
	for _, write := range writes {
		if _, err := collector.Write([]byte(write)); err != nil {
			t.Fatalf("Write(%q): %v", write, err)
		}
	}
	collector.Flush()
	return drainLines(sink)
}

func drainLines(sink <-chan string) []string {
	var lines []string
	for {
		select {
		case line := <-sink:
			lines = append(lines, line)
			continue
		default:
		}
		break
	}
	return lines
}

func equalStrings(got, want []string) bool {
	if len(got) != len(want) {
		return false
	}
	for i := range got {
		if got[i] != want[i] {
			return false
		}
	}
	return true
}

// TestLineTapSplitsWrites: lines are reported when their newline arrives, so a write
// that ends mid-line produces nothing yet and the next write completes it. That is
// the behavior a supervisor reading a listen line depends on.
func TestLineTapSplitsWrites(t *testing.T) {
	cases := []struct {
		name   string
		writes []string
		want   []string
	}{
		{"one line per write", []string{"first\n", "second\n"}, []string{"first", "second"}},
		{"line split across writes", []string{"fir", "st\n"}, []string{"first"}},
		{"several lines in one write", []string{"a\nb\nc\n"}, []string{"a", "b", "c"}},
		{"trailing partial is delivered by Flush", []string{"a\nno newline"}, []string{"a", "no newline"}},
		{"crlf is stripped", []string{"windows\r\n"}, []string{"windows"}},
		{"empty lines are kept", []string{"a\n\nb\n"}, []string{"a", "", "b"}},
		{"no newline at all, delivered by Flush", []string{"only"}, []string{"only"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := collectLines(t, NewOutputCollector(1<<16, 0), tc.writes...)
			if !equalStrings(got, tc.want) {
				t.Errorf("lines = %q, want %q", got, tc.want)
			}
		})
	}
}

// TestLineTapSeesBytesDroppedPastTheHeadCap: the tap must observe every byte the child
// wrote, not only the ones the retained head kept. A supervisor whose tap stopped at
// the cap would lose the listen line exactly when a chatty child had filled its buffer.
func TestLineTapSeesBytesDroppedPastTheHeadCap(t *testing.T) {
	// A head of 4 bytes: the port line arrives long after the cap is full.
	collector := NewOutputCollector(4, 0)
	got := collectLines(t, collector,
		"noise one\n",
		"noise two\n",
		"[nex-server:http][7] http://127.0.0.1:41234\n",
	)
	want := []string{"noise one", "noise two", "[nex-server:http][7] http://127.0.0.1:41234"}
	if !equalStrings(got, want) {
		t.Errorf("lines = %q, want %q", got, want)
	}
	// The tap must not change the caps: a 4-byte head over 57 written bytes is
	// truncated, and the accounting still reports every byte.
	result := collector.Result()
	if !result.Truncated {
		t.Error("Truncated = false; the head cap was 4 bytes and far more was written")
	}
	if len(result.Text) > 4 {
		t.Errorf("retained head = %d bytes, want at most the 4-byte cap", len(result.Text))
	}
	if want := int64(len("noise one\nnoise two\n[nex-server:http][7] http://127.0.0.1:41234\n")); result.Bytes != want {
		t.Errorf("Bytes = %d, want %d \u2014 dropping bytes must not drop accounting", result.Bytes, want)
	}
}

// TestLineTapFlushDeliversTheUnterminatedLine: a banner without a trailing newline is
// only visible after Flush, which is what lets a caller that waits for the exit still
// see it.
func TestLineTapFlushDeliversTheUnterminatedLine(t *testing.T) {
	collector := NewOutputCollector(1<<16, 0)
	sink := make(chan string, 8)
	collector.SetLineSink(sink)
	if _, err := collector.Write([]byte("no trailing newline")); err != nil {
		t.Fatalf("Write: %v", err)
	}
	select {
	case line := <-sink:
		t.Fatalf("a line was reported before Flush: %q", line)
	default:
	}
	collector.Flush()
	select {
	case line := <-sink:
		if line != "no trailing newline" {
			t.Errorf("flushed line = %q", line)
		}
	default:
		t.Fatal("Flush did not deliver the unterminated line")
	}
}

// TestLineTapDropsRatherThanBlocks is a hard requirement, not a nicety: Write is the
// io.Writer os/exec hands to the child's pipe, so a full sink channel would block the
// CHILD. The child must keep running and the collector must keep counting.
func TestLineTapDropsRatherThanBlocks(t *testing.T) {
	collector := NewOutputCollector(1<<20, 0)
	sink := make(chan string, 1) // deliberately tiny, and never read
	collector.SetLineSink(sink)

	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := 0; i < 500; i++ {
			if _, err := collector.Write([]byte("line\n")); err != nil {
				t.Errorf("Write: %v", err)
				return
			}
		}
	}()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("a full sink channel blocked the child's writer")
	}
	if collector.Bytes() != 500*5 {
		t.Errorf("Bytes = %d, want %d — dropping lines must not drop accounting", collector.Bytes(), 500*5)
	}
}

// TestLineTapBoundsTheUnterminatedLine: a child that never emits a newline must not be
// able to grow the pending buffer without limit.
func TestLineTapBoundsTheUnterminatedLine(t *testing.T) {
	collector := NewOutputCollector(1<<20, 0)
	sink := make(chan string, 1)
	collector.SetLineSink(sink)
	huge := strings.Repeat("x", maxPendingLine*3)
	if _, err := collector.Write([]byte(huge)); err != nil {
		t.Fatalf("Write: %v", err)
	}
	collector.mu.Lock()
	pending := len(collector.pending)
	collector.mu.Unlock()
	if pending > maxPendingLine {
		t.Errorf("pending line buffer = %d bytes, want at most %d", pending, maxPendingLine)
	}
}

// TestStdoutLinesReachesTheTap: the seam the supervisor actually uses. A child that
// prints the listen line must have it delivered to the caller's channel while it is
// still running.
func TestStdoutLinesReachesTheTap(t *testing.T) {
	// A shell script is the platform-portable way to produce a child whose stdout the
	// test controls; the Unix build tag is why this file is !windows.
	lines := make(chan string, 16)
	process, err := Start(context.Background(), Options{
		Args:        []string{"/bin/sh", "-c", "echo '[nex-server:http][1] http://127.0.0.1:41234'; sleep 5"},
		StdoutLines: lines,
	})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer func() {
		_ = process.KillTree(context.Background())
		process.Wait()
	}()

	select {
	case line := <-lines:
		if !strings.HasSuffix(line, "http://127.0.0.1:41234") {
			t.Errorf("line = %q, want the listen line", line)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the listen line never reached StdoutLines")
	}
	// The retained output must still work for everyone else.
	if !strings.Contains(process.Stdout().Result().Text, "127.0.0.1:41234") {
		t.Errorf("retained stdout = %q, want it to still contain the line", process.Stdout().Result().Text)
	}
}

// TestLineTapIsRaceFreeUnderConcurrentWrites: Write is called from os/exec's copier
// goroutine while Flush may be called from the caller's; the race detector is the
// assertion.
func TestLineTapIsRaceFreeUnderConcurrentWrites(t *testing.T) {
	collector := NewOutputCollector(1<<16, 0)
	sink := make(chan string, 16)
	collector.SetLineSink(sink)
	go func() {
		for range sink { // drain so the tap never drops
		}
	}()

	var wg sync.WaitGroup
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 200; j++ {
				_, _ = collector.Write([]byte("concurrent line\n"))
			}
		}()
	}
	wg.Wait()
	collector.Flush()
	if collector.Bytes() != 4*200*int64(len("concurrent line\n")) {
		t.Errorf("Bytes = %d, want the full total", collector.Bytes())
	}
}
