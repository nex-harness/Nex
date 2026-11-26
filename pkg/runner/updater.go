package runner

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/nex-harness/nex/pkg/protocol"
)

const (
	defaultDrainPollInterval = 250 * time.Millisecond
	defaultDrainTimeout      = 10 * time.Minute
	defaultWatchdogTimeout   = 30 * time.Second
	defaultRollbackCooldown  = 15 * time.Minute
)

// UpdateState persists self-update metadata and rollback/cooldown state in <DataDir>/update_state.json.
type UpdateState struct {
	RequestID     string `json:"request_id,omitempty"`
	TargetVersion string `json:"target_version,omitempty"`
	TargetSHA256  string `json:"target_sha256,omitempty"`
	PreviousPath  string `json:"previous_path,omitempty"`
	BackupPath    string `json:"backup_path,omitempty"`
	AttemptedAt   int64  `json:"attempted_at,omitempty"`
	FailedSHA256  string `json:"failed_sha256,omitempty"`
	CooldownUntil int64  `json:"cooldown_until,omitempty"`
	Status        string `json:"status,omitempty"` // "pending_verify" | "succeeded" | "rolled_back"
	LastError     string `json:"last_error,omitempty"`
}

// Updater coordinates idle draining, Release-first download with Server fallback,
// verification, atomic binary swap/restart, and startup watchdog rollback.
type Updater struct {
	node              *Node
	exePathFn         func() (string, error)
	smokeCheckFn      func(ctx context.Context, binPath string) error
	swapAndRestartFn  func(ctx context.Context, currentExe, stagedPath, backupPath string, state *UpdateState) error
	rollbackRestartFn func(ctx context.Context, currentExe, backupPath string, state *UpdateState) error
	drainPollInterval time.Duration
	drainTimeout      time.Duration
	watchdogTimeout   time.Duration
	cooldownDuration  time.Duration

	mu               sync.Mutex
	updating         bool
	forceImmediateCh chan struct{}
	watchdogCancel   context.CancelFunc
	watchdogDone     bool
	lastWelcome      *protocol.RunnerWelcomePayload
}

func newUpdater(n *Node) *Updater {
	return &Updater{
		node:              n,
		exePathFn:         resolvedExecutablePath,
		smokeCheckFn:      defaultSmokeCheckBinary,
		swapAndRestartFn:  n.platformSwapAndRestart,
		rollbackRestartFn: n.platformRollbackAndRestart,
		drainPollInterval: defaultDrainPollInterval,
		drainTimeout:      defaultDrainTimeout,
		watchdogTimeout:   defaultWatchdogTimeout,
		cooldownDuration:  defaultRollbackCooldown,
		forceImmediateCh:  make(chan struct{}, 1),
	}
}

func (u *Updater) stateFilePath() string {
	return filepath.Join(u.node.cfg.DataDir, "update_state.json")
}

func (u *Updater) loadState() UpdateState {
	raw, err := os.ReadFile(u.stateFilePath())
	if err != nil {
		return UpdateState{}
	}
	var st UpdateState
	if json.Unmarshal(raw, &st) != nil {
		return UpdateState{}
	}
	return st
}

func (u *Updater) saveState(st UpdateState) error {
	b, err := json.MarshalIndent(st, "", "  ")
	if err != nil {
		return err
	}
	tmp := u.stateFilePath() + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, u.stateFilePath())
}

func resolvedExecutablePath() (string, error) {
	exe, err := os.Executable()
	if err != nil {
		return "", err
	}
	if realPath, err := filepath.EvalSymlinks(exe); err == nil && realPath != "" {
		return realPath, nil
	}
	return filepath.Abs(exe)
}

func defaultSmokeCheckBinary(ctx context.Context, binPath string) error {
	sCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	out, err := exec.CommandContext(sCtx, binPath, "--version").CombinedOutput()
	if err != nil {
		return fmt.Errorf("smoke check --version failed: %w (%s)", err, strings.TrimSpace(string(out)))
	}
	return nil
}

// HandleWelcome evaluates whether a runner.welcome response should schedule an automatic self-update.
func (u *Updater) HandleWelcome(ctx context.Context, welcome protocol.RunnerWelcomePayload) {
	u.ConfirmHealthy(ctx)
	u.mu.Lock()
	cp := welcome
	cp.DownloadURLs = append([]string(nil), welcome.DownloadURLs...)
	u.lastWelcome = &cp
	u.mu.Unlock()

	if u.node.isDev {
		if welcome.UpdateStatus == "update_required" || welcome.UpdateStatus == "update_available" {
			log.Printf("nex-runner (%s): update available (%s -> %s, status=%s), auto-update disabled in dev mode; use manual upgrade",
				u.node.cfg.RunnerID, u.node.cfg.Version, welcome.TargetVersion, welcome.UpdateStatus)
		}
		return
	}
	if !u.node.autoUpdateEnabled() {
		return
	}
	if len(welcome.DownloadURLs) == 0 {
		return
	}

	var mode string
	switch welcome.UpdateStatus {
	case "update_required":
		mode = "immediate"
	case "update_available":
		if !welcome.AutoUpdateAllowed {
			return
		}
		mode = "when_idle"
	default:
		return
	}

	payload := protocol.RunnerUpdatePayload{
		RequestID:      fmt.Sprintf("auto_%d", time.Now().UnixMilli()),
		TargetVersion:  welcome.TargetVersion,
		ExpectedSHA256: welcome.TargetSHA256,
		DownloadURLs:   append([]string(nil), welcome.DownloadURLs...),
		Mode:           mode,
		Force:          false,
	}
	u.TriggerAsync(ctx, payload)
}

// HandleUpdateCommand processes an explicit server runner.update frame.
func (u *Updater) HandleUpdateCommand(ctx context.Context, payload protocol.RunnerUpdatePayload) {
	if u.node.isDev && !payload.Force {
		msg := "dev runner auto-update is disabled; use manual force update"
		log.Printf("nex-runner (%s): %s", u.node.cfg.RunnerID, msg)
		u.emitStatus(ctx, protocol.RunnerUpdateStatusPayload{
			RequestID:     payload.RequestID,
			Phase:         "failed",
			TargetVersion: payload.TargetVersion,
			Error:         msg,
		})
		return
	}
	if len(payload.DownloadURLs) == 0 {
		u.emitStatus(ctx, protocol.RunnerUpdateStatusPayload{
			RequestID:     payload.RequestID,
			Phase:         "failed",
			TargetVersion: payload.TargetVersion,
			Error:         "no download URLs provided",
		})
		return
	}
	u.TriggerAsync(ctx, payload)
}

// TriggerManualFromLastWelcome starts a forced manual update if the server advertised update URLs on last welcome.
func (u *Updater) TriggerManualFromLastWelcome(ctx context.Context) bool {
	u.mu.Lock()
	w := u.lastWelcome
	u.mu.Unlock()
	if w == nil || len(w.DownloadURLs) == 0 || (w.UpdateStatus != "update_available" && w.UpdateStatus != "update_required") {
		return false
	}
	u.TriggerAsync(ctx, protocol.RunnerUpdatePayload{
		RequestID:      fmt.Sprintf("manual_%d", time.Now().UnixMilli()),
		TargetVersion:  w.TargetVersion,
		ExpectedSHA256: w.TargetSHA256,
		DownloadURLs:   append([]string(nil), w.DownloadURLs...),
		Mode:           "when_idle",
		Force:          true,
	})
	return true
}

// TriggerAsync schedules an update attempt if not already updating and not in rollback cooldown (unless forced).
func (u *Updater) TriggerAsync(ctx context.Context, payload protocol.RunnerUpdatePayload) {
	st := u.loadState()
	if !payload.Force && payload.ExpectedSHA256 != "" && st.FailedSHA256 != "" &&
		strings.EqualFold(st.FailedSHA256, payload.ExpectedSHA256) && time.Now().UnixMilli() < st.CooldownUntil {
		log.Printf("nex-runner (%s): skipping update to %s due to rollback cooldown until %s",
			u.node.cfg.RunnerID, payload.TargetVersion, time.UnixMilli(st.CooldownUntil).Format(time.RFC3339))
		return
	}

	u.mu.Lock()
	if u.updating {
		u.mu.Unlock()
		if strings.TrimSpace(payload.Mode) == "immediate" && u.forceImmediateCh != nil {
			select {
			case u.forceImmediateCh <- struct{}{}:
			default:
			}
		}
		return
	}
	u.updating = true
	u.mu.Unlock()

	go u.runUpdate(ctx, payload)
}

func (u *Updater) blockingWorkload(includeAux bool) (int, string) {
	w := u.node.ActiveWorkload()
	if includeAux {
		return w.Total(), w.Describe(true)
	}
	return w.Turns, w.Describe(false)
}

func (u *Updater) runUpdate(ctx context.Context, payload protocol.RunnerUpdatePayload) {
	defer func() {
		u.mu.Lock()
		u.updating = false
		u.mu.Unlock()
	}()

drainForceCh:
	for {
		select {
		case <-u.forceImmediateCh:
		default:
			break drainForceCh
		}
	}

	mode := strings.TrimSpace(payload.Mode)
	if mode == "" {
		mode = "when_idle"
	}

	u.node.SetDraining(true)
	drainingClearedOnErr := true
	defer func() {
		if drainingClearedOnErr {
			u.node.SetDraining(false)
		}
	}()

	// Manual upgrades (Force: true) only drain active AI turns/sessions so lingering idle
	// PTYs or browser tabs never stall user-triggered upgrades for 10 minutes.
	includeAux := !payload.Force
	if count, desc := u.blockingWorkload(includeAux); mode == "when_idle" && count > 0 {
		msg := "waiting for active tasks to become idle"
		if desc != "" {
			msg = "waiting for " + desc
		}
		u.emitStatus(ctx, protocol.RunnerUpdateStatusPayload{
			RequestID:     payload.RequestID,
			Phase:         "draining",
			TargetVersion: payload.TargetVersion,
			Message:       msg,
		})
		if err := u.waitForIdle(ctx, includeAux); err != nil {
			u.emitStatus(ctx, protocol.RunnerUpdateStatusPayload{
				RequestID:     payload.RequestID,
				Phase:         "failed",
				TargetVersion: payload.TargetVersion,
				Error:         err.Error(),
			})
			return
		}
	}

	stagingDir := filepath.Join(u.node.cfg.DataDir, "staging")
	if err := os.MkdirAll(stagingDir, 0o755); err != nil {
		u.emitStatus(ctx, protocol.RunnerUpdateStatusPayload{
			RequestID:     payload.RequestID,
			Phase:         "failed",
			TargetVersion: payload.TargetVersion,
			Error:         err.Error(),
		})
		return
	}

	stagedName := "nex-runner.new"
	if runtime.GOOS == "windows" {
		stagedName = "nex-runner.new.exe"
	}
	stagedPath := filepath.Join(stagingDir, stagedName)

	actualSHA, err := u.downloadWithFallback(ctx, payload, stagedPath)
	if err != nil {
		u.emitStatus(ctx, protocol.RunnerUpdateStatusPayload{
			RequestID:     payload.RequestID,
			Phase:         "failed",
			TargetVersion: payload.TargetVersion,
			Error:         err.Error(),
		})
		return
	}

	u.emitStatus(ctx, protocol.RunnerUpdateStatusPayload{
		RequestID:     payload.RequestID,
		Phase:         "verifying",
		ProgressPct:   90,
		TargetVersion: payload.TargetVersion,
	})
	if err := u.smokeCheckFn(ctx, stagedPath); err != nil {
		_ = os.Remove(stagedPath)
		u.emitStatus(ctx, protocol.RunnerUpdateStatusPayload{
			RequestID:     payload.RequestID,
			Phase:         "failed",
			TargetVersion: payload.TargetVersion,
			Error:         err.Error(),
		})
		return
	}

	currentExe, err := u.exePathFn()
	if err != nil {
		_ = os.Remove(stagedPath)
		u.emitStatus(ctx, protocol.RunnerUpdateStatusPayload{
			RequestID:     payload.RequestID,
			Phase:         "failed",
			TargetVersion: payload.TargetVersion,
			Error:         err.Error(),
		})
		return
	}

	u.emitStatus(ctx, protocol.RunnerUpdateStatusPayload{
		RequestID:     payload.RequestID,
		Phase:         "installing",
		ProgressPct:   95,
		TargetVersion: payload.TargetVersion,
	})

	backupPath := currentExe + ".bak"
	if runtime.GOOS == "windows" {
		backupPath = currentExe + ".old"
	}

	prevState := u.loadState()
	nextState := UpdateState{
		RequestID:     payload.RequestID,
		TargetVersion: payload.TargetVersion,
		TargetSHA256:  actualSHA,
		PreviousPath:  currentExe,
		BackupPath:    backupPath,
		AttemptedAt:   time.Now().UnixMilli(),
		Status:        "pending_verify",
	}

	u.emitStatus(ctx, protocol.RunnerUpdateStatusPayload{
		RequestID:     payload.RequestID,
		Phase:         "restarting",
		ProgressPct:   100,
		TargetVersion: payload.TargetVersion,
	})

	drainingClearedOnErr = false
	if err := u.swapAndRestartFn(ctx, currentExe, stagedPath, backupPath, &nextState); err != nil {
		drainingClearedOnErr = true
		_ = u.saveState(prevState)
		u.emitStatus(ctx, protocol.RunnerUpdateStatusPayload{
			RequestID:     payload.RequestID,
			Phase:         "failed",
			TargetVersion: payload.TargetVersion,
			Error:         err.Error(),
		})
		return
	}
}

func (u *Updater) waitForIdle(ctx context.Context, includeAux bool) error {
	deadline := time.NewTimer(u.drainTimeout)
	defer deadline.Stop()
	ticker := time.NewTicker(u.drainPollInterval)
	defer ticker.Stop()

	for {
		count, desc := u.blockingWorkload(includeAux)
		if count == 0 {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-u.forceImmediateCh:
			return nil
		case <-deadline.C:
			if desc != "" {
				return fmt.Errorf("timed out waiting for runner workload to become idle (still active: %s)", desc)
			}
			return errors.New("timed out waiting for runner workload to become idle")
		case <-ticker.C:
		}
	}
}

func (u *Updater) downloadWithFallback(ctx context.Context, payload protocol.RunnerUpdatePayload, stagedPath string) (string, error) {
	var errs []string
	expected := strings.ToLower(strings.TrimSpace(payload.ExpectedSHA256))

	for idx, rawURL := range payload.DownloadURLs {
		targetURL, isServerOrigin, err := resolveDownloadURL(rawURL, u.node.cfg.ServerWSURL)
		if err != nil {
			errs = append(errs, fmt.Sprintf("url[%d]: %v", idx, err))
			continue
		}

		tmpPath := stagedPath + ".tmp"
		_ = os.Remove(tmpPath)

		gotSHA, dlErr := u.downloadOne(ctx, targetURL, isServerOrigin, tmpPath, payload)
		if dlErr != nil {
			_ = os.Remove(tmpPath)
			errs = append(errs, fmt.Sprintf("%s: %v", targetURL, dlErr))
			log.Printf("nex-runner (%s): download attempt from %s failed: %v", u.node.cfg.RunnerID, targetURL, dlErr)
			continue
		}
		if expected != "" && !strings.EqualFold(gotSHA, expected) {
			_ = os.Remove(tmpPath)
			errMsg := fmt.Sprintf("sha256 mismatch (got %s, want %s)", gotSHA, expected)
			errs = append(errs, fmt.Sprintf("%s: %s", targetURL, errMsg))
			log.Printf("nex-runner (%s): verification failed for %s: %s", u.node.cfg.RunnerID, targetURL, errMsg)
			continue
		}
		if err := os.Chmod(tmpPath, 0o755); err != nil {
			_ = os.Remove(tmpPath)
			errs = append(errs, err.Error())
			continue
		}
		if err := os.Rename(tmpPath, stagedPath); err != nil {
			_ = os.Remove(tmpPath)
			errs = append(errs, err.Error())
			continue
		}
		return gotSHA, nil
	}

	return "", fmt.Errorf("all download URLs failed: %s", strings.Join(errs, "; "))
}

func (u *Updater) downloadOne(ctx context.Context, targetURL string, isServerOrigin bool, destPath string, payload protocol.RunnerUpdatePayload) (string, error) {
	u.emitStatus(ctx, protocol.RunnerUpdateStatusPayload{
		RequestID:     payload.RequestID,
		Phase:         "downloading",
		ProgressPct:   0,
		TargetVersion: payload.TargetVersion,
	})

	dlCtx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()

	req, err := http.NewRequestWithContext(dlCtx, http.MethodGet, targetURL, nil)
	if err != nil {
		return "", err
	}
	// Never leak Runner AuthToken to external Release hosts; attach Bearer token only on Server fallback URLs.
	if isServerOrigin && u.node.cfg.AuthToken != "" {
		req.Header.Set("Authorization", "Bearer "+u.node.cfg.AuthToken)
	}

	resp, err := u.node.hc.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", fmt.Errorf("HTTP %d", resp.StatusCode)
	}

	f, err := os.OpenFile(destPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o755)
	if err != nil {
		return "", err
	}
	defer f.Close()

	hasher := sha256.New()
	total := resp.ContentLength
	var written int64
	lastPct := 0
	buf := make([]byte, 32*1024)
	for {
		n, rErr := resp.Body.Read(buf)
		if n > 0 {
			chunk := buf[:n]
			if _, wErr := f.Write(chunk); wErr != nil {
				return "", wErr
			}
			_, _ = hasher.Write(chunk)
			written += int64(n)
			if total > 0 {
				pct := int((written * 85) / total)
				if pct > 85 {
					pct = 85
				}
				if pct >= lastPct+10 {
					lastPct = pct
					u.emitStatus(ctx, protocol.RunnerUpdateStatusPayload{
						RequestID:     payload.RequestID,
						Phase:         "downloading",
						ProgressPct:   pct,
						TargetVersion: payload.TargetVersion,
					})
				}
			}
		}
		if rErr == io.EOF {
			break
		}
		if rErr != nil {
			return "", rErr
		}
	}
	if err := f.Sync(); err != nil {
		return "", err
	}
	if err := f.Close(); err != nil {
		return "", err
	}
	return hex.EncodeToString(hasher.Sum(nil)), nil
}

func resolveDownloadURL(rawURL, serverWSURL string) (string, bool, error) {
	trimmed := strings.TrimSpace(rawURL)
	if trimmed == "" {
		return "", false, errors.New("empty download url")
	}

	var serverHTTPBase *url.URL
	if strings.TrimSpace(serverWSURL) != "" {
		if wsParsed, err := url.Parse(strings.TrimSpace(serverWSURL)); err == nil {
			scheme := wsParsed.Scheme
			switch strings.ToLower(scheme) {
			case "ws":
				scheme = "http"
			case "wss":
				scheme = "https"
			}
			serverHTTPBase = &url.URL{
				Scheme: scheme,
				Host:   wsParsed.Host,
			}
		}
	}

	if strings.HasPrefix(trimmed, "/") {
		if serverHTTPBase == nil {
			return "", false, fmt.Errorf("cannot resolve relative URL %q without ServerWSURL", trimmed)
		}
		rel, err := url.Parse(trimmed)
		if err != nil {
			return "", false, err
		}
		return serverHTTPBase.ResolveReference(rel).String(), true, nil
	}

	parsed, err := url.Parse(trimmed)
	if err != nil {
		return "", false, err
	}
	isSameServer := serverHTTPBase != nil &&
		strings.EqualFold(parsed.Scheme, serverHTTPBase.Scheme) &&
		strings.EqualFold(parsed.Host, serverHTTPBase.Host)
	return parsed.String(), isSameServer, nil
}

// StartWatchdog checks pending update verification on startup and rolls back if the handshake
// fails to complete within watchdogTimeout.
func (u *Updater) StartWatchdog(ctx context.Context) {
	st := u.loadState()
	if st.Status != "pending_verify" {
		if st.BackupPath != "" {
			_ = os.Remove(st.BackupPath)
		}
		return
	}

	wCtx, cancel := context.WithCancel(ctx)
	u.mu.Lock()
	if u.watchdogCancel != nil {
		u.watchdogCancel()
	}
	u.watchdogCancel = cancel
	u.watchdogDone = false
	u.mu.Unlock()

	go func() {
		timer := time.NewTimer(u.watchdogTimeout)
		defer timer.Stop()
		select {
		case <-wCtx.Done():
			return
		case <-timer.C:
			u.mu.Lock()
			if u.watchdogDone {
				u.mu.Unlock()
				return
			}
			u.watchdogDone = true
			u.mu.Unlock()
			u.performRollback(st, "startup handshake watchdog timed out")
		}
	}()
}

// ConfirmHealthy marks a pending update as succeeded once runner.welcome completes.
func (u *Updater) ConfirmHealthy(ctx context.Context) {
	u.mu.Lock()
	u.watchdogDone = true
	if u.watchdogCancel != nil {
		u.watchdogCancel()
		u.watchdogCancel = nil
	}
	u.mu.Unlock()

	st := u.loadState()
	if st.Status != "pending_verify" {
		return
	}
	backup := st.BackupPath
	st.Status = "succeeded"
	st.LastError = ""
	st.BackupPath = ""
	_ = u.saveState(st)
	if backup != "" {
		_ = os.Remove(backup)
	}
	u.emitStatus(ctx, protocol.RunnerUpdateStatusPayload{
		RequestID:     st.RequestID,
		Phase:         "succeeded",
		ProgressPct:   100,
		TargetVersion: st.TargetVersion,
	})
}

func (u *Updater) performRollback(st UpdateState, reason string) {
	log.Printf("nex-runner (%s): rolling back failed update (%s): %s", u.node.cfg.RunnerID, st.TargetVersion, reason)
	st.Status = "rolled_back"
	st.FailedSHA256 = st.TargetSHA256
	st.CooldownUntil = time.Now().Add(u.cooldownDuration).UnixMilli()
	st.LastError = reason
	_ = u.saveState(st)

	if st.PreviousPath != "" && st.BackupPath != "" {
		if err := u.rollbackRestartFn(context.Background(), st.PreviousPath, st.BackupPath, &st); err != nil {
			log.Printf("nex-runner (%s): rollback restart failed: %v", u.node.cfg.RunnerID, err)
		}
	}
}

func (u *Updater) emitStatus(ctx context.Context, payload protocol.RunnerUpdateStatusPayload) {
	env, err := protocol.NewEnvelope(protocol.TypeRunnerUpdateStatus, payload)
	if err != nil {
		return
	}
	env.RunnerID = u.node.cfg.RunnerID
	_ = u.node.sendEnvelope(ctx, env)
}

func copyFile(src, dst string, mode os.FileMode) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()

	out, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, mode)
	if err != nil {
		return err
	}
	defer out.Close()

	if _, err := io.Copy(out, in); err != nil {
		return err
	}
	if err := out.Sync(); err != nil {
		return err
	}
	if err := out.Close(); err != nil {
		return err
	}
	return os.Chmod(dst, mode)
}
