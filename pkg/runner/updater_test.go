package runner

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nex-harness/nex/pkg/protocol"
)

func TestUpdaterReleaseFirstFallbackAndAuthIsolation(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	newBin := []byte("#!/bin/sh\necho 0.2.0\n")
	sum := sha256.Sum256(newBin)
	wantSHA := hex.EncodeToString(sum[:])

	var mu sync.Mutex
	var releaseSawAuth string
	var serverSawAuth string
	var releaseHits int
	var serverHits int

	releaseSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		releaseHits++
		releaseSawAuth = r.Header.Get("Authorization")
		mu.Unlock()
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer releaseSrv.Close()

	ctrlSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		serverHits++
		serverSawAuth = r.Header.Get("Authorization")
		mu.Unlock()
		w.Header().Set("Content-Type", "application/octet-stream")
		_, _ = w.Write(newBin)
	}))
	defer ctrlSrv.Close()

	dataDir := t.TempDir()
	wsURL := "ws" + strings.TrimPrefix(ctrlSrv.URL, "http") + "/ws/runner"
	node, err := NewNode(Config{
		RunnerID:    "run_upd_1",
		AuthToken:   "secret-tok-abc",
		ServerWSURL: wsURL,
		DataDir:     dataDir,
		Version:     "0.1.0",
	})
	if err != nil {
		t.Fatal(err)
	}
	defer node.Close()

	fakeExe := filepath.Join(t.TempDir(), "nex-runner")
	_ = os.WriteFile(fakeExe, []byte("old"), 0o755)

	swapped := make(chan UpdateState, 1)
	node.updater.exePathFn = func() (string, error) { return fakeExe, nil }
	node.updater.smokeCheckFn = func(ctx context.Context, binPath string) error { return nil }
	node.updater.swapAndRestartFn = func(ctx context.Context, currentExe, stagedPath, backupPath string, state *UpdateState) error {
		_ = node.updater.saveState(*state)
		swapped <- *state
		return nil
	}

	node.updater.HandleWelcome(ctx, protocol.RunnerWelcomePayload{
		RunnerID:          "run_upd_1",
		UpdateStatus:      "update_available",
		TargetVersion:     "0.2.0",
		TargetSHA256:      wantSHA,
		DownloadURLs:      []string{releaseSrv.URL + "/nex-runner", "/api/v1/runners/binary/linux/amd64"},
		AutoUpdateAllowed: true,
	})

	select {
	case st := <-swapped:
		if st.Status != "pending_verify" || st.TargetSHA256 != wantSHA || st.TargetVersion != "0.2.0" {
			t.Fatalf("unexpected UpdateState on swap: %+v", st)
		}
	case <-ctx.Done():
		t.Fatal("timed out waiting for fallback swap")
	}

	mu.Lock()
	defer mu.Unlock()
	if releaseHits != 1 || serverHits != 1 {
		t.Fatalf("expected 1 release hit + 1 server hit, got %d / %d", releaseHits, serverHits)
	}
	if releaseSawAuth != "" {
		t.Fatalf("expected NO Authorization header on external release URL, got %q", releaseSawAuth)
	}
	if serverSawAuth != "Bearer secret-tok-abc" {
		t.Fatalf("expected Bearer auth on server fallback URL, got %q", serverSawAuth)
	}
}

func TestUpdaterSHA256MismatchRejected(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	badSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("corrupted-bytes"))
	}))
	defer badSrv.Close()

	node, err := NewNode(Config{
		RunnerID:    "run_sha_bad",
		ServerWSURL: "ws" + strings.TrimPrefix(badSrv.URL, "http") + "/ws/runner",
		DataDir:     t.TempDir(),
		Version:     "0.1.0",
	})
	if err != nil {
		t.Fatal(err)
	}
	defer node.Close()

	swapCalled := false
	node.updater.swapAndRestartFn = func(ctx context.Context, currentExe, stagedPath, backupPath string, state *UpdateState) error {
		swapCalled = true
		return nil
	}

	node.updater.runUpdate(ctx, protocol.RunnerUpdatePayload{
		RequestID:      "req_bad_sha",
		TargetVersion:  "0.2.0",
		ExpectedSHA256: strings.Repeat("0", 64),
		DownloadURLs:   []string{"/api/v1/runners/binary/linux/amd64"},
		Mode:           "immediate",
	})

	if swapCalled {
		t.Fatal("expected swapAndRestartFn NOT to be called when SHA-256 mismatches")
	}
	if node.IsDraining() {
		t.Fatal("expected draining to reset after failed update")
	}
}

func TestUpdaterWhenIdleDrainsAndRejectsNewPrompts(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	bin := []byte("valid-binary")
	sum := sha256.Sum256(bin)
	wantSHA := hex.EncodeToString(sum[:])

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(bin)
	}))
	defer srv.Close()

	node, err := NewNode(Config{
		RunnerID:    "run_idle_drain",
		ServerWSURL: "ws" + strings.TrimPrefix(srv.URL, "http") + "/ws/runner",
		DataDir:     t.TempDir(),
		Version:     "0.1.0",
	})
	if err != nil {
		t.Fatal(err)
	}
	defer node.Close()

	node.mu.Lock()
	node.activeTurns = 1
	node.mu.Unlock()

	swapped := make(chan struct{}, 1)
	node.updater.drainPollInterval = 10 * time.Millisecond
	node.updater.smokeCheckFn = func(ctx context.Context, binPath string) error { return nil }
	node.updater.swapAndRestartFn = func(ctx context.Context, currentExe, stagedPath, backupPath string, state *UpdateState) error {
		close(swapped)
		return nil
	}

	node.updater.TriggerAsync(ctx, protocol.RunnerUpdatePayload{
		RequestID:      "req_drain",
		TargetVersion:  "0.2.0",
		ExpectedSHA256: wantSHA,
		DownloadURLs:   []string{"/api/v1/runners/binary/linux/amd64"},
		Mode:           "when_idle",
	})

	// Poll briefly until draining flag turns true while work is still active
	deadline := time.Now().Add(time.Second)
	for !node.IsDraining() && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if !node.IsDraining() {
		t.Fatal("expected runner to enter draining mode")
	}

	// New prompts should be rejected while draining
	if _, err := node.ExecutePrompt(ctx, protocol.PromptPayload{Text: "hello"}, "chat", false); err == nil || !strings.Contains(err.Error(), "draining") {
		t.Fatalf("expected draining error for new ExecutePrompt, got %v", err)
	}

	// Swap must not proceed while activeTurns == 1
	select {
	case <-swapped:
		t.Fatal("swap proceeded before workload drained to 0")
	case <-time.After(40 * time.Millisecond):
	}

	// Finish active turn -> update proceeds immediately
	node.mu.Lock()
	node.activeTurns = 0
	node.mu.Unlock()

	select {
	case <-swapped:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for swap after workload drained")
	}
}

func TestUpdaterDevBuildAutoVsForce(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	bin := []byte("release-bin")
	sum := sha256.Sum256(bin)
	wantSHA := hex.EncodeToString(sum[:])

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(bin)
	}))
	defer srv.Close()

	node, err := NewNode(Config{
		RunnerID:    "run_dev_1",
		ServerWSURL: "ws" + strings.TrimPrefix(srv.URL, "http") + "/ws/runner",
		DataDir:     t.TempDir(),
		Version:     "dev",
	})
	if err != nil {
		t.Fatal(err)
	}
	defer node.Close()

	swapped := make(chan struct{}, 2)
	node.updater.smokeCheckFn = func(ctx context.Context, binPath string) error { return nil }
	node.updater.swapAndRestartFn = func(ctx context.Context, currentExe, stagedPath, backupPath string, state *UpdateState) error {
		swapped <- struct{}{}
		return nil
	}

	// Welcome auto-update & non-forced runner.update must both be ignored for dev runners
	node.updater.HandleWelcome(ctx, protocol.RunnerWelcomePayload{
		RunnerID:          "run_dev_1",
		UpdateStatus:      "update_required",
		TargetVersion:     "0.2.0",
		TargetSHA256:      wantSHA,
		DownloadURLs:      []string{"/api/v1/runners/binary/linux/amd64"},
		AutoUpdateAllowed: false,
	})
	node.updater.HandleUpdateCommand(ctx, protocol.RunnerUpdatePayload{
		RequestID:      "req_non_force",
		TargetVersion:  "0.2.0",
		ExpectedSHA256: wantSHA,
		DownloadURLs:   []string{"/api/v1/runners/binary/linux/amd64"},
		Mode:           "immediate",
		Force:          false,
	})

	select {
	case <-swapped:
		t.Fatal("dev runner should not swap without Force=true")
	case <-time.After(60 * time.Millisecond):
	}

	// Manual Force=true update succeeds
	node.updater.HandleUpdateCommand(ctx, protocol.RunnerUpdatePayload{
		RequestID:      "req_force",
		TargetVersion:  "0.2.0",
		ExpectedSHA256: wantSHA,
		DownloadURLs:   []string{"/api/v1/runners/binary/linux/amd64"},
		Mode:           "immediate",
		Force:          true,
	})

	select {
	case <-swapped:
	case <-time.After(2 * time.Second):
		t.Fatal("expected forced manual update on dev runner to succeed")
	}
}

func TestUpdaterWatchdogRollbackAndCooldown(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	node, err := NewNode(Config{
		RunnerID:    "run_watchdog",
		ServerWSURL: "ws://127.0.0.1:1/ws/runner",
		DataDir:     t.TempDir(),
		Version:     "0.2.0",
	})
	if err != nil {
		t.Fatal(err)
	}
	defer node.Close()

	rolledBack := make(chan UpdateState, 1)
	node.updater.watchdogTimeout = 35 * time.Millisecond
	node.updater.cooldownDuration = 10 * time.Minute
	node.updater.rollbackRestartFn = func(ctx context.Context, currentExe, backupPath string, state *UpdateState) error {
		rolledBack <- *state
		return nil
	}

	failedSHA := strings.Repeat("d", 64)
	if err := node.updater.saveState(UpdateState{
		RequestID:     "req_wd",
		TargetVersion: "0.2.0",
		TargetSHA256:  failedSHA,
		PreviousPath:  "/tmp/nex-runner",
		BackupPath:    "/tmp/nex-runner.bak",
		Status:        "pending_verify",
	}); err != nil {
		t.Fatal(err)
	}

	node.StartWatchdog(ctx)

	select {
	case rb := <-rolledBack:
		if rb.Status != "rolled_back" || rb.FailedSHA256 != failedSHA || rb.CooldownUntil <= time.Now().UnixMilli() {
			t.Fatalf("unexpected rollback state: %+v", rb)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for watchdog rollback")
	}

	// Verify auto retry for same FailedSHA256 is suppressed during cooldown
	swapCalled := false
	node.updater.swapAndRestartFn = func(ctx context.Context, currentExe, stagedPath, backupPath string, state *UpdateState) error {
		swapCalled = true
		return nil
	}
	node.updater.TriggerAsync(ctx, protocol.RunnerUpdatePayload{
		RequestID:      "req_retry_blocked",
		TargetVersion:  "0.2.0",
		ExpectedSHA256: failedSHA,
		DownloadURLs:   []string{"http://127.0.0.1:1/bin"},
		Mode:           "immediate",
		Force:          false,
	})
	time.Sleep(40 * time.Millisecond)
	if swapCalled {
		t.Fatal("expected cooldown to suppress automatic retry for failed SHA-256")
	}
}

func TestUpdaterManualForceSkipsIdlePTYsButAutoWaits(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	bin := []byte("valid-binary")
	sum := sha256.Sum256(bin)
	wantSHA := hex.EncodeToString(sum[:])

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(bin)
	}))
	defer srv.Close()

	node, err := NewNode(Config{
		RunnerID:    "run_pty_drain",
		ServerWSURL: "ws" + strings.TrimPrefix(srv.URL, "http") + "/ws/runner",
		DataDir:     t.TempDir(),
		Version:     "0.1.0",
	})
	if err != nil {
		t.Fatal(err)
	}
	defer node.Close()

	// Lingering idle PTY (like an old shell left open) with zero active turns
	node.mu.Lock()
	node.activePTYs["pty_stale"] = true
	node.mu.Unlock()

	// 1. Auto update (Force: false) must wait while PTY is open
	autoSwapped := make(chan struct{}, 1)
	node.updater.drainPollInterval = 10 * time.Millisecond
	node.updater.smokeCheckFn = func(ctx context.Context, binPath string) error { return nil }
	node.updater.swapAndRestartFn = func(ctx context.Context, currentExe, stagedPath, backupPath string, state *UpdateState) error {
		close(autoSwapped)
		return nil
	}
	node.updater.TriggerAsync(ctx, protocol.RunnerUpdatePayload{
		RequestID:      "req_auto_pty",
		TargetVersion:  "0.2.0",
		ExpectedSHA256: wantSHA,
		DownloadURLs:   []string{"/api/v1/runners/binary/darwin/arm64"},
		Mode:           "when_idle",
		Force:          false,
	})

	select {
	case <-autoSwapped:
		t.Fatal("auto update must wait for open PTY")
	case <-time.After(50 * time.Millisecond):
	}

	// Escalating to manual/immediate breaks through and finishes swap
	node.updater.TriggerAsync(ctx, protocol.RunnerUpdatePayload{
		RequestID:      "req_force_escalate",
		TargetVersion:  "0.2.0",
		ExpectedSHA256: wantSHA,
		DownloadURLs:   []string{"/api/v1/runners/binary/darwin/arm64"},
		Mode:           "immediate",
		Force:          true,
	})
	select {
	case <-autoSwapped:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for escalated swap")
	}

	// 2. Fresh manual upgrade (Force: true, Mode: when_idle) with lingering PTY and 0 turns proceeds immediately
	manualSwapped := make(chan struct{}, 1)
	node.updater.swapAndRestartFn = func(ctx context.Context, currentExe, stagedPath, backupPath string, state *UpdateState) error {
		close(manualSwapped)
		return nil
	}
	// Wait until updater goroutine clears updating=false
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		node.updater.mu.Lock()
		busy := node.updater.updating
		node.updater.mu.Unlock()
		if !busy {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	node.updater.TriggerAsync(ctx, protocol.RunnerUpdatePayload{
		RequestID:      "req_manual_pty",
		TargetVersion:  "0.2.0",
		ExpectedSHA256: wantSHA,
		DownloadURLs:   []string{"/api/v1/runners/binary/darwin/arm64"},
		Mode:           "when_idle",
		Force:          true,
	})
	select {
	case <-manualSwapped:
	case <-time.After(2 * time.Second):
		t.Fatal("expected manual force upgrade to bypass lingering idle PTY")
	}
}

func TestUpdaterImmediateEscalatesWhileDrainingTurns(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	bin := []byte("valid-binary")
	sum := sha256.Sum256(bin)
	wantSHA := hex.EncodeToString(sum[:])

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(bin)
	}))
	defer srv.Close()

	node, err := NewNode(Config{
		RunnerID:    "run_escalate_turns",
		ServerWSURL: "ws" + strings.TrimPrefix(srv.URL, "http") + "/ws/runner",
		DataDir:     t.TempDir(),
		Version:     "0.1.0",
	})
	if err != nil {
		t.Fatal(err)
	}
	defer node.Close()

	node.mu.Lock()
	node.activeTurns = 1
	node.mu.Unlock()

	swapped := make(chan struct{}, 1)
	node.updater.drainPollInterval = 10 * time.Millisecond
	node.updater.smokeCheckFn = func(ctx context.Context, binPath string) error { return nil }
	node.updater.swapAndRestartFn = func(ctx context.Context, currentExe, stagedPath, backupPath string, state *UpdateState) error {
		close(swapped)
		return nil
	}

	node.updater.TriggerAsync(ctx, protocol.RunnerUpdatePayload{
		RequestID:      "req_turn_drain",
		TargetVersion:  "0.2.0",
		ExpectedSHA256: wantSHA,
		DownloadURLs:   []string{"/api/v1/runners/binary/darwin/arm64"},
		Mode:           "when_idle",
		Force:          true,
	})

	deadline := time.Now().Add(time.Second)
	for !node.IsDraining() && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if !node.IsDraining() {
		t.Fatal("expected runner to enter draining")
	}

	// User clicks Update Now -> Mode=immediate escalates past activeTurns draining
	node.updater.TriggerAsync(ctx, protocol.RunnerUpdatePayload{
		RequestID:      "req_turn_now",
		TargetVersion:  "0.2.0",
		ExpectedSHA256: wantSHA,
		DownloadURLs:   []string{"/api/v1/runners/binary/darwin/arm64"},
		Mode:           "immediate",
		Force:          true,
	})
	select {
	case <-swapped:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for immediate escalation during draining")
	}
}
