package runtime

import (
	"encoding/json"

	"github.com/tylinux/nex/go/internal/emitters"
	"github.com/tylinux/nex/go/internal/v4pub"
)

// flushSubscribers drains every subscription's 30 ms flush buffer into a wire
// frame. The desktop-continuous profile coalesces within the window; the runtime
// snapshots the whole buffer per flush (the host drives the timer).
//
// It goes through ReserveFlush (not Flush) so the reservation's LogicalFrameID
// and LogicalFrameOrdinal survive to the wire encoder — the host's wire schema
// requires a non-empty logicalFrameId, and dropping it makes the host discard
// the frame as invalid.
func (r *Runtime) flushSubscribers(s *session) {
	for _, id := range s.subscriptionIDs() {
		if reservation := s.pub.ReserveFlush(id); reservation != nil {
			if reservation.Commit() {
				r.emitWireFrame(reservation.Frame, reservation)
			}
		}
	}
}

// emitStateUpdated emits the post-turn state.updated notification. The patch is
// derived from the provider config (the session-state mapper is not ported).
func (r *Runtime) emitStateUpdated(s *session, reason string) {
	patch := emitters.StatePatch{
		Mode:           "build",
		Available:      r.availableModels(),
		Current:        emitters.ModelRef{ProviderID: r.deps.Provider.ProviderID, ModelID: r.deps.Provider.ModelID},
		CurrentOptions: s.modelSelectionOptions(),
		LastUsed:       &emitters.ModelRef{ProviderID: r.deps.Provider.ProviderID, ModelID: r.deps.Provider.ModelID},
		Permission:     "build",
		ThoughtAvail:   r.thoughtLevels(),
		ThoughtCurrent: r.deps.Provider.DefaultReasoning,
		ThoughtEnabled: true,
	}
	msg := emitters.StateUpdatedFrame(s.id, emitters.WorkspaceRef{
		WorkspacePath: s.ws.Path, WorkspaceKey: s.ws.Key,
	}, reason, 1, patch)
	s.write(msg)
}

// modelOptionContextWindow is the contextWindow the session-settings mapper puts
// on a model option. The Node app's model registry supplies it (the mock model
// reports 200000) and it is independent of the provider config's
// properties.contextWindow (100000), which the usage patch uses as maxTokens.
// The registry/mapper is not ported, so this default is fixed for the mock.
const modelOptionContextWindow = 200000

// availableModels builds the model.available list from the provider config.
func (r *Runtime) availableModels() []emitters.ModelOption {
	p := r.deps.Provider
	return []emitters.ModelOption{{
		Ref:             emitters.ModelRef{ProviderID: p.ProviderID, ModelID: p.ModelID},
		Label:           p.ModelName,
		ProviderLabel:   p.ProviderName,
		ContextWindow:   modelOptionContextWindow,
		MaxOutputToken:  p.MaxOutputToken,
		ReasoningLevels: p.ReasoningLevels,
		DefaultLevel:    p.DefaultReasoning,
		HasInputFormat:  true,
		SupportsText:    true,
	}}
}

func (r *Runtime) thoughtLevels() []emitters.ThoughtLevel {
	out := make([]emitters.ThoughtLevel, 0, len(r.deps.Provider.ReasoningLevels))
	for _, lvl := range r.deps.Provider.ReasoningLevels {
		out = append(out, emitters.ThoughtLevel{Label: lvl, Value: lvl})
	}
	return out
}

// modelSelectionOptions is the raw options object of the current selection.
func (s *session) modelSelectionOptions() json.RawMessage {
	var m map[string]json.RawMessage
	if err := json.Unmarshal(s.modelSelection, &m); err != nil {
		return nil
	}
	return m["options"]
}

// subscriptionIDs lists the active subscription ids of a session's publisher.
func (s *session) subscriptionIDs() []string {
	return s.pub.SubscriptionIDs()
}

var _ = v4pub.DeliveryOnline
