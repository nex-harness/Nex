package tools

import (
	"fmt"
	"os"
	"path/filepath"
	"sync"
)

type fileReadRecord struct {
	modTimeUnixNano int64
	size            int64
}

// FileStateTracker tracks files read or written within a session to enforce:
// 1. Per-file mutation serialization (pi-agent file-mutation-queue parity)
// 2. Read-before-edit / read-before-overwrite guardrail
// 3. Stale-file detection when a file was modified externally since last read
type FileStateTracker struct {
	mu      sync.Mutex
	records map[string]fileReadRecord
	locks   map[string]*sync.Mutex
}

// NewFileStateTracker constructs a per-session FileStateTracker.
func NewFileStateTracker() *FileStateTracker {
	return &FileStateTracker{
		records: make(map[string]fileReadRecord),
		locks:   make(map[string]*sync.Mutex),
	}
}

func canonicalTrackPath(absPath string) string {
	clean := filepath.Clean(absPath)
	if resolved, err := filepath.EvalSymlinks(clean); err == nil {
		return resolved
	}
	return clean
}

// LockFile acquires a per-file mutex and returns an unlock function.
func (t *FileStateTracker) LockFile(absPath string) func() {
	if t == nil {
		return func() {}
	}
	key := canonicalTrackPath(absPath)
	t.mu.Lock()
	fm, ok := t.locks[key]
	if !ok {
		fm = &sync.Mutex{}
		t.locks[key] = fm
	}
	t.mu.Unlock()

	fm.Lock()
	return fm.Unlock
}

// RecordRead records the latest known mtime and size of absPath after read/write/edit.
func (t *FileStateTracker) RecordRead(absPath string, info os.FileInfo) {
	if t == nil || info == nil {
		return
	}
	key := canonicalTrackPath(absPath)
	t.mu.Lock()
	t.records[key] = fileReadRecord{
		modTimeUnixNano: info.ModTime().UnixNano(),
		size:            info.Size(),
	}
	t.mu.Unlock()
}

// RecordStat stats absPath and records its current mtime and size if accessible.
func (t *FileStateTracker) RecordStat(absPath string) {
	if t == nil {
		return
	}
	if info, err := os.Stat(absPath); err == nil && !info.IsDir() {
		t.RecordRead(absPath, info)
	}
}

// CheckBeforeModify verifies that an existing file was read in this session and has not
// changed externally since it was last read or written by the agent.
func (t *FileStateTracker) CheckBeforeModify(absPath string, currentInfo os.FileInfo, action string) error {
	if t == nil || currentInfo == nil {
		return nil
	}
	key := canonicalTrackPath(absPath)
	t.mu.Lock()
	rec, seen := t.records[key]
	t.mu.Unlock()

	if !seen {
		return fmt.Errorf("file %s has not been read in this session yet; call `read` first before %s so you have its exact current contents", filepath.Base(absPath), action)
	}
	if rec.modTimeUnixNano != currentInfo.ModTime().UnixNano() || rec.size != currentInfo.Size() {
		return fmt.Errorf("file %s was modified externally since it was last read (stale state); re-read it with `read` before %s", filepath.Base(absPath), action)
	}
	return nil
}
