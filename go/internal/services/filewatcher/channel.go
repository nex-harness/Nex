package filewatcher

// channel.go adapts the Service to internal/rpc's Channel interface under the
// "file-watcher" channel name. watch/unwatch are methods; onDynamicChange(id)
// is a DYNAMIC EVENT: the listen argument selects the watcher, and a stale id
// subscribes to a closed (never-firing) stream instead of erroring — the TS
// returns Event.None so a removed watcher cannot break the subscription path.

import (
	"context"
	"github.com/tylinux/nex/go/internal/rpc"
)

// Channel adapts Service to rpc.Channel.
type Channel struct{ Service *Service }

var _ rpc.Channel = (*Channel)(nil)

// Call dispatches watch/unwatch/disposeAll.
func (c *Channel) Call(ctx context.Context, method string, args []rpc.Value) (rpc.Value, error) {
	switch method {
	case "disposeAll":
		c.Service.DisposeAll()
		return nil, nil
	case "watch":
		if len(args) == 0 {
			return nil, &rpc.Error{Message: "Method not found: " + method}
		}
		params, _ := args[0].(map[string]any)
		path, _ := params["path"].(string)
		recursive := false
		if v, ok := params["recursive"].(bool); ok {
			recursive = v
		}
		id, err := c.Service.Watch(path, recursive)
		if err != nil {
			return nil, err
		}
		return rpc.NewObject("id", id), nil
	case "unwatch":
		if len(args) == 0 {
			return nil, &rpc.Error{Message: "Method not found: " + method}
		}
		params, _ := args[0].(map[string]any)
		id, _ := params["id"].(string)
		c.Service.Unwatch(id)
		return nil, nil
	}
	return nil, &rpc.Error{Message: "Method not found: " + method}
}

// Listen serves onDynamicChange(id).
func (c *Channel) Listen(ctx context.Context, event string, arg rpc.Value) (<-chan rpc.Value, func(), error) {
	if event != "onDynamicChange" {
		return nil, nil, &rpc.Error{Message: "Event not found: " + event}
	}
	id := ""
	switch v := arg.(type) {
	case string:
		id = v
	case map[string]any:
		// the capture driver sends the listen argument directly; tolerate an
		// object form for robustness
		if raw, ok := v["id"].(string); ok {
			id = raw
		}
	case []any:
		if len(v) > 0 {
			if s, ok := v[0].(string); ok {
				id = s
			}
		}
	}
	source, unsub := c.Service.Subscribe(id)
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
			case ev, ok := <-source:
				if !ok {
					return
				}
				obj := rpc.NewObject("dirPath", ev.DirPath)
				if ev.ChangedPath != "" {
					obj.Set("changedPath", ev.ChangedPath)
				}
				select {
				case out <- obj:
				case <-ctx.Done():
					return
				case <-done:
					return
				}
			}
		}
	}()
	return out, unsub, nil
}
