// channel.go: the rpc.Channel adapter for IPetService ("pets").
// No on* events exist on this interface.
package pets

import (
	"context"

	"github.com/tylinux/nex/go/internal/rpc"
)

// Channel adapts the service to the RPC surface.
type Channel struct{ Service *Service }

var _ rpc.Channel = (*Channel)(nil)

// Listen: IPetService has no on* events.
func (c *Channel) Listen(_ context.Context, event string, _ rpc.Value) (<-chan rpc.Value, func(), error) {
	return nil, nil, &rpc.Error{Message: "Event not found: " + event}
}

// Call dispatches one IPetService method.
func (c *Channel) Call(_ context.Context, method string, args []rpc.Value) (rpc.Value, error) {
	switch method {
	case "listPets":
		if len(args) != 0 {
			return nil, &rpc.Error{Message: "Method not found: " + method}
		}
		return c.Service.ListPets(), nil
	case "refreshPets":
		if len(args) != 0 {
			return nil, &rpc.Error{Message: "Method not found: " + method}
		}
		return c.Service.RefreshPets(), nil
	case "getPetSpritesheetPath":
		petID, err := stringArg(args)
		if err != nil {
			return nil, err
		}
		if result := c.Service.GetPetSpritesheetPath(petID); result != nil {
			return rpc.NewObject("path", result.Path), nil
		}
		return nil, nil
	case "getPetSpritesheetUrl":
		petID, err := stringArg(args)
		if err != nil {
			return nil, err
		}
		if result := c.Service.GetPetSpritesheetURL(petID); result != nil {
			return rpc.NewObject("url", result.URL), nil
		}
		return nil, nil
	default:
		return nil, &rpc.Error{Message: "Method not found: " + method}
	}
}

// stringArg reads params.petId from the single object argument.
func stringArg(args []rpc.Value) (string, error) {
	if len(args) == 0 {
		return "", &rpc.Error{Message: "invalid params"}
	}
	params, _ := args[0].(map[string]any)
	petID, _ := params["petId"].(string)
	return petID, nil
}
