// Package services is the Go service layer root (step 3.1). It owns the
// ServiceCollection port — the Registry — which every later Phase-3 tier
// registers its channels into.
//
// The Registry replaces two TS pieces at once:
//
//   - ServiceCollection (collection.ts): name → instance registration, plus
//     exposeOnChannelServer(server, overrides).
//   - the per-connection override map in setupChannelServer (http.ts): the
//     three connection-scoped services (Phase 2 fact 7) — INexAgentService,
//     IProviderProvisioningTargetService, IWindowControllerService.
//
// In Go an instance IS an rpc.Channel (there is no ProxyChannel reflection
// step), so registration is name → rpc.Channel and "expose" is RegisterChannel
// on the per-connection server.
package services

import (
	"fmt"
	"sort"
	"sync"

	"github.com/tylinux/nex/go/internal/rpc"
)

// Channel names of the three per-connection overrides, from ServiceChannels
// (packages/shared/src/channels.ts): nex-agent, provider-provisioning-target,
// window-controller. These are the channels whose behaviour depends on WHO is
// connected (client mode, role, attachment), so the registry refuses a global
// registration for them and requires a per-connection scope instead.
const (
	ChannelNexAgent                   = "nex-agent"
	ChannelProviderProvisioningTarget = "provider-provisioning-target"
	ChannelWindowController           = "window-controller"
)

// connectionScopedChannels is the set above, for the fast membership check.
var connectionScopedChannels = map[string]bool{
	ChannelNexAgent:                   true,
	ChannelProviderProvisioningTarget: true,
	ChannelWindowController:           true,
}

// IsConnectionScoped reports whether a channel name is one of the three the
// server must scope per connection.
func IsConnectionScoped(channelName string) bool {
	return connectionScopedChannels[channelName]
}

// ConnectionScope supplies the per-connection channels for the three scoped
// services (the three overrides from Phase 2 fact 7) and disposes them when the
// connection ends. It is the Go shape of the `overrides` map plus the
// socket.onClose disposal in setupChannelServer.
//
// ScopeFor returns the channel to expose to THIS connection for the given
// scoped channel name, or nil when the connection gets no view of it (the
// TS leaves the global instance registered in that case — e.g. a server with
// no agent service never builds a connection scope).
type ConnectionScope interface {
	// ScopeFor returns the per-connection override for channelName, or nil.
	ScopeFor(channelName string) rpc.Channel
	// Dispose tears the connection's scope down when the socket closes.
	Dispose()
}

// Registry is the service collection. It is safe for concurrent use: channels
// are registered at startup but looked up on every connection.
type Registry struct {
	mu       sync.RWMutex
	channels map[string]rpc.Channel
}

// NewRegistry builds an empty registry.
func NewRegistry() *Registry {
	return &Registry{channels: map[string]rpc.Channel{}}
}

// Register adds a global channel. Registering one of the three
// connection-scoped names globally is refused: those channels MUST come from a
// ConnectionScope per connection (an agent scope built for the wrong client
// mode would leak another window's subscriptions).
func (r *Registry) Register(name string, channel rpc.Channel) error {
	if channel == nil {
		return fmt.Errorf("services: register %q: nil channel", name)
	}
	if connectionScopedChannels[name] {
		return fmt.Errorf("services: %q is connection-scoped; provide it through a ConnectionScope, not a global registration", name)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, exists := r.channels[name]; exists {
		return fmt.Errorf("services: channel %q already registered", name)
	}
	r.channels[name] = channel
	return nil
}

// MustRegister is Register for startup-time registration, where a duplicate is
// a programming error.
func (r *Registry) MustRegister(name string, channel rpc.Channel) {
	if err := r.Register(name, channel); err != nil {
		panic(err)
	}
}

// Lookup returns the global channel registered under name, or an error — the
// TS get() throws `Service not registered: <name>` and the Go port keeps the
// exact message so logs are greppable across engines.
func (r *Registry) Lookup(name string) (rpc.Channel, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	channel, ok := r.channels[name]
	if !ok {
		return nil, fmt.Errorf("Service not registered: %s", name)
	}
	return channel, nil
}

// LookupOptional returns the channel or nil, the TS getOptional.
func (r *Registry) LookupOptional(name string) rpc.Channel {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.channels[name]
}

// ChannelNames returns the registered names, sorted — the iteration order the
// TS Map preserves, made deterministic here because Go map order is not.
func (r *Registry) ChannelNames() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	names := make([]string, 0, len(r.channels))
	for name := range r.channels {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// ExposeOnChannelServer ports exposeOnChannelServer(server, overrides): every
// globally registered channel is registered on the connection's server, and a
// ConnectionScope's channel — when the scope provides one — takes its place.
//
// The scope is also returned a disposal: when the returned func runs, the
// scope's Dispose runs exactly once. The caller (the WebSocket endpoint) runs
// it on socket close, the same place the TS disposes the connection scope.
func (r *Registry) ExposeOnChannelServer(server ServerChannelRegistrar, scope ConnectionScope) (dispose func()) {
	// Snapshot as name→channel pairs: sorting the names alone would desynchronize
	// the parallel slices (map iteration order is random, so names[i] must keep
	// its own channel).
	r.mu.RLock()
	pairs := make([]struct {
		name    string
		channel rpc.Channel
	}, 0, len(r.channels))
	for name, channel := range r.channels {
		pairs = append(pairs, struct {
			name    string
			channel rpc.Channel
		}{name, channel})
	}
	r.mu.RUnlock()
	sort.Slice(pairs, func(i, j int) bool { return pairs[i].name < pairs[j].name })

	var once sync.Once
	dispose = func() {
		once.Do(func() {
			if scope != nil {
				scope.Dispose()
			}
		})
	}
	for _, pair := range pairs {
		exposed := pair.channel
		if scope != nil {
			if override := scope.ScopeFor(pair.name); override != nil {
				exposed = override
			}
		}
		server.RegisterChannel(pair.name, exposed)
	}
	// A scope may also provide channels for names NOT globally registered (the
	// server variant registers IWindowControllerService only through the
	// attachment); expose those too, and only to this connection.
	if scope != nil {
		for _, name := range []string{ChannelNexAgent, ChannelProviderProvisioningTarget, ChannelWindowController} {
			if r.LookupOptional(name) == nil {
				if override := scope.ScopeFor(name); override != nil {
					server.RegisterChannel(name, override)
				}
			}
		}
	}
	return dispose
}

// ServerChannelRegistrar is the per-connection surface the Registry exposes
// onto — the shape *rpc.Server already provides (RegisterChannel).
type ServerChannelRegistrar interface {
	RegisterChannel(name string, channel rpc.Channel)
}
