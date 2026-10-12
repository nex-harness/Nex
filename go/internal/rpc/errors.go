package rpc

import (
	"errors"
	"fmt"
	"runtime/debug"
	"strings"
)

// This file is the port of the error mapping in `onPromise` (packages/rpc/src/channelServer.ts
// lines 134-185). The wire payload is a PromiseError (202) whose body is the object's own
// enumerable properties, so the member ORDER is part of the contract: `message`, `name`,
// `stack`, then the passthrough keys in the order the TS `passthroughKeys` array lists them.

// passthroughKeys are copied from the failed error onto the PromiseError payload when set.
// ChannelClient copies the same list back onto the reconstructed Error, so a key that is
// missing here is silently lost to the caller.
var passthroughKeys = []string{
	"code",
	"kind",
	"status",
	"retryAfterMs",
	"data",
	"detail",
	"details",
	"taskId",
	"traceId",
}

// Error is an RPC error carrying the structured metadata the protocol passes through. A
// handler returns one of these instead of a bare `error` to control the payload the client
// rebuilds; `errors.As` finds it whether it is wrapped.
//
// The nine optional fields mirror the passthrough keys one-for-one and are omitted from the
// payload when nil, matching `if (value !== undefined)` in the TS.
type Error struct {
	// Name is the JS `error.name`. Empty means "Error", which is what an `errors.New` value
	// maps to. It is never omitted: the TS payload always carries `name`.
	Name string
	// Stack is the pre-split `error.stack`. When nil the payload carries `stack: undefined`,
	// which JSON.stringify then DROPS — the key disappears from the object entirely, exactly
	// as it does for a JS error created without a stack. Populate it via CaptureStack.
	Stack []string
	// Message is the JS `error.message`; it maps to Error() so `errors.Is/As` keep working.
	Message string

	Code         Value
	Kind         Value
	Status       Value
	RetryAfterMs Value
	Data         Value
	Detail       Value
	Details      Value
	TaskID       Value
	TraceID      Value
}

// Error implements the error interface.
func (e *Error) Error() string { return e.Message }

// CaptureStack returns the caller's stack, split on newlines, the way the TS builds
// `error.stack.split("\n")`. Go stacks are already newline-separated and are prefixed with
// the function name rather than the bare "Message: text" head JS produces; the frames are
// the useful part and the client only joins them back with "\n".
func CaptureStack() []string {
	lines := strings.Split(strings.TrimRight(string(debug.Stack()), "\n"), "\n")
	out := make([]string, 0, len(lines))
	for _, line := range lines {
		if strings.TrimSpace(line) == "" {
			continue
		}
		out = append(out, line)
	}
	return out
}

// NewError returns an *Error with Name defaulted to "Error" and a stack captured at the
// call site, so a handler can add metadata without hand-assembling the payload.
func NewError(message string) *Error {
	return &Error{Name: "Error", Message: message, Stack: CaptureStack()}
}

// Errorf is NewError with formatting.
func Errorf(format string, args ...any) *Error {
	return NewError(fmt.Sprintf(format, args...))
}

// AsError extracts an *Error from err's chain. The second result is false for any error
// that is not (or does not wrap) an *Error, which is the `error instanceof Error` test the
// TS performs to choose between PromiseError and PromiseErrorObj.
func AsError(err error) (*Error, bool) {
	var rpcErr *Error
	if errors.As(err, &rpcErr) {
		return rpcErr, true
	}
	return nil, false
}

// payload returns the ordered object the TS error literal produces.
//
// `stack` is always present in the TS source, as `string[] | undefined`. Here Undefined
// stands for that undefined and the serializer drops the member, so the two cases agree on
// the wire: a stack is an array, and its absence is an absent key.
//
// The nine passthrough members follow, each appended only when it is set — the Go spelling
// of `if (value !== undefined)`, since a nil Value is the only "not set" a Value can have.
func (e *Error) payload() Object {
	name := e.Name
	if name == "" {
		name = "Error"
	}
	stack := Value(Undefined)
	if e.Stack != nil {
		stack = stringsToValues(e.Stack)
	}
	obj := NewObject("message", e.Message, "name", name, "stack", stack)
	for _, field := range []struct {
		key string
		val Value
	}{
		{"code", e.Code},
		{"kind", e.Kind},
		{"status", e.Status},
		{"retryAfterMs", e.RetryAfterMs},
		{"data", e.Data},
		{"detail", e.Detail},
		{"details", e.Details},
		{"taskId", e.TaskID},
		{"traceId", e.TraceID},
	} {
		if field.val != nil {
			obj.Set(field.key, field.val)
		}
	}
	return obj
}

// errorPayload builds the PromiseError body for any error. A non-*Error carries only
// `{message, name: "Error", stack: undefined}` — the TS falls back to `name` from the
// prototype chain, and every Go error is a plain error, so "Error" is the honest answer.
func errorPayload(err error) Object {
	if rpcErr, ok := AsError(err); ok {
		return rpcErr.payload()
	}
	return NewObject("message", err.Error(), "name", "Error", "stack", Undefined)
}

// traceIDKey is the passthrough key the TS client already copies onto the Error it rebuilds, and
// therefore the one a server can fill in without a protocol change. It is listed last in
// passthroughKeys, so appending it here keeps the payload's member order the TS produces.
const traceIDKey = "traceId"

// errorPayloadTraced is errorPayload with the connection's traceId appended when there is one.
//
// Two rules make this safe to do unconditionally rather than per call site: a connection with no
// traceId (Options.TraceId empty — every test, and the Node-equivalent default) produces the
// bytes it always did, so the recorded R01-R11 vectors are unaffected; and a handler that set
// traceId itself keeps its own value, because a per-request id beats a per-connection one.
func errorPayloadTraced(err error, traceID string) Object {
	payload := errorPayload(err)
	if traceID == "" {
		return payload
	}
	if _, present := payload.Get(traceIDKey); present {
		return payload
	}
	payload.Set(traceIDKey, traceID)
	return payload
}

func stringsToValues(lines []string) []Value {
	out := make([]Value, len(lines))
	for i, line := range lines {
		out[i] = line
	}
	return out
}

// UnknownChannelMessage is the message of the PromiseError the server sends when a request
// names a channel that was still unregistered when the pending timer expired. The timeout
// value is interpolated exactly as the TS template literal does.
func UnknownChannelMessage(channelName string, timeoutDelayMS int) string {
	return fmt.Sprintf("Channel name '%s' timed out after %dms", channelName, timeoutDelayMS)
}

// ErrEventOverflow is delivered as the payload of the final EventFire of a listener whose
// bounded buffer overflowed, and closes that listener.
//
// Policy (documented, per step 2.3): a listener that outruns the socket must not be allowed to
// grow the process heap, and the connection reader must never block on it. So the listener
// goroutine gives up — it fires this error once with the listener's request id and stops —
// and the connection stays up for every other listener. Slow-listener death is the client's
// problem to observe (it sees a malformed event), which is strictly better than the
// alternatives: an unbounded queue grows without limit, and blocking would wedge the reader
// and every in-flight call behind one stuck subscription.
var ErrEventOverflow = errors.New("rpc: event listener overflowed its bounded buffer")
