// personalrepo.go ports NodePersonalProviderConfigRepository
// (packages/provider-node/src/personal-provider-config-repository.ts): the
// provider_config.json state owner — read with recovery-degrade, update under
// the same-directory file lock (Node and Go writers interoperate through the
// same <path>.lock convention), canonical-normalize on write, and a polling
// watcher that detects other processes' writes without false notifications.
//
// 架构说明（TS 注释保留）:
//   - 正式文件无效时必须原样保留，不能备份后覆盖成空配置；本进程仅以内存空
//     Overlay 降级，等待用户修复原文件.
//   - 原子写可能产生多次文件系统事件。写入完成后先记录内容版本，polling
//     随后读取到同一版本时不会再次发布失效通知.
package modelprovider

import (
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"sync"
	"time"

	"github.com/tylinux/nex/go/internal/adapters/fsx"
	"github.com/tylinux/nex/go/internal/providerconfig"
)

// PersonalRepoOptions ports NodePersonalProviderConfigRepositoryOptions.
type PersonalRepoOptions struct {
	FilePath        string
	ImportLegacy    func(ctx context.Context) (*providerconfig.LayerUpdate, error)
	OnRecovery      func(err error)
	OnPollingError  func(err error)
	PollingInterval time.Duration // 0 disables polling (TS `false`)
	Now             func() time.Time
}

// PersonalRepo is the repository. Safe for concurrent use.
type PersonalRepo struct {
	opts        PersonalRepoOptions
	mu          sync.Mutex
	listeners   []func(reason string)
	observedRev string
	observedSet bool
	writeGen    int
	pollActive  bool
	pollingErr  bool
	disposed    bool
}

// NewPersonalRepo validates options (filePath 不能为空).
func NewPersonalRepo(opts PersonalRepoOptions) (*PersonalRepo, error) {
	if opts.FilePath == "" {
		return nil, errors.New("Personal Provider Config filePath 不能为空")
	}
	if opts.PollingInterval < 0 {
		return nil, errors.New("Personal Provider Config pollingIntervalMs 必须大于 0")
	}
	return &PersonalRepo{opts: opts}, nil
}

// Read returns the current snapshot, degrading to empty on an invalid file
// (the file itself is never rewritten by the recovery path).
func (r *PersonalRepo) Read(ctx context.Context) (*providerconfig.LayerSnapshot, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.disposed {
		return nil, errors.New("NodePersonalProviderConfigRepository 已 dispose")
	}
	snapshot, err := r.readCurrent(ctx)
	if err != nil {
		if r.opts.OnRecovery != nil {
			r.opts.OnRecovery(err)
		}
		snapshot, err = providerconfig.SnapshotFromUpdate(providerconfig.EmptyLayerUpdate())
		if err != nil {
			return nil, err
		}
	}
	if !r.observedSet {
		r.observedRev, r.observedSet = snapshot.Revision, true
	}
	return snapshot, nil
}

// Update runs one read-modify-write cycle: lock file, read, transform,
// canonical-normalize, atomic private write; then emit "updated".
func (r *PersonalRepo) Update(ctx context.Context, transform func(current *providerconfig.LayerSnapshot) (*providerconfig.LayerUpdate, error)) (*providerconfig.LayerSnapshot, error) {
	r.mu.Lock()
	if r.disposed {
		r.mu.Unlock()
		return nil, errors.New("NodePersonalProviderConfigRepository 已 dispose")
	}
	var result *providerconfig.LayerSnapshot
	// Panic-safe unlock: a panic inside transform runs runtime.Dispose on the
	// same goroutine, and its listener removals must not deadlock against a
	// lock a panicking frame still holds.
	err := func() (err error) {
		defer func() {
			if panicObj := recover(); panicObj != nil {
				r.mu.Unlock()
				panic(panicObj)
			}
		}()
		err = r.withLock(ctx, func() error {
			current, err := r.readLocked(ctx)
			if err != nil {
				return err
			}
			next, err := transform(current)
			if err != nil {
				return err
			}
			committed, err := r.writeLocked(ctx, next)
			if err != nil {
				return err
			}
			snapshot, err := providerconfig.SnapshotFromUpdate(committed)
			if err != nil {
				return err
			}
			// 原子写可能产生多次文件系统事件：先记录内容版本.
			r.observedRev, r.observedSet = snapshot.Revision, true
			result = snapshot
			return nil
		})
		r.mu.Unlock()
		return err
	}()
	if err != nil {
		return nil, err
	}
	// emit AFTER releasing the repo lock: listeners run registry refreshes that
	// read the repo (the TS emits synchronously too, but its async read model
	// cannot re-enter a held lock the way the Go mutex can).
	r.emit("updated")
	return result, nil
}

// OnDidChange registers a listener.
func (r *PersonalRepo) OnDidChange(listener func(reason string)) func() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.listeners = append(r.listeners, listener)
	return func() {
		r.mu.Lock()
		defer r.mu.Unlock()
		for i, candidate := range r.listeners {
			if &candidate == &listener {
				r.listeners = append(r.listeners[:i], r.listeners[i+1:]...)
				return
			}
		}
	}
}

// Dispose stops polling and clears listeners.
func (r *PersonalRepo) Dispose() {
	r.mu.Lock()
	r.disposed = true
	r.listeners = nil
	r.mu.Unlock()
}

// readCurrent ports #readCurrent: a plain read suffices when the file is
// absent-without-legacy or already canonical; import/normalize paths need the
// lock (must re-read inside it).
func (r *PersonalRepo) readCurrent(ctx context.Context) (*providerconfig.LayerSnapshot, error) {
	file, err := readJSONFileIfExists(r.opts.FilePath)
	if err != nil {
		return nil, err
	}
	if file == nil && r.opts.ImportLegacy == nil {
		return providerconfig.SnapshotFromUpdate(providerconfig.EmptyLayerUpdate())
	}
	if file != nil {
		update, err := providerconfig.DecodeProviderConfigFile(file)
		if err == nil {
			if jsonEqual(file, update) {
				return providerconfig.SnapshotFromUpdate(update)
			}
		}
	}
	// 导入和规范化仍会写盘。拿锁后必须重读.
	return r.withLockResult(ctx, func() (*providerconfig.LayerSnapshot, error) {
		return r.readLocked(ctx)
	})
}

// readLocked ports #readLocked.
func (r *PersonalRepo) readLocked(ctx context.Context) (*providerconfig.LayerSnapshot, error) {
	file, err := readJSONFileIfExists(r.opts.FilePath)
	if err != nil {
		return nil, err
	}
	if file == nil {
		if r.opts.ImportLegacy != nil {
			imported, err := r.opts.ImportLegacy(ctx)
			if err != nil {
				return nil, err
			}
			if imported != nil {
				committed, err := r.writeLocked(ctx, imported)
				if err != nil {
					return nil, err
				}
				return providerconfig.SnapshotFromUpdate(committed)
			}
		}
		return providerconfig.SnapshotFromUpdate(providerconfig.EmptyLayerUpdate())
	}
	update, err := providerconfig.DecodeProviderConfigFile(file)
	if err != nil {
		return nil, err
	}
	if !jsonEqual(file, update) {
		if _, err := r.writeLocked(ctx, update); err != nil {
			return nil, err
		}
	}
	return providerconfig.SnapshotFromUpdate(update)
}

// writeLocked ports #writeLocked: canonical round-trip then atomic private
// write of the 2-space-indented encoding.
func (r *PersonalRepo) writeLocked(ctx context.Context, update *providerconfig.LayerUpdate) (*providerconfig.LayerUpdate, error) {
	encoded, err := providerconfig.EncodeProviderConfigFile(update)
	if err != nil {
		return nil, err
	}
	// decode(encode(x)) canonicalization: Go's codec is already canonical
	// (map keys are the only unordered part and json.Marshal sorts map keys,
	// matching JSON.stringify on the TS side which relies on key insertion —
	// both produce stable output, so the round-trip is identity here).
	data, err := json.MarshalIndent(encoded, "", "  ")
	if err != nil {
		return nil, err
	}
	if err := fsx.AtomicWritePrivateTextFile(ctx, r.opts.FilePath, data, nil); err != nil {
		return nil, err
	}
	r.writeGen++
	return update, nil
}

// withLock serializes against other processes through the fsx <path>.lock
// convention (WithFileLock) — the same lock the TS withFileLock takes, so
// Node and Go writers interoperate during the transition. In-process,
// PersonalRepo.mu is already held.
func (r *PersonalRepo) withLock(ctx context.Context, body func() error) error {
	return fsx.WithFileLock(ctx, r.opts.FilePath, body)
}

func (r *PersonalRepo) withLockResult(ctx context.Context, body func() (*providerconfig.LayerSnapshot, error)) (*providerconfig.LayerSnapshot, error) {
	var snapshot *providerconfig.LayerSnapshot
	err := r.withLock(ctx, func() error {
		s, e := body()
		snapshot = s
		return e
	})
	return snapshot, err
}

func (r *PersonalRepo) emit(reason string) {
	for _, listener := range r.listeners {
		listener(reason)
	}
}

// PollOnce ports #pollOnce: read the file without the lock; a revision
// different from the last observed one emits "poll-changed". Superseded polls
// (a write finished during the read) are dropped.
func (r *PersonalRepo) PollOnce(ctx context.Context) {
	r.mu.Lock()
	if r.disposed {
		r.mu.Unlock()
		return
	}
	writeGeneration := r.writeGen
	file, err := readJSONFileIfExists(r.opts.FilePath)
	if err != nil {
		// 成功保存也使旧轮询的失败失效.
		if r.disposed || writeGeneration != r.writeGen {
			r.mu.Unlock()
			return
		}
		if !r.pollingErr {
			r.pollingErr = true
			if r.opts.OnPollingError != nil {
				r.opts.OnPollingError(err)
			}
			listeners := append([]func(string){}, r.listeners...)
			r.mu.Unlock()
			for _, listener := range listeners {
				listener("poll-error")
			}
			return
		}
		r.mu.Unlock()
		return
	}
	// 去掉读锁后，旧轮询可能晚于本进程保存返回；丢弃它.
	if writeGeneration != r.writeGen {
		r.mu.Unlock()
		return
	}
	r.pollingErr = false
	var snapshot *providerconfig.LayerSnapshot
	if file == nil {
		snapshot, _ = providerconfig.SnapshotFromUpdate(providerconfig.EmptyLayerUpdate())
	} else if update, derr := providerconfig.DecodeProviderConfigFile(file); derr == nil {
		snapshot, _ = providerconfig.SnapshotFromUpdate(update)
	} else {
		snapshot, _ = providerconfig.SnapshotFromUpdate(providerconfig.EmptyLayerUpdate())
	}
	if r.disposed || !r.observedSet || snapshot.Revision == r.observedRev {
		r.mu.Unlock()
		return
	}
	r.observedRev = snapshot.Revision
	listeners := append([]func(string){}, r.listeners...)
	r.mu.Unlock()
	for _, listener := range listeners {
		listener("poll-changed")
	}
}

// readJSONFileIfExists returns the raw file bytes, nil when absent.
func readJSONFileIfExists(path string) ([]byte, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	return data, nil
}

// jsonEqual reports whether the raw file bytes equal the canonical encoding
// of the decoded update (the TS JSON.stringify comparison).
func jsonEqual(file []byte, update *providerconfig.LayerUpdate) bool {
	encoded, err := providerconfig.EncodeProviderConfigFile(update)
	if err != nil {
		return false
	}
	var raw, canonical any
	if err := json.Unmarshal(file, &raw); err != nil {
		return false
	}
	if err := json.Unmarshal(mustMarshal(encoded), &canonical); err != nil {
		return false
	}
	return JSONEqual(raw, canonical)
}

func mustMarshal(v any) []byte {
	data, _ := json.Marshal(v)
	return data
}

// JSONEqual compares two decoded JSON values structurally (map key order
// independent — the TS stringify comparison is on key-insertion order, but the
// canonical writer always emits the same order, so structural equality gives
// the same decision).
func JSONEqual(a, b any) bool {
	return jsonEqualValue(a, b)
}

func jsonEqualValue(a, b any) bool {
	switch av := a.(type) {
	case map[string]any:
		bv, ok := b.(map[string]any)
		if !ok || len(av) != len(bv) {
			return false
		}
		for k, v := range av {
			bvv, ok := bv[k]
			if !ok || !jsonEqualValue(v, bvv) {
				return false
			}
		}
		return true
	case []any:
		bv, ok := b.([]any)
		if !ok || len(av) != len(bv) {
			return false
		}
		for i := range av {
			if !jsonEqualValue(av[i], bv[i]) {
				return false
			}
		}
		return true
	default:
		return a == b
	}
}
