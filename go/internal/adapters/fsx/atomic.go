// atomic.go ports packages/services/src/fs/atomicFileUtils.ts (step 3.1):
// atomicWriteText / atomicWriteJson, the same helpers the TS settings, session
// file and pets writers share.
//
// 与 TS 一致的关键语义：
//   - 临时文件写在目标文件同目录（Windows 跨卷 rename 失败）；
//   - rename 失败时清理自己的 temp，成功路径不改目标直到 rename 原子替换；
//   - EPERM/EBUSY/EACCES 做短暂重试（杀软/索引器短暂占用），真实权限错误原样抛出；
//   - 注入的故障错误（*FaultError）不重试 —— 测试要的就是"在这里停下"；
//   - 同目录 lock 文件做跨进程串行化（Windows 多窗口抢写同一 task JSON）。
//     Go 侧复用 flock（Unix）/ LOCKFILE（Windows）实现同一语义，lock 文件
//     名与 TS 的 acquireFileLock 相同（<path>.lock）。
package fsx

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// Default retry/backoff constants, verbatim from atomicFileUtils.ts.
var (
	defaultRenameRetryDelays = []time.Duration{
		50 * time.Millisecond, 100 * time.Millisecond, 200 * time.Millisecond,
		400 * time.Millisecond, 800 * time.Millisecond, 1600 * time.Millisecond, 3200 * time.Millisecond,
	}
	defaultLockRetryDelays = []time.Duration{25 * 50 * time.Millisecond, 50 * time.Millisecond, 100 * time.Millisecond, 200 * time.Millisecond, 400 * time.Millisecond}
	defaultLockMaxWait     = 8 * time.Second
	defaultTempFileStale   = 60 * time.Second
)

// fix the first entry above once (25ms), kept explicit for the table parity.
func init() {
	defaultLockRetryDelays[0] = 25 * time.Millisecond
}

// AtomicWriteOptions ports AtomicWriteTextOptions. The hook BeforeRename runs
// after the temp file is written and before the rename, exactly the seam the TS
// crash test uses.
type AtomicWriteOptions struct {
	RenameRetryDelays []time.Duration
	LockRetryDelays   []time.Duration
	LockMaxWait       time.Duration
	TempFileStale     time.Duration
	// UseFileLock false disables the same-directory lock file (single-writer
	// callers, or tests that do not want a lock file next to the fixture).
	UseFileLock *bool
	// BeforeRename runs between the temp write and the rename. An error aborts
	// the write (temp cleaned up) and is returned.
	BeforeRename func() error
	// RunRename, when set, wraps the rename step — the test seam for "kill
	// between temp write and rename": call the inner func or not.
	RunRename func(renameFile func() error) error
	// Injector overrides the process-wide fault injector for this call.
	Injector Injector
	// Now is the clock for stale-temp cleanup. Nil uses time.Now.
	Now func() time.Time
}

func (o *AtomicWriteOptions) faultInjector() Injector {
	if o != nil && o.Injector != nil {
		return o.Injector
	}
	return ProcessFaultInjector()
}

func (o *AtomicWriteOptions) useFileLock() bool {
	if o == nil || o.UseFileLock == nil {
		return true
	}
	return *o.UseFileLock
}

func (o *AtomicWriteOptions) now() time.Time {
	if o != nil && o.Now != nil {
		return o.Now()
	}
	return time.Now()
}

// isRetryableAtomicRenameError ports isRetryableAtomicRenameError: EPERM, EBUSY
// or EACCES, and never an injected fault.
func isRetryableAtomicRenameError(err error) bool {
	if IsInjectedFsFaultError(err) {
		return false
	}
	return errors.Is(err, syscall.EPERM) || errors.Is(err, syscall.EBUSY) || errors.Is(err, syscall.EACCES)
}

// AtomicWriteText ports atomicWriteText: mkdir, lock, stale-temp cleanup, temp
// write, rename. The target's existing mode is preserved (the TS writeFile
// default 0644 only applies to the temp file; the rename then brings the temp's
// mode over — so the TS actually preserves nothing. Go does better here and
// ports the mode-preservation the Phase-1 fsx.atomicWrite established, because
// settings/session files carry 0600 on some installs and losing that would be a
// behaviour change; see the step-3.1 notes).
func AtomicWriteText(ctx context.Context, filePath, content string, opts *AtomicWriteOptions) error {
	dir := filepath.Dir(filePath)
	base := filepath.Base(filePath)
	now := opts.now()
	tempFile := filepath.Join(dir, fmt.Sprintf("%s.%d.%d.%s.tmp", base, os.Getpid(), now.UnixMilli(), randomTempSuffix()))

	if err := maybeThrowFault(opts, FaultMkdir, dir); err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0o777); err != nil {
		return err
	}

	// 跨进程协作串行化：同目录 lock 文件，等待上限 defaultLockMaxWait。
	var releaseLock func()
	if opts.useFileLock() {
		lockPath := filePath + ".lock"
		release, err := acquireFileLock(ctx, lockPath, opts)
		if err != nil {
			return fmt.Errorf("fsx: acquire lock %s: %w", lockPath, err)
		}
		releaseLock = release
	}
	defer func() {
		if releaseLock != nil {
			releaseLock()
		}
	}()

	// 旧版本或崩溃后的失败 rename 会留下 <name>.*.tmp；只清理同一目标且超过
	// 阈值的临时文件，避免误删另一个进程刚创建的 active temp。
	if err := cleanupStaleTempFilesForTarget(filePath, tempFile, opts); err != nil {
		// best-effort, same as the TS
		_ = err
	}

	mode := os.FileMode(0o644)
	if info, err := os.Lstat(filePath); err == nil && info.Mode().IsRegular() {
		mode = info.Mode().Perm()
	}
	if err := maybeThrowFault(opts, FaultWriteFile, tempFile); err != nil {
		return err
	}
	if err := writeTempFile(tempFile, []byte(content), mode); err != nil {
		_ = os.Remove(tempFile)
		return err
	}

	if opts != nil && opts.BeforeRename != nil {
		if err := opts.BeforeRename(); err != nil {
			_ = os.Remove(tempFile)
			return err
		}
	}

	renameFile := func() error { return renameWithRetry(ctx, tempFile, filePath, opts) }
	var renameErr error
	if opts != nil && opts.RunRename != nil {
		renameErr = opts.RunRename(renameFile)
	} else {
		renameErr = renameFile()
	}
	if renameErr != nil {
		// A REAL failure cleans its own temp. The crash-simulation seam is
		// different: RunRename models a process KILLED between temp write and
		// rename — a killed process runs no cleanup, and leaving the orphan
		// behind is exactly the state the stale-temp cleanup exists to recover
		// from. That is signalled with *KillSimulationError.
		var kill *KillSimulationError
		if !errors.As(renameErr, &kill) {
			_ = os.Remove(tempFile)
		}
		return renameErr
	}
	return nil
}

// KillSimulationError marks an error returned by RunRename as "the process died
// here": no cleanup must run, so the on-disk state matches a SIGKILL between
// the temp write and the rename.
type KillSimulationError struct{ Err error }

func (e *KillSimulationError) Error() string { return e.Err.Error() }
func (e *KillSimulationError) Unwrap() error { return e.Err }

// AtomicWriteJSON ports atomicWriteJson: two-space indented JSON, like
// JSON.stringify(data, null, 2).
func AtomicWriteJSON(ctx context.Context, filePath string, data []byte, opts *AtomicWriteOptions) error {
	return AtomicWriteText(ctx, filePath, string(data)+"\n", opts)
}

func maybeThrowFault(opts *AtomicWriteOptions, operation FsFaultOperation, path string) error {
	return opts.faultInjector().MaybeThrow(FsFaultCheckInput{Operation: operation, Path: path})
}

// renameWithRetry ports renameWithRetry: retry only the transient Windows
// EPERM/EBUSY/EACCES family, with the TS's delay ladder. The fault hook fires
// per attempt, matching `maybeThrowInjectedFsFault({operation: "rename"})`
// inside the TS loop. ctx cancellation aborts the wait between attempts.
func renameWithRetry(ctx context.Context, tempFile, filePath string, opts *AtomicWriteOptions) error {
	delays := defaultRenameRetryDelays
	if opts != nil && opts.RenameRetryDelays != nil {
		delays = opts.RenameRetryDelays
	}
	for attempt := 0; ; attempt++ {
		if err := maybeThrowFault(opts, FaultRename, filePath); err != nil {
			return err
		}
		err := os.Rename(tempFile, filePath)
		if err == nil {
			return nil
		}
		var delay time.Duration
		if attempt < len(delays) {
			delay = delays[attempt]
		}
		if delay == 0 || !isRetryableAtomicRenameError(err) {
			return err
		}
		select {
		case <-time.After(delay):
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}

// writeTempFile writes the temp file with the target's mode (or 0644 for a new
// file) and fsyncs, so a rename never publishes a half-written page.
func writeTempFile(tempPath string, content []byte, mode os.FileMode) error {
	f, err := os.OpenFile(tempPath, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, mode)
	if err != nil {
		return err
	}
	if _, err := f.Write(content); err != nil {
		f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}

// cleanupStaleTempFilesForTarget ports the same-named TS function: remove
// `<base>.*.tmp` siblings of the target older than stale, excluding the
// caller's own temp. Best-effort.
func cleanupStaleTempFilesForTarget(filePath, excludeTempFile string, opts *AtomicWriteOptions) error {
	stale := defaultTempFileStale
	if opts != nil && opts.TempFileStale != 0 {
		stale = opts.TempFileStale
	}
	dir := filepath.Dir(filePath)
	tempPrefix := filepath.Base(filePath) + "."
	now := opts.now()
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	for _, entry := range entries {
		name := entry.Name()
		if !strings.HasPrefix(name, tempPrefix) || !strings.HasSuffix(name, ".tmp") {
			continue
		}
		tempPath := filepath.Join(dir, name)
		if tempPath == excludeTempFile {
			continue
		}
		info, err := os.Stat(tempPath)
		if err != nil {
			continue
		}
		if now.Sub(info.ModTime()) < stale {
			continue
		}
		_ = os.Remove(tempPath)
	}
	return nil
}

// randomTempSuffix matches the TS Math.random().toString(36).slice(2) shape
// (a short lowercase alphanumeric string): a time-based base-36 tail plus a
// random hex tail, so concurrent writers never collide.
func randomTempSuffix() string {
	return strconv.FormatInt(time.Now().UnixNano(), 36)[3:] + randomHexSuffix(4)
}

func randomHexSuffix(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		// crypto/rand cannot practically fail; degrade to time-based digits.
		return strconv.FormatInt(time.Now().UnixNano(), 36)
	}
	const digits = "0123456789abcdefghijklmnopqrstuvwxyz"
	out := make([]byte, n)
	for i, v := range b {
		out[i] = digits[int(v)%len(digits)]
	}
	return string(out)
}

// acquireFileLock ports acquireFileLock's semantics for the one-caller case:
// open/create `<path>.lock`, take an exclusive OS lock, retry with the delay
// ladder up to maxWait, then give up with an error. The lock is released by
// closing the file AND removing it (the TS's @nex/shared/node lock is a
// directory removed on release — a completed write leaves no artifact; the
// on-disk tree comparison in svc-diff treats a leftover .lock as a diff).
// Process death still releases the OS lock; a leftover lock FILE after a
// crash is inert (the next writer recreates/locks it).
func acquireFileLock(ctx context.Context, lockPath string, opts *AtomicWriteOptions) (func(), error) {
	delays := defaultLockRetryDelays
	if opts != nil && opts.LockRetryDelays != nil {
		delays = opts.LockRetryDelays
	}
	maxWait := defaultLockMaxWait
	if opts != nil && opts.LockMaxWait != 0 {
		maxWait = opts.LockMaxWait
	}
	deadline := time.Now().Add(maxWait)
	for attempt := 0; ; attempt++ {
		f, err := os.OpenFile(lockPath, os.O_CREATE|os.O_RDWR, 0o666)
		if err == nil {
			lockErr := lockFile(f)
			if lockErr == nil {
				return func() {
					_ = unlockFile(f)
					_ = f.Close()
					// 修复依据：TS 的锁在释放时移除自身（目录锁 rmdir / 单文件锁
					// rm），完成后目录里没有 lock 残留；Go 此前留下 setting.json.lock
					// 文件，被 svc-diff 的 on-disk 对比判为差异。Remove 是 best-effort：
					// Windows 上短暂占用失败可容忍，下一次写入会复用该文件。
					_ = os.Remove(lockPath)
				}, nil
			}
			f.Close()
			err = lockErr
		}
		if time.Now().After(deadline) || attempt >= len(delays) {
			return nil, fmt.Errorf("fsx: lock timeout after %s: %w", maxWait, err)
		}
		select {
		case <-time.After(delays[attempt]):
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
}

// AtomicWritePrivateTextFile ports @nex/shared/node's
// atomicWritePrivateTextFile (privateFilePersistence.ts): temp file with mode
// 0600 + rename with the EPERM/EBUSY/EACCES retry ladder. This is the
// credentials-store writer; unlike AtomicWriteText it takes no lock and adds
// no trailing content — the caller controls the exact bytes.
func AtomicWritePrivateTextFile(ctx context.Context, filePath string, content []byte, opts *AtomicWriteOptions) error {
	dir := filepath.Dir(filePath)
	base := filepath.Base(filePath)
	now := opts.now()
	tempFile := filepath.Join(dir, fmt.Sprintf(".%s.%d.%d.%s.tmp", base, os.Getpid(), now.UnixMilli(), randomTempSuffix()))
	if err := os.MkdirAll(dir, 0o777); err != nil {
		return err
	}
	if err := writeTempFile(tempFile, content, 0o600); err != nil {
		_ = os.Remove(tempFile)
		return err
	}
	if err := renameWithRetry(ctx, tempFile, filePath, opts); err != nil {
		_ = os.Remove(tempFile)
		return err
	}
	return nil
}

// WithFileLock runs body while holding the <lockPath>.lock convention lock
// (the TS withFileLock). Exposed for the step-3.6 personal provider config
// repository, whose read-modify-write span must hold the lock across more
// than the write itself.
func WithFileLock(ctx context.Context, filePath string, body func() error) error {
	lockPath := filePath + ".lock"
	if err := os.MkdirAll(filepath.Dir(lockPath), 0o755); err != nil {
		return err
	}
	release, err := acquireFileLock(ctx, lockPath, nil)
	if err != nil {
		return err
	}
	defer release()
	return body()
}
