// snapshots.go holds the preflight snapshot store: the FIFO-bounded entry
// list, the mutex-guarded accessors and the key construction. Split out of
// service.go; symbols are unchanged.
package conversationshare

// orderedSnapshots is the FIFO-bounded preflight snapshot store (LRU by
// re-insertion, capped at 200 entries — the TS Map insertion-order rule).
type orderedSnapshots struct {
	entries []snapshotEntry
	limit   int
}

type snapshotEntry struct {
	key      string
	snapshot previewPreflightSnapshot
}

func (o *orderedSnapshots) set(key string, snapshot previewPreflightSnapshot) {
	for index, entry := range o.entries {
		if entry.key == key {
			o.entries = append(o.entries[:index], o.entries[index+1:]...)
			break
		}
	}
	o.entries = append(o.entries, snapshotEntry{key: key, snapshot: snapshot})
	for len(o.entries) > o.limit {
		o.entries = o.entries[1:]
	}
}

func (o *orderedSnapshots) get(key string) (previewPreflightSnapshot, bool) {
	for _, entry := range o.entries {
		if entry.key == key {
			return entry.snapshot, true
		}
	}
	return previewPreflightSnapshot{}, false
}

func (o *orderedSnapshots) deletePrefix(prefix string) {
	kept := o.entries[:0]
	for _, entry := range o.entries {
		if len(entry.key) >= len(prefix) && entry.key[:len(prefix)] == prefix {
			continue
		}
		kept = append(kept, entry)
	}
	o.entries = kept
}

// takeSnapshot / putSnapshot / dropSnapshots are the mutex-guarded accessors:
// preflight writes and publish reads the same store concurrently.
func (s *Service) takeSnapshot(key string) (previewPreflightSnapshot, bool) {
	s.snapshotMu.Lock()
	defer s.snapshotMu.Unlock()
	return s.previewPreflightSnapshots.get(key)
}

func (s *Service) putSnapshot(key string, snapshot previewPreflightSnapshot) {
	s.snapshotMu.Lock()
	defer s.snapshotMu.Unlock()
	s.previewPreflightSnapshots.set(key, snapshot)
}

func (s *Service) dropSnapshots(prefix string) {
	s.snapshotMu.Lock()
	defer s.snapshotMu.Unlock()
	s.previewPreflightSnapshots.deletePrefix(prefix)
}
