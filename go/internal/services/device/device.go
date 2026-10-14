// Package device ports packages/services/src/device/deviceMid.ts (step 3.5):
// ensureDeviceMid — the cross-process device identity over
// {appConfigDir}/telemetry-state.json guarded by telemetry-state.lock.
//
// `telemetry-state.json` 是设备身份文件沿用至今的磁盘文件名：CLI、Desktop、
// 远端 server 都读写同一路径与字段，改名等于重置设备身份。
package device

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/tylinux/nex/go/internal/services/paths"
)

// Lock timing constants, verbatim from deviceMid.ts.
const (
	LockRetryDelayMS = 10
	LockRetryCount   = 200
	LockStaleMS      = 5 * time.Minute
)

// deviceState is DeviceState.
type deviceState struct {
	DeviceMid string `json:"deviceMid,omitempty"`
}

// deviceStateLockOwner is DeviceStateLockOwner.
type deviceStateLockOwner struct {
	PID       int64 `json:"pid"`
	CreatedAt int64 `json:"createdAt"`
}

// Options configures EnsureDeviceMid.
type Options struct {
	// HomeDir: when set, {home}/.nex/v2/telemetry-state.json (the TS's
	// explicit-home branch); empty uses the paths package resolution.
	HomeDir string
	// Paths owns app-config-dir resolution when HomeDir is empty.
	Paths *paths.Paths
	// RandomUUID is the generator seam (tests).
	RandomUUID func() string
	// Now/Sleep are the clock/retry seams (tests).
	Now   func() time.Time
	Sleep func(time.Duration)
}

type ensureOptions struct {
	Options
}

// cacheEntry is the per-state-file memo (deviceMidCacheByStateFile).
type cacheEntry struct {
	mid string
}

var (
	cacheMu    sync.Mutex
	stateCache = map[string]*cacheEntry{}
)

func withCache(fn func()) {
	cacheMu.Lock()
	defer cacheMu.Unlock()
	fn()
}

// StateFilePath resolves the device state file path (exported for the
// onboarding/telemetry consumers that need the same file).
func StateFilePath(homeDir string) string {
	if homeDir != "" {
		return filepath.Join(homeDir, ".nex", "v2", "telemetry-state.json")
	}
	return filepath.Join(paths.NewFromProcess().AppConfigDir(), "telemetry-state.json")
}

// EnsureDeviceMid ports ensureDeviceMid: 确保设备身份文件里存在 deviceMid 并返回它.
func EnsureDeviceMid(ctx context.Context, opts Options) (string, error) {
	if opts.RandomUUID == nil {
		opts.RandomUUID = func() string { return uuid.NewString() }
	}
	if opts.Now == nil {
		opts.Now = time.Now
	}
	if opts.Sleep == nil {
		opts.Sleep = time.Sleep
	}
	stateFile := StateFilePath(opts.HomeDir)

	// 快路径：同一进程内已解析过该文件则直接复用（deviceMidCacheByStateFile）。
	if mid, cached := cachedMid(stateFile); cached {
		return mid, nil
	}

	mid, err := withDeviceStateLock(ctx, opts, stateFile, func(state deviceState) (deviceState, error) {
		if state.DeviceMid != "" {
			return state, nil
		}
		state.DeviceMid = opts.RandomUUID()
		return state, nil
	})
	if err != nil {
		// 失败时清除缓存槽位，下次调用重新抢锁.
		withCache(func() { delete(stateCache, stateFile) })
		return "", err
	}
	withCache(func() {
		stateCache[stateFile] = &cacheEntry{mid: mid}
	})
	return mid, nil
}

func cachedMid(stateFile string) (string, bool) {
	cached := ""
	withCache(func() {
		if entry, ok := stateCache[stateFile]; ok && entry.mid != "" {
			cached = entry.mid
		}
	})
	return cached, cached != ""
}

// withDeviceStateLock ports withDeviceStateLock: exclusive-create lock file
// with owner pid, stale recovery, bounded retries.
func withDeviceStateLock(ctx context.Context, opts Options, stateFile string, run func(deviceState) (deviceState, error)) (string, error) {
	lockFile := lockFilePath(stateFile)
	if err := os.MkdirAll(filepath.Dir(lockFile), 0o777); err != nil {
		return "", err
	}
	for attempt := 0; attempt < LockRetryCount; attempt++ {
		if err := ctx.Err(); err != nil {
			return "", err
		}
		f, err := os.OpenFile(lockFile, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o666)
		if err != nil {
			if !errors.Is(err, os.ErrExist) {
				return "", err
			}
			// 锁写入 owner pid，崩溃后 5 分钟内即可判定孤儿锁并被安全回收.
			removedStale := removeStaleDeviceStateLockIfNeeded(lockFile, opts.Now())
			if removedStale {
				continue
			}
			opts.Sleep(time.Duration(LockRetryDelayMS) * time.Millisecond)
			continue
		}
		// Hold the lock: read state, run, write back, release.
		mid, runErr := func() (string, error) {
			defer func() {
				f.Close()
				_ = os.Remove(lockFile)
			}()
			payload, _ := json.Marshal(deviceStateLockOwner{PID: int64(os.Getpid()), CreatedAt: opts.Now().UnixMilli()})
			if _, err := f.Write(payload); err != nil {
				return "", err
			}
			state, err := readDeviceState(stateFile)
			if err != nil {
				return "", err
			}
			next, err := run(state)
			if err != nil {
				return "", err
			}
			if err := writeDeviceState(stateFile, next); err != nil {
				return "", err
			}
			return next.DeviceMid, nil
		}()
		if runErr != nil {
			return "", runErr
		}
		return mid, nil
	}
	return "", errors.New("Device state lock timeout")
}

func lockFilePath(stateFile string) string {
	return filepath.Join(filepath.Dir(stateFile), "telemetry-state.lock")
}

// removeStaleDeviceStateLockIfNeeded ports
// removeStaleDeviceStateLockIfNeeded: a lock older than LOCK_STALE_MS whose
// owner pid is dead is reclaimed; a fresh lock with a live owner is not.
func removeStaleDeviceStateLockIfNeeded(lockFile string, now time.Time) bool {
	info, err := os.Stat(lockFile)
	if err != nil {
		return false
	}
	if now.Sub(info.ModTime()) < LockStaleMS {
		owner, ok := readDeviceStateLockOwner(lockFile)
		if !ok || isProcessAlive(owner.PID) {
			return false
		}
	}
	_ = os.Remove(lockFile)
	return true
}

func readDeviceStateLockOwner(lockFile string) (deviceStateLockOwner, bool) {
	data, err := os.ReadFile(lockFile)
	if err != nil {
		return deviceStateLockOwner{}, false
	}
	var owner deviceStateLockOwner
	if err := json.Unmarshal(data, &owner); err != nil {
		return deviceStateLockOwner{}, false
	}
	if owner.PID <= 0 {
		return deviceStateLockOwner{}, false
	}
	return owner, true
}

// isProcessAlive ports isProcessAlive (signal 0 probe; the platform probe
// lives in kill_unix.go / kill_windows.go).
func isProcessAlive(pid int64) bool {
	if pid <= 0 {
		return false
	}
	err := signalProbe(pid)
	if err == nil {
		return true
	}
	return !errors.Is(err, errNoProcess)
}

func readDeviceState(stateFile string) (deviceState, error) {
	data, err := os.ReadFile(stateFile)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return deviceState{}, nil
		}
		return deviceState{}, err
	}
	var probe any
	if err := json.Unmarshal(data, &probe); err != nil {
		return deviceState{}, nil
	}
	if obj, ok := probe.(map[string]any); ok {
		if mid, ok := obj["deviceMid"].(string); ok {
			return deviceState{DeviceMid: mid}, nil
		}
	}
	return deviceState{}, nil
}

func writeDeviceState(stateFile string, state deviceState) error {
	if err := os.MkdirAll(filepath.Dir(stateFile), 0o777); err != nil {
		return err
	}
	data, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(stateFile, data, 0o644)
}

var _ = fmt.Sprintf
