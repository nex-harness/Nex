package local

// The `config` channel the browser may call (`status`; exportLocal is forwarded
// to a runner by the router). Split out of config.go for the 400-line file
// budget.

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/tylinux/nex-next/server/internal/configstore"
	"github.com/tylinux/nex-next/server/internal/rpcwire"
)

// configChannel serves the parts of the `config` channel the browser may call.
// exportLocal is forwarded to a runner (it reads that machine's files).
type configChannel struct {
	manager *Manager
}

func newConfigChannel(manager *Manager) *configChannel {
	return &configChannel{manager: manager}
}

// Call implements Channel.
func (c *configChannel) Call(ctx context.Context, _ *Env, method string, arg rpcwire.Value) (rpcwire.Value, error) {
	switch method {
	case "status":
		return c.status()
	case "exportLocal":
		// Only reached when the router sent it here (no runnerId ⇒ first runner
		// is handled by the router, so a call here is missing the id).
		runnerID := strings.TrimSpace(stringField(arg, "runnerId"))
		if runnerID == "" {
			return nil, fmt.Errorf("runnerId is required")
		}
		result, err := c.manager.ExportLocal(ctx, runnerID)
		if err != nil {
			return nil, err
		}
		encoded, err := json.Marshal(result)
		if err != nil {
			return nil, err
		}
		return json.RawMessage(encoded), nil
	}
	return nil, &MethodNotFoundError{Method: method}
}

func (c *configChannel) status() (rpcwire.Value, error) {
	doc, err := c.manager.store.Snapshot()
	if err != nil {
		return nil, err
	}
	effective := doc.EffectiveForRunner("")
	runners := []map[string]any{}
	for _, id := range c.manager.KnownRunners() {
		state, _ := c.manager.Applied(id)
		runners = append(runners, map[string]any{
			"runnerId":       id,
			"connected":      state.Connected,
			"lastApplied":    state.LastApplied,
			"ok":             state.OK,
			"pendingVersion": state.PendingVersion,
			"errors":         state.Errors,
		})
	}
	body, err := json.Marshal(map[string]any{
		"schema":          configstore.SchemaVersion,
		"version":         doc.Version,
		"owned":           false,
		"globalProviders": len(effective.Providers),
		"runners":         runners,
	})
	if err != nil {
		return nil, err
	}
	return json.RawMessage(body), nil
}

// Listen implements Channel: onDidChange fires with the doc version.
func (c *configChannel) Listen(context.Context, *Env, string, rpcwire.Value, func(rpcwire.Value)) (func(), error) {
	return nil, nil
}
