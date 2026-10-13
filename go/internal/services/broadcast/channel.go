// channel.go adapts a broadcast Service to internal/rpc's Channel interface, so
// the Registry can expose it under the "broadcast" channel name. The mapping is
// the proxy convention from packages/rpc (send/acquireClaim/commitClaim/
// releaseClaim/tryClaim are methods; onMessage is the single event).
package broadcast

import (
	"context"
	"fmt"
	"sync"

	"github.com/tylinux/nex/go/internal/rpc"
)

// Channel adapts a Service to the rpc.Channel interface.
type Channel struct{ Service *Service }

// Compile-time proof that the adapter satisfies the channel contract.
var _ rpc.Channel = (*Channel)(nil)

// Call dispatches a proxy-convention method call.
func (c *Channel) Call(ctx context.Context, method string, args []rpc.Value) (rpc.Value, error) {
	if len(args) == 0 {
		return nil, &rpc.Error{Message: "Method not found: " + method}
	}
	switch method {
	case "send":
		msg, err := decodeMessage(args[0])
		if err != nil {
			return nil, err
		}
		if err := c.Service.Send(ctx, msg); err != nil {
			return nil, err
		}
		return nil, nil
	case "acquireClaim":
		key, _ := args[0].(string)
		result, err := c.Service.AcquireClaim(ctx, key)
		if err != nil {
			return nil, err
		}
		return claimResultToValue(result), nil
	case "commitClaim", "releaseClaim":
		lease, err := decodeLease(args[0])
		if err != nil {
			return nil, err
		}
		if method == "commitClaim" {
			return nil, c.Service.CommitClaim(ctx, lease)
		}
		return nil, c.Service.ReleaseClaim(ctx, lease)
	case "tryClaim":
		key, _ := args[0].(string)
		ok, err := c.Service.TryClaim(ctx, key)
		if err != nil {
			return nil, err
		}
		return ok, nil
	}
	return nil, &rpc.Error{Message: "Method not found: " + method}
}

// Listen serves the onMessage event: values flow until the context ends or the
// caller unsubscribes. The producer never blocks on a full buffer (drops are
// the Service's own channel policy), which is what the rpc contract requires.
func (c *Channel) Listen(ctx context.Context, event string, _ rpc.Value) (<-chan rpc.Value, func(), error) {
	if event != "onMessage" {
		return nil, nil, &rpc.Error{Message: "Event not found: " + event}
	}
	out := make(chan rpc.Value, 64)
	done := make(chan struct{})
	go func() {
		defer close(out)
		for {
			select {
			case <-ctx.Done():
				return
			case <-done:
				return
			case msg, ok := <-c.Service.Messages():
				if !ok {
					return
				}
				select {
				case out <- messageToValue(msg):
				case <-ctx.Done():
					return
				case <-done:
					return
				}
			}
		}
	}()
	var once sync.Once
	return out, func() {
		once.Do(func() { close(done) })
	}, nil
}

// decodeMessage accepts the positional body the proxy convention sends: either
// a Message-shaped map or a [channel, payload] pair.
func decodeMessage(v rpc.Value) (Message, error) {
	switch arg := v.(type) {
	case map[string]any:
		channel, _ := arg["channel"].(string)
		if channel == "" {
			return Message{}, fmt.Errorf("broadcast: channel must be a non-empty string")
		}
		return Message{Channel: channel, Payload: arg["payload"]}, nil
	case []any:
		if len(arg) >= 2 {
			channel, _ := arg[0].(string)
			return Message{Channel: channel, Payload: arg[1]}, nil
		}
	}
	return Message{}, fmt.Errorf("broadcast: invalid message argument")
}

func decodeLease(v rpc.Value) (ClaimLease, error) {
	if m, ok := v.(map[string]any); ok {
		key, _ := m["key"].(string)
		token, _ := m["token"].(string)
		return ClaimLease{Key: key, Token: token}, nil
	}
	return ClaimLease{}, fmt.Errorf("broadcast: invalid lease argument")
}

func claimResultToValue(r ClaimAcquireResult) rpc.Value {
	out := map[string]any{"status": string(r.Status)}
	if r.Lease != nil {
		out["lease"] = map[string]any{"key": r.Lease.Key, "token": r.Lease.Token}
	}
	if r.RetryAfterMs != 0 {
		out["retryAfterMs"] = r.RetryAfterMs
	}
	return out
}

func messageToValue(m Message) rpc.Value {
	return map[string]any{
		"channel":          m.Channel,
		"payload":          m.Payload,
		"sourceSubscriber": m.SourceSubscriber,
	}
}
