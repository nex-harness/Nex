// Package local implements the channel-RPC services that live inside the Go
// server itself (setting, broadcast and later the window-controller family).
package local

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/nex-harness/nex/server/internal/rpcwire"
	"github.com/nex-harness/nex/server/internal/runners"
)

// Env is the per-connection context handed to local channels.
type Env struct {
	// ConnectionID identifies one browser WebSocket. It is stable for the
	// lifetime of the connection and used for broadcast fan-out and claims.
	ConnectionID string
}

// Channel is one server-local Channel RPC service.
//
// Call handles one promise. Listen handles one event stream; returning a nil
// stop function is valid for events that never fire. Listen must return an
// empty (nil stop) result for unknown event names rather than an error, so the
// UI's always-truthy proxy methods stay quiet.
type Channel interface {
	Call(ctx context.Context, env *Env, method string, arg rpcwire.Value) (rpcwire.Value, error)
	Listen(ctx context.Context, env *Env, event string, arg rpcwire.Value, emit func(rpcwire.Value)) (stop func(), err error)
}

// Registry maps channel names to local implementations.
type Registry struct {
	channels map[string]Channel
}

// NewRegistry returns an empty local registry.
func NewRegistry() *Registry {
	return &Registry{channels: make(map[string]Channel)}
}

// Register adds or replaces one channel implementation.
func (r *Registry) Register(name string, channel Channel) {
	r.channels[name] = channel
}

// Lookup returns the implementation for name.
func (r *Registry) Lookup(name string) (Channel, bool) {
	channel, ok := r.channels[name]
	return channel, ok
}

// Names returns the registered channel names.
func (r *Registry) Names() []string {
	names := make([]string, 0, len(r.channels))
	for name := range r.channels {
		names = append(names, name)
	}
	return names
}

// MethodNotFoundError mirrors the TypeScript ProxyChannel error for a known
// channel with an unknown method.
type MethodNotFoundError struct{ Method string }

// Error implements error.
func (e *MethodNotFoundError) Error() string { return "Method not found: " + e.Method }

// EventNotFoundError is reserved for local channels that choose to reject an
// unknown event. The browser dispatcher converts it to an empty event.
type EventNotFoundError struct{ Event string }

// Error implements error.
func (e *EventNotFoundError) Error() string { return "Event not found: " + e.Event }

// rawObject returns the JSON object text of a wire value. Tag 5 objects keep
// their exact bytes; maps are marshalled as a fallback.
//
// The web client's ProxyChannel always sends method arguments as an array
// (`channel.call(name, args)`), so a single-element array is unwrapped to the
// bare argument first.
func rawObject(value rpcwire.Value) ([]byte, error) {
	value = unwrapCallArgs(value)
	switch typed := value.(type) {
	case json.RawMessage:
		if !json.Valid([]byte(typed)) {
			return nil, errors.New("argument is not valid JSON")
		}
		return typed, nil
	case map[string]any:
		return json.Marshal(typed)
	case nil, rpcwire.Undefined:
		return []byte("{}"), nil
	default:
		return nil, fmt.Errorf("argument must be an object, got %T", value)
	}
}

// unwrapCallArgs turns the client's `[arg, ...]` call envelope into the first
// argument. None of the local channels take more than one meaningful argument;
// trailing optional arguments (e.g. setting.update's expectedAccountSettings)
// are ignored, matching the TypeScript service signatures used by the UI.
func unwrapCallArgs(value rpcwire.Value) rpcwire.Value {
	switch typed := value.(type) {
	case []any:
		if len(typed) == 0 {
			return rpcwire.Undefined{}
		}
		return unwrapCallArgs(typed[0])
	case json.RawMessage:
		trimmed := bytesTrimSpace(typed)
		if len(trimmed) > 1 && trimmed[0] == '[' {
			var elements []json.RawMessage
			if err := json.Unmarshal(trimmed, &elements); err == nil {
				if len(elements) == 0 {
					return rpcwire.Undefined{}
				}
				return unwrapCallArgs(elements[0])
			}
		}
		return typed
	}
	return value
}

func bytesTrimSpace(raw []byte) []byte {
	start, end := 0, len(raw)
	for start < end && (raw[start] == ' ' || raw[start] == '\t' || raw[start] == '\n' || raw[start] == '\r') {
		start++
	}
	for end > start && (raw[end-1] == ' ' || raw[end-1] == '\t' || raw[end-1] == '\n' || raw[end-1] == '\r') {
		end--
	}
	return raw[start:end]
}

// decodeObject decodes a wire value into a string-keyed map. Values stay raw
// so numbers round-trip without float coercion.
func decodeObject(value rpcwire.Value) (map[string]json.RawMessage, error) {
	raw, err := rawObject(value)
	if err != nil {
		return nil, err
	}
	var out map[string]json.RawMessage
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, fmt.Errorf("decoding object argument: %w", err)
	}
	if out == nil {
		out = map[string]json.RawMessage{}
	}
	return out, nil
}

// objectField returns one property of an object-valued wire argument.
func objectField(value rpcwire.Value, key string) (json.RawMessage, bool) {
	object, err := decodeObject(value)
	if err != nil {
		return nil, false
	}
	field, ok := object[key]
	return field, ok
}

// stringField returns a trimmed string property.
func stringField(value rpcwire.Value, key string) string {
	field, ok := objectField(value, key)
	if !ok {
		return ""
	}
	var text string
	if err := json.Unmarshal(field, &text); err != nil {
		return ""
	}
	return text
}

// firstArgument returns the first positional argument of a call.
//
// ProxyChannel.toService sends `[arg0, arg1, ...]`; methods whose only
// parameter is a primitive (broadcast.acquireClaim(key)) therefore arrive as
// `["key"]`, not as an object. This helper extracts it.
func firstArgument(value rpcwire.Value) rpcwire.Value {
	switch typed := value.(type) {
	case []any:
		if len(typed) == 0 {
			return rpcwire.Undefined{}
		}
		return typed[0]
	case json.RawMessage:
		trimmed := bytesTrimSpace(typed)
		if len(trimmed) > 1 && trimmed[0] == '[' {
			var elements []json.RawMessage
			if err := json.Unmarshal(trimmed, &elements); err == nil {
				if len(elements) == 0 {
					return rpcwire.Undefined{}
				}
				return elements[0]
			}
		}
		return typed
	}
	return value
}

// parseRunnerIdentity parses a server-built `runner:<runnerId>:<path>` identity.
func parseRunnerIdentity(identity string) (string, string, bool) {
	return runners.ParseIdentity(strings.TrimSpace(identity))
}

// stringArgument decodes a positional string argument.
func stringArgument(value rpcwire.Value) string {
	raw, ok := firstArgument(value).(json.RawMessage)
	if !ok {
		if text, isString := firstArgument(value).(string); isString {
			return strings.TrimSpace(text)
		}
		return ""
	}
	var text string
	if err := json.Unmarshal(raw, &text); err != nil {
		return ""
	}
	return strings.TrimSpace(text)
}
