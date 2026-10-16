package agentscope

import (
	"context"
	"encoding/json"
	"sync"

	"github.com/tylinux/nex/go/internal/agentproc/proto"
)

// interaction.go is the step 3.10.2 seam as the facade consumes it: an agent's
// interaction/* request has to be answered by the connection that can actually
// show it, and a request that belongs to no live connection must fall back to the
// method-not-found default the protocol client already has.
//
// Why the routing is per connection and not global:
//
//   - A permission prompt is a LOCAL desktop side effect. A phone replay attachment
//     may consume recoverable conversation facts, but it cannot be the one that
//     answers a permission request; the answer has to come from the desktop window
//     that owns the runtime.
//   - Two desktop windows can each own a subscription on the SAME topic, so "which
//     connection asked" is not derivable from the topic. It is derivable from the
//     agent's params when they carry a connection id, and that is the only signal
//     this layer trusts.
//   - With no live connection to attribute the request to, the honest answer is
//     method-not-found: the agent already degrades on it (the tool runs without the
//     prompt, the headers go unfilled, the browser call falls back). Inventing a
//     synthetic answer would be worse than the documented degradation.

// interactionMethods is the closed set of agent→host requests this facade routes.
// It is proto.InteractionMethods verbatim: the scope must not answer a method the
// protocol client does not consider an interaction, because such a request has no
// host-answer contract at all.
var interactionMethods = proto.InteractionMethods

// InteractionResolver answers one interaction request for the connection that owns
// it. It returns the result JSON for the agent, or an error to answer with a
// JSON-RPC error (a *proto.RequestError carries a chosen code).
type InteractionResolver func(ctx context.Context, req proto.AgentRequest) (json.RawMessage, error)

// InteractionDirectory is the registry of the live scopes' resolvers, keyed by the
// connection identity the agent's request names. One directory serves every scope
// of one host process; a scope registers itself on construction and releases itself
// on dispose.
type InteractionDirectory struct {
	mu        sync.RWMutex
	resolvers map[string]InteractionResolver
	// fallback answers a request that names no live connection. The protocol
	// client's own default (method-not-found) applies when it is nil.
	fallback InteractionResolver
}

// NewInteractionDirectory builds an empty directory.
func NewInteractionDirectory() *InteractionDirectory {
	return &InteractionDirectory{resolvers: map[string]InteractionResolver{}}
}

// Register adds (or replaces) the resolver for one connection id.
func (d *InteractionDirectory) Register(connectionID string, resolver InteractionResolver) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.resolvers[connectionID] = resolver
}

// Unregister removes one connection's resolver. A request that arrives after this
// finds nothing and gets the method-not-found default, which is the honest answer:
// the window that could have shown the prompt is gone.
func (d *InteractionDirectory) Unregister(connectionID string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	delete(d.resolvers, connectionID)
}

// SetFallback installs the answer for requests that name no live connection.
func (d *InteractionDirectory) SetFallback(resolver InteractionResolver) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.fallback = resolver
}

// Live reports how many connections currently have a resolver. Tests assert on it
// so a leak (a scope that never released its handler) cannot pass quietly.
func (d *InteractionDirectory) Live() int {
	d.mu.RLock()
	defer d.mu.RUnlock()
	return len(d.resolvers)
}

// requestTargetKey is the connection id an agent request attributes itself to. The
// agent names it in params when it multiplexes several connections on one process;
// a request with no such field is a single-connection request, which the current
// scope serves as the default when it is the only one.
func requestTargetKey(params json.RawMessage) string {
	if len(params) == 0 {
		return ""
	}
	var fields struct {
		ConnectionID string `json:"connectionId"`
	}
	if err := json.Unmarshal(params, &fields); err != nil {
		return ""
	}
	return fields.ConnectionID
}

// Handler returns the handler to install on the protocol client for one method. It
// routes by the request's connection id, falls back to the sole live scope when the
// request names none, and otherwise declines (nil → method-not-found).
func (d *InteractionDirectory) Handler(method string) proto.InteractionHandler {
	return func(ctx context.Context, req proto.AgentRequest) (json.RawMessage, error) {
		resolver, ok := d.lookup(req)
		if !ok {
			return nil, nil
		}
		return resolver(ctx, req)
	}
}

func (d *InteractionDirectory) lookup(req proto.AgentRequest) (InteractionResolver, bool) {
	d.mu.RLock()
	defer d.mu.RUnlock()
	if key := requestTargetKey(req.Params); key != "" {
		if resolver, ok := d.resolvers[key]; ok {
			return resolver, true
		}
		return d.fallback, d.fallback != nil
	}
	// A request that names no connection belongs to the sole live scope. With more
	// than one there is no honest answer: guessing would show one window's prompt
	// for another window's turn.
	if len(d.resolvers) == 1 {
		for _, resolver := range d.resolvers {
			return resolver, true
		}
	}
	return d.fallback, d.fallback != nil
}

// interactionHandler builds one scope's handler for one method: the resolver is
// looked up in the directory rather than captured, so the same installed handler
// keeps working after another scope registered or released.
func (s *Scope) interactionHandler(_ string) proto.InteractionHandler {
	directory := s.interactionDirectory
	if directory == nil {
		return nil
	}
	return directory.Handler("")
}

// SetInteractionDirectory installs the shared directory and registers this scope's
// resolver under its own connection id. A second call replaces the registration.
func (s *Scope) SetInteractionDirectory(directory *InteractionDirectory, resolver InteractionResolver) {
	s.disposedMu.Lock()
	if s.disposed {
		s.disposedMu.Unlock()
		return
	}
	s.disposedMu.Unlock()
	if directory == nil {
		return
	}
	s.interactionDirectory = directory
	s.interactionResolver = resolver
	directory.Register(s.cfg.ConnectionID, resolver)
	s.RegisterInteractionHandler()
}
