package local

// `window-controller` stub (docs/next/channels-research.md §3).
//
// The pinned section is always rendered in project mode and subscribes to both
// controller topics, so the channel must acknowledge subscriptions and emit a
// schema-valid empty snapshot for each. Task rows come from `nex-task`, not
// from this controller (see the research doc §0.1).
// TODO(local-channels): replace with the real controller runtime when task
// rows/pin state move onto the server.

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"time"

	"github.com/tylinux/nex-next/server/internal/rpcwire"
)

const controllerLogEpoch = "nex-next-controller-1"

type controllerListener struct {
	id   uint64
	emit func(rpcwire.Value)
}

// WindowController is the minimal sidebar controller.
type WindowController struct {
	mu      sync.Mutex
	nextID  uint64
	seq     uint64
	listens map[string][]controllerListener
	// topics remembers the topic each subscription id was created for, so
	// resyncControllerV4 (which only carries subscriptionId) emits a frame for
	// the right topic instead of defaulting every resync to tasks-index.
	topics map[string]string
}

// NewWindowController returns the stub controller.
func NewWindowController() *WindowController {
	return &WindowController{
		listens: make(map[string][]controllerListener),
		topics:  make(map[string]string),
	}
}

// Call implements Channel.
func (c *WindowController) Call(_ context.Context, env *Env, method string, arg rpcwire.Value) (rpcwire.Value, error) {
	switch method {
	case "listTaskList":
		return json.RawMessage(`{"items":[],"total":0,"hasMore":false}`), nil
	case "deleteArchivedTask":
		return false, nil
	case "deleteArchivedTasks":
		return json.RawMessage(`{"deleted":0,"failed":[]}`), nil
	case "mutateTask":
		return json.RawMessage("null"), nil
	case "subscribeControllerV4":
		topic := normalizeControllerTopic(stringField(firstArgument(arg), "topic"))
		c.mu.Lock()
		c.nextID++
		c.seq++
		seq := c.seq
		subscriptionID := fmt.Sprintf("ctl-%d", c.nextID)
		c.topics[subscriptionID] = topic
		c.mu.Unlock()
		c.emitSnapshot(env.ConnectionID, subscriptionID, topic, seq)
		return ackJSON(subscriptionID), nil
	case "resyncControllerV4":
		params := firstArgument(arg)
		subscriptionID := stringField(params, "subscriptionId")
		if subscriptionID == "" {
			subscriptionID = "ctl"
		}
		c.mu.Lock()
		topic := c.topics[subscriptionID]
		c.seq++
		seq := c.seq
		c.mu.Unlock()
		c.emitSnapshot(env.ConnectionID, subscriptionID, topic, seq)
		return ackJSON(subscriptionID), nil
	case "unsubscribeControllerV4":
		subscriptionID := stringField(firstArgument(arg), "subscriptionId")
		if subscriptionID != "" {
			c.mu.Lock()
			delete(c.topics, subscriptionID)
			c.mu.Unlock()
		}
		return rpcwire.Undefined{}, nil
	}
	return nil, &MethodNotFoundError{Method: method}
}

func ackJSON(subscriptionID string) json.RawMessage {
	ack, _ := json.Marshal(map[string]any{
		"ack": map[string]any{
			"subscriptionId": subscriptionID,
			"mode":           "snapshot",
			"logEpoch":       controllerLogEpoch,
		},
	})
	return ack
}

func normalizeControllerTopic(topic string) string {
	if topic == "controller/workspaces" {
		return topic
	}
	return "controller/tasks-index"
}

// emitSnapshot delivers one empty controller frame to the connection's
// onDynamicControllerFrame listeners.
func (c *WindowController) emitSnapshot(connectionID, subscriptionID, topic string, seq uint64) {
	topic = normalizeControllerTopic(topic)
	snapshot := map[string]any{
		"protocolVersion": 1,
		"logEpoch":        controllerLogEpoch,
	}
	if topic == "controller/workspaces" {
		snapshot["workspaces"] = []any{}
	} else {
		snapshot["tasks"] = []any{}
	}
	frame := map[string]any{
		"topic":          topic,
		"subscriptionId": subscriptionID,
		"logEpoch":       controllerLogEpoch,
		"fromSeq":        0,
		"toSeq":          seq,
		"sentAt":         time.Now().UnixMilli(),
		"payload": map[string]any{
			"kind":     "snapshot",
			"snapshot": snapshot,
		},
	}
	encoded, err := json.Marshal(frame)
	if err != nil {
		return
	}
	c.mu.Lock()
	listeners := append([]controllerListener(nil), c.listens[connectionID]...)
	c.mu.Unlock()
	for _, listener := range listeners {
		listener.emit(json.RawMessage(encoded))
	}
}

// Listen implements Channel. onDynamicControllerFrame is the only event; the
// payload is pushed when subscribe/resync is called, matching the reference
// service's notification ordering.
func (c *WindowController) Listen(_ context.Context, env *Env, event string, _ rpcwire.Value, emit func(rpcwire.Value)) (func(), error) {
	if event != "onDynamicControllerFrame" {
		return nil, nil
	}
	c.mu.Lock()
	c.nextID++
	id := c.nextID
	c.listens[env.ConnectionID] = append(c.listens[env.ConnectionID], controllerListener{id: id, emit: emit})
	c.mu.Unlock()
	return func() {
		c.mu.Lock()
		remaining := c.listens[env.ConnectionID][:0]
		for _, listener := range c.listens[env.ConnectionID] {
			if listener.id != id {
				remaining = append(remaining, listener)
			}
		}
		if len(remaining) == 0 {
			delete(c.listens, env.ConnectionID)
		} else {
			c.listens[env.ConnectionID] = remaining
		}
		c.mu.Unlock()
	}, nil
}
