// channel.go + cipher vector test for the credential package.

package credential

import (
	"context"

	"github.com/tylinux/nex/go/internal/rpc"
)

// Channel adapts Service to rpc.Channel.
type Channel struct{ Service *Service }

var _ rpc.Channel = (*Channel)(nil)

// Call dispatches one ICredentialService method.
func (c *Channel) Call(ctx context.Context, method string, args []rpc.Value) (rpc.Value, error) {
	if len(args) == 0 {
		return nil, &rpc.Error{Message: "Method not found: " + method}
	}
	// The RPC convention passes the positional args array; each method takes
	// (key[, value]).
	key, _ := args[0].(string)
	switch method {
	case "load":
		value, err := c.Service.Load(ctx, key)
		if err != nil {
			return nil, err
		}
		if value == "" {
			// The TS returns null for a missing key (load returns
			// string | null); an empty string value cannot be stored
			// (validateValue allows it, but the RPC null/undefined
			// distinction requires null here).
			return rpc.Null, nil
		}
		return value, nil
	case "save":
		if len(args) < 2 {
			return nil, &rpc.Error{Message: "save requires (key, value)"}
		}
		value, _ := args[1].(string)
		if err := c.Service.Save(ctx, key, value); err != nil {
			return nil, err
		}
		return nil, nil
	case "delete":
		if err := c.Service.Delete(ctx, key); err != nil {
			return nil, err
		}
		return nil, nil
	}
	return nil, &rpc.Error{Message: "Method not found: " + method}
}

// Listen: no events.
func (c *Channel) Listen(ctx context.Context, event string, _ rpc.Value) (<-chan rpc.Value, func(), error) {
	return nil, nil, &rpc.Error{Message: "Event not found: " + event}
}
