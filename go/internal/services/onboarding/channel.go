// channel.go adapts the onboarding Service to rpc.Channel. The proxy
// convention passes positional arguments: appendRecord(deviceMid, entry),
// dismissOnboarding(deviceMid), updateRecordPreferences(patch); the
// no-argument methods accept anything.

package onboarding

import (
	"context"

	"github.com/tylinux/nex/go/internal/rpc"
)

// Channel adapts Service to rpc.Channel.
type Channel struct{ Service *Service }

var _ rpc.Channel = (*Channel)(nil)

// Call dispatches one IOnboardingRecordService method.
func (c *Channel) Call(ctx context.Context, method string, args []rpc.Value) (rpc.Value, error) {
	switch method {
	case "appendRecord":
		deviceMid := argString(args, 0)
		entry, err := decodeEntry(argValue(args, 1))
		if err != nil {
			return nil, err
		}
		return nil, c.Service.AppendRecord(ctx, deviceMid, entry)
	case "dismissOnboarding":
		return nil, c.Service.DismissOnboarding(ctx, argString(args, 0))
	case "getLatestEntry":
		entry, err := c.Service.GetLatestEntry(ctx)
		if err != nil {
			return nil, err
		}
		if entry == nil {
			return rpc.Null, nil
		}
		return entryToRPC(*entry), nil
	case "updateRecordPreferences":
		patch := PreferencesPatch{}
		if params, ok := argValue(args, 0).(map[string]any); ok {
			if v, ok := params["memoryEnabled"]; ok {
				patch.MemoryEnabled, patch.MemoryEnabledSet = v, true
			}
			if v, ok := params["proactiveSuggestionsEnabled"]; ok {
				patch.ProactiveSuggestionsEnabled, patch.ProactiveSuggestionsEnabledSet = v, true
			}
		}
		return nil, c.Service.UpdateRecordPreferences(ctx, patch)
	case "getRecords":
		file, err := c.Service.GetRecords(ctx)
		if err != nil {
			return nil, err
		}
		if file == nil {
			return rpc.Null, nil
		}
		return fileToRPC(file), nil
	case "clearRecords":
		return nil, c.Service.ClearRecords(ctx)
	}
	return nil, &rpc.Error{Message: "Method not found: " + method}
}

// Listen: no events.
func (c *Channel) Listen(ctx context.Context, event string, _ rpc.Value) (<-chan rpc.Value, func(), error) {
	return nil, nil, &rpc.Error{Message: "Event not found: " + event}
}

// argValue returns args[i] (nil when absent).
func argValue(args []rpc.Value, i int) rpc.Value {
	if i < len(args) {
		return args[i]
	}
	return nil
}

// argString returns args[i] as a string ("" otherwise); a single-object arg
// is also accepted for the named-field spelling.
func argString(args []rpc.Value, i int) string {
	value := argValue(args, i)
	if s, ok := value.(string); ok {
		return s
	}
	if params, ok := value.(map[string]any); ok && i == 0 {
		for _, key := range []string{"deviceMid", "homedir"} {
			if s, ok := params[key].(string); ok {
				return s
			}
		}
	}
	return ""
}

func decodeEntry(v any) (Entry, error) {
	obj, ok := v.(map[string]any)
	if !ok {
		return Entry{}, &rpc.Error{Message: "entry must be an object"}
	}
	completedAt, _ := obj["completedAt"].(string)
	return Entry{
		Occupation:                  obj["occupation"],
		InterfaceMode:               obj["interfaceMode"],
		MemoryEnabled:               obj["memoryEnabled"],
		ProactiveSuggestionsEnabled: obj["proactiveSuggestionsEnabled"],
		CompletedAt:                 completedAt,
	}, nil
}

func entryToRPC(entry Entry) rpc.Object {
	return rpc.NewObject(
		"userId", entry.UserID,
		"occupation", entry.Occupation,
		"interfaceMode", entry.InterfaceMode,
		"memoryEnabled", entry.MemoryEnabled,
		"proactiveSuggestionsEnabled", entry.ProactiveSuggestionsEnabled,
		"completedAt", entry.CompletedAt,
		"uploadState", entry.UploadState,
	)
}

func fileToRPC(file *RecordFile) rpc.Object {
	entries := make([]any, len(file.Entries))
	for i, entry := range file.Entries {
		entries[i] = entryToRPC(entry)
	}
	decisions := make([]any, len(file.Decisions))
	for i, decision := range file.Decisions {
		decisions[i] = rpc.NewObject(
			"userId", decision.UserID,
			"status", decision.Status,
			"reason", decision.Reason,
			"decidedAt", decision.DecidedAt,
		)
	}
	return rpc.NewObject(
		"version", file.Version,
		"deviceMid", file.DeviceMid,
		"entries", entries,
		"decisions", decisions,
	)
}
