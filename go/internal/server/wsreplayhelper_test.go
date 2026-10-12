package server

// Helpers shared by the replay tests: a channel that reproduces the recorded R11 answer, and a
// decoded-value comparison that ignores only what the recording normalized away (request ids,
// which each side allocates independently).

import (
	"context"

	"github.com/tylinux/nex/go/internal/rpc"
)

// ackChannel answers with the exact payload the R11 recording captured:
//
//	{ ack: { subscriptionId: "s1", mode: "snapshot", logEpoch: "e1" } }
//
// Member order matters because the codec serializes objects as JSON, whose key order is the
// insertion order — so the Object below is built in the recorded order, not alphabetically.
type ackChannel struct{}

// Call returns the recorded ack for `subscribe`, and the proxy-channel error text for anything else.
func (c *ackChannel) Call(ctx context.Context, method string, args []rpc.Value) (rpc.Value, error) {
	if method != "subscribe" {
		return nil, &rpc.Error{Message: "Method not found: " + method}
	}
	return rpc.NewObject("ack", rpc.NewObject(
		"subscriptionId", "s1",
		"mode", "snapshot",
		"logEpoch", "e1",
	)), nil
}

// Listen is unused by R11.
func (c *ackChannel) Listen(ctx context.Context, event string, arg rpc.Value) (<-chan rpc.Value, func(), error) {
	return nil, nil, &rpc.Error{Message: "Event not found: " + event}
}

var _ rpc.Channel = (*ackChannel)(nil)

// equalBodies compares two decoded RPC bodies structurally. The comparison is by value, not by
// identity, and it recurses through the Object form so a nested key ORDER difference would be
// caught — the only thing it deliberately does not compare is Go's int32/int64 split, because the
// codec is free to widen within int32.
func equalBodies(a, b any) bool {
	switch av := a.(type) {
	case rpc.Object:
		bv, ok := b.(rpc.Object)
		if !ok || av.Len() != bv.Len() {
			return false
		}
		for i, key := range av.Keys {
			other, found := bv.Get(key)
			if !found || !equalBodies(av.Values[i], other) {
				return false
			}
		}
		return true
	case []any:
		bv, ok := b.([]any)
		if !ok || len(av) != len(bv) {
			return false
		}
		for i := range av {
			if !equalBodies(av[i], bv[i]) {
				return false
			}
		}
		return true
	case int32:
		switch bv := b.(type) {
		case int32:
			return av == bv
		case int64:
			return int64(av) == bv
		case float64:
			return float64(av) == bv
		}
		return false
	case int64:
		switch bv := b.(type) {
		case int32:
			return av == int64(bv)
		case int64:
			return av == bv
		case float64:
			return float64(av) == bv
		}
		return false
	case nil:
		return b == nil || b == rpc.Undefined
	default:
		if b == rpc.Undefined {
			return false
		}
		return a == b
	}
}
