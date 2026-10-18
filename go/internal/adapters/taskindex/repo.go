package taskindex

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/tylinux/nex/go/internal/services/paths"
)

// Repo ports TaskIndexRepo: the only writer of the tasks table and the reader
// behind the sidebar, the global session search and the grouped view.
//
// Concurrency: writes are serialized per (workspaceKey, taskId) — the TypeScript
// promise chain — so two writes to the same row cannot interleave, while writes
// to different rows proceed in parallel. SQLite itself is single-connection here,
// exactly as the Node repo drives a single synchronous handle.
type Repo struct {
	path string
	busy int
	now  func() int64

	mu     sync.Mutex
	db     *sql.DB
	closed bool

	chainsMu sync.Mutex
	chains   map[string]*writeChain
}

type writeChain struct {
	done chan struct{}
}

// RepoOptions configures New. An empty Path uses the resolved
// {dataBaseDir}/.nex/v2/tasks-index.sqlite.
type RepoOptions struct {
	Path          string
	BusyTimeoutMs int
	// Now is the injectable clock (tests pin it so unread markers stay
	// comparable and sort orders deterministic).
	Now func() int64
}

// New opens the task index, applying pending migrations, and runs the same
// post-migration self-healing passes TaskIndexRepo.initialize does.
func New(ctx context.Context, opts RepoOptions) (*Repo, error) {
	path := opts.Path
	if path == "" {
		path = paths.NewFromProcess().TasksIndexDatabasePath()
	}
	busy := opts.BusyTimeoutMs
	if busy <= 0 {
		busy = 5000
	}
	now := opts.Now
	if now == nil {
		now = func() int64 { return time.Now().UnixMilli() }
	}
	r := &Repo{path: path, busy: busy, now: now, chains: map[string]*writeChain{}}
	if err := r.ensureReady(ctx); err != nil {
		return nil, err
	}
	return r, nil
}

func (r *Repo) ensureReady(ctx context.Context) error {
	r.mu.Lock()
	if r.closed {
		r.mu.Unlock()
		return errors.New("task index repo is closed")
	}
	if r.db != nil {
		r.mu.Unlock()
		return nil
	}
	path, busy := r.path, r.busy
	r.mu.Unlock()

	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	// 多窗口 Host 共用 tasks-index；PRAGMA 与迁移在业务连接上一次完成，
	// 依赖 worker 先跑过 prepare 会让替换/清空文件后仍假装 ready。
	db, err := OpenWithBusyTimeout(ctx, path, busy, nil)
	if err != nil {
		return err
	}
	if _, err := db.ExecContext(ctx, "PRAGMA synchronous = NORMAL"); err != nil {
		db.Close()
		return err
	}
	r.mu.Lock()
	if r.closed {
		r.mu.Unlock()
		db.Close()
		return errors.New("task index repo is closed")
	}
	if r.db != nil {
		// Lost the race: keep the existing handle, drop the new one.
		r.mu.Unlock()
		db.Close()
		return nil
	}
	r.db = db
	r.mu.Unlock()
	return r.runStartupRepairs(ctx)
}

// Close releases the handle. A close failure surfaces to the caller: the
// TypeScript close({ throwOnError: true }) used at startup handover must not be
// silently swallowed.
func (r *Repo) Close() error {
	r.mu.Lock()
	r.closed = true
	db := r.db
	r.db = nil
	r.mu.Unlock()
	r.chainsMu.Lock()
	r.chains = map[string]*writeChain{}
	r.chainsMu.Unlock()
	if db == nil {
		return nil
	}
	return db.Close()
}

func (r *Repo) database(ctx context.Context) (*sql.DB, error) {
	r.mu.Lock()
	if r.closed {
		r.mu.Unlock()
		return nil, errors.New("task index repo is closed")
	}
	db := r.db
	r.mu.Unlock()
	if db == nil {
		if err := r.ensureReady(ctx); err != nil {
			return nil, err
		}
		r.mu.Lock()
		db = r.db
		r.mu.Unlock()
	}
	if db == nil {
		return nil, errors.New("task index sqlite 尚未初始化")
	}
	return db, nil
}

// enqueueWrite ports enqueueWrite: operations for one key run in call order and
// a failure does not break the chain for the next writer.
func (r *Repo) enqueueWrite(key string, op func() error) error {
	// closed is written under r.mu, so it is read under r.mu; the chain map is
	// guarded by chainsMu.
	r.mu.Lock()
	closed := r.closed
	r.mu.Unlock()
	r.chainsMu.Lock()
	if closed {
		r.chainsMu.Unlock()
		return errors.New("task index repo is closed")
	}
	prev := r.chains[key]
	current := &writeChain{done: make(chan struct{})}
	r.chains[key] = current
	r.chainsMu.Unlock()

	var opErr error
	if prev != nil {
		<-prev.done
	}
	defer func() {
		r.chainsMu.Lock()
		if r.chains[key] == current {
			delete(r.chains, key)
		}
		r.chainsMu.Unlock()
		close(current.done)
	}()
	opErr = op()
	return opErr
}

func itoa(v int) string { return strconv.Itoa(v) }

// serializeMetaJSON is JSON.stringify(meta). HTML escaping is disabled so the
// bytes match what Node writes: a title containing "<" must not be stored as
// "\\u003c" on one side only, or the two engines stop agreeing on meta_json.
func serializeMetaJSON(meta TaskMeta) (string, error) {
	var b strings.Builder
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(meta); err != nil {
		return "", err
	}
	return strings.TrimRight(b.String(), "\n"), nil
}

func nullToAnyString(v sql.NullString) any {
	if !v.Valid {
		return nil
	}
	return v.String
}
