// Package agent holds the protocol-level behaviour of the Go agent: which
// methods it answers and what it reports at startup.
package agent

import (
	"context"
	"encoding/json"

	"github.com/tylinux/nex/go/internal/protocol"
)

// Method names (nexProtocolMethods / nexProtocolNotifications in the shared package).
const (
	MethodRuntimeCapabilities = "runtime/capabilities"
	NotifyStorageState        = "startup/storageState"
)

// Handler routes requests. Methods the agent does not implement answer -32601,
// which the host reads as "capability absent" and degrades on.
type Handler struct {
	routes map[string]func(context.Context, protocol.Message) ([]byte, error)
}

func NewHandler() *Handler {
	h := &Handler{routes: map[string]func(context.Context, protocol.Message) ([]byte, error){}}
	h.routes[MethodRuntimeCapabilities] = func(context.Context, protocol.Message) ([]byte, error) {
		// nexRuntimeCapabilitiesSchema has one field. It is advertised true because the
		// Node agent does; the Go agent's plan state is tracked by the v4 snapshot.
		return json.Marshal(map[string]bool{"independentPlanState": true})
	}
	return h
}

func (h *Handler) HandleRequest(ctx context.Context, req protocol.Message) ([]byte, error) {
	if fn, ok := h.routes[req.Method]; ok {
		return fn(ctx, req)
	}
	return nil, &protocol.RPCError{Code: protocol.CodeMethodNotFound, Message: "Method not found: " + req.Method}
}

func (h *Handler) HandleNotification(context.Context, protocol.Message) {}

// StorageStateNotifier builds startup/storageState notifications. One notifier is
// one attempt: attemptId is fixed and sequence counts up from 1.
type StorageStateNotifier struct {
	AttemptID  string
	DatabaseID string
	seq        int
}

// Notification wraps progress (anything that marshals to the progress fields) in
// the envelope of nexStorageStartupStateSchema.
func (n *StorageStateNotifier) Notification(progress any) (protocol.Message, error) {
	raw, err := json.Marshal(progress)
	if err != nil {
		return protocol.Message{}, err
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		return protocol.Message{}, err
	}
	n.seq++
	set := func(k string, v any) { b, _ := json.Marshal(v); fields[k] = b }
	set("schemaVersion", 1)
	set("attemptId", n.AttemptID)
	set("databaseId", n.DatabaseID)
	set("databaseKind", "session")
	set("sequence", n.seq)
	params, err := json.Marshal(fields)
	if err != nil {
		return protocol.Message{}, err
	}
	return protocol.Message{Kind: protocol.KindNotification, Method: NotifyStorageState, Params: params, HasParams: true}, nil
}
