// rpcvalues.go converts the bots result structs into rpc.Values.
//
// The conversion goes through encoding/json rather than hand-written
// rpc.NewObject pairs: the wire shape of these results is the shared contract
// (packages/shared/src/bots.ts), so the JSON tags ARE the contract, and a
// hand-written object would be a second place to keep in sync. The
// conversationshare channel uses the same round-trip for the same reason.
//
// Numbers are the one place the round-trip is lossy in principle: an int lands
// as float64, which the RPC codec re-tags as Int because the tag is chosen by
// VALUE, not by the Go type (see internal/rpc/value.go).
package bots

import (
	"encoding/json"

	"github.com/tylinux/nex/go/internal/rpc"
)

// toValue encodes a result struct into an rpc.Value tree.
func toValue(value any) (rpc.Value, error) {
	data, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	var decoded any
	if err := json.Unmarshal(data, &decoded); err != nil {
		return nil, err
	}
	return decoded, nil
}

func configToValue(config ConfigFile) (rpc.Value, error) { return toValue(config) }

func configsToValue(configs []Config) (rpc.Value, error) {
	if configs == nil {
		return []any{}, nil
	}
	return toValue(configs)
}

func workspaceRefsToValue(refs []WorkspaceRef) (rpc.Value, error) {
	if refs == nil {
		return []any{}, nil
	}
	return toValue(refs)
}

func statesToValue(states []State) (rpc.Value, error) {
	if states == nil {
		return []any{}, nil
	}
	return toValue(states)
}

func outboundsToValue(messages []OutboundMessage) (rpc.Value, error) {
	if messages == nil {
		return []any{}, nil
	}
	return toValue(messages)
}
