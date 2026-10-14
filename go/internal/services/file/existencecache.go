package file

// existencecache.go is the port of the FileExistenceCache class: LRU with a
// 60s TTL, expiry purged on write, positive AND negative results cached
// (assistant mentions of missing paths repeat), plus the in-flight
// deduplication map (pendingFileExistenceChecks).

import (
	"context"
	"os"
	"sync"
	"time"
)

// ---------------------------------------------------------------------------
// existence cache
// ---------------------------------------------------------------------------

type existenceEntry struct {
	exists  bool
	expires time.Time
	lastHit time.Time // LRU ordering
}

type existenceCache struct {
	mu      sync.Mutex
	entries map[string]*existenceEntry
	order   []string // LRU queue: least recently used first
}

func newExistenceCache() *existenceCache {
	return &existenceCache{entries: map[string]*existenceEntry{}}
}

func (c *existenceCache) get(path string, now time.Time) (bool, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.entries[path]
	if !ok {
		return false, false
	}
	if !e.expires.After(now) {
		delete(c.entries, path)
		c.removeOrder(path)
		return false, false
	}
	c.touch(path)
	return e.exists, true
}

func (c *existenceCache) set(path string, exists bool, now time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	// purge expired entries first (TS does this on every set)
	for p, e := range c.entries {
		if !e.expires.After(now) {
			delete(c.entries, p)
			c.removeOrder(p)
		}
	}
	c.entries[path] = &existenceEntry{exists: exists, expires: now.Add(existenceCacheTTL)}
	c.touch(path)
	for len(c.order) > existenceCacheMax {
		oldest := c.order[0]
		c.order = c.order[1:]
		delete(c.entries, oldest)
	}
}

func (c *existenceCache) touch(path string) {
	c.removeOrder(path)
	c.order = append(c.order, path)
}

func (c *existenceCache) removeOrder(path string) {
	for i, p := range c.order {
		if p == path {
			c.order = append(c.order[:i], c.order[i+1:]...)
			return
		}
	}
}

// checkFileExists ports the inner checkFileExists: cache -> in-flight -> stat.
// 正负结果都缓存。
func (s *Service) checkFileExists(ctx context.Context, path string) (bool, error) {
	now := s.now()
	if v, ok := s.existence.get(path, now); ok {
		return v, nil
	}
	s.mu.Lock()
	pending, ok := s.pendingChecks[path]
	if !ok {
		pending = &pendingExistence{ch: make(chan bool, 1)}
		s.pendingChecks[path] = pending
	}
	s.mu.Unlock()
	if ok {
		select {
		case v := <-pending.ch:
			return v, nil
		case <-ctx.Done():
			return false, ctx.Err()
		}
	}
	defer func() {
		s.mu.Lock()
		delete(s.pendingChecks, path)
		s.mu.Unlock()
		pending.once.Do(func() { close(pending.ch) })
	}()
	info, err := os.Stat(path)
	exists := err == nil && !info.IsDir()
	s.existence.set(path, exists, s.now())
	pending.value = exists
	return exists, nil
}
