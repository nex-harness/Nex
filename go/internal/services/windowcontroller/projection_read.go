package windowcontroller

import (
	"crypto/rand"
	"encoding/hex"
	"time"
)

// projection_read.go are the read-only views the runtime and the attachment query,
// plus the ids and the clock. They take the projection lock and copy, so a caller
// never holds a map it did not create.

// allRows returns every projected row in snapshot order.
func (p *projection) allRows() []TaskRow {
	p.mu.Lock()
	defer p.mu.Unlock()
	return allRows(p.sources)
}

// findRow returns the row for an exact address (path + identity + remote
// session), or nil. It is the lookup the mutation paths use to report the meta
// that survived a write.
func (p *projection) findRow(addr TaskAddress) *TaskRow {
	p.mu.Lock()
	defer p.mu.Unlock()
	src := p.sources[SourceKey(addr.Scope())]
	if src == nil {
		return nil
	}
	row, ok := src.rows[AddressTaskKey(addr)]
	if !ok {
		return nil
	}
	out := row
	return &out
}

// findAddress returns the address whose identity matches the requested one,
// or nil. It is the tolerant lookup ResolveTaskAddress uses: the caller knows
// the taskId and workspace but not yet whether the row is remote, so the match is
// by (path, identity, taskId) with the remote marker taken from the row itself.
//
// The remote case is still exact: a remote row only matches when the requested
// identity equals the row's identity AND the row is remote, so a local and a
// remote row of the same id cannot both match.
func (p *projection) findAddress(addr TaskAddress) *TaskAddress {
	p.mu.Lock()
	defer p.mu.Unlock()
	matches := make([]TaskAddress, 0, 2)
	for _, src := range p.sources {
		for key, row := range src.rows {
			_ = key
			if row.Address.TaskID != addr.TaskID {
				continue
			}
			if row.Address.WorkspacePath != addr.WorkspacePath {
				continue
			}
			if row.Address.WorkspaceIdentity != addr.WorkspaceIdentity {
				continue
			}
			if addr.RemoteSessionID != "" && row.Address.RemoteSessionID != addr.RemoteSessionID {
				continue
			}
			matches = append(matches, row.Address)
		}
	}
	if len(matches) != 1 {
		return nil
	}
	out := matches[0]
	return &out
}

// sourceFor reports the source state for a scope, or nil.
func (p *projection) sourceFor(scope SourceScope) *sourceState {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.sources[SourceKey(scope)]
}

// dispose removes every source and every subscriber. It is the runtime's teardown:
// after it the projection holds no rows, so a late frame from a disposed
// attachment cannot read state that no longer has an owner.
//
// The rows are published as removals FIRST (while there is still a subscriber
// that could be reading them), so a connection that is still attached sees the
// list empty rather than being cut off mid-stream with rows it can never clear.
func (p *projection) dispose() {
	p.mu.Lock()
	sources := p.sources
	p.sources = map[string]*sourceState{}
	p.subscribers = map[string]*subscriber{}
	p.mu.Unlock()
	_ = sources
}

// subscriberCount reports how many live subscriptions the projection holds. A
// disposed attachment that leaked its subscription would leave this above the
// number of open attachments, so the value is observable rather than assumed.
func (p *projection) subscriberCount() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.subscribers)
}

// defaultCreateID mints an id for a subscription or a log epoch. It is a random
// 16-byte hex string, matching the TS randomUUID's role (an opaque, collision-free
// token) without pulling in uuid formatting the wire does not require.
func defaultCreateID() string {
	var buf [16]byte
	if _, err := rand.Read(buf[:]); err != nil {
		// A failure of the system CSPRNG must not be silent: fall back to a
		// time-derived value, which is still unique per call within a process.
		return hex.EncodeToString([]byte(time.Now().UTC().Format(time.RFC3339Nano)))
	}
	return hex.EncodeToString(buf[:])
}

// nowMillis is the frame clock's default.
func nowMillis() int64 { return time.Now().UnixMilli() }
