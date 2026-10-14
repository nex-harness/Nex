// store.go: setting.json 读写。settingService.ts 的 readSettingsWithMeta /
// writeSettings 移植：损坏文件隔离备份、解析短重试、legacy 字段保留合并。
// 原子写与跨进程锁复用 step-3.1 的 fsx（与 TS atomicFileUtils.ts 同一
// <path>.lock 约定，Node/Go 过渡期互斥）。

package setting

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/tylinux/nex/go/internal/adapters/fsx"
)

// ReadSettingsResult is ReadSettingsResult.
type ReadSettingsResult struct {
	Settings              *Ordered
	NeedsMigrationPersist bool
}

// Store owns the setting.json file boundary.
type Store struct {
	// SettingsFile is the resolved setting.json path (host injects; tests
	// point it at a temp dir).
	SettingsFile string
	// Now is the clock for corrupt-backup names; nil uses time.Now.
	Now func() time.Time
	// RetrySleep is the parse-retry sleep; tests shorten it.
	RetrySleep func(time.Duration)
}

// SettingsDir returns the file's directory.
func (s *Store) SettingsDir() string { return filepath.Dir(s.SettingsFile) }

func (s *Store) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

func (s *Store) sleep(d time.Duration) {
	if s.RetrySleep != nil {
		s.RetrySleep(d)
		return
	}
	time.Sleep(d)
}

// ReadSettingsWithMeta ports readSettingsWithMeta. Missing/corrupt files yield
// defaults (a corrupt file is quarantined with a timestamped backup first).
func (s *Store) ReadSettingsWithMeta(ctx context.Context) ReadSettingsResult {
	rawValue, parseErr := s.readAndParseWithRetry()
	if parseErr != nil && IsReadMissing(parseErr) {
		return ReadSettingsResult{Settings: DefaultSettings()}
	}
	if parseErr != nil {
		// setting.json 可能正被另一次 update 覆盖写入，读者会短暂读到半截
		// JSON。连续重试失败才按坏文件隔离。
		s.quarantineCorruptSettingsFile(parseErr)
		return ReadSettingsResult{Settings: DefaultSettings()}
	}

	sanitized, validErr := ValidateSettings(rawValue)
	if validErr != nil {
		return ReadSettingsResult{Settings: DefaultSettings()}
	}
	return ReadSettingsResult{
		Settings:              NormalizeSettings(sanitized),
		NeedsMigrationPersist: shouldPersistSettingsMigrations(rawValue),
	}
}

// readAndParseWithRetry ports the read + parse + retry flow: one read, and on
// parse failure SETTINGS_PARSE_RETRY_COUNT re-reads after the delay.
func (s *Store) readAndParseWithRetry() (map[string]any, error) {
	readFile := func() ([]byte, error) { return os.ReadFile(s.SettingsFile) }
	data, err := readFile()
	if err != nil {
		// ENOENT → defaults (the caller maps any read error the same way the
		// TS does: read failure falls back to defaults without quarantine).
		return nil, errReadFailed{err}
	}
	parsed, parseErr := parseJSONObject(data)
	if parseErr == nil {
		return parsed, nil
	}
	lastErr := parseErr
	for attempt := 1; attempt <= SettingsParseRetryCount; attempt++ {
		s.sleep(time.Duration(SettingsParseRetryDelayMS) * time.Millisecond)
		retryData, readErr := readFile()
		if readErr != nil {
			return nil, errReadFailed{readErr}
		}
		if parsed, err := parseJSONObject(retryData); err == nil {
			return parsed, nil
		} else {
			lastErr = err
		}
	}
	return nil, lastErr
}

// errReadFailed marks a READ failure (no quarantine — defaults); distinct from
// a parse failure, which quarantines after the retries.
type errReadFailed struct{ err error }

func (e errReadFailed) Error() string { return e.err.Error() }
func (e errReadFailed) Unwrap() error { return e.err }

// IsReadMissing reports whether the store read failed for any reason the TS
// maps to "return defaults" without quarantining (ENOENT or other IO error).
func IsReadMissing(err error) bool {
	var readErr errReadFailed
	return errors.As(err, &readErr)
}

// parseJSONObject parses bytes as a JSON object; non-object JSON and broken
// JSON are both parse errors.
func parseJSONObject(data []byte) (map[string]any, error) {
	var probe any
	if err := json.Unmarshal(data, &probe); err != nil {
		return nil, err
	}
	obj, ok := probe.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("settings: not a JSON object")
	}
	return obj, nil
}

// quarantineCorruptSettingsFile ports quarantineCorruptSettingsFile: rename to
// setting.json.corrupt-<timestamp> (":" and "." replaced by "-"). ENOENT from
// a concurrent reader that quarantined first is tolerated.
func (s *Store) quarantineCorruptSettingsFile(cause error) {
	timestamp := s.now().UTC().Format("2006-01-02T15-04-05-000Z")
	backupPath := s.SettingsFile + ".corrupt-" + timestamp
	if err := os.Rename(s.SettingsFile, backupPath); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return
		}
		_ = cause
	}
}

// WriteSettingsOptions carries the queue's commit guards into the store write.
type WriteSettingsOptions struct {
	// ShouldCommit is the generation check (false → skip the rename).
	ShouldCommit func() bool
	// EnterCommitPhase disables the queue timeout (the rename critical
	// section must complete).
	EnterCommitPhase func()
	// RunExclusiveCommit serializes the rename through the commit queue.
	RunExclusiveCommit func(commit func() error) error
	// CommitAccountSelection: writing providerFamilyConnectionSelections
	// explicitly ends the legacy import hold.
	CommitAccountSelection bool
	// Fault injection for the filesystem-fault test suite.
	Injector fsx.Injector
}

// WriteSettings ports writeSettings: mkdir, legacy-field retention merge, then
// the atomic write with the queue's commit guards.
func (s *Store) WriteSettings(ctx context.Context, settings *Ordered, opts WriteSettingsOptions) error {
	if err := os.MkdirAll(s.SettingsDir(), 0o777); err != nil {
		return err
	}
	shouldCommit := opts.ShouldCommit
	if shouldCommit == nil {
		shouldCommit = func() bool { return true }
	}
	runExclusive := opts.RunExclusiveCommit
	if runExclusive == nil {
		runExclusive = func(commit func() error) error { return commit() }
	}

	raw, _ := os.ReadFile(s.SettingsFile)
	if len(raw) > 0 {
		if _, err := os.Stat(s.SettingsFile); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}
	var rawValue map[string]any
	if len(raw) > 0 {
		if parsed, err := ReadRawSettingsFile(raw); err == nil {
			rawValue = parsed
		}
	}

	rollbackFields := RetainLegacyAccountConnectionFields(rawValue)
	persisted := rollbackFields
	// settings fields ride after the retained legacy fields (probe16 order).
	for _, key := range settings.Keys() {
		value, _ := settings.Get(key)
		persisted.Set(key, value)
	}
	// 旧 Team 尚待 OAuth 补组织时，schema 的默认 {} 不是用户的新选择。
	if !opts.CommitAccountSelection && len(ReadIncompleteLegacyTeamConnections(rawValue)) > 0 {
		persisted.Delete("providerFamilyConnectionSelections")
	}

	content, err := persisted.WriteJSONString()
	if err != nil {
		return err
	}

	useLock := true
	writeOpts := &fsx.AtomicWriteOptions{
		UseFileLock: &useLock,
		Injector:    opts.Injector,
	}
	if opts.EnterCommitPhase != nil {
		writeOpts.BeforeRename = func() error {
			if !shouldCommit() {
				// 提交前超时的旧写只能清理临时文件，不能晚到 rename 覆盖新语言偏好。
				return errors.New("stale settings write skipped before atomic rename")
			}
			opts.EnterCommitPhase()
			return nil
		}
	} else {
		writeOpts.BeforeRename = func() error {
			if !shouldCommit() {
				return errors.New("stale settings write skipped before atomic rename")
			}
			return nil
		}
	}
	writeOpts.RunRename = func(renameFile func() error) error {
		return runExclusive(func() error {
			if !shouldCommit() {
				return errors.New("stale settings write skipped before atomic rename")
			}
			return renameFile()
		})
	}
	return fsx.AtomicWriteText(ctx, s.SettingsFile, content, writeOpts)
}

// shouldPersistSettingsMigrations ports shouldPersistSettingsMigrations.
func shouldPersistSettingsMigrations(rawValue any) bool {
	raw := legacyRecord(rawValue)
	if len(raw) == 0 {
		return false
	}
	if NeedsLegacyAccountConnectionMigration(rawValue) &&
		len(ReadIncompleteLegacyTeamConnections(rawValue)) == 0 {
		return true
	}
	if v, ok := raw["closeToTrayOnWindowsMigrationInitialized"].(bool); !ok || v != true {
		return true
	}
	if v, ok := raw["messageStreamShowReasoningMigrationInitialized"].(bool); !ok || v != true {
		return true
	}
	return false
}
