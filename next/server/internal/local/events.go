package local

// View broadcast fan-out for the server-owned provider-settings /
// model-selection channels. Each browser listen is keyed by its own emit
// function; a change (doc version or runner facts) re-reads the view per listen
// so a targeted listen rebuilds with its own runner facts.

import (
	"sync"
	"time"

	"github.com/tylinux/nex-next/server/internal/rpcwire"
)

type viewBroadcast struct {
	mu          sync.Mutex
	nextID      uint64
	subscribers map[uint64]func()
}

var viewBroadcasts = &viewBroadcast{subscribers: map[uint64]func(){}}

func (b *viewBroadcast) subscribe(emit func()) (uint64, func()) {
	b.mu.Lock()
	b.nextID++
	id := b.nextID
	b.subscribers[id] = emit
	b.mu.Unlock()
	return id, nil
}

func (b *viewBroadcast) unsubscribe(id uint64, _ func()) {
	b.mu.Lock()
	delete(b.subscribers, id)
	b.mu.Unlock()
}

// publish runs every subscriber outside the lock.
func (b *viewBroadcast) publish() {
	b.mu.Lock()
	callbacks := make([]func(), 0, len(b.subscribers))
	for _, callback := range b.subscribers {
		callbacks = append(callbacks, callback)
	}
	b.mu.Unlock()
	for _, callback := range callbacks {
		callback()
	}
}

var timeNow = time.Now

var _ rpcwire.Value
