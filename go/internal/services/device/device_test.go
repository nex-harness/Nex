package device

// device_test.go: device identity creation, persistence, cache reuse, stale
// lock recovery, and two-concurrent-ensure convergence.

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

func TestEnsureDeviceMidCreatesAndPersists(t *testing.T) {
	home := t.TempDir()
	mid, err := EnsureDeviceMid(context.Background(), Options{HomeDir: home})
	if err != nil {
		t.Fatal(err)
	}
	if mid == "" {
		t.Fatal("mid must not be empty")
	}
	// The state file carries the mid.
	data, err := os.ReadFile(filepath.Join(home, ".nex", "v2", "telemetry-state.json"))
	if err != nil {
		t.Fatal(err)
	}
	var state deviceState
	if err := json.Unmarshal(data, &state); err != nil || state.DeviceMid != mid {
		t.Fatalf("state file wrong: %s (%v)", data, err)
	}
	// A second call returns the SAME mid (file value wins).
	again, err := EnsureDeviceMid(context.Background(), Options{HomeDir: home})
	if err != nil {
		t.Fatal(err)
	}
	if again != mid {
		t.Fatalf("mid not stable: %s vs %s", again, mid)
	}
}

func TestEnsureDeviceMidKeepsExistingMid(t *testing.T) {
	home := t.TempDir()
	v2 := filepath.Join(home, ".nex", "v2")
	if err := os.MkdirAll(v2, 0o777); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(v2, "telemetry-state.json"), []byte(`{"deviceMid":"existing-mid"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	mid, err := EnsureDeviceMid(context.Background(), Options{HomeDir: home})
	if err != nil {
		t.Fatal(err)
	}
	if mid != "existing-mid" {
		t.Fatalf("existing mid must be kept, got %s", mid)
	}
}

func TestStaleLockReclaimed(t *testing.T) {
	home := t.TempDir()
	v2 := filepath.Join(home, ".nex", "v2")
	if err := os.MkdirAll(v2, 0o777); err != nil {
		t.Fatal(err)
	}
	// A lock owned by a definitely-dead pid.
	lockFile := filepath.Join(v2, "telemetry-state.lock")
	deadOwner := `{"pid":999999997,"createdAt":` + time.Now().Format("2006") + `}`
	_ = deadOwner
	if err := os.WriteFile(lockFile, []byte(`{"pid":999999999,"createdAt":123}`), 0o644); err != nil {
		t.Fatal(err)
	}
	// The lock mtime is fresh but the owner is dead → reclaimed.
	mid, err := EnsureDeviceMid(context.Background(), Options{
		HomeDir: home,
		Sleep:   func(time.Duration) {},
	})
	if err != nil {
		t.Fatalf("stale lock must be reclaimed: %v", err)
	}
	if mid == "" {
		t.Fatal("mid missing")
	}
	if _, err := os.Stat(lockFile); !os.IsNotExist(err) {
		t.Fatal("lock must be removed after the run")
	}
}

func TestLiveLockWaitsThenRetries(t *testing.T) {
	home := t.TempDir()
	v2 := filepath.Join(home, ".nex", "v2")
	if err := os.MkdirAll(v2, 0o777); err != nil {
		t.Fatal(err)
	}
	lockFile := filepath.Join(v2, "telemetry-state.lock")
	// Owned by THIS process (alive) with a fresh mtime → not reclaimable.
	if err := os.WriteFile(lockFile, []byte(`{"pid":`+pidString()+`,"createdAt":1}`), 0o644); err != nil {
		t.Fatal(err)
	}
	slept := 0
	_, err := EnsureDeviceMid(context.Background(), Options{
		HomeDir: home,
		Sleep: func(d time.Duration) {
			slept++
			// After a few retries, release the lock (the owner finished).
			if slept == 3 {
				_ = os.Remove(lockFile)
			}
		},
	})
	if err != nil {
		t.Fatalf("live lock must be waited out: %v", err)
	}
	if slept < 3 {
		t.Fatalf("expected retries, slept=%d", slept)
	}
}

func pidString() string {
	return itoa(int64(os.Getpid()))
}

func itoa(n int64) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var digits []byte
	for n > 0 {
		digits = append([]byte{byte('0' + n%10)}, digits...)
		n /= 10
	}
	if neg {
		return "-" + string(digits)
	}
	return string(digits)
}

func TestConcurrentEnsureSingleMid(t *testing.T) {
	// 并发 ensure（两个入口同时启动）：锁串行化后只生成一个 mid.
	home := t.TempDir()
	var wg sync.WaitGroup
	mids := make(chan string, 8)
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			mid, err := EnsureDeviceMid(context.Background(), Options{
				HomeDir: home,
				Sleep:   func(time.Duration) {},
			})
			if err != nil {
				t.Errorf("ensure: %v", err)
				return
			}
			mids <- mid
		}()
	}
	wg.Wait()
	close(mids)
	first := ""
	for mid := range mids {
		if first == "" {
			first = mid
			continue
		}
		if mid != first {
			t.Fatalf("concurrent ensure produced different mids: %s vs %s", mid, first)
		}
	}
}
