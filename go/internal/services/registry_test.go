package services

import (
	"context"
	"strings"
	"testing"

	"github.com/tylinux/nex/go/internal/rpc"
)

// recordingRegistrar collects what a Registry exposed onto it.
type recordingRegistrar struct {
	registered map[string]rpc.Channel
}

func (rec *recordingRegistrar) RegisterChannel(name string, channel rpc.Channel) {
	if rec.registered == nil {
		rec.registered = map[string]rpc.Channel{}
	}
	rec.registered[name] = channel
}

// stubChannel is a minimal channel for identity comparisons.
type stubChannel struct{ id string }

func (c stubChannel) Call(ctx context.Context, method string, args []rpc.Value) (rpc.Value, error) {
	return c.id, nil
}
func (c stubChannel) Listen(ctx context.Context, event string, arg rpc.Value) (<-chan rpc.Value, func(), error) {
	return nil, nil, nil
}

// fixedScope is a ConnectionScope with fixed overrides and a disposal counter.
type fixedScope struct {
	overrides map[string]rpc.Channel
	disposals int
}

func (s *fixedScope) ScopeFor(name string) rpc.Channel { return s.overrides[name] }
func (s *fixedScope) Dispose()                         { s.disposals++ }

// TestRegistryRegisterLookup pins register/get/getOptional, including the exact
// not-registered error message the TS throws.
func TestRegistryRegisterLookup(t *testing.T) {
	reg := NewRegistry()
	broadcast := stubChannel{id: "broadcast"}
	reg.MustRegister("broadcast", broadcast)

	got, err := reg.Lookup("broadcast")
	if err != nil || got != rpc.Channel(broadcast) {
		t.Fatalf("lookup: %v, %v", got, err)
	}
	if optional := reg.LookupOptional("broadcast"); optional != rpc.Channel(broadcast) {
		t.Fatalf("lookupOptional returned %v", optional)
	}
	if missing := reg.LookupOptional("nope"); missing != nil {
		t.Fatalf("lookupOptional must be nil for a missing channel, got %v", missing)
	}
	_, err = reg.Lookup("nope")
	if err == nil || err.Error() != "Service not registered: nope" {
		t.Fatalf("error = %v, want the exact TS message", err)
	}

	names := reg.ChannelNames()
	if len(names) != 1 || names[0] != "broadcast" {
		t.Fatalf("names = %v", names)
	}

	// Duplicate registration is refused.
	if err := reg.Register("broadcast", stubChannel{id: "other"}); err == nil || !strings.Contains(err.Error(), "already registered") {
		t.Fatalf("duplicate register error = %v", err)
	}
	// Nil channel is refused.
	if err := reg.Register("nil-chan", nil); err == nil {
		t.Fatal("nil channel must be refused")
	}
}

// TestRegistryConnectionScopedRefusedGlobally pins the Phase-2-fact-7 rule: the
// three scoped channels cannot be registered globally.
func TestRegistryConnectionScopedRefusedGlobally(t *testing.T) {
	reg := NewRegistry()
	for _, name := range []string{ChannelNexAgent, ChannelProviderProvisioningTarget, ChannelWindowController} {
		if err := reg.Register(name, stubChannel{id: name}); err == nil {
			t.Fatalf("%s must be refused as a global registration", name)
		}
		if !IsConnectionScoped(name) {
			t.Errorf("IsConnectionScoped(%q) must be true", name)
		}
	}
	if IsConnectionScoped("broadcast") {
		t.Error("broadcast is not connection-scoped")
	}
}

// TestRegistryOverrideWins pins exposeOnChannelServer + overrides: a scope's
// channel replaces the global one for that connection, others see the global.
func TestRegistryOverrideWins(t *testing.T) {
	reg := NewRegistry()
	reg.MustRegister("broadcast", stubChannel{id: "global-broadcast"})
	reg.MustRegister("setting", stubChannel{id: "global-setting"})

	scope := &fixedScope{overrides: map[string]rpc.Channel{
		// The scoped override replaces the global "setting" for THIS connection.
		"setting": stubChannel{id: "scope-setting"},
		// A scoped-only channel (no global registration) is exposed too.
		ChannelWindowController: stubChannel{id: "scope-window-controller"},
	}}

	rec := &recordingRegistrar{}
	dispose := reg.ExposeOnChannelServer(rec, scope)
	defer dispose()

	if got := rec.registered["setting"]; got.(stubChannel).id != "scope-setting" {
		t.Errorf("per-connection override must win for setting, got %v", got)
	}
	if got := rec.registered["broadcast"]; got.(stubChannel).id != "global-broadcast" {
		t.Errorf("global channel must stay for broadcast, got %v", got)
	}
	if got := rec.registered[ChannelWindowController]; got == nil || got.(stubChannel).id != "scope-window-controller" {
		t.Errorf("scope-only channel must be exposed, got %v", got)
	}

	// Disposal runs exactly once even if called twice.
	dispose()
	dispose()
	if scope.disposals != 1 {
		t.Errorf("scope disposals = %d, want 1", scope.disposals)
	}
}

// TestRegistryExposeWithoutScope pins the no-connection path: every channel is
// the global one, and disposal is a no-op.
func TestRegistryExposeWithoutScope(t *testing.T) {
	reg := NewRegistry()
	reg.MustRegister("broadcast", stubChannel{id: "global-broadcast"})
	rec := &recordingRegistrar{}
	dispose := reg.ExposeOnChannelServer(rec, nil)
	defer dispose()
	if got := rec.registered["broadcast"]; got.(stubChannel).id != "global-broadcast" {
		t.Fatalf("global channel must be exposed, got %v", got)
	}
	dispose() // must not panic
}

// TestRegistryScopeWithoutOverride keeps the global channel when the scope has
// no view for that name (the TS `overrides.get(name) ?? instance`).
func TestRegistryScopeWithoutOverride(t *testing.T) {
	reg := NewRegistry()
	reg.MustRegister("broadcast", stubChannel{id: "global-broadcast"})
	scope := &fixedScope{overrides: map[string]rpc.Channel{ChannelNexAgent: stubChannel{id: "scope-agent"}}}
	rec := &recordingRegistrar{}
	defer reg.ExposeOnChannelServer(rec, scope)()

	if got := rec.registered["broadcast"]; got.(stubChannel).id != "global-broadcast" {
		t.Errorf("broadcast must keep the global channel, got %v", got)
	}
	if got := rec.registered[ChannelNexAgent]; got == nil || got.(stubChannel).id != "scope-agent" {
		t.Errorf("agent scope must be exposed, got %v", got)
	}
}
