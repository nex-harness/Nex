package mediapreview

// channel.go adapts the Service to internal/rpc's Channel interface under the
// "media-preview" channel name.

import (
	"context"

	"github.com/tylinux/nex/go/internal/rpc"
)

// Channel adapts Service to rpc.Channel.
type Channel struct{ Service *Service }

var _ rpc.Channel = (*Channel)(nil)

// Call dispatches one IMediaPreviewService method.
func (c *Channel) Call(ctx context.Context, method string, args []rpc.Value) (rpc.Value, error) {
	if len(args) == 0 {
		return nil, &rpc.Error{Message: "Method not found: " + method}
	}
	params, _ := args[0].(map[string]any)
	switch method {
	case "prepare":
		path, _ := params["path"].(string)
		expected, _ := params["expectedKind"].(string)
		prep, err := c.Service.Prepare(ctx, path, MediaPreviewKind(expected))
		if err != nil {
			return nil, err
		}
		obj := rpc.NewObject(
			"kind", prep.Kind,
			"mediaType", prep.MediaType,
			"path", prep.Path,
			"size", prep.Size,
		)
		switch prep.Kind {
		case "local-url":
			obj.Set("url", prep.URL)
		case "inline":
			obj.Set("dataBase64", prep.DataBase64)
		}
		return obj, nil
	}
	return nil, &rpc.Error{Message: "Method not found: " + method}
}

// Listen: no events.
func (c *Channel) Listen(ctx context.Context, event string, _ rpc.Value) (<-chan rpc.Value, func(), error) {
	return nil, nil, &rpc.Error{Message: "Event not found: " + event}
}
