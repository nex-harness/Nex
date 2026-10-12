package rpc

import "errors"

// This file is the client half of the error contract in errors.go: it rebuilds the *Error a
// handler produced from the PromiseError payload the server sent.
//
// The rule is a port of the TS client's response handler, which reconstructs
// `new Error(response.data.message)`, sets `.name`, joins `.stack` back with "\n", and copies
// the nine passthrough keys onto the result. The stack is the one place Go deliberately keeps
// the wire shape instead of the JS shape: the TS join exists so a JS consumer can read a
// stack string, and re-splitting it would be pointless work, whereas keeping the array means a
// payload this client rebuilds re-serializes to the bytes the server sent.

// errorFromPayload reconstitutes the PromiseError payload into the *Error a handler produced.
// A key the payload omits stays nil, which is the Go spelling of the TS
// `if (value !== undefined)` guard on the passthrough copy.
func errorFromPayload(body Value) error {
	obj, ok := body.(Object)
	if !ok {
		// The TS would build `new Error(undefined)`, whose message is the string "undefined".
		// Go cannot do that without quoting a placeholder into an error that callers log and
		// compare, so the payload's shape is reported instead.
		return errors.New("rpc: PromiseError payload is not an object")
	}
	e := &Error{}
	if message, ok := obj.Get("message"); ok {
		e.Message, _ = message.(string)
	}
	if name, ok := obj.Get("name"); ok {
		e.Name, _ = name.(string)
	}
	if stack, ok := obj.Get("stack"); ok {
		e.Stack = stackLines(stack)
	}
	for _, field := range []struct {
		key string
		dst *Value
	}{
		{"code", &e.Code},
		{"kind", &e.Kind},
		{"status", &e.Status},
		{"retryAfterMs", &e.RetryAfterMs},
		{"data", &e.Data},
		{"detail", &e.Detail},
		{"details", &e.Details},
		{"taskId", &e.TaskID},
		{"traceId", &e.TraceID},
	} {
		if value, ok := obj.Get(field.key); ok {
			*field.dst = value
		}
	}
	return e
}

// stackLines reads the pre-split stack, dropping anything that is not an array of strings
// rather than failing: a peer that sends a malformed stack must not turn a readable error into
// an unreadable one.
func stackLines(stack Value) []string {
	frames, ok := stack.([]Value)
	if !ok {
		return nil
	}
	out := make([]string, 0, len(frames))
	for _, frame := range frames {
		line, _ := frame.(string)
		out = append(out, line)
	}
	return out
}
