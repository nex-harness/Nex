// service.go ports createSettingServiceWithMigrations: the ISettingService
// surface (get / update / updateDataBaseDir / ensureDefaultProject) plus the
// observable wrapper's listener set (onDidUpdate, used by the Host for
// broadcast fan-out; not part of the RPC contract).
package setting

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/tylinux/nex/go/internal/adapters/fsx"
	"github.com/tylinux/nex/go/internal/services/paths"
)

// ServiceOptions configures NewService.
type ServiceOptions struct {
	// SettingsFile: the setting.json path. Empty resolves
	// {home}/.nex/v2/setting.json with home from
	// NEX_DESKTOP_HOME_DIR > HOME > USERPROFILE (resolveUserHomeDir).
	SettingsFile string
	// Paths owns the data-base-dir resolution for updateDataBaseDir.
	// Nil builds one from the process environment.
	Paths *paths.Paths
	// QueueTimeoutMs overrides the write-queue timeout (tests).
	QueueTimeoutMs int64
	// RetrySleep shortens the parse-retry delay (tests).
	RetrySleep func(time.Duration)
	// CopyDataDir is the copyDataDirectory seam (updateDataBaseDir tests
	// inject fault scenarios). Nil uses CopyDataDirectory.
	CopyDataDir func(ctx context.Context, oldBaseDir, newBaseDir string) error
	// ValidateTarget is the validateDataBaseDirTarget seam; nil uses the
	// paths package's Windows rules against the process env.
	ValidateTarget func(targetBaseDir string) paths.ValidateDataBaseDirTargetResult
	// EnvSnapshot supplies env reads (NEX_DATA_BASE_DIR, HOME, USERPROFILE,
	// NEX_WINDOWS_APP_INSTALL_DIR). Nil reads the process env.
	EnvSnapshot func() map[string]string
	// Injector is the fs fault-injection seam (tests).
	Injector fsx.Injector
}

// Service is ISettingService.
type Service struct {
	store       *Store
	paths       *paths.Paths
	queue       *Queue
	opts        ServiceOptions
	listenersMu sync.Mutex
	listeners   map[int]func(keys []string)
	listenerSeq int
}

// NewService builds the service. The queue and store are per-service, like
// the TS closure state in createSettingServiceWithMigrations.
func NewService(opts ServiceOptions) *Service {
	if opts.SettingsFile == "" {
		opts.SettingsFile = DefaultSettingsFilePath()
	}
	if opts.Paths == nil {
		opts.Paths = paths.NewFromProcess()
	}
	store := &Store{
		SettingsFile: opts.SettingsFile,
		RetrySleep:   opts.RetrySleep,
	}
	queue := NewQueue(QueueOptions{TimeoutMs: opts.QueueTimeoutMs})
	return &Service{
		store:     store,
		paths:     opts.Paths,
		queue:     queue,
		opts:      opts,
		listeners: map[int]func(keys []string){},
	}
}

// DefaultSettingsFilePath ports getSettingsFile via resolveUserHomeDir.
func DefaultSettingsFilePath() string {
	home := resolveUserHomeDir(envMap(nil))
	return filepath.Join(home, ".nex", "v2", "setting.json")
}

// envMap reads the process env into a map (nil snapshot reads os.Environ).
func envMap(snapshot func() map[string]string) map[string]string {
	if snapshot != nil {
		return snapshot()
	}
	out := map[string]string{}
	for _, entry := range os.Environ() {
		for i := 0; i < len(entry); i++ {
			if entry[i] == '=' {
				out[entry[:i]] = entry[i+1:]
				break
			}
		}
	}
	return out
}

// resolveUserHomeDir ports resolveUserHomeDir: NEX_DESKTOP_HOME_DIR > HOME >
// USERPROFILE. (独立桌面 Dev 实例已设置自己的 home；与 Electron 的显式 home
// 覆盖保持一致，避免启动迁移污染其他实例。)
func resolveUserHomeDir(env map[string]string) string {
	for _, key := range []string{"NEX_DESKTOP_HOME_DIR", "HOME", "USERPROFILE"} {
		if v := trimSpace(env[key]); v != "" {
			return v
		}
	}
	if home, err := os.UserHomeDir(); err == nil {
		return home
	}
	return ""
}

// SettingsFile exposes the resolved path (tests / the sync service).
func (s *Service) SettingsFile() string { return s.opts.SettingsFile }

// Get ports get(): wait for the write queue, read, persist pending migrations.
func (s *Service) Get(ctx context.Context) (*Ordered, error) {
	// 设置切换后可能立即创建或冷恢复 Session；读取若越过已入队写入，
	// runtime 会固定旧开关值。先等待现有写队列。
	if err := s.queue.WaitUpdateQueue(ctx); err != nil {
		return nil, err
	}
	result := s.store.ReadSettingsWithMeta(ctx)
	if !result.NeedsMigrationPersist {
		return result.Settings, nil
	}
	if err := s.queue.EnqueueSettingsWrite(ctx, func(shouldCommit func() bool, enterCommitPhase func()) error {
		latest := s.store.ReadSettingsWithMeta(ctx)
		if !latest.NeedsMigrationPersist {
			return nil
		}
		// 迁移写盘必须进入 updateQueue，并在队列内重读最新文件，避免覆盖并发保存的其他设置。
		return s.store.WriteSettings(ctx, latest.Settings, WriteSettingsOptions{
			ShouldCommit:       shouldCommit,
			EnterCommitPhase:   enterCommitPhase,
			RunExclusiveCommit: s.queue.RunExclusiveCommit,
			Injector:           s.opts.Injector,
		})
	}); err != nil {
		return nil, err
	}
	final := s.store.ReadSettingsWithMeta(ctx)
	return final.Settings, nil
}

// ExpectedAccountSettings is the expectedAccountSettings parameter: the two
// account connection fields update() re-verifies inside the write queue.
type ExpectedAccountSettings struct {
	ProviderFamilyDomain               string
	ProviderFamilyDomainSet            bool
	ProviderFamilyConnectionSelections map[string]any
}

// Update ports update(patch, expectedAccountSettings?).
func (s *Service) Update(ctx context.Context, patch map[string]any, expected *ExpectedAccountSettings) error {
	return s.queue.EnqueueSettingsWrite(ctx, func(shouldCommit func() bool, enterCommitPhase func()) error {
		validatedPatch := NormalizePatch(patch)
		if err := ValidatePatch(validatedPatch); err != nil {
			return err
		}
		current := s.store.ReadSettingsWithMeta(ctx).Settings
		if expected != nil {
			// 账号查询期间用户可能已手动切换。必须在同一写队列内校验。
			expectedDomain := ""
			if expected.ProviderFamilyDomainSet {
				expectedDomain = expected.ProviderFamilyDomain
			}
			currentDomain, _ := current.Get("providerFamilyDomain")
			currentDomainStr, _ := currentDomain.(string)
			if currentDomainStr != expectedDomain ||
				!settingsEqualJSON(current.Value("providerFamilyConnectionSelections"), expected.ProviderFamilyConnectionSelections) {
				return errors.New("Account connection settings changed")
			}
		}
		merged := MergePatch(current, validatedPatch)
		// 打开工作区后会几乎同时写 recentProjects 和 lastWorkspaceSession。
		// 写入串行化，让每个补丁都基于上一次真正落盘后的最新状态继续合并。
		if projects, ok := merged.Get("recentProjects"); ok {
			merged.Set("recentProjects", DedupeRecentProjects(projects.([]any)))
		}
		_, hasSelection := patch["providerFamilyConnectionSelections"]
		if err := s.store.WriteSettings(ctx, merged, WriteSettingsOptions{
			ShouldCommit:           shouldCommit,
			EnterCommitPhase:       enterCommitPhase,
			RunExclusiveCommit:     s.queue.RunExclusiveCommit,
			CommitAccountSelection: hasSelection,
			Injector:               s.opts.Injector,
		}); err != nil {
			return err
		}
		s.notifyDidUpdate(patch)
		return nil
	})
}

// UpdateDataBaseDir ports updateDataBaseDir: validate, copy the whole data
// directory, then persist the setting. 本方法是 tier 内最后移植的一环，
// 带有文件系统故障测试套件（磁盘满、跨设备、部分拷贝、并发写者）。
func (s *Service) UpdateDataBaseDir(ctx context.Context, newDir *string) error {
	env := envMap(s.opts.EnvSnapshot)
	currentBaseDir := s.paths.DataBaseDir()
	targetBaseDir := func() string {
		if newDir != nil {
			if trimmed := trimSpace(*newDir); trimmed != "" {
				return trimmed
			}
		}
		if home := resolveUserHomeDir(env); home != "" {
			return home
		}
		if home, err := os.UserHomeDir(); err == nil {
			return home
		}
		return currentBaseDir
	}()

	validation := s.validateTarget(targetBaseDir, env)
	if !validation.OK {
		// Windows 安装目录由安装器/自动更新管理；迁移前在 service 层拦截。
		return &fsx.Error{
			Code:    validation.Code,
			Message: fmt.Sprintf("%s: %s", validation.Code, validation.ForbiddenDir),
		}
	}

	if currentBaseDir != targetBaseDir {
		copyDir := s.opts.CopyDataDir
		if copyDir == nil {
			copyDir = CopyDataDirectory
		}
		if err := copyDir(ctx, currentBaseDir, targetBaseDir); err != nil {
			return err
		}
	}

	patch := map[string]any{}
	if newDir != nil {
		patch["dataBaseDir"] = *newDir
	} else {
		// update(undefined) removes the override: the TS sends an empty patch
		// with dataBaseDir absent (undefined is swallowed by RPC) — the merge
		// then keeps the CURRENT dataBaseDir... read the TS: `this.update({
		// dataBaseDir: newDir })` with newDir undefined posts
		// {dataBaseDir: undefined}; JSON.stringify drops it, so the merged
		// write RETAINS the previous dataBaseDir value. That quirk is
		// reproduced: empty patch.
	}
	return s.Update(ctx, patch, nil)
}

func (s *Service) validateTarget(target string, env map[string]string) paths.ValidateDataBaseDirTargetResult {
	if s.opts.ValidateTarget != nil {
		return s.opts.ValidateTarget(target)
	}
	appInstallDir := env["NEX_WINDOWS_APP_INSTALL_DIR"]
	return paths.ValidateCurrentProcessDataBaseDirTarget(osName(), target, env, appInstallDir)
}

// EnsureDefaultProject ports ensureDefaultProject.
func (s *Service) EnsureDefaultProject(ctx context.Context, userHomeDir string) (EnsureDefaultProjectResult, error) {
	path := filepath.Join(userHomeDir, DefaultProjectName)
	existedBefore := true
	if _, err := os.Stat(path); err != nil {
		existedBefore = false
	}
	if err := os.MkdirAll(path, 0o777); err != nil {
		return EnsureDefaultProjectResult{}, err
	}
	return EnsureDefaultProjectResult{Path: path, Created: !existedBefore}, nil
}

// EnsureDefaultProjectResult is { path, created }.
type EnsureDefaultProjectResult struct {
	Path    string
	Created bool
}

// ---------------------------------------------------------------------------
// onDidUpdate (observableSettingService.ts): host-side post-commit notify,
// NOT part of the Setting RPC contract. Failure to update does not fire.
// ---------------------------------------------------------------------------

// OnDidUpdate registers a listener; the returned func unregisters it.
func (s *Service) OnDidUpdate(listener func(keys []string)) func() {
	s.listenersMu.Lock()
	defer s.listenersMu.Unlock()
	s.listenerSeq++
	id := s.listenerSeq
	s.listeners[id] = listener
	return func() {
		s.listenersMu.Lock()
		defer s.listenersMu.Unlock()
		delete(s.listeners, id)
	}
}

func (s *Service) notifyDidUpdate(patch map[string]any) {
	keys := make([]string, 0, len(patch))
	for key := range patch {
		keys = append(keys, key)
	}
	if len(keys) == 0 {
		return
	}
	sortStrings(keys)
	s.listenersMu.Lock()
	listeners := make([]func([]string), 0, len(s.listeners))
	for _, listener := range s.listeners {
		listeners = append(listeners, listener)
	}
	s.listenersMu.Unlock()
	for _, listener := range listeners {
		listener(keys)
	}
}

// Value is a nil-safe Get.
func (o *Ordered) Value(key string) any {
	v, _ := o.Get(key)
	return v
}
