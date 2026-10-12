package emitters

// The bounded side tables mirror the TypeScript BoundedKeySet / BoundedValueMap.
// They cap at 2000 entries and evict the oldest, so a long-lived session cannot
// grow without bound. Go maps do not keep insertion order, so the ordered
// eviction is implemented with a parallel ring of keys.

const maxTrackedLifecycleKeys = 2000

type boundedKeySet struct {
	seen  map[string]struct{}
	order []string
}

func newBoundedKeySet() *boundedKeySet {
	return &boundedKeySet{seen: map[string]struct{}{}}
}

// add returns true the first time key is seen.
func (s *boundedKeySet) add(key string) bool {
	if _, ok := s.seen[key]; ok {
		return false
	}
	s.seen[key] = struct{}{}
	s.order = append(s.order, key)
	if len(s.order) > maxTrackedLifecycleKeys {
		oldest := s.order[0]
		s.order = s.order[1:]
		delete(s.seen, oldest)
	}
	return true
}

func (s *boundedKeySet) deletePrefix(prefix string) {
	kept := s.order[:0]
	for _, k := range s.order {
		if hasPrefix(k, prefix) {
			delete(s.seen, k)
			continue
		}
		kept = append(kept, k)
	}
	s.order = kept
}

type boundedValueMap struct {
	values map[string]any
	order  []string
}

func newBoundedValueMap() *boundedValueMap {
	return &boundedValueMap{values: map[string]any{}}
}

func (m *boundedValueMap) get(key string) (any, bool) {
	v, ok := m.values[key]
	return v, ok
}

func (m *boundedValueMap) set(key string, value any) {
	if _, ok := m.values[key]; ok {
		// Move to the most-recent position, matching Map.delete+set.
		m.removeOrder(key)
	} else if len(m.order) >= maxTrackedLifecycleKeys {
		oldest := m.order[0]
		m.order = m.order[1:]
		delete(m.values, oldest)
	}
	m.values[key] = value
	m.order = append(m.order, key)
}

func (m *boundedValueMap) delete(key string) {
	delete(m.values, key)
	m.removeOrder(key)
}

func (m *boundedValueMap) deletePrefix(prefix string) {
	kept := m.order[:0]
	for _, k := range m.order {
		if hasPrefix(k, prefix) {
			delete(m.values, k)
			continue
		}
		kept = append(kept, k)
	}
	m.order = kept
}

func (m *boundedValueMap) removeOrder(key string) {
	for i, k := range m.order {
		if k == key {
			m.order = append(m.order[:i], m.order[i+1:]...)
			return
		}
	}
}

func hasPrefix(s, prefix string) bool {
	return len(s) >= len(prefix) && s[:len(prefix)] == prefix
}
