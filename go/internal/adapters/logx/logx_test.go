package logx

import (
	"bytes"
	"strings"
	"sync"
	"testing"
	"time"
)

var fixedNow = time.Date(2026, 10, 13, 8, 30, 15, 123_000_000, time.Local)

// TestServiceLoggerPrefixFormat pins the exact line shapes: the shared prefix
// (timestamp + pid + source), the trace composition `scope][trace:<id>`, the
// level tag and the message.
func TestServiceLoggerPrefixFormat(t *testing.T) {
	var buf bytes.Buffer
	log := NewServiceLogger("nex-task", &Options{
		PID: 4242, Sink: &buf, Now: func() time.Time { return fixedNow },
		IsDebugEnabled: func() bool { return true },
	})
	log.Info("", "started")
	log.Info("tr-1", "resumed", "taskId", "t9")
	log.Warn("", "retrying")
	log.Error("tr-2", "failed")
	log.Debug("tr-3", "chunk")

	lines := strings.Split(strings.TrimRight(buf.String(), "\n"), "\n")
	want := []string{
		"[2026-10-13 08:30:15.123] [pid:4242] [nex-task] info  started",
		"[2026-10-13 08:30:15.123] [pid:4242] [nex-task][trace:tr-1] info  resumed taskId=t9",
		"[2026-10-13 08:30:15.123] [pid:4242] [nex-task] warn  retrying",
		"[2026-10-13 08:30:15.123] [pid:4242] [nex-task][trace:tr-2] error failed",
		"[2026-10-13 08:30:15.123] [pid:4242] [nex-task][trace:tr-3] debug chunk",
	}
	if len(lines) != len(want) {
		t.Fatalf("got %d lines:\n%s", len(lines), buf.String())
	}
	for i := range want {
		if lines[i] != want[i] {
			t.Errorf("line %d:\n got %q\nwant %q", i, lines[i], want[i])
		}
	}
}

// TestServiceLoggerDebugGating pins the TS rule: debug is dropped unless the
// debug flag resolves true; every other level always writes.
func TestServiceLoggerDebugGating(t *testing.T) {
	var buf bytes.Buffer
	log := NewServiceLogger("scope", &Options{
		PID: 1, Sink: &buf, Now: func() time.Time { return fixedNow },
		IsDebugEnabled: func() bool { return false },
	})
	log.Debug("", "hidden")
	log.Info("", "shown")
	if strings.Contains(buf.String(), "hidden") {
		t.Error("debug must be gated off")
	}
	if !strings.Contains(buf.String(), "shown") {
		t.Error("info must always write")
	}
}

// TestServiceLoggerConcurrentScopes writes from many goroutines through one
// sink and asserts no interleaving corrupted a line.
func TestServiceLoggerConcurrentScopes(t *testing.T) {
	var buf bytes.Buffer
	var wg sync.WaitGroup
	var mu sync.Mutex
	const writers = 16
	for i := 0; i < writers; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			var local bytes.Buffer
			log := NewServiceLogger("scope", &Options{PID: i, Sink: &local, Now: func() time.Time { return fixedNow }})
			log.Info("", strings.Repeat("x", 64))
			mu.Lock()
			buf.Write(local.Bytes())
			mu.Unlock()
		}(i)
	}
	wg.Wait()
	for _, line := range strings.Split(strings.TrimRight(buf.String(), "\n"), "\n") {
		if !strings.HasSuffix(line, strings.Repeat("x", 64)) {
			t.Fatalf("corrupted line: %q", line)
		}
	}
}
