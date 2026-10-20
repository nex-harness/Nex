package local

// Stubs for the channels the web UI calls unconditionally but whose real
// behavior is out of scope for the MVP server pass.
//
// Shapes come from docs/next/channels-research.md §1, §3 and §9. Each channel
// keeps its literal JSON in its own file (stubs_<channel>.go) so a later
// contract pass can replace one channel without touching the others.

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/tylinux/nex-next/server/internal/rpcwire"
)

// Stub is a channel whose methods return a fixed JSON document and whose
// events never fire. Unknown events are an empty event by contract.
type Stub struct {
	Methods map[string]json.RawMessage
	// Errors maps a method to a stable error message instead of a value.
	Errors map[string]string
}

// newStubWithErrors builds a Stub that rejects selected methods.
func newStubWithErrors(methods map[string]string, errorsByMethod map[string]string) *Stub {
	stub := NewStub(methods)
	stub.Errors = errorsByMethod
	return stub
}

// NewStub builds a Stub from JSON literals keyed by method name.
func NewStub(methods map[string]string) *Stub {
	stub := &Stub{Methods: make(map[string]json.RawMessage, len(methods))}
	for method, literal := range methods {
		stub.Methods[method] = json.RawMessage(literal)
	}
	return stub
}

// Call implements Channel.
func (s *Stub) Call(_ context.Context, _ *Env, method string, _ rpcwire.Value) (rpcwire.Value, error) {
	if message, ok := s.Errors[method]; ok {
		return nil, errors.New(message)
	}
	if value, ok := s.Methods[method]; ok {
		return value, nil
	}
	return nil, &MethodNotFoundError{Method: method}
}

// Listen implements Channel: every event is an empty event.
func (s *Stub) Listen(context.Context, *Env, string, rpcwire.Value, func(rpcwire.Value)) (func(), error) {
	return nil, nil
}

// EmptyList is the shared "no rows" answer used by several stubs.
const emptyList = `[]`

// StubChannels returns every cheap stub channel, keyed by the names in
// packages/shared/src/channels.ts.
func StubChannels() map[string]Channel {
	return map[string]Channel{
		"file":                     newFileStub(),
		"system":                   newSystemStub(),
		"git":                      newGitStub(),
		"mcp-sync":                 newMcpSyncStub(),
		"coding-plan-subscription": newCodingPlanStub(),
		"settings-sync":            newSettingsSyncStub(),
		"subagents":                newSubagentsStub(),
		"client-scenes":            newClientScenesStub(),
		"onboarding-record":        newOnboardingRecordStub(),
		"bots":                     newBotsStub(),
	}
}
