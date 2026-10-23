package automations

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/tylinux/nex-next/server/internal/rpcwire"
)

// ChannelName is the browser channel that carries these methods.
const ChannelName = "nex-agent"

// Methods lists the call methods this channel answers (nex-agent automation
// methods; docs/next/automations-views-search.md §A).
var Methods = []string{
	"listAllAutomations",
	"listAutomations",
	"createAutomation",
	"updateAutomation",
	"deleteAutomation",
	"setAutomationEnabled",
	"restartAutomation",
	"runAutomationNow",
	"listAutomationRuns",
	"deleteAutomationRun",
}

// HandlesCall reports whether method is answered by this channel.
func (s *Service) HandlesCall(method string) bool {
	switch method {
	case "listAllAutomations", "listAutomations", "createAutomation", "updateAutomation",
		"deleteAutomation", "setAutomationEnabled", "restartAutomation", "runAutomationNow",
		"listAutomationRuns", "deleteAutomationRun":
		return true
	}
	return false
}

// Call implements the local channel handler. arg is the browser's positional
// argument array `[arg0]` in rpcwire form (see internal/local); it is
// converted to the JSON bridge representation and unwrapped by call.
//
// The wiring step registers this on the local channel registry (alongside
// internal/local's nex-agent entries) with the router's no-workspace
// nex-agent table entry for every method here.
func (s *Service) Call(ctx context.Context, method string, arg rpcwire.Value) (rpcwire.Value, error) {
	raw, err := rpcwire.ToJSON(arg)
	if err != nil {
		return nil, fmt.Errorf("encoding arguments: %w", err)
	}
	result, err := s.call(ctx, method, json.RawMessage(raw))
	if err != nil {
		return nil, err
	}
	return rpcwire.Value(result), nil
}
