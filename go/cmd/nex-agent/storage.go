package main

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"fmt"

	"github.com/tylinux/nex/go/internal/adapters/config"
	"github.com/tylinux/nex/go/internal/adapters/store"
	"github.com/tylinux/nex/go/internal/agent"
	"github.com/tylinux/nex/go/internal/protocol"
)

func newUUID() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	b[6] = b[6]&0x0f | 0x40
	b[8] = b[8]&0x3f | 0x80
	h := hex.EncodeToString(b[:])
	return fmt.Sprintf("%s-%s-%s-%s-%s", h[0:8], h[8:12], h[12:16], h[16:20], h[20:])
}

// openStorage opens the session database, reporting each migration state to the
// host as startup/storageState notifications before any request is served. The
// host gates database writes on seeing these, so they are written synchronously.
func openStorage(ctx context.Context, w *protocol.FrameWriter) (*sql.DB, error) {
	path, err := config.SessionDBPath()
	if err != nil {
		return nil, err
	}
	if err := config.EnsureParent(path); err != nil {
		return nil, err
	}
	sum := sha256.Sum256([]byte(path))
	notifier := &agent.StorageStateNotifier{AttemptID: newUUID(), DatabaseID: hex.EncodeToString(sum[:])}
	send := func(p store.Progress) error {
		msg, err := notifier.Notification(p)
		if err != nil {
			return err
		}
		return w.WriteMessage(msg)
	}
	// The Node entrypoint reports one "checking" before it opens the store, and the
	// runner reports its own; both appear in the host-visible sequence.
	if err := send(store.Progress{Phase: store.PhaseChecking}); err != nil {
		return nil, err
	}
	return store.Open(ctx, path, send)
}
